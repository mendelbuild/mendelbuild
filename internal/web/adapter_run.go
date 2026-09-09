package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/experiment"
)

// Starting an adapter, which is where everything else in this area finally
// touches a cluster.
//
// The ordering is not arbitrary and is the part worth reading. Mendel mints an
// invocation, applies the Secret, applies the Job, then patches the Secret to be
// owned by the Job — and it has to be that order because the Secret must exist
// before the pod resolves its environment, while the Job's UID does not exist
// until the Job does. Getting it wrong gives either a pod that cannot start or a
// Secret nothing will ever collect.

// startProbe asks a project's datastore what it is.
//
// Returns the invocation so a caller can say what it started. The answer arrives
// later and separately, at the report endpoint — nothing here waits for it,
// because the whole arrangement exists so that a job Mendel cannot reach can
// still tell it something.
func (s *Server) startProbe(
	ctx context.Context,
	projectID uuid.UUID,
	g *gkeSession,
	image, datastoreEnv, reportTo, prodEnvFrom string,
) (*domain.AdapterInvocation, error) {

	// The invocation comes first because it mints the token, and an instruction
	// without one does not validate -- it names somewhere to report and no way
	// to authenticate, which is a job that will be turned away.
	//
	// The token exists in a readable form only between here and the Secret it
	// goes into.
	inv, token, err := s.db.CreateAdapterInvocation(ctx, projectID, string(experiment.PhaseProbe), nil)
	if err != nil {
		return nil, err
	}

	instruction := ProbeInstructionFor(inv.ID.String(), reportTo, token, datastoreEnv)

	// Checked before anything reaches the cluster, because the alternative is a
	// Job that starts, cannot report, and is discovered when nothing arrives --
	// which reads as an adapter that failed rather than as an instruction that
	// was never going to work. The invocation left behind expires on its own.
	if why := instruction.Validate(); why != "" {
		return nil, fmt.Errorf("cannot start a probe: %s", why)
	}

	body, err := json.MarshalIndent(instruction, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the instruction: %w", err)
	}
	// Recorded without the token: this is what a late or malformed report is
	// checked against, and it has no business also being a second copy of the
	// credential.
	if err := s.db.SetAdapterInstruction(ctx, inv.ID, redactedInstruction(instruction)); err != nil {
		return nil, err
	}

	secretName := AdapterInstructionSecretName(inv.ID.String())
	if err := g.applyManifest(ctx, adapterInstructionSecret(secretName, body, nil)); err != nil {
		return nil, err
	}

	job := AdapterJob{
		Name:              AdapterJobName(inv.ID.String()),
		Image:             image,
		InstructionSecret: secretName,
		EnvFrom:           prodEnvFrom,
	}
	manifest, err := adapterJobManifest(job)
	if err != nil {
		return nil, err
	}
	if err := g.applyManifest(ctx, manifest); err != nil {
		return nil, err
	}

	// Best effort, deliberately. The probe is already running and the answer is
	// what matters; failing the whole thing over a garbage-collection hint would
	// throw away a job that is about to succeed. What it costs when it fails is
	// one Secret left behind, holding a token that is dead within the hour.
	if uid, err := g.jobUID(ctx, job.Name); err == nil {
		_ = g.applyManifest(ctx, adapterInstructionSecret(secretName, body, &JobOwner{Name: job.Name, UID: uid}))
	}

	return inv, nil
}

// redactedInstruction is what Mendel keeps: the question as asked, with the
// credential taken out.
//
// The stored instruction exists so a report can be checked against what was
// asked, and none of that checking needs the token -- which is why keeping it
// would be storing a credential for no purpose, in the one place designed to be
// read back later.
func redactedInstruction(i *experiment.Instruction) []byte {
	redacted := *i
	redacted.Token = ""
	body, err := json.Marshal(redacted)
	if err != nil {
		return []byte(`{}`)
	}
	return body
}

// jobUID reads back what the cluster called the Job, which is the one thing an
// ownerReference cannot be written without.
func (g *gkeSession) jobUID(ctx context.Context, name string) (string, error) {
	out, err := g.kubectl(ctx, "get", "job", name, "-o", "jsonpath={.metadata.uid}").Output()
	if err != nil {
		return "", fmt.Errorf("read back the job's uid: %w", err)
	}
	uid := strings.TrimSpace(string(out))
	if uid == "" {
		return "", fmt.Errorf("the job reported no uid")
	}
	return uid, nil
}

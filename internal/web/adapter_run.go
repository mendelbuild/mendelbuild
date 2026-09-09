package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

// ProbeProject asks one project's datastore what it is, end to end.
//
// The entry point both callers use: the CLI, so a probe can be run by hand
// against staging, and the reconcile loop when there is one. One function with
// two callers rather than two paths that agree by inspection.
//
// Everything it needs beyond the project is what the deployment channel already
// holds — which is the point of running the adapter there: Mendel authenticates
// to the cluster the way it does for any deploy, and the adapter reaches the
// datastore the way the application does.
// ProbeStarted is a probe now running, and where to watch it.
//
// The location is not decoration. The adapter runs in the *project's* cluster,
// and whoever started it was almost certainly talking to Mendel's -- so a
// follow-up command without a context looks in the wrong place and says the job
// does not exist, which is indistinguishable from it never having been created.
type ProbeStarted struct {
	Invocation *domain.AdapterInvocation

	// Context names the project's cluster in a kubeconfig, or "" when the
	// channel's credentials do not say enough to name it.
	Context string

	// GetCredentials is what puts that context in a kubeconfig, for a terminal
	// that has never talked to this cluster. Empty on the same terms as Context.
	GetCredentials string

	// Namespace is where the Job is, in that cluster.
	Namespace string
}

// Watch renders the commands for following a probe, correct in a terminal that
// is pointed anywhere.
func (p ProbeStarted) Watch() string {
	job := AdapterJobName(p.Invocation.ID.String())
	ctxFlag := ""
	if p.Context != "" {
		ctxFlag = " --context " + p.Context
	}
	lines := ""
	if p.GetCredentials != "" {
		lines += "  cluster     " + p.GetCredentials + "\n"
	}
	lines += fmt.Sprintf("  job         kubectl%s -n %s get job %s\n", ctxFlag, p.Namespace, job)
	lines += fmt.Sprintf("  logs        kubectl%s -n %s logs job/%s", ctxFlag, p.Namespace, job)
	return lines
}

func (s *Server) ProbeProject(ctx context.Context, projectID uuid.UUID, image, reportTo string, force bool) (*ProbeStarted, error) {
	// Asked before anything is spent, because starting one costs a Job and a pod
	// in someone's cluster. The case that matters is a second probe joining one
	// already running: two jobs against a datastore means two reports, and the
	// second is refused as a duplicate for an invocation it does not belong to.
	// Not overridable by force for that reason -- what a person asking for a
	// refresh wants is a fresh answer, and one is already on its way.
	latest, err := s.db.LatestAdapterInvocation(ctx, projectID, string(experiment.PhaseProbe))
	if err != nil {
		return nil, err
	}
	if d := ShouldProbe(latest, time.Now(), force); !d.Start {
		return nil, fmt.Errorf("%s", d.Because)
	}

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil || channel == nil {
		return nil, fmt.Errorf("this project has no deployment channel, so there is nowhere to run an adapter")
	}

	env, err := s.deployCredentialsForChannel(ctx, projectID, channel)
	if err != nil {
		return nil, fmt.Errorf("the channel's credentials are not available: %w", err)
	}

	session, err := newGKESession(ctx, env)
	if err != nil {
		return nil, err
	}
	defer session.cleanup()

	// The variable the application reads its datastore from. Mendel knows it
	// because Mendel wrote the application; where it does not, that is a
	// `secret` requirement like any other rather than something to guess at.
	datastoreEnv := AdapterDatastoreEnv

	// Production's own environment, applied the way an Arm's is: the values the
	// merged code needs, in a Secret the pod names.
	//
	// Whether the datastore connection is among them depends on whether it is a
	// declared requirement, and where it is not, §13 §15's unfinished half is
	// what fills the gap -- Mendel recovering how the application connects from
	// the repository it wrote. Until then a project whose connection comes from
	// somewhere else gets a probe that reports it could not find the datastore,
	// naming the variable it looked for, which is the right failure to have.
	values, err := s.appSecretsFor(ctx, projectID, mergedStatuses(ctx, s, projectID))
	if err != nil {
		return nil, fmt.Errorf("read the values production runs with: %w", err)
	}
	envSecret := AdapterJobName(uuid.New().String()) + "-env"
	prodEnvFrom, err := session.applyEnvSecret(ctx, envSecret, values,
		map[string]string{"mendel-adapter": "true"})
	if err != nil {
		return nil, fmt.Errorf("give the adapter production's environment: %w", err)
	}

	inv, err := s.startProbe(ctx, projectID, session, image, datastoreEnv, reportTo, prodEnvFrom)
	if err != nil {
		return nil, err
	}

	started := &ProbeStarted{Invocation: inv, Namespace: session.namespace}
	if name := gkeContextName(env); name != "" {
		started.Context = name
		started.GetCredentials = fmt.Sprintf(
			"gcloud container clusters get-credentials %s --location %s --project %s",
			env["GKE_CLUSTER_NAME"], env["GKE_ZONE"], env["GCP_PROJECT_ID"])
	}
	return started, nil
}

// AdapterDatastoreEnv is the variable an adapter reads its connection from.
//
// One name for now, and a placeholder for a per-project answer rather than a
// convention worth keeping: §13 §15 says Mendel can recover how the application
// connects from the repository it wrote, and until it does, a project whose
// application reads something else needs this told to it rather than assumed.
const AdapterDatastoreEnv = "DATABASE_URL"

// mergedStatuses is what the code on main needs to run, which is what
// production runs with.
func mergedStatuses(ctx context.Context, s *Server, projectID uuid.UUID) []domain.RequirementStatus {
	statuses, err := s.prodRequirementStatus(ctx, projectID, "")
	if err != nil {
		// Not fatal: an adapter with fewer values than production has fails by
		// saying it could not find the datastore, which is a better failure
		// than refusing to start over a lookup that may have found nothing to
		// begin with.
		return nil
	}
	return statuses
}

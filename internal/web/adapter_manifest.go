package web

import (
	"fmt"
	"strings"

	"github.com/bhs/mendelbuild/internal/experiment"
	"github.com/bhs/mendelbuild/internal/hosting"
)

// What a datastore adapter needs in the cluster, rendered without one.
//
// A pure function for the same reason `k8sManifestFor` and `experimentManifest`
// are: the resources can be read, diffed and tested without a cluster, and the
// things that have silently not worked on GKE before are at least visible in a
// test's output.
//
// A **Job**, not a Deployment, and that is the shape rather than an
// implementation detail. §20 D59 settled it: a service would need an inbound
// path to a process holding database credentials, which is the reachability
// problem in reverse. A Job runs, reports outbound, and stops.

// AdapterJob is one invocation of an adapter, as a thing to apply.
type AdapterJob struct {
	// Name is what the Job is called. Derived from the invocation, so a second
	// phase for the same project does not collide with one still running.
	Name string

	// Image is the adapter. Which adapter it is, is a fact about the project's
	// datastore -- this renders whatever it is given and knows nothing about
	// any engine.
	Image string

	// InstructionSecret names the Secret carrying the instruction, which
	// includes the token the report authenticates with.
	//
	// A Secret rather than an env var in the pod spec, because the spec is
	// logged, diffed and golden-tested, and a bearer token in it would be in
	// all three. The manifest names the Secret; the values are applied
	// separately and never rendered here.
	InstructionSecret string

	// EnvFrom is the pod-spec fragment naming the Secret holding production's
	// environment -- how the application reaches its datastore.
	//
	// This is the whole reason the job runs here. The adapter finds the
	// datastore the way the application does, so a database on a private
	// address is reachable, and Mendel never needs a connection string it
	// could not use anyway.
	EnvFrom string
}

// AdapterJobTimeout bounds a run in the cluster.
//
// Shorter than the token's own life, deliberately: a job that has stopped
// should not leave a credential valid behind it, and the ordering means the
// window a token is good for is always at least as long as the window a job
// could report in.
const AdapterJobTimeout = 20 * 60 // seconds

// Validate reports why this job cannot be applied, or "" when it can.
//
// Checked before anything is applied, because the alternative is a Job that
// starts, cannot report, and is discovered when nothing arrives -- which reads
// as an adapter that failed rather than as a manifest that was never going to
// work.
func (j AdapterJob) Validate() string {
	switch {
	case strings.TrimSpace(j.Name) == "":
		return "an adapter job needs a name, or a second phase collides with one still running"
	case strings.TrimSpace(j.Image) == "":
		return "an adapter job needs an image; which adapter that is, is a fact about the project's datastore"
	case strings.TrimSpace(j.InstructionSecret) == "":
		return "an adapter job needs the secret carrying its instruction, which is how it learns " +
			"what to do and where to report"
	}
	return ""
}

// AdapterJobName is what one invocation's Job is called.
//
// Named for the invocation rather than the project or the phase, so two runs
// cannot collide -- a probe refreshing while an admission is still going is
// normal, and a Job name that collided would have the second silently not
// created.
func AdapterJobName(invocationID string) string {
	id := sanitizeAppName(invocationID)
	if id == "" {
		id = "unknown"
	}
	name := "mendel-adapter-" + id
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.Trim(name, "-")
}

// adapterJobManifest renders the Job.
//
// `restartPolicy: Never` with `backoffLimit: 0`: a phase is not idempotent in
// general -- applying a migration twice is not applying it once -- and a
// silently retried Job would do the second thing while Mendel believed the
// first. A run that fails is reported as a run that failed, and retrying is
// Mendel's decision to make with a new invocation and a new token.
func adapterJobManifest(j AdapterJob) (string, error) {
	if why := j.Validate(); why != "" {
		return "", fmt.Errorf("%s", why)
	}
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    mendel-adapter: "true"
spec:
  backoffLimit: 0
  activeDeadlineSeconds: %d
  # Kept briefly after finishing so its logs can be read when a report never
  # arrives, which is the case hardest to diagnose from Mendel's side.
  ttlSecondsAfterFinished: 3600
  template:
    metadata:
      labels:
        mendel-adapter: "true"
    spec:
      restartPolicy: Never
      containers:
      - name: adapter
        image: %s
        env:
        - name: MENDEL_ADAPTER_INSTRUCTION
          valueFrom:
            secretKeyRef:
              name: %s
              key: instruction
%s`, j.Name, hosting.Namespace, AdapterJobTimeout, j.Image, j.InstructionSecret, j.EnvFrom), nil
}

// adapterInstructionSecret renders the Secret carrying one instruction.
//
// Separate from the Job so the Job's manifest can be logged and diffed freely.
// The instruction contains the bearer token, which is the one thing in this
// exchange that must not end up somewhere it can be read later.
func adapterInstructionSecret(name string, instruction []byte) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  labels:
    mendel-adapter: "true"
type: Opaque
stringData:
  instruction: |
%s
`, name, hosting.Namespace, indentBlock(string(instruction), 4))
}

// AdapterInstructionSecretName is what one invocation's Secret is called.
func AdapterInstructionSecretName(invocationID string) string {
	return AdapterJobName(invocationID) + "-instruction"
}

// indentBlock indents every line, so a multi-line value survives being a YAML
// block scalar. A single long line would work too and is unreadable in a
// `kubectl get -o yaml`, which is where someone looks when a job did not do what
// they expected.
func indentBlock(s string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// ProbeInstructionFor builds the instruction for a probe of one project.
//
// Probe takes no migration and no sandbox: it asks what the datastore is and
// what it can do, which is the only question that has an answer before any
// experiment exists (§20 D64).
func ProbeInstructionFor(invocationID, reportTo, token, datastoreEnv string) *experiment.Instruction {
	return &experiment.Instruction{
		Phase:        experiment.PhaseProbe,
		InvocationID: invocationID,
		ReportTo:     reportTo,
		Token:        token,
		DatastoreEnv: datastoreEnv,
	}
}

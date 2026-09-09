package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/experiment"
)

// The manifest is rendered without a cluster, so what it says can be read in a
// test rather than discovered from something quietly not happening.

func probeJob() AdapterJob {
	return AdapterJob{
		Name:              AdapterJobName("inv-1"),
		Image:             "registry.example/adapter:abc",
		InstructionSecret: AdapterInstructionSecretName("inv-1"),
		EnvFrom: `        envFrom:
        - secretRef:
            name: prod-env`,
	}
}

// A phase is not idempotent in general -- applying a migration twice is not
// applying it once -- so a silently retried Job would do the second thing while
// Mendel believed the first.
func TestAnAdapterJobIsNeverRetriedByTheCluster(t *testing.T) {
	m, err := adapterJobManifest(probeJob())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(m, "backoffLimit: 0") {
		t.Error("a Job the cluster may retry would re-run a phase Mendel believes ran once")
	}
	if !strings.Contains(m, "restartPolicy: Never") {
		t.Error("a restarting container is the same problem inside the pod")
	}
	if !strings.Contains(m, "kind: Job") {
		t.Error("an adapter runs and stops; a Deployment would need an inbound path to reach it")
	}
}

// The bearer token must not be in a spec that is logged, diffed and
// golden-tested. It lives in a Secret the Job names.
func TestTheJobSpecCarriesNoToken(t *testing.T) {
	m, err := adapterJobManifest(probeJob())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(m, "secret-token-value") || strings.Contains(m, "Bearer") {
		t.Error("the job spec must not carry a credential")
	}
	if !strings.Contains(m, "secretKeyRef") {
		t.Error("the instruction should be read from a Secret, not inlined")
	}

	// And the adapter has to be able to reach the datastore the way the
	// application does, which is the reason it runs here at all.
	if !strings.Contains(m, "prod-env") {
		t.Error("the job must get production's environment, or it cannot find the datastore")
	}
}

// A job that cannot report is worse than one that was never created: it starts,
// does work, and is discovered when nothing arrives.
func TestAnUnworkableJobIsRefusedBeforeItIsApplied(t *testing.T) {
	for _, tc := range []struct{ name string; bad func(*AdapterJob); want string }{
		{"no name", func(j *AdapterJob) { j.Name = "" }, "name"},
		{"no image", func(j *AdapterJob) { j.Image = "" }, "image"},
		{"no instruction", func(j *AdapterJob) { j.InstructionSecret = "" }, "instruction"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := probeJob()
			tc.bad(&j)
			if why := j.Validate(); why == "" || !strings.Contains(why, tc.want) {
				t.Errorf("Validate() = %q, want it to mention %q", why, tc.want)
			}
			if _, err := adapterJobManifest(j); err == nil {
				t.Error("an unworkable job should not render")
			}
		})
	}
}

// Two runs must not collide. A probe refreshing while an admission is still
// going is normal, and a shared name would have the second silently not created.
func TestEachInvocationGetsItsOwnNames(t *testing.T) {
	a, b := AdapterJobName("inv-a"), AdapterJobName("inv-b")
	if a == b {
		t.Errorf("two invocations share the job name %q", a)
	}
	if AdapterInstructionSecretName("inv-a") == AdapterInstructionSecretName("inv-b") {
		t.Error("two invocations share a secret name")
	}
	if len(AdapterJobName(strings.Repeat("x", 200))) > 63 {
		t.Error("a name Kubernetes would refuse was produced anyway")
	}
}

// The instruction survives being a YAML block, since it is multi-line JSON and a
// mangled one is a job that cannot parse what it was asked.
func TestTheInstructionSurvivesBeingASecret(t *testing.T) {
	instr := ProbeInstructionFor("inv-1", "https://mendel.example/adapters/report", "tok", "DATABASE_URL")
	raw, err := json.MarshalIndent(instr, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	secret := adapterInstructionSecret(AdapterInstructionSecretName("inv-1"), raw, nil)

	if !strings.Contains(secret, "kind: Secret") {
		t.Fatal("not a Secret")
	}
	// Every line of the payload has to be indented under the block scalar, or
	// the YAML is a different document from the one intended.
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if !strings.Contains(secret, "    "+line) {
			t.Errorf("line %q is not indented into the block", line)
			break
		}
	}
	if why := instr.Validate(); why != "" {
		t.Errorf("the probe instruction is not usable: %s", why)
	}
}

// --- Reading a probe back ---

func completedProbe(t *testing.T, probe experiment.ProbeResult) *domain.AdapterInvocation {
	t.Helper()
	raw, err := json.Marshal(experiment.Result{
		Phase: experiment.PhaseProbe, InvocationID: "inv-1",
		Outcome: experiment.OutcomeCompleted, Probe: &probe,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	now := time.Now()
	return &domain.AdapterInvocation{
		Phase: "probe", Result: raw, Outcome: domain.AdapterOutcomeCompleted,
		ExpiresAt: now.Add(time.Hour), ReportedAt: &now,
	}
}

// Four things are not "no": never asked, still going, could not tell, and a
// recorded answer that will not read back. Collapsing any of them tells someone
// their datastore is unsuitable because a job was slow.
func TestOnlyAnAnswerIsAnAnswer(t *testing.T) {
	now := time.Now()

	if got := probeFacts(nil, now); got.Supported != domain.FactUnknown {
		t.Errorf("a project nobody has probed is %q, want unknown", got.Supported)
	} else if !strings.Contains(got.Why, "has not looked") {
		t.Errorf("it should say nobody has looked, got %q", got.Why)
	}

	running := &domain.AdapterInvocation{Phase: "probe", ExpiresAt: now.Add(time.Hour)}
	if got := probeFacts(running, now); got.Supported != domain.FactUnknown {
		t.Errorf("a probe still going is %q, want unknown", got.Supported)
	}

	abandoned := &domain.AdapterInvocation{Phase: "probe", ExpiresAt: now.Add(-time.Hour)}
	if got := probeFacts(abandoned, now); got.Supported != domain.FactUnknown {
		t.Errorf("a probe that never reported is %q, want unknown", got.Supported)
	} else if !strings.Contains(got.Why, "about the attempt") {
		t.Errorf("it should say the failure is Mendel's, got %q", got.Why)
	}

	failedRaw, _ := json.Marshal(experiment.Result{
		Outcome: experiment.OutcomeFailed, Failure: "could not connect",
	})
	reported := now
	failed := &domain.AdapterInvocation{
		Phase: "probe", Result: failedRaw, Outcome: domain.AdapterOutcomeFailed,
		ExpiresAt: now.Add(time.Hour), ReportedAt: &reported,
	}
	if got := probeFacts(failed, now); got.Supported != domain.FactUnknown {
		t.Errorf("a probe that could not conclude is %q, want unknown", got.Supported)
	} else if !strings.Contains(got.Why, "could not connect") {
		t.Errorf("it should carry what the adapter said, got %q", got.Why)
	}
}

// And an answer is read for what it says, including a "no" that is genuinely a
// no: an adapter that answered and cannot provision has established something.
func TestAProbeThatAnsweredIsRead(t *testing.T) {
	now := time.Now()

	ok := completedProbe(t, experiment.ProbeResult{
		Kind: "whatever", Version: "9.9", CanProvision: true,
	})
	got := probeFacts(ok, now)
	if got.Supported != domain.FactTrue || got.CanProvision != domain.FactTrue {
		t.Errorf("a good probe read as %+v", got)
	}
	if got.Kind != "whatever" || got.Version != "9.9" {
		t.Errorf("the engine identity should survive, got %+v", got)
	}

	cannot := completedProbe(t, experiment.ProbeResult{
		Kind: "whatever", CanProvision: false,
		CannotProvisionBecause: "this credential may not create a database",
	})
	got = probeFacts(cannot, now)
	if got.Supported != domain.FactTrue {
		t.Error("an adapter that answered has established the datastore is workable")
	}
	if got.CanProvision != domain.FactFalse {
		t.Errorf("CanProvision = %q, want false; this one was actually established", got.CanProvision)
	}
	if !strings.Contains(got.Why, "may not create a database") {
		t.Errorf("the reason should reach the reader, got %q", got.Why)
	}
}

// --- Nothing an invocation creates outlives its own window ---

// The Job stops itself and then removes itself. Without both, a run that hangs
// holds a pod open indefinitely, and a run that finishes leaves one behind for
// every probe ever taken.
func TestAJobStopsAndThenRemovesItself(t *testing.T) {
	m, err := adapterJobManifest(probeJob())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(m, "activeDeadlineSeconds:") {
		t.Error("without a deadline a hung adapter holds a pod until someone notices")
	}
	if !strings.Contains(m, "ttlSecondsAfterFinished:") {
		t.Error("without a TTL every probe ever taken leaves a finished Job behind")
	}
}

// A Secret is an ordinary object and stays until something deletes it. One per
// invocation, with probes refreshing on a schedule, accumulates without bound --
// each holding a bearer token. The Job's ownerReference is what has the cluster
// collect one with the other.
func TestTheInstructionSecretIsCollectedWithItsJob(t *testing.T) {
	raw := []byte(`{"phase":"probe"}`)

	orphan := adapterInstructionSecret("s", raw, nil)
	if strings.Contains(orphan, "ownerReferences") {
		t.Error("the pre-Job form has no owner to name yet")
	}

	owned := adapterInstructionSecret("s", raw, &JobOwner{Name: "mendel-adapter-inv-1", UID: "uid-123"})
	for _, want := range []string{"ownerReferences", "kind: Job", "mendel-adapter-inv-1", "uid-123"} {
		if !strings.Contains(owned, want) {
			t.Errorf("the owned form should carry %q:\n%s", want, owned)
		}
	}
	// blockOwnerDeletion would make deleting the Job wait on this Secret, which
	// is backwards: the Secret is the incidental thing.
	if !strings.Contains(owned, "blockOwnerDeletion: false") {
		t.Error("a Secret must not hold up the deletion of the Job that owns it")
	}
}

// A job must not outlive the credential it was given, or a pod goes on running
// with a token it can no longer use -- which reads as an adapter that failed
// rather than one that was cut off.
func TestAJobCannotOutliveItsToken(t *testing.T) {
	jobLife := time.Duration(AdapterJobTimeout) * time.Second
	if jobLife >= db.AdapterTokenTTL {
		t.Errorf("a job may run for %s and its token is good for %s. The job has to be the "+
			"shorter of the two, so that running out of time is what stops it rather than "+
			"discovering its credential has expired.", jobLife, db.AdapterTokenTTL)
	}
}

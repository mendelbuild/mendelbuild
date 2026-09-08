package experiment_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/experiment"
)

func probeInstruction() *experiment.Instruction {
	return &experiment.Instruction{
		Phase:        experiment.PhaseProbe,
		InvocationID: "inv-1",
		ReportTo:     "https://mendel.example/adapters/report",
		Token:        "t",
	}
}

// An instruction is checked before a job is deployed, because the alternative is
// paying for a deployment to find out a field was missing.
func TestAnInstructionThatCannotBeActedOnIsRefusedBeforeDeploying(t *testing.T) {
	for _, tc := range []struct {
		name string
		bad  func(*experiment.Instruction)
		want string
	}{
		{"no phase", func(i *experiment.Instruction) { i.Phase = "" }, "phase"},
		{"unknown phase", func(i *experiment.Instruction) { i.Phase = "guess" }, "not a phase"},
		{"no invocation", func(i *experiment.Instruction) { i.InvocationID = "" }, "invocation"},
		{"nowhere to report", func(i *experiment.Instruction) { i.ReportTo = "" }, "report"},
		{"no token", func(i *experiment.Instruction) { i.Token = "" }, "report"},
		{
			"admit without a migration",
			func(i *experiment.Instruction) { i.Phase = experiment.PhaseAdmit; i.Sandbox = "s" },
			"migration",
		},
		{
			"admit without a sandbox name",
			func(i *experiment.Instruction) {
				i.Phase = experiment.PhaseAdmit
				i.Migration = &experiment.Migration{Up: "u", Down: "d"}
			},
			"sandbox",
		},
		{
			"restore without an archive",
			func(i *experiment.Instruction) { i.Phase = experiment.PhaseRestore },
			"archive",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := probeInstruction()
			tc.bad(i)
			got := i.Validate()
			if got == "" {
				t.Fatalf("expected a refusal")
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("the refusal should mention %q, got %q", tc.want, got)
			}
		})
	}

	if got := probeInstruction().Validate(); got != "" {
		t.Errorf("a well-formed probe was refused: %s", got)
	}
}

// Probe is asked before any experiment exists, so being handed a migration means
// the caller has confused it with admission.
func TestProbeTakesNoMigration(t *testing.T) {
	i := probeInstruction()
	i.Migration = &experiment.Migration{Up: "u", Down: "d"}
	if !strings.Contains(i.Validate(), "no migration") {
		t.Errorf("probe should refuse a migration, got %q", i.Validate())
	}
}

// Silence, failure and a negative answer are three things. A caller that reads
// them alike reports Mendel's problem as the user's -- the mistake Fact and
// DomainObservation.Known both exist to prevent.
func TestSilenceIsNotAnAnswer(t *testing.T) {
	var missing *experiment.Result
	if missing.Answered() {
		t.Error("a job that never reported has not answered")
	}
	if missing.Validate(probeInstruction()) == "" {
		t.Error("no result at all must not validate as one")
	}

	failed := &experiment.Result{
		Phase: experiment.PhaseProbe, InvocationID: "inv-1",
		Outcome: experiment.OutcomeFailed, Failure: "could not reach the datastore",
	}
	if failed.Answered() {
		t.Error("a failed run has not answered either")
	}
	if got := failed.Validate(probeInstruction()); got != "" {
		t.Errorf("a failure that says why is a valid report: %s", got)
	}

	silent := &experiment.Result{Phase: experiment.PhaseProbe, InvocationID: "inv-1"}
	if !strings.Contains(silent.Validate(probeInstruction()), "silence") {
		t.Errorf("a result with no outcome must be refused, got %q", silent.Validate(probeInstruction()))
	}

	quiet := &experiment.Result{
		Phase: experiment.PhaseProbe, InvocationID: "inv-1", Outcome: experiment.OutcomeFailed,
	}
	if quiet.Validate(probeInstruction()) == "" {
		t.Error("a failure that does not say why leaves a retry nothing to work from")
	}
}

// A late reply from a previous run answers a question nobody is asking any more.
func TestAStaleReplyIsNotAnAnswerToThisRun(t *testing.T) {
	stale := &experiment.Result{
		Phase: experiment.PhaseProbe, InvocationID: "inv-0", Outcome: experiment.OutcomeCompleted,
		Probe: &experiment.ProbeResult{Kind: "whatever", CanProvision: true},
	}
	if got := stale.Validate(probeInstruction()); !strings.Contains(got, "earlier run") {
		t.Errorf("a mismatched invocation should be refused as stale, got %q", got)
	}

	wrongPhase := &experiment.Result{
		Phase: experiment.PhaseAdmit, InvocationID: "inv-1", Outcome: experiment.OutcomeCompleted,
	}
	if wrongPhase.Validate(probeInstruction()) == "" {
		t.Error("a result answering a different phase must be refused")
	}
}

// A completed probe has to carry what a decline would say out loud, and what a
// cached adapter is re-gated against.
func TestACompletedProbeCarriesWhatItFound(t *testing.T) {
	empty := &experiment.Result{
		Phase: experiment.PhaseProbe, InvocationID: "inv-1", Outcome: experiment.OutcomeCompleted,
	}
	if empty.Validate(probeInstruction()) == "" {
		t.Error("a completed probe with no findings must be refused")
	}

	nameless := &experiment.Result{
		Phase: experiment.PhaseProbe, InvocationID: "inv-1", Outcome: experiment.OutcomeCompleted,
		Probe: &experiment.ProbeResult{CanProvision: true},
	}
	if !strings.Contains(nameless.Validate(probeInstruction()), "name the datastore") {
		t.Error("a probe that does not name the datastore must be refused")
	}

	silentRefusal := &experiment.Result{
		Phase: experiment.PhaseProbe, InvocationID: "inv-1", Outcome: experiment.OutcomeCompleted,
		Probe: &experiment.ProbeResult{Kind: "whatever", CanProvision: false},
	}
	if silentRefusal.Validate(probeInstruction()) == "" {
		t.Error("a probe that cannot provision must say what happened when it tried")
	}
}

// Admission compares the sandbox against production, so a report that names a
// collection without both shapes describes a comparison that cannot be made.
func TestAnAdmissionReportMustBeInternallyConsistent(t *testing.T) {
	instr := &experiment.Instruction{
		Phase: experiment.PhaseAdmit, InvocationID: "inv-1",
		ReportTo: "https://x", Token: "t", Sandbox: "s",
		Migration: &experiment.Migration{Up: "u", Down: "d"},
	}
	added := []experiment.Object{{Kind: experiment.ObjectField, Collection: "orders", Name: "mendel_exp_x"}}

	partial := &experiment.Result{
		Phase: experiment.PhaseAdmit, InvocationID: "inv-1", Outcome: experiment.OutcomeCompleted,
		Admit: &experiment.AdmitResult{
			Delta:      experiment.Delta{Added: added},
			LiveShapes: map[string]experiment.TableSchema{"orders": {"id": "integer"}},
			Identities: map[string][]string{"orders": {"id"}},
		},
	}
	if !strings.Contains(partial.Validate(instr), "sandbox") {
		t.Errorf("a missing sandbox shape must be refused, got %q", partial.Validate(instr))
	}

	// Absent and empty are different: empty is what admission refuses on, and
	// absent is a report that forgot to say.
	noIdentity := &experiment.Result{
		Phase: experiment.PhaseAdmit, InvocationID: "inv-1", Outcome: experiment.OutcomeCompleted,
		Admit: &experiment.AdmitResult{
			Delta:         experiment.Delta{Added: added},
			LiveShapes:    map[string]experiment.TableSchema{"orders": {"id": "integer"}},
			SandboxShapes: map[string]experiment.TableSchema{"orders": {"id": "integer"}},
		},
	}
	if !strings.Contains(noIdentity.Validate(instr), "identity") {
		t.Errorf("an unreported identity must be refused, got %q", noIdentity.Validate(instr))
	}

	whole := &experiment.Result{
		Phase: experiment.PhaseAdmit, InvocationID: "inv-1", Outcome: experiment.OutcomeCompleted,
		Admit: &experiment.AdmitResult{
			Delta:         experiment.Delta{Added: added},
			LiveShapes:    map[string]experiment.TableSchema{"orders": {"id": "integer"}},
			SandboxShapes: map[string]experiment.TableSchema{"orders": {"id": "integer"}},
			Identities:    map[string][]string{"orders": {"id"}},
		},
	}
	if got := whole.Validate(instr); got != "" {
		t.Errorf("a consistent admission report was refused: %s", got)
	}
}

// The exchange is JSON so it stays inspectable, diffable and pasteable into a
// bug report. A field that does not survive the round trip is one a reader of
// the wire cannot see.
func TestTheExchangeSurvivesJSON(t *testing.T) {
	in := &experiment.Result{
		Phase: experiment.PhaseProbe, InvocationID: "inv-1", Outcome: experiment.OutcomeCompleted,
		Probe: &experiment.ProbeResult{
			Kind: "whatever", Version: "9.9",
			Capabilities: experiment.Capabilities{StructuralDiff: true, Disposable: true},
			CanProvision: true,
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out experiment.Result
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Probe == nil || out.Probe.Kind != "whatever" || !out.Probe.Capabilities.StructuralDiff {
		t.Errorf("the round trip lost something: %s", raw)
	}
	if got := out.Validate(probeInstruction()); got != "" {
		t.Errorf("a result read back off the wire was refused: %s", got)
	}
}

// Every phase is either handled or deliberately not, so one added later cannot
// be silently accepted by a validator that has never heard of it.
func TestEveryPhaseIsAccountedFor(t *testing.T) {
	for _, p := range experiment.AllPhases() {
		i := probeInstruction()
		i.Phase = p
		switch p {
		case experiment.PhaseAdmit:
			i.Migration = &experiment.Migration{Up: "u", Down: "d"}
			i.Sandbox = "s"
		case experiment.PhaseApply, experiment.PhaseWithdraw:
			i.Migration = &experiment.Migration{Up: "u", Down: "d"}
		case experiment.PhaseRestore:
			i.Archive = &experiment.Archive{ArmID: "a"}
		}
		if got := i.Validate(); got != "" {
			t.Errorf("a well-formed %s instruction was refused: %s", p, got)
		}
	}
}

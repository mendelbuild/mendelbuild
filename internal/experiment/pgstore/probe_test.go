package pgstore_test

import (
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/experiment"
	"github.com/bhs/mendelbuild/internal/testdb"
)

// The probe phase, run against a real datastore and no infrastructure beyond it.
//
// Worth being explicit about why this needs nothing else. In production an
// adapter runs as a container in the user's own environment, but that is how it
// is *deployed*; what it does is read an instruction, ask a datastore some
// questions, and report. Each of those is exercisable here, and the only thing
// left over is whether Kubernetes schedules a Job — which is a fact about
// Kubernetes.

func TestProbeEstablishesWhatItReports(t *testing.T) {
	url := testdb.Require(t)
	store := newLiveStoreFor(t, url)

	probe := experiment.RunProbe(t.Context(), store)

	if probe.Kind == "" {
		t.Error("a probe must name the datastore; it is what a decline says out loud")
	}
	if !probe.Capabilities.StructuralDiff {
		t.Error("this adapter can describe structural change and should say so")
	}

	// CanProvision is a bool, so it cannot mean "did not check" — which means
	// nothing is allowed to leave it meaning that. An adapter with no way to
	// make a sandbox reports false *and* says why.
	if !probe.CanProvision && strings.TrimSpace(probe.CannotProvisionBecause) == "" {
		t.Error("a probe that cannot provision must say what happened, or false is indistinguishable " +
			"from unasked")
	}
}

// An adapter that does not exist has established nothing, and must not read as
// a datastore that was examined and found wanting.
func TestProbingNothingIsNotAFinding(t *testing.T) {
	probe := experiment.RunProbe(t.Context(), nil)
	if probe.CanProvision {
		t.Error("nothing was asked, so nothing can be true")
	}
	if !strings.Contains(probe.CannotProvisionBecause, "no adapter") {
		t.Errorf("the reason should name the absent adapter, got %q", probe.CannotProvisionBecause)
	}
}

// The whole exchange, end to end, with no container anywhere: an instruction is
// built, the phase runs against a real datastore, the report is assembled from
// the instruction that asked, and Mendel checks it the way the endpoint would.
func TestTheProbeExchangeClosesWithoutInfrastructure(t *testing.T) {
	url := testdb.Require(t)
	store := newLiveStoreFor(t, url)

	instruction := &experiment.Instruction{
		Phase:        experiment.PhaseProbe,
		InvocationID: "inv-loop",
		ReportTo:     "https://mendel.example/adapters/report",
		Token:        "minted-per-invocation",
	}
	if why := instruction.Validate(); why != "" {
		t.Fatalf("the instruction is not usable: %s", why)
	}

	report := experiment.ProbeReport(instruction, experiment.RunProbe(t.Context(), store))

	if why := report.Validate(instruction); why != "" {
		t.Fatalf("Mendel would refuse this adapter's own report: %s", why)
	}
	if !report.Answered() {
		t.Error("a probe that ran has answered")
	}

	// And a report built from a different instruction is refused, which is what
	// stops a late reply from an earlier run being read as this one's answer.
	stale := &experiment.Instruction{
		Phase: experiment.PhaseProbe, InvocationID: "inv-earlier",
		ReportTo: "https://x", Token: "t",
	}
	if why := report.Validate(stale); why == "" {
		t.Error("a report answering another invocation should not validate against this one")
	}
}

// A failure is a report too, and it is not a finding about the datastore.
func TestAFailedRunReportsWithoutConcluding(t *testing.T) {
	instruction := &experiment.Instruction{
		Phase: experiment.PhaseProbe, InvocationID: "inv-1",
		ReportTo: "https://x", Token: "t",
	}
	report := experiment.FailedReport(instruction, "could not resolve the datastore host")

	if why := report.Validate(instruction); why != "" {
		t.Errorf("a failure that says why is a valid report: %s", why)
	}
	if report.Answered() {
		t.Error("a run that failed has not answered")
	}
	if report.Probe != nil {
		t.Error("a failed run carries no findings")
	}
}

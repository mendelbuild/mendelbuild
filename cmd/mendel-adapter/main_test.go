package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/experiment"
)

func goodInstruction() *experiment.Instruction {
	return &experiment.Instruction{
		Phase:        experiment.PhaseProbe,
		InvocationID: "inv-1",
		ReportTo:     "https://mendel.example/adapters/report",
		Token:        "minted-per-invocation",
		DatastoreEnv: "DATABASE_URL",
	}
}

func encoded(t *testing.T, i *experiment.Instruction) string {
	t.Helper()
	raw, err := json.Marshal(i)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// The instruction is checked here as well as before the Job was created, because
// the two ends can be different versions: an image built last month meeting an
// instruction written today should say so rather than act on the parts it
// recognises.
func TestAnInstructionThisAdapterCannotActOnIsRefused(t *testing.T) {
	if _, err := readInstruction(""); err == nil {
		t.Error("an empty instruction must be refused")
	}
	if _, err := readInstruction("{not json"); err == nil {
		t.Error("an unreadable instruction must be refused")
	}

	incomplete := goodInstruction()
	incomplete.DatastoreEnv = ""
	if _, err := readInstruction(encoded(t, incomplete)); err == nil {
		t.Error("an instruction that does not say where the datastore is must be refused")
	}

	if _, err := readInstruction(encoded(t, goodInstruction())); err != nil {
		t.Errorf("a well-formed instruction was refused: %v", err)
	}
}

// A phase this image does not implement is reported, not ignored. Naming it is
// what tells a reader the adapter is behind rather than the phase being broken.
func TestAnUnimplementedPhaseIsReportedRatherThanIgnored(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")

	instruction := goodInstruction()
	instruction.Phase = experiment.PhaseRestore
	instruction.Archive = &experiment.Archive{ArmID: "a"}

	result := perform(t.Context(), instruction)
	if result.Answered() {
		t.Error("an adapter that cannot do the phase has not answered")
	}
	if result.Outcome != experiment.OutcomeFailed {
		t.Errorf("outcome = %q, want failed", result.Outcome)
	}
	if result.Phase != instruction.Phase || result.InvocationID != instruction.InvocationID {
		t.Error("a report carries the phase and invocation it answers, from the instruction that asked")
	}
}

// A datastore that cannot be reached is a failure with a reason, never a finding
// about the datastore. The commonest way a datastore looks unsuitable is that
// something unrelated to it went wrong.
func TestAnUnreachableDatastoreFailsWithAReason(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")

	result := perform(t.Context(), goodInstruction())
	if result.Answered() {
		t.Fatal("nothing was established, so nothing may be reported as established")
	}
	if result.Probe != nil {
		t.Error("a failed run carries no findings")
	}
	if strings.TrimSpace(result.Failure) == "" {
		t.Error("a failure must say why; it reaches a page and a retry works from it")
	}
}

// And an environment with no such variable says which one it looked for, since
// the fix is naming the right one.
func TestAMissingDatastoreVariableNamesItself(t *testing.T) {
	instruction := goodInstruction()
	instruction.DatastoreEnv = "LEDGER_DATABASE_URL"

	result := perform(t.Context(), instruction)
	if !strings.Contains(result.Failure, "LEDGER_DATABASE_URL") {
		t.Errorf("the failure should name the variable it looked for, got %q", result.Failure)
	}
}

// The token is the whole of the authentication, and it goes in a header rather
// than the URL so it is not in anyone's access logs.
func TestTheReportAuthenticatesWithTheTokenInAHeader(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	instruction := goodInstruction()
	instruction.ReportTo = srv.URL

	sent := experiment.ProbeReport(instruction, experiment.ProbeResult{
		Kind: "whatever", CanProvision: true,
	})
	if err := report(t.Context(), instruction, sent); err != nil {
		t.Fatalf("report: %v", err)
	}

	if gotAuth != "Bearer minted-per-invocation" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if strings.Contains(srv.URL, "minted-per-invocation") {
		t.Error("the token must not be in the URL")
	}
	var back experiment.Result
	if err := json.Unmarshal([]byte(gotBody), &back); err != nil {
		t.Fatalf("what arrived is not a Result: %v (%s)", err, gotBody)
	}
	if why := back.Validate(instruction); why != "" {
		t.Errorf("Mendel would refuse what this adapter sent: %s", why)
	}
}

// A refused report is not retried. Mendel accepts one report per invocation, so
// a retry could only be rejected -- and a Job the cluster restarts would run the
// phase again, which is what backoffLimit 0 exists to prevent.
func TestARefusedReportIsNotRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "already reported", http.StatusConflict)
	}))
	defer srv.Close()

	instruction := goodInstruction()
	instruction.ReportTo = srv.URL

	err := report(t.Context(), instruction, experiment.FailedReport(instruction, "whatever"))
	if err == nil {
		t.Error("a refused report should be surfaced, so the Job's exit code says so")
	}
	if calls != 1 {
		t.Errorf("the report was sent %d times, want once", calls)
	}
}

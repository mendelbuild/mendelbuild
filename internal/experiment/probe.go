package experiment

import "context"

// Running the probe phase, apart from how it was asked and where it reports.
//
// Separated from both so that the whole exchange can be exercised without any of
// its infrastructure: this is a function over a Datastore, and a Datastore is
// satisfiable by a local database. The container an adapter runs in is a
// deployment mechanism rather than a behaviour, and testing that a Job gets
// scheduled is testing Kubernetes.
//
// The real adapter binary calls this too. One implementation, so what a test
// exercises is what runs.

// SandboxMaker is implemented by an adapter that can create a verification
// database on the server it is connected to.
//
// An optional interface rather than part of Datastore, because the two are
// different things: a Datastore is one database and this is about the server it
// was made on. An adapter that cannot make sandboxes is not a broken adapter —
// it is one that limits experiments to those changing no schema, which is a
// designed outcome and something Probe has to be able to report.
type SandboxMaker interface {
	// CanMakeSandbox reports whether a verification database can actually be
	// created, by attempting it rather than by reading a grant. Nil means yes.
	CanMakeSandbox(ctx context.Context) error
}

// RunProbe answers what a datastore is and what it can do.
//
// Every field is established rather than assumed, including the negative ones.
// `CanProvision: false` says Mendel found out it cannot, and carries what
// happened — which is why an adapter that does not implement SandboxMaker gets
// a sentence saying so rather than a bare false. The bool cannot express "did
// not check", so nothing here is allowed to leave it meaning that.
func RunProbe(ctx context.Context, store Datastore) ProbeResult {
	if store == nil {
		return ProbeResult{
			Kind:                   "unknown",
			CannotProvisionBecause: "there is no adapter for this project's datastore, so nothing could be asked of it",
		}
	}

	result := ProbeResult{
		Kind:         store.Kind(),
		Capabilities: store.Capabilities(),
	}

	maker, ok := store.(SandboxMaker)
	if !ok {
		result.CannotProvisionBecause = "this adapter cannot create a verification database, so an " +
			"experiment that changes the schema has nowhere to be proved additive. One that changes " +
			"no schema needs none of this."
		return result
	}
	if err := maker.CanMakeSandbox(ctx); err != nil {
		result.CannotProvisionBecause = err.Error()
		return result
	}
	result.CanProvision = true
	return result
}

// ProbeReport wraps a probe as the Result a job reports.
//
// Here rather than in the caller so that the invocation id and phase are carried
// from the instruction that asked, and cannot be filled in from somewhere that
// merely believes it knows them.
func ProbeReport(instruction *Instruction, probe ProbeResult) *Result {
	return &Result{
		Phase:        instruction.Phase,
		InvocationID: instruction.InvocationID,
		Outcome:      OutcomeCompleted,
		Probe:        &probe,
	}
}

// FailedReport is what a job says when it could not reach an answer.
//
// A distinct thing from a probe reporting that a datastore is unsuitable. This
// one has established nothing, and the difference is what stops a page telling
// someone their datastore is the problem when the adapter never connected.
func FailedReport(instruction *Instruction, why string) *Result {
	return &Result{
		Phase:        instruction.Phase,
		InvocationID: instruction.InvocationID,
		Outcome:      OutcomeFailed,
		Failure:      why,
	}
}

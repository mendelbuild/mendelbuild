package experiment

import (
	"fmt"
	"strings"
)

// The contract between Mendel and a datastore adapter running somewhere else.
//
// An adapter is deployed through the project's own channel and invoked as a job:
// it reads an Instruction, does one phase's work, reports a Result outbound, and
// exits (§20 D54, D59). This file is that exchange and nothing more — no
// transport, no deployment, no dialect. It is the piece both halves have to
// agree on, so it is written down before either exists.
//
// Three properties are load-bearing, and each is a mistake this codebase has
// already made once somewhere else:
//
//   - **Silence is not an answer.** A job that never reported is not a job that
//     reported "no". Reporting a missing answer as a negative one is how a user
//     gets told to fix something that is not broken, which is why DomainObservation
//     has Known and why Fact has three values. Result therefore says what it
//     concluded *and* whether it concluded anything, and the absence of a Result
//     is a third thing the caller must handle.
//
//   - **A phase is a unit of work, not a method call.** Admission is a sequence
//     whose later steps depend on earlier results, and a job cannot be chatty. So
//     an adapter is asked for a phase and returns everything Mendel needs to judge
//     it, gathered while the adapter still had the connection open. Mendel does
//     all the judging afterwards, on data it was handed (§20 D64).
//
//   - **Identity comes from the credential, never the payload.** A Result names
//     the invocation it answers, and that name is a cross-check rather than the
//     source of truth: the bearer token Mendel minted for the job is what resolves
//     which project and which invocation this is. §13 D10 settled this for metrics
//     ingest and the reasoning is identical — an endpoint that trusts what the body
//     says about itself can be told anything.

// Phase is the unit an adapter is invoked for.
type Phase string

const (
	// PhaseProbe asks what this datastore is and what it can do. It involves no
	// migration, and it is the only phase that runs when no experiment exists --
	// which is exactly when a settings page needs the answer.
	PhaseProbe Phase = "probe"

	// PhaseAdmit provisions a sandbox, verifies a migration against it, and
	// reports everything Mendel needs to judge admission.
	PhaseAdmit Phase = "admit"

	// PhaseApply runs the up migration against production.
	PhaseApply Phase = "apply"

	// PhaseWithdraw archives what the experiment wrote and runs the down
	// migration. Separate from apply because days pass between them, not
	// because one is chosen over the other.
	PhaseWithdraw Phase = "withdraw"

	// PhaseRestore puts an archive back.
	PhaseRestore Phase = "restore"
)

// AllPhases is every phase, for validation and for tests that must not silently
// skip one that was added later.
func AllPhases() []Phase {
	return []Phase{PhaseProbe, PhaseAdmit, PhaseApply, PhaseWithdraw, PhaseRestore}
}

// Instruction is what Mendel hands a job.
//
// Deliberately not a connection string. The adapter runs where the application
// runs and finds the datastore the way the application does; handing it a URL
// would put Mendel back in the business of knowing how to reach a database it
// cannot reach.
type Instruction struct {
	Phase Phase `json:"phase"`

	// InvocationID names this run, so a Result can say which question it is
	// answering and a late reply from a previous run is recognisable as one.
	InvocationID string `json:"invocation_id"`

	// ReportTo is where the Result is POSTed, and Token authenticates it.
	// Minted per invocation: it is what resolves the project server-side, so it
	// must not outlive the job that carries it.
	ReportTo string `json:"report_to"`
	Token    string `json:"token"`

	// Migration is the change under consideration. Absent for probe.
	Migration *Migration `json:"migration,omitempty"`

	// Sandbox names the verification database to create, for admit. Named by
	// Mendel rather than by the adapter so that a second attempt reuses the name
	// rather than accumulating databases nobody will collect.
	Sandbox string `json:"sandbox,omitempty"`

	// Archive is what to put back, for restore.
	Archive *Archive `json:"archive,omitempty"`
}

// Validate reports why an Instruction cannot be acted on, or "" when it can.
//
// Checked before a job is deployed, because the alternative is paying for a
// deployment to discover a field is missing -- the same reason a missing channel
// credential is reported before a deploy starts rather than from inside one.
func (i *Instruction) Validate() string {
	if i == nil {
		return "there is no instruction"
	}
	switch i.Phase {
	case PhaseProbe, PhaseAdmit, PhaseApply, PhaseWithdraw, PhaseRestore:
	case "":
		return "an instruction must name a phase; an adapter does one thing per run"
	default:
		return fmt.Sprintf("%q is not a phase", i.Phase)
	}
	if strings.TrimSpace(i.InvocationID) == "" {
		return "an instruction needs an invocation id, or a reply cannot be told from a stale one"
	}
	if strings.TrimSpace(i.ReportTo) == "" || strings.TrimSpace(i.Token) == "" {
		return "an instruction needs somewhere to report to and a token to report with; " +
			"a job that cannot answer is a deployment paid for and thrown away"
	}

	needsMigration := i.Phase == PhaseAdmit || i.Phase == PhaseApply || i.Phase == PhaseWithdraw
	if needsMigration && i.Migration == nil {
		return string(i.Phase) + " needs the migration it is about"
	}
	if i.Phase == PhaseProbe && i.Migration != nil {
		return "probe is asked before any experiment exists and takes no migration"
	}
	if i.Phase == PhaseAdmit && strings.TrimSpace(i.Sandbox) == "" {
		return "admit needs the name of the sandbox to verify against, which Mendel chooses so " +
			"that a retry reuses it rather than leaving a database nobody will collect"
	}
	if i.Phase == PhaseRestore && i.Archive == nil {
		return "restore needs the archive to put back"
	}
	return ""
}

// Outcome is whether the adapter got to an answer.
//
// Distinct from what the answer was. An adapter that could not connect has not
// said the datastore is unsuitable; it has said nothing, and a caller that reads
// the two alike will report Mendel's problem as the user's.
type Outcome string

const (
	// OutcomeCompleted means the phase ran and the result is the answer.
	OutcomeCompleted Outcome = "completed"

	// OutcomeFailed means the adapter could not reach an answer. Failure says
	// why, for a person.
	OutcomeFailed Outcome = "failed"
)

// Result is what a job reports.
type Result struct {
	Phase        Phase   `json:"phase"`
	InvocationID string  `json:"invocation_id"`
	Outcome      Outcome `json:"outcome"`

	// Failure is why there is no answer, when Outcome is failed. Written for a
	// person: it reaches a page, and where the adapter was generated it is also
	// what a retry is given to work from.
	Failure string `json:"failure,omitempty"`

	Probe *ProbeResult `json:"probe,omitempty"`
	Admit *AdmitResult `json:"admit,omitempty"`
}

// ProbeResult is what the datastore turned out to be.
type ProbeResult struct {
	// Kind and Version identify the engine. Together they are what D61 re-gates
	// on: an adapter is written for an engine, and this is observed on every run
	// rather than inferred from files in the repository, which would have to be
	// maintained and would break quietly on a different layout.
	Kind    string `json:"kind"`
	Version string `json:"version"`

	// Capabilities is the adapter's self-report, which RequireForExperiments
	// already knows how to refuse.
	Capabilities Capabilities `json:"capabilities"`

	// CanProvision is whether a verification sandbox can be created, established
	// by attempting it rather than by reading a grant -- the same reasoning §16
	// applies to installing a cluster controller. CannotProvisionBecause says
	// what happened when it could not.
	CanProvision        bool   `json:"can_provision"`
	CannotProvisionBecause string `json:"cannot_provision_because,omitempty"`
}

// AdmitResult is everything Mendel needs to judge an admission, gathered while
// the adapter still had the connection.
//
// Shapes carries both stores because admission compares them: a verification
// datastore that has drifted from production gives a confident answer about the
// wrong schema, which is worse than no answer.
type AdmitResult struct {
	Delta Delta `json:"delta"`

	// LiveShapes and SandboxShapes are the touched collections as each store
	// has them, keyed by collection.
	LiveShapes    map[string]TableSchema `json:"live_shapes"`
	SandboxShapes map[string]TableSchema `json:"sandbox_shapes"`

	// Identities are the fields each touched collection is keyed by, empty
	// where it has none -- which admission refuses on, since an archive with no
	// identity could be captured and never put back.
	Identities map[string][]string `json:"identities"`

	// Forbidden is what the adapter's deny-list said, if anything.
	Forbidden []string `json:"forbidden,omitempty"`
}

// Validate reports why a Result cannot be believed, or "" when it can.
//
// A result is read from a job Mendel deployed but did not write, so it is
// checked rather than trusted -- the same posture as the conformance suite, at
// the other end of the wire.
func (r *Result) Validate(against *Instruction) string {
	if r == nil {
		return "no result"
	}
	if against != nil {
		if r.Phase != against.Phase {
			return fmt.Sprintf("this answers %q and %q was asked", r.Phase, against.Phase)
		}
		// A cross-check, not the source of truth: the token resolved which
		// invocation this is. A mismatch means a stale job reporting late, and
		// acting on it would answer a question with a previous run's findings.
		if r.InvocationID != against.InvocationID {
			return fmt.Sprintf("this answers invocation %q and %q was asked; a late reply from "+
				"an earlier run is not an answer to this one", r.InvocationID, against.InvocationID)
		}
	}

	switch r.Outcome {
	case OutcomeFailed:
		if strings.TrimSpace(r.Failure) == "" {
			return "a failed result must say why; it reaches a page, and a retry is given it to work from"
		}
		return ""
	case OutcomeCompleted:
	case "":
		return "a result must say whether it reached an answer; silence and a negative answer are " +
			"different things and a caller that reads them alike reports Mendel's problem as the user's"
	default:
		return fmt.Sprintf("%q is not an outcome", r.Outcome)
	}

	switch r.Phase {
	case PhaseProbe:
		if r.Probe == nil {
			return "a completed probe must carry what it found"
		}
		if strings.TrimSpace(r.Probe.Kind) == "" {
			return "a probe must name the datastore; it is what a decline says out loud and what " +
				"a cached adapter is re-gated against"
		}
		if !r.Probe.CanProvision && strings.TrimSpace(r.Probe.CannotProvisionBecause) == "" {
			return "a probe that cannot provision must say what happened when it tried"
		}
	case PhaseAdmit:
		if r.Admit == nil {
			return "a completed admission must carry what it found"
		}
		return r.Admit.validate()
	}
	return ""
}

func (a *AdmitResult) validate() string {
	// Nothing added is not an error here -- admission decides that, and this is
	// only checking the report is answerable. What is checked is that the report
	// is internally consistent, since a caller reading a shape that is absent
	// would find a difference the adapter never observed.
	for _, c := range TouchedCollections(a.Delta.Added) {
		if _, ok := a.LiveShapes[c]; !ok {
			return fmt.Sprintf("the delta touches %s and no shape of it was reported from "+
				"production, so there is nothing to compare the sandbox against", c)
		}
		if _, ok := a.SandboxShapes[c]; !ok {
			return fmt.Sprintf("the delta touches %s and no shape of it was reported from the "+
				"sandbox, so what the verification proved cannot be checked against production", c)
		}
		if _, ok := a.Identities[c]; !ok {
			return fmt.Sprintf("the delta touches %s and its identity was not reported; "+
				"absent and empty are different, and empty is what admission refuses on", c)
		}
	}
	return ""
}

// Answered reports whether this result carries a conclusion, as opposed to
// having failed or not arrived.
//
// The nil receiver is the case worth having a method for: a job that never
// reported is the commonest way to get here, and `r.Outcome == OutcomeCompleted`
// on a nil pointer is a panic where this is a question.
func (r *Result) Answered() bool { return r != nil && r.Outcome == OutcomeCompleted }

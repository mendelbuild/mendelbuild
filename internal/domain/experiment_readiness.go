package domain

import "fmt"

// Running an experiment on live traffic needs several things to be true of the
// user's project that are not true by default and are not implied by each other.
// They are project-scoped -- a cluster, a domain, a datastore -- so they belong
// in settings rather than in the Variation lifecycle, where they would be asked
// about at the moment they block something.
//
// None of these is implied by the deployment channel working. Mendel's own
// staging cluster serves external DNS and TLS on GKE and has no Gateway API at
// all, so a project can have demos deploying and everything green and still not
// be able to run an experiment.

// ReadinessStep and ReadinessState are the ladder vocabulary, shared with the
// domain ladder because it is the same idea: an ordered list of properties, each
// with a state and a reason, and a headline naming who is holding it up.
//
// Aliases rather than a rename, so the two can converge without churning the
// domain page while a design for the general form is still being written.
type ReadinessStep = DomainStep
type ReadinessState = DomainStepState

// Fact is what Mendel found when it looked: yes, no, or it could not tell.
//
// Three values rather than a bool, because the two ways of not being true are
// different and confusing them is a bug this project has already had. A
// certificate whose state could not be read is not a certificate that has not
// been issued: the first is Mendel's problem and the second is the user's, and
// showing the first as the second sends someone to fix something that is not
// broken.
type Fact int

const (
	FactUnknown Fact = iota
	FactTrue
	FactFalse
)

func (f Fact) String() string {
	switch f {
	case FactTrue:
		return "true"
	case FactFalse:
		return "false"
	}
	return "unknown"
}

// ExperimentObservation is what Mendel found about a project's readiness to run
// live-traffic experiments.
type ExperimentObservation struct {
	// GatewayAPI is whether the cluster can reconcile a Gateway at all. Not
	// implied by being on GKE: it is a cluster-level feature that is off unless
	// someone turned it on.
	GatewayAPI Fact

	// EnableGatewayCommand is the one command that makes it true, when Mendel
	// knows enough to state it.
	EnableGatewayCommand string

	// CookieMatching is whether a controller is installed that can match a
	// cookie. Separate from GatewayAPI because it is a separate property with a
	// separate remedy, and because GKE's own class does not have it: it matches
	// headers Exact only, and an Exact match on a Cookie header cannot pick one
	// cookie out of the several a visitor carries.
	CookieMatching Fact

	// CanInstallController is whether Mendel's own credentials may install it.
	// Asked of the cluster rather than inferred from a role name, because a
	// service account's rights are the union of IAM and RBAC.
	CanInstallController  Fact
	InstallControllerHint string

	// CloudShellURL is a browser terminal, signed in as whoever follows it, for
	// the case a person has to run the command themselves. Nothing to install.
	CloudShellURL string

	// ProdHostname is whether production answers at a name. Without one there is
	// no HTTPRoute -- Mendel's Gateway is shared across deployments and the
	// hostname is what tells one deployment's traffic from another's -- so there
	// is nothing for per-Arm matching to attach to.
	ProdHostname Fact
	ProdHost     string

	// ProdHTTPS is whether that name serves a certificate that covers it. Not
	// required by the routing mechanism: an assignment cookie works over plain
	// http. It matters because such a cookie cannot be Secure, so it can be
	// rewritten in transit, and a participant who can choose their own Arm makes
	// the comparison quietly meaningless.
	ProdHTTPS Fact

	// SchemaChanges is whether any Arm of the experiment in question declares a
	// migration. Unknown on the settings page, where there is no experiment yet
	// and the honest answer is "it depends what you run".
	//
	// An experiment that changes no schema needs none of the datastore
	// machinery: it is a presentation-only comparison, and demanding a database
	// of a project that has none would block the simplest experiment there is on
	// the requirements of the hardest.
	SchemaChanges Fact

	// VerifyDatastore is whether a non-production datastore has been given.
	// Additivity is settled by running the migration and diffing, and running it
	// against production is not free even rolled back.
	VerifyDatastore Fact
	VerifyReachable Fact
}

// ExperimentReadiness is every property that must hold, in the order it makes
// sense to establish them.
//
// One assessment of the "experiment" functional area, as DomainReadiness is of
// its own. The conditions and their wording live in
// functional_area_experiment.go; what is left here is the observation this is
// judged against, and the rendering the page already expects.
func ExperimentReadiness(obs ExperimentObservation) []ReadinessStep {
	a := FunctionalAreas().Assess(AreaExperiment, Observations{
		ProjectDomain: &ProjectDomain{},
		Experiment:    obs,
	})

	steps := make([]ReadinessStep, 0, len(a.Steps)+len(a.Warnings))
	for _, s := range a.Steps {
		steps = append(steps, readinessStep(s, false))
	}
	// Warnings render on the same ladder and are marked rather than separated:
	// a reader wants one list of what is true of their project, with the thing
	// that does not stop them plainly labelled.
	for _, s := range a.Warnings {
		steps = append(steps, readinessStep(s, true))
	}
	return steps
}

// readinessStep renders one condition in the ladder's vocabulary.
func readinessStep(s Step, advisory bool) ReadinessStep {
	return ReadinessStep{
		Name:     s.Name,
		State:    domainStepState(s),
		Detail:   s.Detail,
		Advisory: advisory,
	}
}

// ExperimentHeadline states where things stand and who is holding it up.
//
// Advisory steps are counted as warnings rather than allowed to block. https is
// always one -- a real concern about the integrity of the assignment and a poor
// reason to refuse to run -- and the datastore is one until an experiment
// actually declares a migration.
func ExperimentHeadline(steps []ReadinessStep) (headline string, blocked bool) {
	warnings := 0
	for _, s := range steps {
		if s.Advisory {
			if s.State == StepYourMove {
				warnings++
			}
			continue
		}
		switch s.State {
		case StepYourMove, StepBlocked:
			return s.Name, true
		case StepChecking:
			return "Checking what this project can do", false
		}
	}
	if warnings > 0 {
		return "Ready, with one warning", false
	}
	return "Ready to run live-traffic experiments", false
}

// ExperimentBlockers is what still has to be true, for a caller that needs to
// refuse rather than to render.
func ExperimentBlockers(steps []ReadinessStep) []string {
	var out []string
	for _, s := range steps {
		if s.Advisory {
			continue
		}
		if s.State == StepYourMove || s.State == StepBlocked {
			out = append(out, fmt.Sprintf("%s: %s", s.Name, s.Detail))
		}
	}
	return out
}

// Fingerprint is a short string that changes when anything on this page would
// look different.
//
// Cheaper than diffing rendered HTML and more reliable than a timestamp: a
// background refresh that finds nothing new leaves it identical, so a watching
// page reloads exactly when there is something to see and not on a timer.
func (o ExperimentObservation) Fingerprint() string {
	return fmt.Sprintf("%v/%v/%v/%v/%v/%v/%s",
		o.GatewayAPI, o.CookieMatching, o.CanInstallController,
		o.ProdHostname, o.ProdHTTPS, o.VerifyDatastore, o.ProdHost)
}

// detailOr prefers the observed value over a generic sentence, since a reader
// looking at their own hostname learns more than one reading that they have one.
func detailOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

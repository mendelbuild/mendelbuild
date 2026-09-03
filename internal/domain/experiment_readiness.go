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

	// VerifyDatastore is whether a non-production datastore has been given.
	// Additivity is settled by running the migration and diffing, and running it
	// against production is not free even rolled back.
	VerifyDatastore Fact
	VerifyReachable Fact
}

// ExperimentReadiness is every property that must hold, in the order it makes
// sense to establish them.
func ExperimentReadiness(obs ExperimentObservation) []ReadinessStep {
	steps := make([]ReadinessStep, 0, 5)

	steps = append(steps, factStep(obs.GatewayAPI,
		"Cluster can route per experiment arm",
		"Gateway API is enabled on the cluster.",
		"Gateway API is not enabled, so nothing can reconcile the routes an experiment needs. "+
			"Being on GKE does not imply it; it is off until someone turns it on.",
		"Mendel could not reach the cluster to check."))

	steps = append(steps, factStep(obs.ProdHostname,
		"Production answers at a name",
		detailOr(obs.ProdHost, "Production has a hostname."),
		"Production has no hostname. Mendel runs one gateway for all its deployments and "+
			"tells their traffic apart by hostname, so without one there is no route to attach "+
			"arm matching to. Set a production subdomain on the Domain tab.",
		"Mendel could not read this project's domain settings."))

	steps = append(steps, factStep(obs.ProdHTTPS,
		"That name serves https",
		"Traffic to production is encrypted, so the assignment cookie can be Secure.",
		"Production answers over http only. An experiment can run, but its assignment cookie "+
			"cannot be marked Secure, so it can be rewritten in transit and a participant could "+
			"choose their own arm.",
		"Mendel could not determine the certificate state."))

	steps = append(steps, factStep(obs.VerifyDatastore,
		"A non-production datastore to verify against",
		"Migrations are proved additive here rather than against production.",
		"No verification datastore. Whether a migration only adds is settled by running it and "+
			"diffing, and doing that against production takes real locks on live tables.",
		"Mendel could not read the stored connection."))

	// Only worth asking once there is something to reach.
	reach := ReadinessStep{
		Name:  "That datastore is reachable",
		State: StepBlocked,
		Detail: "Checked once a connection has been given.",
	}
	if obs.VerifyDatastore == FactTrue {
		reach = factStep(obs.VerifyReachable,
			"That datastore is reachable",
			"Mendel connected to it and can read its structure.",
			"Mendel could not connect, so nothing can be verified against it.",
			"Not checked yet.")
	}
	steps = append(steps, reach)

	return steps
}

// factStep renders one property, keeping "could not tell" distinct from "no".
func factStep(f Fact, name, whenTrue, whenFalse, whenUnknown string) ReadinessStep {
	switch f {
	case FactTrue:
		return ReadinessStep{Name: name, State: StepDone, Detail: whenTrue}
	case FactFalse:
		return ReadinessStep{Name: name, State: StepYourMove, Detail: whenFalse}
	default:
		return ReadinessStep{Name: name, State: StepChecking, Detail: whenUnknown}
	}
}

func detailOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// ExperimentHeadline states where things stand and who is holding it up.
//
// HTTPS is deliberately not allowed to hold it up. It is a real concern about
// the integrity of the assignment and a poor reason to refuse to run: reporting
// it as a warning says so, where blocking on it would overstate the case.
func ExperimentHeadline(steps []ReadinessStep) (headline string, blocked bool) {
	warnings := 0
	for _, s := range steps {
		if s.Name == "That name serves https" {
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
		if s.Name == "That name serves https" {
			continue // A warning, not a blocker.
		}
		if s.State == StepYourMove || s.State == StepBlocked {
			out = append(out, fmt.Sprintf("%s: %s", s.Name, s.Detail))
		}
	}
	return out
}

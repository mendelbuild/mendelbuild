package web

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/assigner"
	"github.com/bhs/mendelbuild/internal/domain"
)

// What a reader needs to see about a live experiment, assembled in one place.
//
// This lives on the Hop's page rather than in project settings. The settings tab
// answers "can this project run experiments at all" -- a cluster, a domain, a
// datastore, none of them about any particular experiment. Everything here is
// about one Hop: its Variations are the Arms, its branches are what the Arms were
// built from, and the split being sampled is the split between its Variations.
// Managing it from a settings page meant picking the Hop out of a dropdown to
// operate on it, which put the controls a page away from the thing they control.

// ArmView is one Arm, with everything a reader wants to know about it.
type ArmView struct {
	Arm       domain.ExperimentArm
	Variation *domain.Variation
	Build     domain.ArmBuild

	// Reach is what to run to see this Arm deliberately, rather than waiting to
	// be assigned to it and hoping. The gap this closes is a real one: the first
	// person to run an experiment could not tell the Arms apart and gave them
	// different background colours to do it.
	Reach string
}

// Mainline reports whether this is the control.
func (a ArmView) Mainline() bool { return a.Arm.IsMainline() }

// Name is what to call this Arm in prose: its Variation's name where there is
// one, since "faster-checkout-a3f21c" is a slug and not a name.
func (a ArmView) Name() string {
	if a.Variation != nil && a.Variation.Name != "" {
		return a.Variation.Name
	}
	if a.Arm.IsMainline() {
		return "Mainline"
	}
	return a.Arm.Slug
}

// ExperimentView is one Hop's experiment.
type ExperimentView struct {
	Experiment *domain.Experiment
	Arms       []ArmView

	// ProdHost is where the experiment serves. Named rather than assumed,
	// because everything below -- the reach commands, the sample -- is only
	// meaningful against the host the routing was actually attached to.
	ProdHost string

	// Failure is the last thing that went wrong, when it is still the last thing
	// that happened. A failure followed by a successful start is history rather
	// than a warning.
	Failure *domain.FailureReport

	// Sample is the most recent observed split, or nil if nobody has asked for
	// one this session. Never persisted: a sample is meaningful for about as
	// long as somebody is looking at it, and a stale one read as current is the
	// mistake this whole area exists to correct.
	Sample *SplitSample

	// Blockers is what the project still needs before an experiment can run,
	// from the same conditions the settings page renders -- the same strings, so
	// the two can never disagree about why something is refused.
	Blockers []string

	// Checking is true while readiness has not been established yet, so the page
	// can say "not checked" rather than showing an empty blocker list as though
	// it were a clean bill of health.
	Checking bool
}

// Ready reports whether this experiment could be started now.
func (v *ExperimentView) Ready() bool {
	return v != nil && !v.Checking && len(v.Blockers) == 0
}

// Running reports whether it is serving traffic.
func (v *ExperimentView) Running() bool {
	return v != nil && v.Experiment != nil && v.Experiment.Status == domain.ExperimentRunning
}

// StaleArms is how many Arms are behind their branch.
//
// Only definitely-behind Arms are counted. An Arm whose branch Mendel could not
// read is unknown, and counting it here would put a "3 arms are out of date"
// badge on a page where the truth is that Mendel did not manage to look.
func (v *ExperimentView) StaleArms() int {
	n := 0
	for _, a := range v.Arms {
		if a.Build.Stale() {
			n++
		}
	}
	return n
}

// ArmHeaderName and CookieName are surfaced so the page can name them exactly
// once and be sure it is naming the real ones.
func (v *ExperimentView) ArmHeaderName() string { return ArmHeader }
func (v *ExperimentView) CookieName() string    { return assigner.CookieName }

// SampleCommand is the equivalent of this page's sample button, for somebody who
// would rather run it themselves or wants it in a script.
//
// Shown rather than hidden: the button exists because running this by hand is an
// unreasonable thing to require, not because knowing how is bad for you.
func (v *ExperimentView) SampleCommand() string {
	if v.ProdHost == "" {
		return ""
	}
	return fmt.Sprintf(
		"for i in $(seq %d); do curl -sI https://%s/ | grep -i '^%s:'; done | sort | uniq -c",
		SplitSampleSize, v.ProdHost, ArmHeader)
}

// experimentViewFor assembles the whole picture for one Hop.
//
// Returns nil when the Hop has no experiment, which is the ordinary case: most
// Hops never run one, and a page should say "this could be an experiment" rather
// than render an empty one.
func (s *Server) experimentViewFor(ctx context.Context, projectID, hopID uuid.UUID) *ExperimentView {
	exp, err := s.db.GetExperimentForHop(ctx, hopID)
	if err != nil || exp == nil {
		return nil
	}

	view := &ExperimentView{Experiment: exp, Sample: s.lastSplitSample(exp.ID)}

	if pd, err := s.db.GetProjectDomain(ctx, projectID); err == nil && pd != nil {
		view.ProdHost = pd.ProdHost()
	}
	view.Failure = s.latestFailure(ctx, exp.ID)

	obs, observedAt := s.experimentObservationFor(projectID)
	view.Checking = observedAt.IsZero()
	view.Blockers = domain.ExperimentBlockers(domain.ExperimentReadiness(obs))

	builds := s.armBuildsFor(ctx, exp)
	for _, arm := range exp.Arms {
		av := ArmView{Arm: arm, Build: builds[arm.ID]}
		if arm.VariationID != nil {
			if v, err := s.db.GetVariation(ctx, *arm.VariationID); err == nil {
				av.Variation = v
			}
		}
		if view.ProdHost != "" {
			// A request carrying the Arm's cookie is routed to that Arm by the
			// same rule that routes an assigned visitor, so this is the Arm as
			// a real visitor sees it and not a special preview path.
			av.Reach = fmt.Sprintf("curl -si -H 'Cookie: %s=%s' https://%s/ | head -20",
				assigner.CookieName, arm.Slug, view.ProdHost)
		}
		view.Arms = append(view.Arms, av)
	}
	return view
}

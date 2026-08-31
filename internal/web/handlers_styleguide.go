package web

import (
	"net/http"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The styleguide is the review surface for the design system: every component
// in every tone, and every lifecycle state the app can actually produce.
//
// The lifecycle half is the point. Those ribbons are not mock-ups — the handler
// constructs real domain values and runs them through the same HopLifecycle,
// VariationLifecycle, DecisionLifecycle, and OnboardingLifecycle the live pages
// call. So the page is a state inventory that cannot drift: add a status to the
// domain without giving it a sensible ribbon and it shows up here looking
// wrong, next to all the ones that look right.

// Specimen is one rendered example with a note saying what it is for.
type Specimen struct {
	Label   string
	Note    string
	Ribbon  domain.Ribbon
	Actions []PageAction
}

// View wraps the specimen's Ribbon for the partial, which takes a *RibbonView.
func (s Specimen) View() *RibbonView { return ribbonView(s.Ribbon, s.Actions...) }

// SpecimenGroup is a set of specimens under one heading.
type SpecimenGroup struct {
	Title     string
	Note      string
	Specimens []Specimen
}

func (s *Server) handleStyleguide(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Title":      "Styleguide",
		"Tones":      []string{"neutral", "progress", "waiting", "success", "warning", "failure"},
		"Lifecycles": lifecycleSpecimens(),
		"LogPanel":   styleguideLogPanel(),
	}
	if err := s.renderPageFor(w, r, "styleguide.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func lifecycleSpecimens() []SpecimenGroup {
	return []SpecimenGroup{
		hopSpecimens(),
		variationSpecimens(),
		decisionSpecimens(),
		onboardingSpecimens(),
	}
}

func hopSpecimens() SpecimenGroup {
	hop := func(st domain.HopStatus) *domain.Hop { return &domain.Hop{Status: st} }
	v := func(st domain.VariationStatus) domain.Variation { return domain.Variation{Status: st} }

	return SpecimenGroup{
		Title: "Hop",
		Note: "One track, because a Hop really is sequential. The three terminal " +
			"statuses share a stage and differ only in tone and label.",
		Specimens: []Specimen{
			{Label: `pending`, Note: "Blocked on upstream Hops.",
				Ribbon: domain.HopLifecycle(hop(domain.HopStatusPending), nil)},
			{Label: `active, no variations yet`, Note: "Mendel is drafting approaches.",
				Ribbon: domain.HopLifecycle(hop(domain.HopStatusActive), []domain.Variation{})},
			{Label: `active, building`, Note: "Code generation underway.",
				Ribbon: domain.HopLifecycle(hop(domain.HopStatusActive), []domain.Variation{
					v(domain.VariationStatusCreating), v(domain.VariationStatusCreating)})},
			{Label: `active, blocked`, Note: "A variation needs something from the user — the whole Hop turns to their move.",
				Ribbon: domain.HopLifecycle(hop(domain.HopStatusActive), []domain.Variation{
					v(domain.VariationStatusBlocked), v(domain.VariationStatusCreating)})},
			{Label: `active, ready`, Note: "Built and awaiting comparison.",
				Ribbon: domain.HopLifecycle(hop(domain.HopStatusActive), []domain.Variation{
					v(domain.VariationStatusPending), v(domain.VariationStatusPending)})},
			{Label: `active, out of candidates`, Note: "Every variation failed or was eliminated. Warning tone, not failure: the Hop is recoverable.",
				Ribbon: domain.HopLifecycle(hop(domain.HopStatusActive), []domain.Variation{
					v(domain.VariationStatusError), v(domain.VariationStatusPruned)})},
			{Label: `selecting`, Note: "The user's move.",
				Ribbon: domain.HopLifecycle(hop(domain.HopStatusSelecting), nil)},
			{Label: `completed`, Ribbon: domain.HopLifecycle(hop(domain.HopStatusCompleted), nil)},
			{Label: `rejected`, Note: "Terminal and unsuccessful — must not look like `completed`.",
				Ribbon: domain.HopLifecycle(hop(domain.HopStatusRejected), nil)},
			{Label: `abandoned`, Note: "Terminal and neither successful nor failed.",
				Ribbon: domain.HopLifecycle(hop(domain.HopStatusAbandoned), nil)},
			{Label: `unrecognized`, Note: "The degradation path. A bare enum must never reach the page.",
				Ribbon: domain.HopLifecycle(hop("wat"), nil)},
		},
	}
}

func variationSpecimens() SpecimenGroup {
	demoHop := &domain.Hop{}
	prodHop := &domain.Hop{RequiresProduction: true}
	vr := func(st domain.VariationStatus) *domain.Variation { return &domain.Variation{Status: st} }
	rev := func(st domain.VariationRevisionStatus) domain.VariationRevision {
		return domain.VariationRevision{Status: st}
	}
	done := []domain.VariationRevision{rev(domain.VariationRevisionStatusCompleted)}

	specs := []Specimen{
		{Label: `creating`, Note: "First build.",
			Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusCreating), nil, demoHop)},
		{Label: `creating, with a revision in flight`, Note: "Same status column, entirely different situation. Only the revision rows can tell them apart, which is why Refine is its own track.",
			Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusCreating),
				[]domain.VariationRevision{rev(domain.VariationRevisionStatusInProgress)}, demoHop)},
		{Label: `blocked`, Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusBlocked), nil, demoHop)},
		{Label: `pending`, Note: "Built, awaiting comparison.",
			Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusPending), nil, demoHop)},
		{Label: `pending, after a revision`, Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusPending), done, demoHop)},
		{Label: `pending, production Hop`, Note: "Same status; the Trial track promises live traffic rather than a demo, because that is what this Hop actually asks for.",
			Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusPending), nil, prodHop)},
		{Label: `migrating`, Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusMigrating), nil, prodHop)},
		{Label: `active`, Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusActive), nil, prodHop)},
		{Label: `draining`, Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusDraining), nil, prodHop)},
		{Label: `error`, Note: "Retryable infrastructure failure.",
			Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusError), nil, demoHop)},
		{Label: `terminated`, Note: "Not retryable, and not a success.",
			Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusTerminated), nil, demoHop)},
		{Label: `pruned`, Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusPruned), nil, demoHop)},
		{Label: `rejected`, Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusRejected), nil, demoHop)},
		{Label: `selected`, Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusSelected), nil, demoHop)},
		{Label: `merged`, Ribbon: domain.VariationLifecycle(vr(domain.VariationStatusMerged), nil, demoHop)},
	}

	return SpecimenGroup{
		Title: "Variation",
		Note: "Four concurrent tracks, not one sequence. A Variation can be built " +
			"and live and unjudged at the same moment, so a single stepper would misstate it.",
		Specimens: specs,
	}
}

func decisionSpecimens() SpecimenGroup {
	ir := func(st domain.InputRequestStatus, k domain.InputRequestKind) *domain.InputRequest {
		return &domain.InputRequest{Status: st, Kind: k}
	}
	specs := []Specimen{
		{Label: `needs_assignment`, Ribbon: domain.DecisionLifecycle(ir(domain.InputRequestStatusNeedsAssignment, domain.InputRequestKindPassFail))},
		{Label: `resolved`, Ribbon: domain.DecisionLifecycle(ir(domain.InputRequestStatusResolved, domain.InputRequestKindPassFail))},
	}
	// Every kind, assigned: the ask is what differs, and it is the whole point
	// of the ribbon on a decision page.
	for _, k := range []domain.InputRequestKind{
		domain.InputRequestKindPassFail,
		domain.InputRequestKindChooseOne,
		domain.InputRequestKindChooseMany,
		domain.InputRequestKindRoadmapReview,
		domain.InputRequestKindVariationReview,
		domain.InputRequestKindVariationSelection,
		domain.InputRequestKindCredentialRequest,
		domain.InputRequestKindManualSetup,
		domain.InputRequestKindConfirmation,
		domain.InputRequestKindHostingPlatform,
	} {
		specs = append(specs, Specimen{
			Label:  "assigned — " + string(k),
			Note:   domain.DecisionKindLabel(k),
			Ribbon: domain.DecisionLifecycle(ir(domain.InputRequestStatusAssigned, k)),
		})
	}
	return SpecimenGroup{
		Title: "Decision",
		Note:  "DESIGN.md calls this a Decision; the schema calls it an InputRequest. User-facing strings use the word that explains itself.",
		Specimens: specs,
	}
}

func onboardingSpecimens() SpecimenGroup {
	st := func(f func(*domain.OnboardingState)) domain.OnboardingState {
		s := domain.OnboardingState{}
		f(&s)
		return s
	}
	return SpecimenGroup{
		Title: "Getting started",
		Note:  "The first three screens a new project sees, before any Mendel vocabulary has been learned.",
		Specimens: []Specimen{
			{Label: "nothing yet", Ribbon: domain.OnboardingLifecycle(st(func(s *domain.OnboardingState) {}))},
			{Label: "drafting OKRs", Ribbon: domain.OnboardingLifecycle(st(func(s *domain.OnboardingState) {
				s.HasStrategy, s.Drafting = true, true
			}))},
			{Label: "draft failed", Note: "Mendel's move became the user's; without this both read as 'no objectives yet'.",
				Ribbon: domain.OnboardingLifecycle(st(func(s *domain.OnboardingState) {
					s.HasStrategy, s.DraftFailed = true, true
				}))},
			{Label: "OKRs awaiting approval", Ribbon: domain.OnboardingLifecycle(st(func(s *domain.OnboardingState) {
				s.HasStrategy, s.HasDraftOKRs = true, true
			}))},
			{Label: "roadmap awaiting approval", Ribbon: domain.OnboardingLifecycle(st(func(s *domain.OnboardingState) {
				s.HasStrategy, s.HasDraftOKRs, s.OKRsApproved, s.RoadmapPending = true, true, true, true
			}))},
			{Label: "roadmap approved, no repository", Note: "Mendel cannot start without somewhere to write code.",
				Ribbon: domain.OnboardingLifecycle(st(func(s *domain.OnboardingState) {
					s.HasStrategy, s.HasDraftOKRs, s.OKRsApproved, s.HasHops = true, true, true, true
				}))},
			{Label: "ready to explore", Ribbon: domain.OnboardingLifecycle(st(func(s *domain.OnboardingState) {
				s.HasStrategy, s.HasDraftOKRs, s.OKRsApproved, s.HasHops, s.RepoConnected = true, true, true, true, true
			}))},
		},
	}
}

func styleguideLogPanel() *LogPanel {
	// A fixed instant, so the page is byte-identical between renders.
	at := time.Date(2026, 8, 31, 9, 14, 2, 0, time.UTC)
	return &LogPanel{
		DOMID: "styleguide-log",
		Lines: []LogLine{
			{LoggedAt: at, Level: "info", Message: "Cloning repository at main@a1b2c3d"},
			{LoggedAt: at.Add(17 * time.Second), Level: "milestone", Message: "Dependencies installed"},
			{LoggedAt: at.Add(158 * time.Second), Level: "error", Message: "npm test exited 1: 2 failing in auth.spec.js"},
			{LoggedAt: at.Add(159 * time.Second), Level: "debug", Message: "container exit code 1"},
		},
	}
}

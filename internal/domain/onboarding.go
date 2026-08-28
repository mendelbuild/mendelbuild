package domain

// Onboarding lifecycle: the path from "I have an idea" to "Mendel is exploring
// approaches", as a single track.
//
//	Describe -> Review OKRs -> Review roadmap -> Explore approaches
//
// This reuses the Ribbon vocabulary for the same reason Hops and Variations do:
// a newcomer's first question is "is this my move?", and the ribbon answers it
// without them having to learn what a Hop is first.
//
// The ribbon retires once the first Variation exists. At that point the project
// is running normally and the per-Hop ribbons say more than this one does.

const (
	OnboardingTrack = "onboarding"

	OnboardingStageDescribe = "describe"
	OnboardingStageOKRs     = "okrs"
	OnboardingStageRoadmap  = "roadmap"
	OnboardingStageBuild    = "build"
)

var (
	onboardingStageKeys   = []string{OnboardingStageDescribe, OnboardingStageOKRs, OnboardingStageRoadmap, OnboardingStageBuild}
	onboardingStageLabels = []string{"Describe the project", "Approve objectives", "Approve roadmap", "Explore approaches"}
)

// OnboardingState is everything the ribbon needs, gathered from the project's
// actual rows rather than stored as a status column. Only OKRsApproved is
// persisted state; the rest is derived, so the ribbon cannot drift out of sync
// with what the project really contains.
type OnboardingState struct {
	HasStrategy    bool // A strategy row exists
	HasDraftOKRs   bool // At least one objective exists
	OKRsApproved   bool // A human signed off on them
	RoadmapPending bool // An unresolved roadmap_review input request is waiting
	HasHops        bool // The roadmap was approved and Hops were created
	HasVariations  bool // Work on the first Hop has begun
	RepoConnected  bool // Repository URL and auth token are configured
}

// Complete reports whether onboarding is over and the ribbon should retire.
func (st OnboardingState) Complete() bool { return st.HasVariations }

// OnboardingLifecycle maps a project's setup progress onto a Ribbon.
func OnboardingLifecycle(st OnboardingState) Ribbon {
	r := Ribbon{Subject: "Getting started"}

	track := func(current int, tone Tone) []Track {
		return []Track{stageSeq(OnboardingTrack, "Setup", onboardingStageKeys, onboardingStageLabels, current, tone)}
	}

	// The repository is needed before any code can be written, but not before
	// the roadmap is drawn, so it qualifies the next action rather than
	// blocking a stage of its own.
	repoNote := ""
	if !st.RepoConnected {
		repoNote = " Mendel also needs a repository to write code into — there is a request for it in Input Needed."
	}

	switch {
	case !st.HasStrategy:
		r.Tracks = track(0, ToneWaiting)
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "Tell Mendel what you want to build"
		r.NextAction = "Describe the project, when you need it, and what you are willing to spend. Mendel drafts objectives from that."

	case !st.HasDraftOKRs:
		r.Tracks = track(1, ToneWaiting)
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "This project has no objectives yet"
		r.NextAction = "Mendel has nothing to plan against. Draft objectives from your brief, or write them yourself in the OKR editor."

	case !st.OKRsApproved:
		r.Tracks = track(1, ToneWaiting)
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "Draft objectives are ready for you"
		r.NextAction = "Read the drafted objectives and key results, change anything that is wrong, then approve them. Nothing is built until you do."

	case st.HasHops && !st.HasVariations:
		r.Tracks = track(3, ToneProgress)
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		r.Headline = "Roadmap approved — starting the first Hop"
		r.NextAction = "Mendel is proposing candidate approaches for the first Hop. You will be asked to review them." + repoNote

	case st.RoadmapPending:
		r.Tracks = track(2, ToneWaiting)
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "Your roadmap is ready to review"
		r.NextAction = "Open the roadmap review under Input Needed. Approve it and Mendel starts on the first Hop." + repoNote

	default:
		// OKRs approved, no roadmap review waiting, no Hops: the proposer is
		// running in the background.
		r.Tracks = track(2, ToneProgress)
		r.Tone, r.WaitingOn = ToneProgress, ActorMendel
		r.Headline = "Drafting your roadmap"
		r.NextAction = "Mendel is breaking your objectives into a sequence of Hops. This usually takes under a minute; the review will appear under Input Needed." + repoNote
	}

	return r
}

package domain

import "fmt"

// Decision lifecycle: the InputRequest track.
//
//	Routing -> Assigned -> In progress -> Resolved
//
// DESIGN.md section 2.3 calls this primitive a "Decision" and its queue the
// "Decision Queue"; the schema calls it an InputRequest. The UI says "Input
// Needed", and that is the word every user-facing string here uses.
//
// The reason is that the queue carries more than decisions. Mendel also asks
// for API keys, for a redirect URI to be added in someone else's console, for a
// repository to point at. Calling those a "Decision" overstates them: nothing
// is being decided, something is being supplied. "Input Needed" covers both,
// and the thing it is wrong about -- that a genuine choice is merely "input" --
// is the smaller error.

const (
	DecisionTrackProgress = "progress"
)

var (
	decisionStageKeys   = []string{"routing", "assigned", "in_progress", "resolved"}
	decisionStageLabels = []string{"Raised", "Waiting for you", "Being worked on", "Resolved"}
)

// DecisionLifecycle maps an InputRequest onto a Ribbon.
//
// A note on WaitingOn. DESIGN.md section 2.3 describes a queue that routes each
// request to a human or an agent, and the status column has a
// `needs_assignment` state for the moment before that has happened. But nothing
// in the codebase performs that routing: every request is created
// `needs_assignment` and the only other status it ever reaches is `resolved`.
// `assigned` and `accepted` are, today, dead states.
//
// So an unrouted request is not being routed. It is waiting on a person, and
// saying otherwise sends them away from the only thing that will unblock it —
// which is exactly what happened: two open requests reported themselves as
// "Mendel is working on this" on a project where Mendel was doing nothing of
// the kind.
//
// This reports what actually happens. When a router exists, `needs_assignment`
// becomes a real transient state and this branch should describe it again.
func DecisionLifecycle(ir *InputRequest) Ribbon {
	r := Ribbon{Subject: "Input needed"}

	switch ir.Status {
	case InputRequestStatusNeedsAssignment:
		// Stage 1 ("Assigned"), not stage 0: with no router, a new request is
		// already as routed as it is going to get.
		r.Tracks = []Track{stageSeq(DecisionTrackProgress, "Progress", decisionStageKeys, decisionStageLabels, 1, ToneWaiting)}
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "Waiting for you"
		r.NextAction = DecisionAsk(ir.Kind)

	case InputRequestStatusAssigned:
		r.Tracks = []Track{stageSeq(DecisionTrackProgress, "Progress", decisionStageKeys, decisionStageLabels, 1, ToneWaiting)}
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "Waiting for you"
		r.NextAction = DecisionAsk(ir.Kind)

	case InputRequestStatusAccepted:
		r.Tracks = []Track{stageSeq(DecisionTrackProgress, "Progress", decisionStageKeys, decisionStageLabels, 2, ToneWaiting)}
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "In progress"
		r.NextAction = DecisionAsk(ir.Kind)

	case InputRequestStatusResolved:
		t := stageSeq(DecisionTrackProgress, "Progress", decisionStageKeys, decisionStageLabels, 3, ToneSuccess)
		t.Stages[3].State = StageDone
		r.Tracks = []Track{t}
		r.Tone, r.WaitingOn = ToneSuccess, ActorNobody
		r.Headline = "Resolved"
		r.NextAction = "Nothing further. Mendel has what it needed and has moved on."

	default:
		r.Tracks = []Track{stageSeq(DecisionTrackProgress, "Progress", decisionStageKeys, decisionStageLabels, -1, ToneNeutral)}
		r.Tone, r.WaitingOn = ToneNeutral, ActorMendel
		r.Headline = fmt.Sprintf("Unrecognized state (%s)", ir.Status)
		r.NextAction = "Mendel does not recognize the state of this request. This is a bug worth reporting."
	}

	return r
}

// DecisionKindLabel renders an InputRequestKind as a short human phrase. The
// queue used to print the raw enum (e.g. "variation_selection"), which is
// unreadable to anyone who has not seen the schema.
func DecisionKindLabel(k InputRequestKind) string {
	switch k {
	case InputRequestKindPassFail:
		return "Approve or reject"
	case InputRequestKindChooseOne:
		return "Pick one"
	case InputRequestKindChooseMany:
		return "Pick any"
	case InputRequestKindRoadmapReview:
		return "Roadmap review"
	case InputRequestKindVariationReview:
		return "Approach review"
	case InputRequestKindVariationSelection:
		return "Pick a winner"
	case InputRequestKindCredentialRequest:
		return "Credential needed"
	case InputRequestKindManualSetup:
		return "Manual setup"
	case InputRequestKindConfirmation:
		return "Confirmation"
	case InputRequestKindHostingPlatform:
		return "Hosting choice"
	default:
		return string(k)
	}
}

// DecisionAsk states, in plain English, what the user is being asked to do.
func DecisionAsk(k InputRequestKind) string {
	switch k {
	case InputRequestKindPassFail:
		return "Approve or reject this, and say why."
	case InputRequestKindChooseOne:
		return "Choose one of the options."
	case InputRequestKindChooseMany:
		return "Choose any options that apply."
	case InputRequestKindRoadmapReview:
		return "Review the proposed Hops. Edit them, discuss with the agent, then approve or reject."
	case InputRequestKindVariationReview:
		return "Review the approaches Mendel wants to try before any code is written."
	case InputRequestKindVariationSelection:
		return "Compare the finished variations and pick the one to merge, or ask for more."
	case InputRequestKindCredentialRequest:
		return "Provide the credential so the blocked work can continue."
	case InputRequestKindManualSetup:
		return "Complete the setup step outside Mendel, then mark it done."
	case InputRequestKindConfirmation:
		return "Confirm whether Mendel should proceed."
	case InputRequestKindHostingPlatform:
		return "Choose where Mendel should deploy demos for this project."
	default:
		return "Provide the input Mendel is asking for."
	}
}

// DecisionImportance renders the 0.0-1.0 importance score as a word. The queue
// currently prints "0.72", which carries no meaning without the scale.
func DecisionImportance(score float64) string {
	switch {
	case score >= 0.75:
		return "High"
	case score >= 0.4:
		return "Medium"
	default:
		return "Low"
	}
}

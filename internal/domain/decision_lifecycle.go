package domain

import "fmt"

// Decision lifecycle: the InputRequest track.
//
//	Routing -> Assigned -> In progress -> Resolved
//
// DESIGN.md section 2.3 calls this primitive a "Decision" and its queue the
// "Decision Queue"; the schema and UI call it an InputRequest and "Input
// Needed". This file uses the DESIGN.md vocabulary in every user-facing string,
// because "Decision" explains itself and "Input Request" does not.

const (
	DecisionTrackProgress = "progress"
)

var (
	decisionStageKeys   = []string{"routing", "assigned", "in_progress", "resolved"}
	decisionStageLabels = []string{"Routing", "Assigned", "Being worked on", "Resolved"}
)

// DecisionLifecycle maps an InputRequest onto a Ribbon.
//
// A note on WaitingOn: the queue routes to either a human or an agent, but the
// record does not reliably say which. Anything already assigned is treated as
// waiting on a human, since that is what surfaces in the human-facing queue.
func DecisionLifecycle(ir *InputRequest) Ribbon {
	r := Ribbon{Subject: "Decision"}

	switch ir.Status {
	case InputRequestStatusNeedsAssignment:
		r.Tracks = []Track{stageSeq(DecisionTrackProgress, "Decision progress", decisionStageKeys, decisionStageLabels, 0, ToneProgress)}
		r.Tone, r.WaitingOn = ToneNeutral, ActorMendel
		r.Headline = "Being routed"
		r.NextAction = "Mendel is deciding whether this needs a person or can be handled by an agent."

	case InputRequestStatusAssigned:
		r.Tracks = []Track{stageSeq(DecisionTrackProgress, "Decision progress", decisionStageKeys, decisionStageLabels, 1, ToneWaiting)}
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "Waiting for you"
		r.NextAction = DecisionAsk(ir.Kind)

	case InputRequestStatusAccepted:
		r.Tracks = []Track{stageSeq(DecisionTrackProgress, "Decision progress", decisionStageKeys, decisionStageLabels, 2, ToneWaiting)}
		r.Tone, r.WaitingOn = ToneWaiting, ActorYou
		r.Headline = "In progress"
		r.NextAction = DecisionAsk(ir.Kind)

	case InputRequestStatusResolved:
		t := stageSeq(DecisionTrackProgress, "Decision progress", decisionStageKeys, decisionStageLabels, 3, ToneSuccess)
		t.Stages[3].State = StageDone
		r.Tracks = []Track{t}
		r.Tone, r.WaitingOn = ToneSuccess, ActorNobody
		r.Headline = "Resolved"
		r.NextAction = "Nothing further. Mendel has what it needed and has moved on."

	default:
		r.Tracks = []Track{stageSeq(DecisionTrackProgress, "Decision progress", decisionStageKeys, decisionStageLabels, -1, ToneNeutral)}
		r.Tone, r.WaitingOn = ToneNeutral, ActorMendel
		r.Headline = fmt.Sprintf("Unrecognized state (%s)", ir.Status)
		r.NextAction = "Mendel does not recognize this Decision's state. This is a bug worth reporting."
	}

	return r
}

// DecisionKindLabel renders an InputRequestKind as a short human phrase. The
// queue currently prints the raw enum (e.g. "variation_selection"), which is
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

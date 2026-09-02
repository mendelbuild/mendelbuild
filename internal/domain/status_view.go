package domain

import "strings"

// Plain-English renderings of the statuses that are not lifecycles.
//
// Hops, Variations, and Decisions get a full Ribbon because they move through
// stages a reader needs to see. The statuses here do not: a demo is starting or
// it is not. But they are subject to exactly the same rule from lifecycle.go —
// a template must never switch on a raw status string — because that is how a
// failure ends up wearing the same colour as a success.
//
// So each gets the smallest thing that satisfies the rule: a word a person can
// read, and a Tone that decides its colour. One function per status enum, and
// every enum value covered, with an honest fallback for values added later.

// StatusView is a status rendered for a reader: what to say, and what tone to
// say it in. Templates emit `badge badge-{{.Tone}}` and never inspect the
// underlying value.
type StatusView struct {
	Label string
	Tone  Tone
}

// DemoStatus renders a demo instance's status.
//
// `stopped` is deliberately neutral rather than a failure: a demo that has been
// torn down did not go wrong, and colouring it red teaches the reader to ignore
// red.
func DemoStatus(s DemoInstanceStatus) StatusView {
	switch s {
	case DemoInstanceStatusStarting:
		return StatusView{"Starting", ToneProgress}
	case DemoInstanceStatusRunning:
		return StatusView{"Running", ToneSuccess}
	case DemoInstanceStatusStopped:
		return StatusView{"Stopped", ToneNeutral}
	case DemoInstanceStatusError:
		return StatusView{"Failed to start", ToneFailure}
	default:
		return StatusView{"Unrecognized (" + string(s) + ")", ToneNeutral}
	}
}

// RevisionStatus renders one requested change.
func RevisionStatus(s VariationRevisionStatus) StatusView {
	switch s {
	case VariationRevisionStatusPending:
		return StatusView{"Queued", ToneNeutral}
	case VariationRevisionStatusInProgress:
		return StatusView{"Being applied", ToneProgress}
	case VariationRevisionStatusCompleted:
		return StatusView{"Applied", ToneSuccess}
	case VariationRevisionStatusFailed:
		return StatusView{"Failed", ToneFailure}
	default:
		return StatusView{"Unrecognized (" + string(s) + ")", ToneNeutral}
	}
}

// DeploymentStatus renders a hosting deployment, used for both production
// deploys and channel validation runs.
func DeploymentStatus(s HostingDeploymentStatus) StatusView {
	switch s {
	case HostingDeploymentStatusDeploying:
		return StatusView{"Deploying", ToneProgress}
	case HostingDeploymentStatusRunning:
		return StatusView{"Live", ToneSuccess}
	case HostingDeploymentStatusFailed:
		return StatusView{"Failed", ToneFailure}
	case HostingDeploymentStatusTerminated:
		return StatusView{"Torn down", ToneNeutral}
	default:
		return StatusView{"Unrecognized (" + string(s) + ")", ToneNeutral}
	}
}

// ValidationStatus renders one leg of a deployment channel's validation.
//
// Not validated is neutral, not a warning: an unvalidated channel is an
// unfinished setup step, not something that has gone wrong. The page says what
// to do about it; the badge does not need to shout.
func ValidationStatus(validating, validated bool, errMsg string) StatusView {
	switch {
	case validating:
		return StatusView{"Validating", ToneProgress}
	case validated:
		return StatusView{"Validated", ToneSuccess}
	case errMsg != "":
		return StatusView{"Validation failed", ToneFailure}
	default:
		return StatusView{"Not validated", ToneNeutral}
	}
}

// MemberRole renders a project membership role.
func MemberRole(role string) StatusView {
	switch role {
	case "owner":
		return StatusView{"Owner", ToneProgress}
	case "member":
		return StatusView{"Member", ToneNeutral}
	default:
		return StatusView{role, ToneNeutral}
	}
}

// DecisionStatusView renders a Decision's position for a queue, where there is
// no room for its full Ribbon. Takes a value so a template can pass a row.
func DecisionStatusView(ir InputRequest) StatusView {
	r := DecisionLifecycle(&ir)
	return StatusView{Label: r.Headline, Tone: r.Tone}
}

// DecisionResolution renders how a Decision was settled.
//
// Approved and rejected are both resolutions, and they are not the same news;
// the queue used to colour a rejection with the success palette.
//
// Every value the code writes is listed. The default is for resolutions that
// are already sentences -- the measurement ask records "3 of 5 key results
// measured" -- and returning those unchanged is right. What it must never do is
// hand back an unmapped enum: "credential_provided" reached a page once, which
// is how a screen meant to say the user had supplied what was asked instead
// showed them a column value.
func DecisionResolution(resolution string) StatusView {
	switch resolution {
	case "":
		return StatusView{"Resolved", ToneSuccess}
	case "approved":
		return StatusView{"Approved", ToneSuccess}
	case "rejected":
		return StatusView{"Rejected", ToneFailure}
	case "requested_more":
		return StatusView{"More options requested", ToneNeutral}
	case "credential_provided":
		return StatusView{"You supplied what was needed", ToneSuccess}
	case "conflicts_resolved":
		return StatusView{"Conflicts resolved", ToneSuccess}
	default:
		return StatusView{resolution, ToneNeutral}
	}
}

// ResolutionIsEnum reports whether a resolution string looks like an unmapped
// column value rather than a sentence, which is the shape of the fault worth
// catching: lower case, underscores, no spaces.
//
// Used by a test rather than by the app. At runtime there is nothing useful to
// do with the answer; at test time it is the difference between prose and a
// leak.
func ResolutionIsEnum(resolution string) bool {
	if resolution == "" || strings.Contains(resolution, " ") {
		return false
	}
	return strings.Contains(resolution, "_") || strings.ToLower(resolution) == resolution
}

// MessageAuthor names who wrote a message on a Decision.
func MessageAuthor(role string) string {
	switch role {
	case "system":
		return "Mendel"
	case "agent":
		return "Agent"
	case "user":
		return "You"
	default:
		return role
	}
}

// HopStatusView renders a Hop's status as a badge, for the places that list
// many Hops at once and have no room for a full Ribbon. It reuses the Ribbon so
// a Hop cannot describe itself one way in a table and another on its own page.
func HopStatusView(h *Hop) StatusView {
	r := HopLifecycle(h, nil)
	return StatusView{Label: r.Headline, Tone: r.Tone}
}

// VariationStatusView renders a Variation's status as a badge, for the same
// reason and with the same guarantee.
//
// revs may be nil, at the cost of not distinguishing a first build from a
// revision in flight — acceptable in a list, never on the Variation's own page.
func VariationStatusView(v *Variation, revs []VariationRevision, h *Hop) StatusView {
	r := VariationLifecycle(v, revs, h)
	return StatusView{Label: r.Headline, Tone: r.Tone}
}

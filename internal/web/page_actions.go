package web

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The actions each detail page offers, assembled here rather than in a
// template. Two rules hold across all of them:
//
//   - At most one action carries the "primary" role. It is the thing the page
//     wants the reader to do, and a page with two of them is a page that has
//     not decided.
//   - Anything with a side effect posts. A link is for going somewhere.

// hopRibbonActions is what to do about a Hop, given what is open on it.
//
// These used to live in a sidebar card titled "Actions" that sat beside a
// ribbon already narrating the same situation. Folding them into the ribbon
// means the sentence explaining why something is being offered sits next to the
// button that does it.
func hopRibbonActions(projectID uuid.UUID, v *HopDetailView) []PageAction {
	base := fmt.Sprintf("/p/%s", projectID)

	switch {
	case v.PendingReview != nil:
		return []PageAction{{
			Label: "Review the approaches",
			Href:  fmt.Sprintf("%s/inputs/%s", base, v.PendingReview.ID),
			Role:  "primary",
			Note:  "Approve them before any code is written.",
		}}

	case v.PendingSelection != nil:
		return []PageAction{{
			Label: "Compare and pick a winner",
			Href:  fmt.Sprintf("%s/inputs/%s", base, v.PendingSelection.ID),
			Role:  "primary",
		}}

	case v.IsStuck:
		return []PageAction{{
			Label: "Request new variations",
			Post:  fmt.Sprintf("%s/hops/%s/propose-variations", base, v.Hop.ID),
			Role:  "primary",
		}}
	}
	return nil
}

// variationRibbon assembles a Variation's ribbon and the actions on it.
//
// One constructor for the handler and the tests, so a test cannot assert
// against a ribbon the app would never build.
func variationRibbon(projectID uuid.UUID, v *domain.Variation, revs []domain.VariationRevision,
	h *domain.Hop, canRetryFix bool) *RibbonView {
	return ribbonView(
		domain.VariationLifecycle(v, revs, h),
		variationRibbonActions(projectID, v, domain.VariationActions(v, canRetryFix))...,
	)
}

// variationRibbonActions turns domain.VariationOffers into buttons.
//
// The domain decides what is legitimate; this decides what it is called, where
// it posts, and which one is the recommended move. Ordering matters: the
// primary comes first, and the destructive option comes last.
func variationRibbonActions(projectID uuid.UUID, v *domain.Variation, o domain.VariationOffers) []PageAction {
	base := fmt.Sprintf("/p/%s/variations/%s", projectID, v.ID)
	var out []PageAction

	// The recommended move, if there is one. Continuing a run that stopped on
	// cost beats retrying it, and retrying against a real diagnosis beats
	// rebuilding blind — so at most one of these claims primary.
	primaryTaken := false
	if o.Continue {
		out = append(out, PageAction{
			Label: "Continue where it left off",
			Post:  base + "/retry",
			Role:  "primary",
			Note: fmt.Sprintf("Costs up to another %s. The code it already wrote is kept.",
				formatUSD(v.BudgetLimitUSD())),
		})
		primaryTaken = true
	}
	if o.RetryWithFix {
		role := "secondary"
		if !primaryTaken {
			role, primaryTaken = "primary", true
		}
		out = append(out, PageAction{
			Label: "Retry with the diagnosed fix",
			Post:  base + "/retry-fix",
			Role:  role,
		})
	}
	if o.RequestChange {
		role := "secondary"
		if !primaryTaken {
			role, primaryTaken = "primary", true
		}
		// A fragment, not a post: this opens the feedback form, which is what
		// actually has the side effect.
		out = append(out, PageAction{
			Label: "Request a change",
			Href:  "#request-change",
			Role:  role,
		})
	}
	if o.Regenerate {
		out = append(out, PageAction{
			Label:   "Rebuild from scratch",
			Post:    base + "/retry",
			Role:    "secondary",
			Confirm: "Rebuild this variation from scratch? The code it wrote will be discarded.",
		})
	}
	if o.Rebase {
		out = append(out, PageAction{
			Label: "Rebase onto main",
			Post:  base + "/rebase",
			Role:  "secondary",
		})
	}
	if o.Terminate {
		out = append(out, PageAction{
			Label:   "Stop this build",
			Post:    base + "/terminate",
			Role:    "danger",
			Confirm: "Stop this build? It cannot be resumed, only rebuilt.",
			Note:    "Use this when a generation is stuck.",
		})
	}
	return out
}

// Package assigner holds the vocabulary of experiment assignment.
//
// It no longer assigns. The gateway does that: a request carrying no Arm cookie
// falls through to a rule whose backends are weighted, and each backend sets the
// cookie naming itself, so the visitor is assigned by the same response that
// serves them. Every request after that matches the cookie and is routed without
// anything computing anything.
//
// What that replaced was a small service Mendel deployed beside the app, which
// received the cookie-less request, picked an Arm, set a cookie and redirected.
// It cost a Deployment, a Service, a ConfigMap, an extra round trip and an
// image to build and publish -- and it was broken for the case it existed to
// serve, since it derived the Arm by hashing a key and a first-time visitor has
// no key, so every new visitor was sent to mainline and no experiment ever
// enrolled anybody.
package assigner

import (
	"fmt"
	"strings"
	"time"
)

// MainlineSlug is the Arm that is the code that was already there.
const MainlineSlug = "0"

// CookieName carries the Arm. Read by the route's header matching and written by
// its backend filters, so it is the contract between the two halves.
const CookieName = "mendel_arm"

// CookieLifetime is how long an assignment sticks.
//
// Longer than any experiment should run. The cookie is what keeps one visitor in
// one Arm, and an assignment that expired mid-experiment would put the same
// person in both -- which is exactly what the Assignment Unit exists to prevent.
const CookieLifetime = 180 * 24 * time.Hour

// SetCookieValue is what a backend sets when it serves an unassigned visitor.
func SetCookieValue(slug string, secure bool) string {
	v := fmt.Sprintf("%s=%s; Path=/; Max-Age=%d; HttpOnly; SameSite=Lax",
		CookieName, slug, int(CookieLifetime.Seconds()))
	if secure {
		// Only over https, where it is both meaningful and permitted. Marking a
		// cookie Secure on an http site means the browser never sends it back,
		// so every request would look unassigned and nobody would stay in an Arm.
		v += "; Secure"
	}
	return v
}

// ArmWeight is one Arm's share of new visitors.
type ArmWeight struct {
	Slug   string `json:"slug"`
	Weight int    `json:"weight"`
}

// ValidateAllocation reports why a set of Arms cannot be served, or "".
func ValidateAllocation(arms []ArmWeight) string {
	if len(arms) < 2 {
		return "an experiment needs mainline and at least one Arm to compare against it"
	}
	seen := make(map[string]bool, len(arms))
	mainline, total := 0, 0
	for _, arm := range arms {
		if strings.TrimSpace(arm.Slug) == "" {
			return "an Arm with no slug cannot be named in a cookie or matched by a route"
		}
		if seen[arm.Slug] {
			return fmt.Sprintf("two Arms are called %q, so a cookie naming it is ambiguous", arm.Slug)
		}
		seen[arm.Slug] = true
		if arm.Weight < 0 {
			return fmt.Sprintf("Arm %q has a negative share", arm.Slug)
		}
		if arm.Slug == MainlineSlug {
			mainline++
		}
		total += arm.Weight
	}
	if mainline != 1 {
		return fmt.Sprintf("exactly one Arm is mainline (%q); this allocation has %d", MainlineSlug, mainline)
	}
	if total == 0 {
		return "every Arm has a zero share, so no visitor could be assigned"
	}
	return ""
}

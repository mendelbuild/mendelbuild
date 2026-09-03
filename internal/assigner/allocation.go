// Package assigner decides which Arm a visitor belongs to.
//
// It runs as a small component beside the user's application, not as a proxy: it
// receives a request that carries no Arm cookie, works out an Arm, sets the
// cookie and redirects to the same URL. It never forwards a body, never streams
// and never holds an upstream connection. Every subsequent request from that
// visitor is routed by plain Gateway API header matching, with no Envoy
// extension anywhere.
//
// It reads its allocation from a file -- a mounted ConfigMap in practice --
// rather than asking Mendel. The user's production traffic must not depend on
// Mendel being reachable; if Mendel is down, the last-written allocation stands.
package assigner

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MainlineSlug is the Arm that is the code that was already there. A visitor
// Mendel cannot place ends up here, which is the one default that is always
// safe.
const MainlineSlug = "0"

// CookieName carries the Arm. Read by the HTTPRoute's header matching, so it is
// part of the contract with the generated routes rather than an internal detail.
const CookieName = "mendel_arm"

// ArmWeight is one Arm's share of new visitors.
type ArmWeight struct {
	Slug   string `json:"slug"`
	Weight int    `json:"weight"`
}

// KeySource is where the participant's identity is read from. It must be
// readable at the edge, before any application code runs -- which is what makes
// "the Arm is decided before divergent code runs" true by construction rather
// than by analysing an arbitrary codebase.
type KeySource struct {
	Source string `json:"source"` // cookie | header
	Name   string `json:"name"`
}

// Allocation is the whole contract: what the assigner reads and Mendel writes.
type Allocation struct {
	// Salt makes assignment independent between experiments. Without it a
	// visitor who lands in the treatment Arm of one experiment lands in the
	// same position in every other, and the experiments correlate for no reason
	// anybody chose.
	Salt string `json:"salt"`

	Key  KeySource   `json:"key"`
	Arms []ArmWeight `json:"arms"`
}

// Assign returns the Arm for a participant key.
//
// A pure function of the key, the salt and the allocation -- deliberately not a
// random draw. Two things depend on that. A visitor who clears the Arm cookie
// gets the same Arm back rather than crossing over mid-experiment; and any other
// hop given the same key derives the same Arm independently, which is what
// removes the need to propagate the Arm itself through an application Mendel did
// not write.
//
// An empty key is mainline. Not knowing who someone is fails towards the code
// that was already there, which is legible and is a known default -- rather than
// towards an arbitrary Arm, which would bias the comparison silently.
func (a Allocation) Assign(key string) string {
	if strings.TrimSpace(key) == "" {
		return MainlineSlug
	}

	arms := a.ordered()
	total := 0
	for _, arm := range arms {
		total += arm.Weight
	}
	if total <= 0 {
		return MainlineSlug
	}

	// The actual total rather than a hard 100, so an allocation that does not
	// add up still divides traffic in the stated proportions instead of sending
	// the remainder somewhere nobody chose.
	bucket := int(a.bucket(key) % uint64(total))

	running := 0
	for _, arm := range arms {
		running += arm.Weight
		if bucket < running {
			return arm.Slug
		}
	}
	return MainlineSlug
}

// bucket hashes the salted key to a number.
//
// SHA-256 rather than Go's map hash or FNV: the result has to be identical
// across processes, machines and releases, because a second hop recomputing the
// Arm must reach the same answer as the assigner did. Go's runtime hash is
// randomised per process and would quietly disagree.
func (a Allocation) bucket(key string) uint64 {
	sum := sha256.Sum256([]byte(a.Salt + "\x00" + key))
	return binary.BigEndian.Uint64(sum[:8])
}

// ordered sorts Arms by slug.
//
// The order decides which key lands in which Arm, so it cannot be whatever order
// the allocation happened to be written in. Sorting means a rewrite that merely
// reorders the list moves nobody -- and rewrites happen, since this is
// regenerated whenever an Arm is added or a weight changes.
func (a Allocation) ordered() []ArmWeight {
	arms := make([]ArmWeight, len(a.Arms))
	copy(arms, a.Arms)
	sort.Slice(arms, func(i, j int) bool { return arms[i].Slug < arms[j].Slug })
	return arms
}

// Validate reports why an allocation cannot be served, or "" when it can.
func (a Allocation) Validate() string {
	if len(a.Arms) < 2 {
		return "an experiment needs mainline and at least one Arm to compare against it"
	}

	seen := make(map[string]bool, len(a.Arms))
	mainline, total := 0, 0
	for _, arm := range a.Arms {
		if arm.Slug == "" {
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
	switch a.Key.Source {
	case "cookie", "header":
	default:
		return fmt.Sprintf("key source %q cannot be read at the edge", a.Key.Source)
	}
	if strings.TrimSpace(a.Key.Name) == "" {
		return "the key has no name, so there is nothing to read"
	}
	return ""
}

// ParseAllocation reads what Mendel wrote.
func ParseAllocation(data []byte) (Allocation, error) {
	var a Allocation
	if err := json.Unmarshal(data, &a); err != nil {
		return Allocation{}, fmt.Errorf("allocation is not readable: %w", err)
	}
	if msg := a.Validate(); msg != "" {
		return Allocation{}, fmt.Errorf("allocation cannot be served: %s", msg)
	}
	return a, nil
}

// Marshal renders an allocation for the ConfigMap.
func (a Allocation) Marshal() ([]byte, error) { return json.MarshalIndent(a, "", "  ") }

// cookieLifetime is how long an assignment sticks.
//
// Longer than any experiment should run. The cookie is what keeps one visitor
// in one Arm, and an assignment that expires mid-experiment puts the same person
// in both -- which is exactly what the Assignment Unit exists to prevent.
const cookieLifetime = 180 * 24 * time.Hour

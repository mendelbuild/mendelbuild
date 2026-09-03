package assigner

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func evenSplit() Allocation {
	return Allocation{
		Salt: "exp-1",
		Key:  KeySource{Source: "cookie", Name: "session_id"},
		Arms: []ArmWeight{
			{Slug: MainlineSlug, Weight: 50},
			{Slug: "treatment", Weight: 50},
		},
	}
}

// The property everything else rests on: the same visitor gets the same Arm,
// every time, in every process.
//
// A visitor who clears the cookie must not cross over mid-experiment, and any
// other hop given the same key must reach the same answer independently -- which
// is what removes the need to propagate the Arm through an application Mendel
// did not write.
func TestAssignmentIsStableForAKey(t *testing.T) {
	a := evenSplit()
	for i := 0; i < 500; i++ {
		key := fmt.Sprintf("visitor-%d", i)
		first := a.Assign(key)
		for try := 0; try < 5; try++ {
			if again := a.Assign(key); again != first {
				t.Fatalf("%s got %q then %q", key, first, again)
			}
		}
	}
}

// Reordering the Arms must move nobody. The allocation is regenerated whenever
// an Arm is added or a weight changes, and if order decided placement then a
// cosmetic rewrite would reshuffle every visitor who had lost their cookie.
func TestReorderingArmsMovesNobody(t *testing.T) {
	a := evenSplit()
	b := Allocation{Salt: a.Salt, Key: a.Key, Arms: []ArmWeight{a.Arms[1], a.Arms[0]}}

	for i := 0; i < 300; i++ {
		key := fmt.Sprintf("visitor-%d", i)
		if a.Assign(key) != b.Assign(key) {
			t.Fatalf("%s moved when the Arms were merely listed in a different order", key)
		}
	}
}

// Different experiments must not correlate. Without a salt, a visitor in the
// treatment Arm of one experiment lands in the same position in every other, and
// the two experiments interfere for no reason anybody chose.
func TestSaltDecorrelatesExperiments(t *testing.T) {
	a := evenSplit()
	b := evenSplit()
	b.Salt = "exp-2"

	same := 0
	const n = 1000
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("visitor-%d", i)
		if a.Assign(key) == b.Assign(key) {
			same++
		}
	}
	// Two independent even splits agree about half the time. Near-total
	// agreement would mean the salt is not reaching the hash.
	if same > n*7/10 {
		t.Errorf("two experiments agreed on %d/%d visitors; the salt is not separating them", same, n)
	}
}

func TestTrafficIsSplitInTheStatedProportions(t *testing.T) {
	a := Allocation{
		Salt: "exp-1",
		Key:  KeySource{Source: "cookie", Name: "sid"},
		Arms: []ArmWeight{
			{Slug: MainlineSlug, Weight: 80},
			{Slug: "treatment", Weight: 20},
		},
	}

	const n = 20000
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		counts[a.Assign(fmt.Sprintf("visitor-%d", i))]++
	}

	for slug, want := range map[string]float64{MainlineSlug: 0.80, "treatment": 0.20} {
		got := float64(counts[slug]) / n
		if math.Abs(got-want) > 0.02 {
			t.Errorf("%s got %.3f of traffic, wanted %.2f", slug, got, want)
		}
	}
}

// An Arm holding no traffic must hold none at all -- not "almost none". This is
// how an Arm is withdrawn from new visitors without being torn down.
func TestZeroWeightArmTakesNobody(t *testing.T) {
	a := Allocation{
		Salt: "exp-1",
		Key:  KeySource{Source: "cookie", Name: "sid"},
		Arms: []ArmWeight{
			{Slug: MainlineSlug, Weight: 100},
			{Slug: "withdrawn", Weight: 0},
		},
	}
	for i := 0; i < 5000; i++ {
		if got := a.Assign(fmt.Sprintf("visitor-%d", i)); got == "withdrawn" {
			t.Fatalf("a zero-weight Arm was assigned visitor-%d", i)
		}
	}
}

// Not knowing who someone is fails towards the code that was already there.
// An arbitrary Arm would bias the comparison silently; mainline is a known
// default and is legible in the data afterwards.
func TestNoKeyIsMainline(t *testing.T) {
	a := evenSplit()
	for _, key := range []string{"", "   ", "\t"} {
		if got := a.Assign(key); got != MainlineSlug {
			t.Errorf("a visitor with no identity got %q rather than mainline", got)
		}
	}
}

func TestAllocationRefusesWhatCannotBeServed(t *testing.T) {
	for name, mutate := range map[string]func(*Allocation){
		"no arms at all":     func(a *Allocation) { a.Arms = nil },
		"only mainline":      func(a *Allocation) { a.Arms = a.Arms[:1] },
		"no mainline":        func(a *Allocation) { a.Arms[0].Slug = "other" },
		"two mainlines":      func(a *Allocation) { a.Arms[1].Slug = MainlineSlug },
		"duplicate slug":     func(a *Allocation) { a.Arms[1].Slug = a.Arms[0].Slug },
		"unnamed arm":        func(a *Allocation) { a.Arms[1].Slug = "" },
		"negative share":     func(a *Allocation) { a.Arms[1].Weight = -1 },
		"all zero":           func(a *Allocation) { a.Arms[0].Weight, a.Arms[1].Weight = 0, 0 },
		"key not at edge":    func(a *Allocation) { a.Key.Source = "jwt_claim" },
		"key with no name":   func(a *Allocation) { a.Key.Name = " " },
	} {
		t.Run(name, func(t *testing.T) {
			a := evenSplit()
			mutate(&a)
			if msg := a.Validate(); msg == "" {
				t.Errorf("%s was accepted", name)
			}
		})
	}

	if msg := evenSplit().Validate(); msg != "" {
		t.Errorf("a serviceable allocation was rejected: %s", msg)
	}
}

func TestAllocationSurvivesTheConfigMap(t *testing.T) {
	a := evenSplit()
	data, err := a.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := ParseAllocation(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("visitor-%d", i)
		if a.Assign(key) != back.Assign(key) {
			t.Fatalf("%s was assigned differently after a round trip through the ConfigMap", key)
		}
	}

	// A ConfigMap Mendel did not write, or wrote wrongly, must not be served.
	if _, err := ParseAllocation([]byte(`{"arms":[]}`)); err == nil {
		t.Error("an unserviceable allocation was loaded")
	}
	if _, err := ParseAllocation([]byte("not json")); err == nil {
		t.Error("unparseable data was loaded")
	}
}

// The handler assigns and gets out of the way. It must never become part of the
// data path.
func TestHandlerSetsTheCookieAndRedirectsBack(t *testing.T) {
	h := NewHandler(evenSplit(), true)

	r := httptest.NewRequest(http.MethodGet, "/checkout?step=2", nil)
	r.AddCookie(&http.Cookie{Name: "session_id", Value: "visitor-7"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("want a redirect, got %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/checkout?step=2" {
		t.Errorf("visitor was sent to %q rather than back where they were going", got)
	}
	// http.Redirect writes a courtesy anchor for GET, which is fine. What must
	// never happen is the assigner returning application content -- that would
	// put it in the data path, which is the property that keeps it a few dozen
	// lines with no proxy responsibilities.
	if w.Body.Len() > 200 {
		t.Errorf("the assigner returned %d bytes; it must never be in the data path", w.Body.Len())
	}

	var arm *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			arm = c
		}
	}
	if arm == nil {
		t.Fatal("no Arm cookie was set, so the next request would come straight back here")
	}
	if arm.Value != evenSplit().Assign("visitor-7") {
		t.Errorf("cookie says %q, the allocation says %q", arm.Value, evenSplit().Assign("visitor-7"))
	}
	if !arm.HttpOnly || !arm.Secure {
		t.Error("an Arm cookie readable or writable by the page lets a visitor pick their own Arm")
	}
	if arm.MaxAge <= 0 {
		t.Error("a session cookie would reassign the visitor when the browser closes, putting one person in both Arms")
	}
}

// A probe is not a participant.
func TestHealthCheckDoesNotAssign(t *testing.T) {
	h := NewHandler(evenSplit(), true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Errorf("health check returned %d", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("the health check was handed an Arm cookie and counted as a visitor")
	}
}

// The allocation is swapped wholesale, so a request sees one allocation or the
// other and never a half-updated mixture.
func TestAllocationCanBeReplacedWhileServing(t *testing.T) {
	h := NewHandler(evenSplit(), false)

	withdrawn := evenSplit()
	withdrawn.Arms[1].Weight = 0
	withdrawn.Arms[0].Weight = 100
	h.Set(withdrawn)

	for i := 0; i < 200; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: "session_id", Value: fmt.Sprintf("v-%d", i)})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		for _, c := range w.Result().Cookies() {
			if c.Name == CookieName && c.Value == "treatment" {
				t.Fatalf("v-%d was assigned to a withdrawn Arm", i)
			}
		}
	}
}

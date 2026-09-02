package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

func newObsServer() *Server {
	return &Server{domainObs: make(map[uuid.UUID]domainObservation)}
}

// The whole point of the cache is that rendering never waits on a lookup. A cold
// cache must hand back "nothing seen yet" rather than an answer, because the
// alternative -- treating the zero observation as fact -- tells a user to create
// records they already created.
func TestColdCacheReportsNotYetLooked(t *testing.T) {
	s := newObsServer()
	id := uuid.New()

	entry, start := s.takeRefreshSlot(id)
	if !start {
		t.Fatal("a cold cache should start a refresh")
	}
	if !entry.at.IsZero() {
		t.Errorf("a cold cache should report no observation, got %v", entry.at)
	}
	if entry.obs.Known {
		t.Error("a cold cache must not claim to know anything")
	}
}

// A page that polls while a slow gcloud call is in flight would otherwise start
// one on every poll, and they queue behind each other for as long as the tab is
// open.
func TestRefreshesDoNotPileUp(t *testing.T) {
	s := newObsServer()
	id := uuid.New()

	if _, start := s.takeRefreshSlot(id); !start {
		t.Fatal("the first caller should claim the slot")
	}
	for i := 0; i < 5; i++ {
		if _, start := s.takeRefreshSlot(id); start {
			t.Fatalf("caller %d started a second refresh while one was in flight", i+2)
		}
	}
}

func TestFreshObservationIsServedWithoutRefreshing(t *testing.T) {
	s := newObsServer()
	id := uuid.New()
	s.domainObs[id] = domainObservation{
		obs: domain.DomainObservation{Known: true, WildcardTarget: "34.1.2.3"},
		at:  time.Now(),
	}

	entry, start := s.takeRefreshSlot(id)
	if start {
		t.Error("a fresh observation should not trigger a lookup")
	}
	if entry.obs.WildcardTarget != "34.1.2.3" {
		t.Errorf("cached observation not returned: %+v", entry.obs)
	}

	s.domainObs[id] = domainObservation{
		obs: entry.obs,
		at:  time.Now().Add(-2 * domainObservationTTL),
	}
	if _, start := s.takeRefreshSlot(id); !start {
		t.Error("a stale observation should trigger a lookup")
	}
}

// Mendel has just changed what the observation describes, so serving the old one
// would show the state from before the change and read as the change failing.
func TestInvalidationForcesAFreshLook(t *testing.T) {
	s := newObsServer()
	id := uuid.New()
	s.domainObs[id] = domainObservation{obs: domain.DomainObservation{Known: true}, at: time.Now()}

	if _, start := s.takeRefreshSlot(id); start {
		t.Fatal("precondition: a fresh entry should not refresh")
	}
	s.invalidateObservation(id)
	if _, start := s.takeRefreshSlot(id); !start {
		t.Error("an invalidated observation should be looked up again")
	}
}

// The ladder is rendered twice: in the page, and on its own for the poll that
// replaces "Checking" with the first real observation. A fragment that does not
// render leaves the page stuck on "Checking" forever, and it fails silently --
// the page itself still looks fine.
func TestDomainLadderFragmentRenders(t *testing.T) {
	pd := &domain.ProjectDomain{
		BaseDomain: "example.com", DemoSubdomain: "mendel-demos", StaticIP: "34.1.2.3",
	}
	steps := pd.DomainReadiness(domain.DomainObservation{Known: true, WildcardTarget: "34.1.2.3"})
	headline, waitingOnYou := domain.DomainHeadline(steps)

	var out strings.Builder
	err := parsePageTemplate("project_domain.html").ExecuteTemplate(&out, "domain-ladder", map[string]interface{}{
		"Steps":        steps,
		"Headline":     headline,
		"WaitingOnYou": waitingOnYou,
		"Checking":     false,
		"CheckedLabel": "just now",
	})
	if err != nil {
		t.Fatalf("the ladder fragment does not render: %v", err)
	}
	for _, want := range []string{"Create the wildcard A record", "ladder-step", "just now"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("fragment is missing %q:\n%s", want, out.String())
		}
	}
}

// The poll has an endpoint to poll, or the page never leaves "Checking".
func TestDomainReadinessRouteIsRegistered(t *testing.T) {
	s := &Server{}
	s.setupRoutes()

	found := false
	chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet && strings.HasSuffix(route, "/domain/readiness") {
			found = true
		}
		return nil
	})
	if !found {
		t.Error("no GET route for the readiness fragment; domain-ladder.js polls nothing")
	}
}

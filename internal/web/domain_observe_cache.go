package web

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The Domain page reports what Mendel can see rather than what it was told, and
// seeing costs a network round trip. Doing that during render made the page take
// seconds to arrive, and the cost was not where it looked: the two DNS lookups
// take about 50ms between them, while asking about the certificate means running
// gcloud, which is a Python process start plus an API call.
//
// So the page no longer waits for it. It renders what was last observed, says
// when that was, and refreshes behind the scenes. Nothing here is authoritative
// enough to be worth blocking on -- a DNS record the user is about to create will
// be equally absent whether the answer is fresh or thirty seconds old.

// domainObservationTTL is how long an observation is served before a render
// triggers a new one.
//
// Short, because the thing being waited on is a record the user has just typed
// into another tab, and the gap between creating it and Mendel acknowledging it
// is the whole experience of this page. Nothing here polls, so this is not a
// cadence: it only decides whether a visit reuses the last answer or asks again,
// and a page nobody is looking at costs nothing.
//
// The cost of asking is one gcloud process for the certificate state; the two
// DNS lookups are about 50ms between them. Refreshing happens behind the render
// either way, so a short interval never makes the page slower to arrive.
const domainObservationTTL = 10 * time.Second

type domainObservation struct {
	obs        domain.DomainObservation
	at         time.Time // Zero until the first observation completes.
	refreshing bool
}

// observationFor returns what was last seen, and starts a refresh if that is
// stale or missing. It never blocks on the network.
//
// A zero `at` means nothing has been observed yet, which the caller must render
// as "checking" rather than as "not found" -- the difference between not knowing
// and knowing the answer is no.
func (s *Server) observationFor(projectID uuid.UUID, pd *domain.ProjectDomain) (domain.DomainObservation, time.Time) {
	if pd == nil || pd.BaseDomain == "" {
		return domain.DomainObservation{}, time.Time{}
	}

	entry, start := s.takeRefreshSlot(projectID)
	if start {
		go s.refreshObservation(projectID, pd)
	}
	return entry.obs, entry.at
}

// takeRefreshSlot returns what is cached and whether the caller should refresh
// it, claiming the slot if so.
//
// Separate from the goroutine it governs so the decision can be tested without
// starting one. One refresh at a time per project: without that, a page polling
// while a slow gcloud call is in flight starts another on every poll, and they
// queue behind each other for as long as the tab stays open.
func (s *Server) takeRefreshSlot(projectID uuid.UUID) (domainObservation, bool) {
	s.domainObsMutex.Lock()
	defer s.domainObsMutex.Unlock()

	entry := s.domainObs[projectID]
	if entry.refreshing || time.Since(entry.at) <= domainObservationTTL {
		return entry, false
	}
	claimed := entry
	claimed.refreshing = true
	s.domainObs[projectID] = claimed
	return entry, true
}

// refreshObservation observes once and stores the result.
//
// Detached from the request that triggered it: the user navigating away should
// not cancel the work that makes the next render fast, which is exactly what
// would happen if this inherited the request context.
func (s *Server) refreshObservation(projectID uuid.UUID, pd *domain.ProjectDomain) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	obs := s.observeDomain(ctx, projectID, pd)

	s.domainObsMutex.Lock()
	s.domainObs[projectID] = domainObservation{obs: obs, at: time.Now(), refreshing: false}
	s.domainObsMutex.Unlock()

	// The queue is meant to close itself when the records start resolving, rather
	// than when someone next submits a form. This is the only place that learns
	// they resolve without a form having been submitted.
	s.syncDomainRequestWith(ctx, projectID, pd, obs)
}

// invalidateObservation drops what was seen, so the next render observes again.
//
// Called where Mendel has just changed something the observation describes.
// Without it, requesting a certificate and then rendering the page shows the
// state from before the request for up to the TTL, which reads as the request
// having failed.
func (s *Server) invalidateObservation(projectID uuid.UUID) {
	s.domainObsMutex.Lock()
	defer s.domainObsMutex.Unlock()
	delete(s.domainObs, projectID)
}

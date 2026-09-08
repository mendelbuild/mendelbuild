package web

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The branch heads are read off the render path.
//
// The first version put a call to the user's git host inside handleHopDetail, so
// every load of the Hop page made a network request and a slow remote made the
// page slow -- on a page that is otherwise a database read.
//
// These assert the cache's contract rather than the look itself. takeBranchHeadSlot
// is where the whole decision lives -- serve, refresh behind, or wait -- and it
// touches nothing but the map, so it can be tested exactly rather than around.
func seedHeads(s *Server, projectID uuid.UUID, heads map[string]string, at time.Time) {
	s.branchHeads.mu.Lock()
	defer s.branchHeads.mu.Unlock()
	s.branchHeads.entries = map[uuid.UUID]branchHeads{projectID: {heads: heads, at: at}}
}

func TestTheCacheDecidesWhoLooks(t *testing.T) {
	heads := map[string]string{"mendel/hop/fast": "abc123"}

	t.Run("warm entries are served and nobody looks", func(t *testing.T) {
		s, projectID := &Server{}, uuid.New()
		seedHeads(s, projectID, heads, time.Now())

		entry, cold, refresh := s.takeBranchHeadSlot(projectID)
		if cold || refresh {
			t.Errorf("a warm entry sent somebody looking (cold=%v refresh=%v)", cold, refresh)
		}
		if entry.heads["mendel/hop/fast"] != "abc123" {
			t.Errorf("the warm entry was not returned: %v", entry.heads)
		}
	})

	t.Run("aged entries still answer, and refresh behind", func(t *testing.T) {
		s, projectID := &Server{}, uuid.New()
		seedHeads(s, projectID, heads, time.Now().Add(-2*branchHeadTTL))

		entry, cold, refresh := s.takeBranchHeadSlot(projectID)
		if cold {
			t.Error("an aged entry was treated as having nothing to show, so a reader waits")
		}
		if !refresh {
			t.Error("an aged entry is never refreshed, so it would go stale forever")
		}
		// The reader gets the previous answer, at most one TTL old. A staleness
		// verdict a minute behind is not a category of error; a page that waits
		// on a network call is.
		if entry.heads["mendel/hop/fast"] != "abc123" {
			t.Errorf("an aged entry returned nothing instead of what it had: %v", entry.heads)
		}
	})

	t.Run("a cold entry is looked up in the foreground", func(t *testing.T) {
		s, projectID := &Server{}, uuid.New()

		_, cold, _ := s.takeBranchHeadSlot(projectID)
		if !cold {
			t.Error("a cold entry did not send anybody looking, so the page would report " +
				"every arm unchecked until somebody reloaded")
		}
	})
}

// Only one caller is sent looking, so a Hop page open in several tabs makes one
// request to the git host rather than one per tab.
func TestOnlyOneReaderIsSentLooking(t *testing.T) {
	s, projectID := &Server{}, uuid.New()

	var mu sync.Mutex
	var looking int
	var wg sync.WaitGroup

	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cold, refresh := s.takeBranchHeadSlot(projectID)
			if cold || refresh {
				mu.Lock()
				looking++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if looking != 1 {
		t.Errorf("%d readers were sent to the git host at once; exactly one should be", looking)
	}
}

// A warm entry is served without any look at all.
//
// The end-to-end version of the first sub-test: this Server has no database, so
// any look would panic. Returning the seeded value proves none was attempted.
func TestAWarmEntryNeverReachesTheRemote(t *testing.T) {
	s, projectID := &Server{}, uuid.New()
	seedHeads(s, projectID, map[string]string{"mendel/hop/fast": "abc123"}, time.Now())

	if got := s.branchHeadsFor(context.Background(), projectID); got["mendel/hop/fast"] != "abc123" {
		t.Errorf("a warm entry was not served from the cache: %v", got)
	}
}

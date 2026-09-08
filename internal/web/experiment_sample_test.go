package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
)

// The sample must count what actually came back, from concurrent requests,
// without losing or double-counting any of them.
func TestSampleCountsEveryResponseExactlyOnce(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 0 {
			w.Header().Set(ArmHeader, "treatment")
		} else {
			// The other half answer only with the assignment cookie, which is
			// what an experiment started before Mendel stamped the header does.
			// Reporting those as unnamed would say the split was broken.
			http.SetCookie(w, &http.Cookie{Name: "mendel_arm", Value: "0"})
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Server{}
	exp := &domain.Experiment{Arms: []domain.ExperimentArm{
		{Slug: "0", AllocationWeight: 50}, {Slug: "treatment", AllocationWeight: 50},
	}}
	got := s.SampleSplit(context.Background(), exp, strings.TrimPrefix(srv.URL, "https://"))

	if got.Failed != 0 {
		t.Fatalf("%d requests failed against a server that answered everything", got.Failed)
	}
	if got.Unnamed != 0 {
		t.Errorf("%d responses were called unnamed; the cookie names the arm too", got.Unnamed)
	}
	if got.Total() != SplitSampleSize {
		t.Errorf("counted %d of %d responses", got.Total(), SplitSampleSize)
	}
	total := 0
	for _, sh := range got.Shares {
		total += sh.Observed
	}
	if total != SplitSampleSize {
		t.Errorf("the shares add to %d, not %d", total, SplitSampleSize)
	}
}

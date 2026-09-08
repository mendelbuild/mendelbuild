package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/assigner"
	"github.com/bhs/mendelbuild/internal/domain"
)

// Sampling the split, because it is the first thing anybody wants and there was
// no way to do it inside the product.
//
// What was actually run to verify the first live experiment was:
//
//	for i in $(seq 12); do curl -s -i https://app.pong.mendel.build/ \
//	  | grep -io 'mendel_arm=[a-z0-9-]*'; done | sort | uniq -c
//
// That is a reasonable thing to want and an unreasonable thing to require. It
// is also the one check that exercises the whole chain end to end -- the edge
// gateway, the proxy, the weighted fallback and the cookie -- which no amount of
// reading Mendel's own records can do.

// SplitSampleSize is how many visitors are simulated.
//
// Enough to see a split and not enough to matter: two dozen requests to a site
// that is serving live traffic is a rounding error, and a larger number would
// invite reading the result as a measurement rather than as a check that
// assignment is happening at all.
const SplitSampleSize = 24

// ArmShare is one Arm's showing in a sample.
type ArmShare struct {
	Slug string

	// Expected is the allocation Mendel asked the gateway for; Observed is what
	// came back. They will not match exactly and are not meant to -- assignment
	// is random, and 24 requests is far too few to tell an unlucky sample from a
	// misconfigured weight.
	Expected int
	Observed int
	Percent  int
}

// SplitSample is what a round of requests found.
type SplitSample struct {
	At       time.Time
	URL      string
	Requests int
	Shares   []ArmShare

	// Unnamed counts responses that named no Arm at all. Its own number rather
	// than folded into an Arm's, because it means something quite different: the
	// experiment is not in the path, or the response came from somewhere other
	// than the gateway. A sample that is entirely unnamed is the clearest
	// possible statement that the split is not happening.
	Unnamed int

	// Failed counts requests that did not complete. Reported rather than
	// retried: a site that refuses one request in six is telling you something.
	Failed int

	// Err is set when the sample could not be taken at all, as opposed to taken
	// and disappointing.
	Err string
}

// Total is how many responses named an Arm.
func (s SplitSample) Total() int { return s.Requests - s.Unnamed - s.Failed }

// Verdict is the sentence a reader gets, in the terms they asked the question.
//
// It says what was seen, never what Mendel did. "Applied the routing" is not an
// answer to "is traffic being split", and treating the two as the same is the
// failure shape this whole area exists to correct.
func (s SplitSample) Verdict() string {
	switch {
	case s.Err != "":
		return "Mendel could not sample the split: " + s.Err
	case s.Failed == s.Requests:
		return fmt.Sprintf("None of the %d requests completed, so nothing was learned about the split.", s.Requests)
	case s.Total() == 0:
		return fmt.Sprintf("%d responses and not one named an arm. Either the experiment is not in "+
			"the path for this hostname, or something ahead of it is answering.", s.Requests)
	case s.Unnamed > 0:
		return fmt.Sprintf("%d of %d responses named an arm; %d named none, which means some "+
			"requests are not reaching the experiment.", s.Total(), s.Requests, s.Unnamed)
	}
	return fmt.Sprintf("All %d responses named an arm, so assignment is happening. The counts are "+
		"a handful of visitors and not a measurement -- they will not match the allocation exactly.", s.Total())
}

// SampleSplit sends a few cookie-less requests and reports which Arm answered
// each one.
//
// Every request is made with a fresh client and no cookie jar, because the whole
// question is what an unassigned visitor gets: reusing a connection that has
// been assigned would return the same Arm every time and look like a broken
// split.
func (s *Server) SampleSplit(ctx context.Context, exp *domain.Experiment, host string) SplitSample {
	sample := SplitSample{At: time.Now(), Requests: SplitSampleSize}
	if host == "" {
		sample.Err = "production has no hostname, so there is nowhere to send the requests"
		return sample
	}
	sample.URL = host
	if !strings.HasPrefix(sample.URL, "http") {
		sample.URL = "https://" + sample.URL
	}

	counts := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < SplitSampleSize; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slug, err := sampleOnce(ctx, sample.URL)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				sample.Failed++
			case slug == "":
				sample.Unnamed++
			default:
				counts[slug]++
			}
		}()
	}
	wg.Wait()

	// Every Arm the experiment has, including those that drew nobody. An Arm
	// missing from the table reads as an Arm that does not exist, when what
	// happened is that 24 requests did not reach it.
	total := sample.Total()
	for _, arm := range exp.Arms {
		share := ArmShare{Slug: arm.Slug, Expected: arm.AllocationWeight, Observed: counts[arm.Slug]}
		if total > 0 {
			share.Percent = share.Observed * 100 / total
		}
		sample.Shares = append(sample.Shares, share)
		delete(counts, arm.Slug)
	}
	// Anything naming an Arm this experiment does not have. Almost certainly a
	// previous experiment's cookie value still being matched by a route that
	// should have been torn down, which is worth seeing rather than discarding.
	for slug := range counts {
		sample.Shares = append(sample.Shares, ArmShare{Slug: slug + " (not an arm of this experiment)",
			Observed: counts[slug]})
	}
	sort.SliceStable(sample.Shares, func(i, j int) bool {
		return sample.Shares[i].Slug < sample.Shares[j].Slug
	})
	return sample
}

// sampleOnce makes one unassigned request and reports the Arm that answered.
func sampleOnce(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		// Redirects are not followed. The assignment happens on the first
		// response, and following a redirect would report the Arm that served
		// the destination -- which, with a cookie now set, is the same Arm and
		// tells you nothing new.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	return armFromResponse(resp), nil
}

// armFromResponse reads the Arm out of a response.
//
// The header first, because it is the direct statement. The Set-Cookie second,
// because it is the assignment itself rather than a description of it -- an
// experiment started before Mendel stamped the header still assigns visitors
// perfectly well, and reporting it as unnamed would say the split was broken
// when it was working.
func armFromResponse(resp *http.Response) string {
	if slug := resp.Header.Get(ArmHeader); slug != "" {
		return slug
	}
	for _, c := range resp.Cookies() {
		if c.Name == assigner.CookieName {
			return c.Value
		}
	}
	return ""
}

// --- The last sample, kept where the page can find it ---
//
// In memory and not in the database. A sample is a diagnostic taken by somebody
// standing in front of the page, meaningful for about as long as they are
// looking at it; persisting it would invite reading a stale one as the current
// state of the split, which is the mistake this whole area is about.

type splitSampleCache struct {
	mu      sync.Mutex
	entries map[uuid.UUID]SplitSample
}

func (s *Server) recordSplitSample(experimentID uuid.UUID, sample SplitSample) {
	s.splitSamples.mu.Lock()
	defer s.splitSamples.mu.Unlock()
	if s.splitSamples.entries == nil {
		s.splitSamples.entries = map[uuid.UUID]SplitSample{}
	}
	s.splitSamples.entries[experimentID] = sample
}

func (s *Server) lastSplitSample(experimentID uuid.UUID) *SplitSample {
	s.splitSamples.mu.Lock()
	defer s.splitSamples.mu.Unlock()
	sample, ok := s.splitSamples.entries[experimentID]
	if !ok {
		return nil
	}
	return &sample
}

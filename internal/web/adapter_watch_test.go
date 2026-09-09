package web

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

func startedProbe(context, getCreds string) ProbeStarted {
	return ProbeStarted{
		Invocation: &domain.AdapterInvocation{
			ID:        uuid.MustParse("77ea75d8-483d-4c35-acc3-4b3c9c9b3242"),
			CreatedAt: time.Now(),
		},
		Context:        context,
		GetCredentials: getCreds,
		Namespace:      "mendel-apps",
	}
}

// The bug: the follow-up commands named a namespace and no cluster. The adapter
// runs in the project's cluster, and whoever started it was talking to Mendel's,
// so both commands reported the job did not exist -- which reads as a job that
// was never created rather than one being looked for in the wrong place.
func TestWatchNamesTheProjectsCluster(t *testing.T) {
	const context = "gke_mendelpong_us-central1-a_pong-cluster"
	w := startedProbe(context, "gcloud container clusters get-credentials pong-cluster --location us-central1-a --project mendelpong")

	for _, line := range strings.Split(w.Watch(), "\n") {
		if !strings.Contains(line, "kubectl") {
			continue
		}
		if !strings.Contains(line, "--context "+context) {
			t.Errorf("a kubectl line does not name the project's cluster, so it runs against\n"+
				"whatever the reader's terminal points at:\n  %s", line)
		}
	}
}

// A terminal that has never talked to this cluster has no context to select, so
// the command that creates one is part of the answer rather than assumed.
func TestWatchSaysHowToReachTheCluster(t *testing.T) {
	const creds = "gcloud container clusters get-credentials pong-cluster --location us-central1-a --project mendelpong"
	got := startedProbe("gke_mendelpong_us-central1-a_pong-cluster", creds).Watch()
	if !strings.Contains(got, creds) {
		t.Errorf("Watch does not say how to reach the cluster:\n%s", got)
	}
}

// Credentials that do not name a cluster are a real state -- a channel may hold
// only some of what a context needs. Better a command without a context than one
// naming a cluster that is not there.
func TestWatchOmitsAnUnknownContextRatherThanGuessing(t *testing.T) {
	got := startedProbe("", "").Watch()
	if strings.Contains(got, "--context") {
		t.Errorf("a context was printed for credentials that name none:\n%s", got)
	}
	if !strings.Contains(got, "get job mendel-adapter-77ea75d8") {
		t.Errorf("the job is not named at all:\n%s", got)
	}
}

func TestTheContextNameNeedsAllThreeParts(t *testing.T) {
	whole := map[string]string{
		"GCP_PROJECT_ID":   "mendelpong",
		"GKE_CLUSTER_NAME": "pong-cluster",
		"GKE_ZONE":         "us-central1-a",
	}
	if got, want := gkeContextName(whole), "gke_mendelpong_us-central1-a_pong-cluster"; got != want {
		t.Errorf("gkeContextName = %q, want %q", got, want)
	}

	for missing := range whole {
		partial := map[string]string{}
		for k, v := range whole {
			if k != missing {
				partial[k] = v
			}
		}
		if got := gkeContextName(partial); got != "" {
			t.Errorf("without %s the context name is %q; a partial name points at no cluster "+
				"and a command carrying it fails less clearly than one with no context at all",
				missing, got)
		}
	}
}

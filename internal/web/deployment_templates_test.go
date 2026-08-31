package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// prodDeployment builds a production HostingDeployment in the given state.
func prodDeployment(status domain.HostingDeploymentStatus) *domain.HostingDeployment {
	url := "https://pong-prod.fly.dev"
	sha := "abcdef1234567890"
	errMsg := "flyctl deploy failed: exit status 1"

	d := &domain.HostingDeployment{
		ID:        uuid.New(),
		ProjectID: uuid.New(),
		ChannelID: uuid.New(),
		Kind:      domain.HostingDeploymentKindProd,
		AppName:   "pong-prod",
		CommitSHA: &sha,
		Status:    status,
		StartedAt: time.Now(),
	}
	switch status {
	case domain.HostingDeploymentStatusRunning:
		d.URL = &url
	case domain.HostingDeploymentStatusFailed:
		d.ErrorMessage = &errMsg
	}
	return d
}

func validatedChannel() *domain.ProjectDeploymentChannel {
	now := time.Now()
	return &domain.ProjectDeploymentChannel{
		ID:              uuid.New(),
		ProjectID:       uuid.New(),
		ArtifactKind:    domain.DeployArtifactContainer,
		DemoValidatedAt: &now,
		ProdValidatedAt: &now,
		CreatedAt:       now,
		HostingPlatform: &domain.HostingPlatform{Slug: "fly-io", Name: "Fly.io"},
	}
}

// renderPageForTest renders a page whose data lives in top-level keys rather
// than under "View", as the deployment and strategy pages do.
func renderPageForTest(t *testing.T, page string, data map[string]interface{}) string {
	t.Helper()
	rec := httptest.NewRecorder()
	if _, ok := data["Title"]; !ok {
		data["Title"] = "test"
	}
	if err := renderPage(rec, page, data); err != nil {
		t.Fatalf("rendering %s: %v", page, err)
	}
	body := rec.Body.String()
	dumpRendered(t, page, body)
	return body
}

// TestDeploymentChannelRendersDeployStates walks the production-deploy states
// the page has to cover. Each state lights up a different branch, and a bad
// field path only fails at execution time, so parsing alone would miss it.
func TestDeploymentChannelRendersDeployStates(t *testing.T) {
	project := &domain.Project{ID: uuid.New(), Name: "pong"}
	projectID := uuid.New().String()

	t.Run("no channel configured", func(t *testing.T) {
		renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
			"ProjectID": projectID, "Project": project,
		})
	})

	t.Run("validated but never deployed", func(t *testing.T) {
		body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
			"ProjectID": projectID, "Project": project, "Channel": validatedChannel(),
		})
		if !strings.Contains(body, "Deploy main to Production") {
			t.Error("a validated channel should offer a production deploy")
		}
	})

	t.Run("deploy running", func(t *testing.T) {
		live := prodDeployment(domain.HostingDeploymentStatusRunning)
		body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
			"ProjectID": projectID, "Project": project, "Channel": validatedChannel(),
			"ProdDeployment": live, "LatestProdDeployment": live,
			"ProdLogPanel": &LogPanel{
				DOMID: "prod-deploy-logs", FeedURL: "/api/deployments/x/logs",
				Status: string(live.Status),
				Lines:  []LogLine{{LoggedAt: time.Now(), Level: "milestone", Message: "Production deployed"}},
			},
			"ProdHistory": []domain.HostingDeployment{
				*live, *prodDeployment(domain.HostingDeploymentStatusFailed),
			},
		})
		for _, want := range []string{"https://pong-prod.fly.dev", "abcdef12", "Production deployed", "Deploy history"} {
			if !strings.Contains(body, want) {
				t.Errorf("running deploy page missing %q", want)
			}
		}
	})

	t.Run("deploy failed surfaces the error and logs", func(t *testing.T) {
		failed := prodDeployment(domain.HostingDeploymentStatusFailed)
		body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
			"ProjectID": projectID, "Project": project, "Channel": validatedChannel(),
			"LatestProdDeployment": failed,
			"ProdLogPanel": &LogPanel{
				DOMID: "prod-deploy-logs", FeedURL: "/api/deployments/x/logs",
				Status: string(failed.Status), Live: false,
				Lines:  []LogLine{{LoggedAt: time.Now(), Level: "error", Message: "flyctl exploded"}},
			},
		})
		// A failed deploy is the case the logs exist for; if the message or the
		// log body is missing the page is useless for diagnosing it.
		if !strings.Contains(body, "flyctl deploy failed") {
			t.Error("failed deploy should show its error message")
		}
		if !strings.Contains(body, "flyctl exploded") {
			t.Error("failed deploy should show its logs")
		}
	})

	// A running deploy streams its log rather than reloading the page. The
	// tailer reloads once the status changes, so the page must not also be
	// refreshing underneath it.
	t.Run("deploy in progress streams instead of reloading", func(t *testing.T) {
		running := prodDeployment(domain.HostingDeploymentStatusDeploying)
		body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
			"ProjectID": projectID, "Project": project, "Channel": validatedChannel(),
			"LatestProdDeployment": running,
			"ProdLogPanel": &LogPanel{
				DOMID: "prod-deploy-logs", FeedURL: "/api/deployments/x/logs",
				Status: string(running.Status), Live: true,
			},
		})
		if strings.Contains(body, `http-equiv="refresh"`) {
			t.Error("a streaming deploy must not also reload the whole page")
		}
		if !strings.Contains(body, `data-log-live="true"`) {
			t.Error("an in-progress deploy should stream its log")
		}
		if !strings.Contains(body, "disabled") {
			t.Error("an in-progress deploy should disable the deploy button")
		}
	})

	// Validation has no log feed, so it is still the one case that polls.
	t.Run("validation in progress still polls the page", func(t *testing.T) {
		ch := validatedChannel()
		now := time.Now()
		ch.ProdValidatedAt = nil
		ch.ProdValidatingAt = &now
		body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
			"ProjectID": projectID, "Project": project, "Channel": ch,
		})
		if !strings.Contains(body, `http-equiv="refresh"`) {
			t.Error("a running validation has no feed, so the page must still poll")
		}
	})
}

// TestDeploymentSummaryStates covers the one-line reading of a project's
// deployment, which the overview shows and which three separate pages used to
// render three different ways.
func TestDeploymentSummaryStates(t *testing.T) {
	const href = "/p/x/deployment"

	t.Run("deployed reports the live release", func(t *testing.T) {
		got := summarizeDeployment(href, validatedChannel(), prodDeployment(domain.HostingDeploymentStatusRunning))
		if !got.Configured {
			t.Error("a project with a channel is configured")
		}
		if got.URL != "https://pong-prod.fly.dev" {
			t.Errorf("URL = %q", got.URL)
		}
		if got.ShortCommit != "abcdef12" {
			t.Errorf("ShortCommit = %q", got.ShortCommit)
		}
		if got.Status.Tone != domain.ToneSuccess {
			t.Errorf("a live deployment should read as success, got %q", got.Status.Tone)
		}
	})

	// A configured, validated channel that has simply never shipped must not be
	// presented as unconfigured. The old card keyed off the production URL
	// rather than the channel, so this state rendered as "Set Up" and read as
	// though the channel still needed creating.
	t.Run("validated but never deployed is not unfinished setup", func(t *testing.T) {
		got := summarizeDeployment(href, validatedChannel(), nil)
		if !got.Configured {
			t.Error("an existing channel is configured, deployed or not")
		}
		if got.URL != "" {
			t.Error("a project that never deployed must not show a production URL")
		}
		if got.Status.Tone == domain.ToneNeutral {
			t.Errorf("a validated channel should not read as not-configured: %q", got.Status.Label)
		}
	})

	t.Run("no channel offers setup", func(t *testing.T) {
		got := summarizeDeployment(href, nil, nil)
		if got.Configured {
			t.Error("a project with no channel is not configured")
		}
		if got.Status.Label != "Not configured" {
			t.Errorf("Status.Label = %q", got.Status.Label)
		}
	})
}

// The overview has to render each of those states without erroring, since a bad

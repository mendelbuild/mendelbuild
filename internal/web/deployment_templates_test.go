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
	return rec.Body.String()
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
			"LatestProdLogs": []domain.HostingDeploymentLog{
				{LoggedAt: time.Now(), Level: domain.LogLevelMilestone, Message: "Production deployed"},
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
			"LatestProdLogs": []domain.HostingDeploymentLog{
				{LoggedAt: time.Now(), Level: domain.LogLevelError, Message: "flyctl exploded"},
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

	t.Run("deploy in progress disables the button and polls", func(t *testing.T) {
		body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
			"ProjectID": projectID, "Project": project, "Channel": validatedChannel(),
			"LatestProdDeployment": prodDeployment(domain.HostingDeploymentStatusDeploying),
		})
		if !strings.Contains(body, "http-equiv=\"refresh\"") {
			t.Error("an in-progress deploy should refresh so the status advances")
		}
		if !strings.Contains(body, "disabled") {
			t.Error("an in-progress deploy should disable the deploy button")
		}
	})
}

// TestStrategyRendersProdDeployment covers the deployment card on the strategy
// page, which reads the live deployment rather than the channel now.
func TestStrategyRendersProdDeployment(t *testing.T) {
	projectID := uuid.New()
	strategyView := &StrategyView{
		Project:  &domain.Project{ID: projectID, Name: "pong"},
		Strategy: &domain.Strategy{ID: uuid.New(), Name: "Q3"},
	}

	t.Run("deployed shows url and commit", func(t *testing.T) {
		body := renderPageForTest(t, "strategy.html", map[string]interface{}{
			"ProjectID": projectID.String(), "Strategy": strategyView,
			"DeploymentChannel": validatedChannel(),
			"ProdDeployment":    prodDeployment(domain.HostingDeploymentStatusRunning),
		})
		for _, want := range []string{"https://pong-prod.fly.dev", "abcdef12", "Manage"} {
			if !strings.Contains(body, want) {
				t.Errorf("strategy page missing %q", want)
			}
		}
	})

	t.Run("channel but never deployed shows validation state", func(t *testing.T) {
		body := renderPageForTest(t, "strategy.html", map[string]interface{}{
			"ProjectID": projectID.String(), "Strategy": strategyView,
			"DeploymentChannel": validatedChannel(),
		})
		if !strings.Contains(body, "Prod validated") {
			t.Error("a validated-but-undeployed channel should say so")
		}
		if strings.Contains(body, "pong-prod.fly.dev") {
			t.Error("a project that never deployed must not show a production URL")
		}
	})

	t.Run("no channel offers setup", func(t *testing.T) {
		body := renderPageForTest(t, "strategy.html", map[string]interface{}{
			"ProjectID": projectID.String(), "Strategy": strategyView,
			"SupportedCombos": []domain.SupportedDeploymentCombo{{}},
		})
		if !strings.Contains(body, "Set Up Deployment") {
			t.Error("a project with no channel should be offered setup")
		}
	})
}

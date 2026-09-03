package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bhs/mendelbuild/internal/domain"
)

// A settings area nothing links to is a settings area nobody finds.
func TestExperimentsTabIsReachable(t *testing.T) {
	s := &Server{}
	s.setupRoutes()

	var hasPage, hasSave bool
	chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		switch {
		case method == http.MethodGet && strings.HasSuffix(route, "/experiments"):
			hasPage = true
		case method == http.MethodPost && strings.HasSuffix(route, "/experiments/verify-datastore"):
			hasSave = true
		}
		return nil
	})
	if !hasPage {
		t.Error("no route serves the live-traffic experiments settings page")
	}
	if !hasSave {
		t.Error("the verification datastore cannot be saved")
	}

	tabs := readTemplateFile(t, "partials.html")
	if !strings.Contains(tabs, `/experiments">Live-traffic experiments`) {
		t.Error("the settings tabs do not link to the experiments area")
	}
}

func TestExperimentsPageRenders(t *testing.T) {
	obs := domain.ExperimentObservation{
		GatewayAPI:           domain.FactFalse,
		EnableGatewayCommand: "gcloud container clusters update c --location l --gateway-api=standard --project p",
		ProdHostname:         domain.FactTrue,
		ProdHost:             "app.example.com",
		ProdHTTPS:            domain.FactTrue,
		VerifyDatastore:      domain.FactFalse,
	}
	steps := domain.ExperimentReadiness(obs)
	headline, blocked := domain.ExperimentHeadline(steps)

	var out strings.Builder
	err := parsePageTemplate("project_experiments.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"SettingsTab": "experiments", "ProjectID": "abc", "Steps": steps,
		"Headline": headline, "Blocked": blocked, "Checking": false,
		"CheckedLabel": "just now", "Observation": obs,
		"DatastoreVar": VerifyDatastoreVar, "Success": false, "Error": "",
	})
	if err != nil {
		t.Fatalf("experiments page does not render: %v", err)
	}
	html := out.String()

	// The remedy for a missing Gateway API is a command, and it has to be
	// copyable rather than transcribed.
	for _, want := range []string{
		"--gateway-api=standard",
		`id="gateway-command"`,
		`data-copy="gateway-command"`,
		"/static/js/copy-button.js",
		"Verification datastore",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// The command only appears when it is the remedy. Showing it beside a cluster
// that already has Gateway API invites someone to run it looking for a problem
// that is not there.
func TestGatewayCommandOnlyShownWhenItIsTheRemedy(t *testing.T) {
	obs := domain.ExperimentObservation{
		GatewayAPI:           domain.FactTrue,
		EnableGatewayCommand: "gcloud container clusters update c --location l --gateway-api=standard --project p",
		ProdHostname:         domain.FactTrue, ProdHTTPS: domain.FactTrue,
		VerifyDatastore: domain.FactTrue, VerifyReachable: domain.FactTrue,
	}
	steps := domain.ExperimentReadiness(obs)
	headline, blocked := domain.ExperimentHeadline(steps)

	var out strings.Builder
	if err := parsePageTemplate("project_experiments.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"SettingsTab": "experiments", "ProjectID": "abc", "Steps": steps,
		"Headline": headline, "Blocked": blocked, "Checking": false,
		"CheckedLabel": "just now", "Observation": obs,
		"DatastoreVar": VerifyDatastoreVar, "Success": false, "Error": "",
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out.String(), "--gateway-api=standard") {
		t.Error("the enable command is offered to a cluster that already has Gateway API")
	}
}

package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// A project's settings page is the only place delete is offered from, so if the
// form stops rendering there is no other way to reach it. The db package covers
// what deleting means; this covers whether anyone can ask for it.
func TestSettingsOffersDeleteAndSaysWhatItDoes(t *testing.T) {
	projectID := uuid.New().String()
	body := renderChrome(t, "project_settings.html", "/p/"+projectID+"/settings", map[string]interface{}{
		"ProjectID":   projectID,
		"ProjectName": "ledger",
		"SettingsTab": "project",
		"Settings":    ProjectSettings{MainBranch: "main"},
	})

	if !strings.Contains(body, "/p/"+projectID+"/settings/delete") {
		t.Error("the settings page does not post anywhere to delete the project")
	}
	if !strings.Contains(body, `name="confirm_name"`) {
		t.Error("delete is offered without asking for the project's name first")
	}
	if !strings.Contains(body, "ledger") {
		t.Error("the confirmation does not say which name to type")
	}
	// The reader has to know that nothing is erased, or the button is scarier
	// than the thing it does.
	if !strings.Contains(body, "restore") {
		t.Error("the page does not say the project can be restored")
	}
}

// The decline has to name what is running and where to stop it. A refusal that
// says only "cannot delete" leaves the reader with nothing to do next.
func TestABlockedDeleteNamesWhatIsRunning(t *testing.T) {
	projectID := uuid.New().String()
	body := renderChrome(t, "project_settings.html", "/p/"+projectID+"/settings", map[string]interface{}{
		"ProjectID":   projectID,
		"ProjectName": "ledger",
		"SettingsTab": "project",
		"Settings":    ProjectSettings{MainBranch: "main"},
		"DeleteError": "blocked",
		"DeletionBlockers": []domain.ProjectDeletionBlocker{{
			Name:    "A demo of cache-layer is running",
			Missing: "Stop the demo, so the hosting it is using is released.",
			Path:    "/p/" + projectID + "/variations/abc",
		}},
	})

	for _, want := range []string{
		"A demo of cache-layer is running",
		"Stop the demo",
		"/p/" + projectID + "/variations/abc",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the decline does not mention %q", want)
		}
	}
}

// Production is a warning rather than a gate, and the difference only helps if
// the warning actually says what will be left running, and where.
func TestAProductionDeploymentIsWarnedAboutBeforeDeleting(t *testing.T) {
	projectID := uuid.New().String()
	body := renderChrome(t, "project_settings.html", "/p/"+projectID+"/settings", map[string]interface{}{
		"ProjectID":   projectID,
		"ProjectName": "ledger",
		"SettingsTab": "project",
		"Settings":    ProjectSettings{MainBranch: "main"},
		"ProdStillUp": "ledger-prod",
	})

	if !strings.Contains(body, "ledger-prod") {
		t.Error("the warning does not name the deployment that stays up")
	}
	if !strings.Contains(body, "/p/"+projectID+"/settings/delete") {
		t.Error("a running production deployment removed the delete form; it is a warning, not a gate")
	}
}

// Every project-scoped route hangs off /p/{projectID}, and requireLiveProject
// is what makes "deleted" mean more than "absent from the dashboard". If it is
// ever dropped from the route tree, a retired project's pages, forms and JSON
// all quietly answer again.
func TestEveryProjectScopedRouteIsBehindTheLiveProjectCheck(t *testing.T) {
	s := &Server{}
	s.setupRoutes()

	var unguarded []string
	err := chi.Walk(s.router, func(method, route string, _ http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if !strings.Contains(route, "{projectID}") {
			return nil
		}
		// chi hands back the middleware chain; the live check is the one whose
		// presence is being asserted, and comparing behaviour is more robust
		// than comparing function pointers.
		guarded := false
		for _, mw := range middlewares {
			rec := &recordingHandler{}
			h := mw(rec)
			req, _ := http.NewRequest(method, "/", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("projectID", "not-a-uuid")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			w := &statusRecorder{}
			h.ServeHTTP(w, req)
			// requireLiveProject rejects an unparseable project ID before it
			// touches the database, which nothing else in the chain does.
			if !rec.called && w.status == http.StatusBadRequest {
				guarded = true
			}
		}
		if !guarded {
			unguarded = append(unguarded, method+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}
	if len(unguarded) > 0 {
		t.Errorf("project-scoped routes not behind requireLiveProject:\n  %s",
			strings.Join(unguarded, "\n  "))
	}
}

type recordingHandler struct{ called bool }

func (h *recordingHandler) ServeHTTP(http.ResponseWriter, *http.Request) { h.called = true }

type statusRecorder struct {
	status int
	header http.Header
}

func (w *statusRecorder) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *statusRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (w *statusRecorder) WriteHeader(code int)        { w.status = code }

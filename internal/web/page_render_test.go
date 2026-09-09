package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/testdb"
)

// Every page a project has, actually fetched, asserting each one arrives whole.
//
// Twice now a page has been served as a 200 with the bottom missing, because
// html/template reports a bad field reference only when execution reaches it and
// the handler was writing straight into the ResponseWriter. Neither failure was
// visible: the OKR editor's objective page kept rendering its Edit buttons and
// dropped the script that made them work, and the new-project page kept its
// heading and dropped the form. The second went nine days.
//
// Both were introduced by editing something shared -- a partial that gained a
// field, a helper that started taking a different type -- so neither showed up in
// the page being worked on. Rendering each page against a real database is the
// only check that sees them, and it is cheap: no browser, no fixtures beyond one
// seeded project.
func TestEveryProjectPageRendersWhole(t *testing.T) {
	database, ids := renderTestDB(t)

	s := &Server{db: database}
	s.setupRoutes()

	for _, path := range projectPagePaths(t, s, ids) {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			body := rec.Body.String()

			// A template that died mid-execution is the failure this test is
			// for, and it reaches the caller two ways depending on whether the
			// handler buffers: as a 500 carrying the message, or as a 200 with
			// the message trailing whatever had already been written.
			if strings.Contains(body, "can't evaluate field") ||
				strings.Contains(body, `executing "`) {
				t.Fatalf("template failed mid-render (status %d): %s",
					rec.Code, lastLine(body))
			}
			if rec.Code >= 500 {
				t.Fatalf("status %d: %s", rec.Code, lastLine(body))
			}

			// Reaching the end of the layout is the thing a truncated page
			// cannot fake, and it is the assertion the two bugs above needed:
			// both returned a 200 whose only symptom was a missing tail.
			//
			// Judged on the response's own content type rather than on the
			// route, so a handler that starts answering JSON stops being held
			// to it automatically. Redirects have no body to judge.
			isHTML := strings.Contains(rec.Header().Get("Content-Type"), "text/html")
			if rec.Code == http.StatusOK && isHTML && !strings.Contains(body, "</html>") {
				t.Errorf("page stopped before the end of the layout: %s", lastLine(body))
			}
		})
	}
}

// projectPagePaths is every GET route reachable with a project and the ids the
// fixture supplies.
//
// Derived from the router rather than listed by hand, so a page added tomorrow
// is covered without anyone remembering to add it here. Routes wanting an id the
// fixture does not have -- a hop, a variation, a deployment -- are named in the
// test log rather than passed over quietly, because a page that stops being
// checked is exactly how both of these bugs survived.
func projectPagePaths(t *testing.T, s *Server, ids fixtureIDs) []string {
	t.Helper()

	fill := map[string]string{
		"projectID":   ids.Project.String(),
		"objectiveID": ids.Objective.String(),
	}

	var paths, unfilled []string
	chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// Wildcards are file serving, not pages, and the literal "*" is not a
		// path anything is reachable at.
		if method != http.MethodGet || strings.HasPrefix(route, "/api/") ||
			strings.Contains(route, "*") {
			return nil
		}

		path := route
		missing := false
		for _, segment := range strings.Split(route, "/") {
			if !strings.HasPrefix(segment, "{") {
				continue
			}
			name := strings.Trim(segment, "{}")
			value, ok := fill[name]
			if !ok {
				missing = true
				break
			}
			path = strings.Replace(path, segment, value, 1)
		}
		if missing {
			unfilled = append(unfilled, route)
			return nil
		}
		paths = append(paths, path)
		return nil
	})

	if len(unfilled) > 0 {
		t.Logf("not covered, no fixture id for their parameters:\n  %s",
			strings.Join(unfilled, "\n  "))
	}
	return paths
}

// fixtureIDs are the rows the pages under test hang from.
type fixtureIDs struct {
	Project   uuid.UUID
	Strategy  uuid.UUID
	Objective uuid.UUID
}

// renderTestDB builds a throwaway schema holding one project far enough along
// to have something on every page: a strategy with a drafted, unapproved
// objective, a key result, a budget, and a consideration in each of the three
// coverage states.
func renderTestDB(t *testing.T) (*db.DB, fixtureIDs) {
	t.Helper()
	connString := testdb.Require(t)
	ctx := context.Background()

	schemaName := "test_pages_" + uuid.New().String()[:8]
	admin, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), connString)
		if err != nil {
			return
		}
		defer cleanup.Close()
		cleanup.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE")
	})

	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(pool.Close)

	full, err := os.ReadFile(filepath.Join("..", "..", "schema", "full.sql"))
	if err != nil {
		t.Fatalf("read full.sql: %v", err)
	}
	if _, err := pool.Exec(ctx, string(full)); err != nil {
		t.Fatalf("load full.sql: %v", err)
	}

	ids := fixtureIDs{Project: uuid.New(), Strategy: uuid.New(), Objective: uuid.New()}
	keyResultID := uuid.New()

	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects (id, name, brief) VALUES ($1, $2, $3)`,
			[]any{ids.Project, "Fixture", "A tool for polling a group and estimating what the rest think."}},
		{`INSERT INTO strategies (id, project_id, name, draft_status, draft_notes)
		  VALUES ($1, $2, $3, 'ready', $4)`,
			[]any{ids.Strategy, ids.Project, "Fixture Pilot",
				`{"summary":"What Mendel understood.","assumptions":["Email only."],` +
					`"open_questions":["Who verifies membership?"],"budget_note":"Tight."}`}},
		{`INSERT INTO objectives (id, strategy_id, description) VALUES ($1, $2, $3)`,
			[]any{ids.Objective, ids.Strategy, "The people it surveys get something back for answering"}},
		{`INSERT INTO key_results (id, strategy_id, description, target_comparator,
		                           target_value, target_unit, target_date)
		  VALUES ($1, $2, $3, 'at_least', 40, '% of those asked', CURRENT_DATE + 30)`,
			[]any{keyResultID, ids.Strategy, "Share of a sample who complete the survey"}},
		{`INSERT INTO objective_key_result_pairs (objective_id, key_result_id) VALUES ($1, $2)`,
			[]any{ids.Objective, keyResultID}},
		{`INSERT INTO funding_sources (id, strategy_id, name, amount_usd, period_start, period_end)
		  VALUES ($1, $2, 'MVP build', 250, CURRENT_DATE, CURRENT_DATE + 90)`,
			[]any{uuid.New(), ids.Strategy}},

		// One consideration in each coverage state, so the review screen renders
		// all three branches rather than only the happy one.
		{`INSERT INTO strategic_considerations
		     (id, strategy_id, kind, statement, covered_by_objective_id, position)
		  VALUES ($1, $2, 'failure_mode', 'Nobody answers, so the sample is worthless', $3, 0)`,
			[]any{uuid.New(), ids.Strategy, ids.Objective}},
		{`INSERT INTO strategic_considerations
		     (id, strategy_id, kind, statement, uncovered_reason, position)
		  VALUES ($1, $2, 'party', 'A program reading the results on a schedule',
		          'Nothing calls this yet, and an interface with no caller cannot be judged.', 1)`,
			[]any{uuid.New(), ids.Strategy}},
		{`INSERT INTO strategic_considerations (id, strategy_id, kind, statement, position)
		  VALUES ($1, $2, 'failure_mode', 'The estimate is wrong in public', 2)`,
			[]any{uuid.New(), ids.Strategy}},
	}
	for _, st := range stmts {
		if _, err := pool.Exec(ctx, st.sql, st.args...); err != nil {
			t.Fatalf("seed (%s): %v", firstLine(st.sql), err)
		}
	}

	return &db.DB{Pool: pool}, ids
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// lastLine is where a template error lands: at the end of whatever had been
// written before execution stopped.
func lastLine(body string) string {
	trimmed := strings.TrimRight(body, "\n")
	if i := strings.LastIndexByte(trimmed, '\n'); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	if len(trimmed) > 300 {
		return trimmed[:300] + "..."
	}
	return trimmed
}

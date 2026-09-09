package db

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Retiring a project has to close every way of reaching it, not merely the
// dashboard. Each of these is a separate query written in a separate place,
// which is exactly why they are asserted together: adding a fifth read path
// that forgets the filter is the way this regresses.
func TestRetiredProjectDisappearsFromEveryReadPath(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	userID := seedUser(t, db)
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO project_members (id, project_id, user_id, role) VALUES ($1, $2, $3, 'owner')`,
		uuid.New(), projectID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	if _, err := db.GetProject(ctx, projectID); err != nil {
		t.Fatalf("a live project is not readable: %v", err)
	}
	if mine, err := db.GetUserProjects(ctx, userID); err != nil || len(mine) != 1 {
		t.Fatalf("a live project is not listed for its owner: %v, %d", err, len(mine))
	}

	if blockers, err := db.SoftDeleteProject(ctx, projectID, &userID); err != nil || len(blockers) > 0 {
		t.Fatalf("deleting an idle project: %v, %v", err, blockers)
	}

	if _, err := db.GetProject(ctx, projectID); err == nil {
		t.Error("GetProject still returns a retired project")
	}
	if _, err := db.GetProjectByName(ctx, "test"); err == nil {
		t.Error("GetProjectByName still returns a retired project")
	}
	if mine, err := db.GetUserProjects(ctx, userID); err != nil || len(mine) != 0 {
		t.Errorf("GetUserProjects still lists a retired project: %v, %d", err, len(mine))
	}

	// The row is still there, which is the whole point: an administrator can
	// undo this, and the ledger hanging off it stays readable.
	var name string
	if err := db.Pool.QueryRow(ctx,
		`SELECT name FROM projects WHERE id = $1 AND deleted_at IS NOT NULL AND deleted_by = $2`,
		projectID, userID).Scan(&name); err != nil {
		t.Fatalf("the row was not kept and marked: %v", err)
	}
}

// Deleting a project has to stop it spending. The worker sweeps hops across
// every project at once, so a retired project whose hops still match would go
// on making agent calls nobody can see a page for, let alone stop.
func TestTheWorkerStopsSweepingARetiredProject(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	seedVariation(t, db, projectID, "creating")
	seedActiveHopWithNoVariations(t, db, projectID)

	creating, err := db.GetHopsWithCreatingVariations(ctx)
	if err != nil || len(creating) != 1 {
		t.Fatalf("a live project's creating variations are not swept: %v, %d", err, len(creating))
	}
	proposing, err := db.GetHopsNeedingVariationProposal(ctx)
	if err != nil || len(proposing) != 1 {
		t.Fatalf("a live project's empty hop is not swept: %v, %d", err, len(proposing))
	}

	if blockers, err := db.SoftDeleteProject(ctx, projectID, nil); err != nil || len(blockers) > 0 {
		t.Fatalf("deleting: %v, %v", err, blockers)
	}

	if hops, err := db.GetHopsWithCreatingVariations(ctx); err != nil || len(hops) != 0 {
		t.Errorf("a retired project's creating variations are still swept: %v, %d", err, len(hops))
	}
	if hops, err := db.GetHopsNeedingVariationProposal(ctx); err != nil || len(hops) != 0 {
		t.Errorf("a retired project is still proposed variations for: %v, %d", err, len(hops))
	}
	if hops, err := db.GetHopsReadyForSelection(ctx); err != nil || len(hops) != 0 {
		t.Errorf("a retired project is still considered for selection: %v, %d", err, len(hops))
	}
	if hops, err := db.GetHopsNeedingSelectionInputRequest(ctx); err != nil || len(hops) != 0 {
		t.Errorf("a retired project still gets selection input requests: %v, %d", err, len(hops))
	}
}

// Retiring marks a row; it does not reach out to a hosting platform and stop
// anything. So a demo still serving would go on serving, and go on billing,
// against a project no page in the app would list. Mendel declines instead.
func TestDeleteDeclinesWhileADemoIsRunning(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	variationID := seedVariation(t, db, projectID, "pending")
	channelID := seedDeploymentChannel(t, db, projectID)
	demoID := seedDeployment(t, db, projectID, channelID, "demo", &variationID, "acme-demo", "running")

	blockers, err := db.SoftDeleteProject(ctx, projectID, nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(blockers) != 1 {
		t.Fatalf("a running demo did not stop the delete: %v", blockers)
	}
	if blockers[0].Missing == "" {
		t.Error("the blocker does not say what would make deletion possible")
	}
	if blockers[0].Path == "" {
		t.Error("the blocker does not say where to go and stop it")
	}
	if _, err := db.GetProject(ctx, projectID); err != nil {
		t.Fatalf("the project was retired anyway: %v", err)
	}

	if err := db.TerminateHostingDeployment(ctx, demoID, ""); err != nil {
		t.Fatalf("stop demo: %v", err)
	}
	if blockers, err := db.SoftDeleteProject(ctx, projectID, nil); err != nil || len(blockers) > 0 {
		t.Fatalf("still declined once the demo stopped: %v, %v", err, blockers)
	}
}

// A live experiment splits real traffic between Arms. Retiring the project it
// belongs to would leave that split running with nothing left to read it by.
func TestDeleteDeclinesWhileAnExperimentIsRunning(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	variationID := seedVariation(t, db, projectID, "pending")
	var hopID uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`SELECT hop_id FROM variations WHERE id = $1`, variationID).Scan(&hopID); err != nil {
		t.Fatalf("find hop: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO experiments (id, project_id, hop_id, assignment_unit,
		   assignment_key_source, assignment_key_name, status,
		   minimum_detectable_effect, planned_duration_hours, stopping_rule)
		 VALUES ($1, $2, $3, 'user', 'cookie', 'session_id', 'running',
		   0.05, 72, 'fixed_horizon')`,
		uuid.New(), projectID, hopID); err != nil {
		t.Fatalf("seed experiment: %v", err)
	}

	blockers, err := db.SoftDeleteProject(ctx, projectID, nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(blockers) != 1 {
		t.Fatalf("a running experiment did not stop the delete: %v", blockers)
	}
	if _, err := db.GetProject(ctx, projectID); err != nil {
		t.Fatalf("the project was retired anyway: %v", err)
	}
}

// Production serving is now a gate rather than a warning, and this is the pair
// of assertions that makes it a legitimate one: it refuses while production is
// up, and it stops refusing once production is taken down. Before there was a
// route that took production down, and code that marked the row terminated, the
// second half of that could not have been written -- which is why this was a
// warning and not a gate.
func TestDeleteDeclinesWhileProductionIsServing(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	channelID := seedDeploymentChannel(t, db, projectID)
	deploymentID := seedDeployment(t, db, projectID, channelID, "prod", nil, "acme-prod", "running")

	blockers, err := db.SoftDeleteProject(ctx, projectID, nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(blockers) != 1 {
		t.Fatalf("a serving production deployment did not stop the delete: %v", blockers)
	}
	if !strings.Contains(blockers[0].Name, "acme-prod") {
		t.Errorf("the blocker does not name the deployment: %q", blockers[0].Name)
	}
	if blockers[0].Missing == "" || blockers[0].Path == "" {
		t.Error("the blocker does not say what would make deletion possible, or where")
	}
	if _, err := db.GetProject(ctx, projectID); err != nil {
		t.Fatalf("the project was retired anyway: %v", err)
	}

	if err := db.TerminateHostingDeployment(ctx, deploymentID, ""); err != nil {
		t.Fatalf("take production down: %v", err)
	}
	if blockers, err := db.SoftDeleteProject(ctx, projectID, nil); err != nil || len(blockers) > 0 {
		t.Fatalf("still declined once production was taken down: %v, %v", err, blockers)
	}
}

// A deploy still coming up is as much a reason to refuse as one already
// serving: it is creating infrastructure right now, and a project retired
// underneath it would leave an app nobody can find the page for.
func TestDeleteDeclinesWhileADeployIsStillInFlight(t *testing.T) {
	db, projectID := testDB(t)

	channelID := seedDeploymentChannel(t, db, projectID)
	seedDeployment(t, db, projectID, channelID, "prod", nil, "acme-prod", "deploying")

	blockers, err := db.SoftDeleteProject(context.Background(), projectID, nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(blockers) != 1 {
		t.Fatalf("an in-flight deploy did not stop the delete: %v", blockers)
	}
}

// A deployment that is over stops mattering. Failed and terminated are the two
// ways that happens, and neither should keep a project alive forever.
func TestFinishedDeploymentsDoNotBlockDeletion(t *testing.T) {
	db, projectID := testDB(t)

	variationID := seedVariation(t, db, projectID, "pending")
	channelID := seedDeploymentChannel(t, db, projectID)
	seedDeployment(t, db, projectID, channelID, "prod", nil, "acme-prod", "terminated")
	seedDeployment(t, db, projectID, channelID, "demo", &variationID, "acme-demo", "failed")

	if blockers, err := db.SoftDeleteProject(context.Background(), projectID, nil); err != nil || len(blockers) > 0 {
		t.Fatalf("a finished deployment blocked the delete: %v, %v", err, blockers)
	}
}

// Pressing delete twice is one person clicking twice, not an error worth
// showing them.
func TestDeletingTwiceIsNotAnError(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	if blockers, err := db.SoftDeleteProject(ctx, projectID, nil); err != nil || len(blockers) > 0 {
		t.Fatalf("first delete: %v, %v", err, blockers)
	}
	if blockers, err := db.SoftDeleteProject(ctx, projectID, nil); err != nil || len(blockers) > 0 {
		t.Fatalf("second delete: %v, %v", err, blockers)
	}
}

// "Retired" and "never existed" are different answers, and the middleware needs
// to tell them apart in order to say which happened.
func TestProjectIsLiveSeparatesRetiredFromAbsent(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	if live, exists, err := db.ProjectIsLive(ctx, projectID); err != nil || !live || !exists {
		t.Fatalf("a live project: %v, live=%v exists=%v", err, live, exists)
	}
	if _, err := db.SoftDeleteProject(ctx, projectID, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if live, exists, err := db.ProjectIsLive(ctx, projectID); err != nil || live || !exists {
		t.Fatalf("a retired project: %v, live=%v exists=%v", err, live, exists)
	}
	if live, exists, err := db.ProjectIsLive(ctx, uuid.New()); err != nil || live || exists {
		t.Fatalf("a project that never existed: %v, live=%v exists=%v", err, live, exists)
	}
}

func seedUser(t *testing.T, db *DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO users (id, email) VALUES ($1, $2)`, id, id.String()+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

// seedActiveHopWithNoVariations creates the shape the proposal sweep looks for.
func seedActiveHopWithNoVariations(t *testing.T, db *DB, projectID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var strategyID uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`SELECT id FROM strategies WHERE project_id = $1`, projectID).Scan(&strategyID); err != nil {
		t.Fatalf("find strategy: %v", err)
	}
	hopID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO hops (id, strategy_id, name, commentary, status)
		 VALUES ($1, $2, $3, 'why', 'active')`,
		hopID, strategyID, "empty-"+hopID.String()[:6]); err != nil {
		t.Fatalf("seed hop: %v", err)
	}
	return hopID
}

// seedDeploymentChannel gives a project somewhere to deploy through, which a
// hosting deployment cannot exist without.
func seedDeploymentChannel(t *testing.T, db *DB, projectID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	platformID, channelID := uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO hosting_platforms (id, slug, name, deployer_image, instructions)
		 VALUES ($1, $2, 'Test platform', 'alpine:latest', 'none')`,
		platformID, "test-"+platformID.String()[:6]); err != nil {
		t.Fatalf("seed platform: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO project_deployment_channels (id, project_id, artifact_kind, hosting_platform_id)
		 VALUES ($1, $2, 'container', $3)`, channelID, projectID, platformID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return channelID
}

// seedDeployment writes one hosting deployment in whatever state the test needs
// it in. Demos and production deploys are the same row since 053, so one helper
// covers both.
func seedDeployment(t *testing.T, db *DB, projectID, channelID uuid.UUID,
	kind string, variationID *uuid.UUID, appName, status string) uuid.UUID {
	t.Helper()

	id := uuid.New()
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO hosting_deployments
		   (id, project_id, channel_id, kind, variation_id, app_name, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, projectID, channelID, kind, variationID, appName, status); err != nil {
		t.Fatalf("seed %s deployment: %v", kind, err)
	}
	return id
}

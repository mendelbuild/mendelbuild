package db

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// seedPlatform inserts a hosting platform and returns its ID.
func seedPlatform(t *testing.T, db *DB, slug string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Pool.Exec(context.Background(),
		`INSERT INTO hosting_platforms (id, slug, name, deployer_image, instructions)
		 VALUES ($1, $2, $2, 'img', 'how')`, id, slug)
	if err != nil {
		t.Fatalf("seed platform %s: %v", slug, err)
	}
	return id
}

// TestSwitchDeploymentChannel covers moving a project that already deploys
// somewhere onto a different platform — the path taken when the pong project
// moved from Fly.io to GKE, which nothing had exercised before.
//
// The partial unique index only permits one channel per project with
// disabled_at IS NULL, so a switch that failed to retire the old row would not
// produce two active channels; it would fail outright.
func TestSwitchDeploymentChannel(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	flyID := seedPlatform(t, db, "fly-io")
	gkeID := seedPlatform(t, db, "gke")

	fly := &domain.ProjectDeploymentChannel{
		ProjectID:         projectID,
		ArtifactKind:      domain.DeployArtifactContainer,
		HostingPlatformID: flyID,
	}
	if err := db.CreateProjectDeploymentChannel(ctx, fly); err != nil {
		t.Fatalf("create fly channel: %v", err)
	}

	// The channel being replaced is a fully validated one; that is the only
	// interesting case, since an unvalidated channel has nothing to lose.
	if err := db.CompleteDemoValidation(ctx, fly.ID); err != nil {
		t.Fatalf("mark demo validated: %v", err)
	}

	gke := &domain.ProjectDeploymentChannel{
		ProjectID:         projectID,
		ArtifactKind:      domain.DeployArtifactKubernetes,
		HostingPlatformID: gkeID,
	}
	if err := db.CreateProjectDeploymentChannel(ctx, gke); err != nil {
		t.Fatalf("switch to gke: %v", err)
	}

	active, err := db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil {
		t.Fatalf("get active channel: %v", err)
	}
	if active.ID != gke.ID {
		t.Fatalf("active channel is %s, want the new gke channel %s", active.ID, gke.ID)
	}
	if active.ArtifactKind != domain.DeployArtifactKubernetes {
		t.Errorf("artifact kind = %q, want kubernetes", active.ArtifactKind)
	}

	// A new channel starts unvalidated even though the one it replaced was
	// validated. Carrying the old timestamps over would let a demo deploy to a
	// platform whose credentials had never been proven to work.
	if active.IsDemoValidated() {
		t.Error("new channel inherited demo validation from the channel it replaced")
	}

	var disabled bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT disabled_at IS NOT NULL FROM project_deployment_channels WHERE id = $1`,
		fly.ID).Scan(&disabled); err != nil {
		t.Fatalf("read old channel: %v", err)
	}
	if !disabled {
		t.Error("previous channel is still active after the switch")
	}
}

package db

import (
	"context"
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/hosting"
)

// TestRefreshCombosAddsMissingCombo covers the case a seed-only path cannot
// reach: an installation whose combo table was populated before a combo existed.
//
// This is how staging ended up unable to offer GKE. The kubernetes/gke combo was
// in DefaultCombos and in no database, and because seeding only runs on an empty
// table there was no command that would ever add it.
func TestRefreshCombosAddsMissingCombo(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()

	flyID := seedPlatform(t, db, "fly-io")
	seedPlatform(t, db, "gke")

	// An installation seeded back when Fly.io was the only pairing.
	notes := "Single container deployment to Fly.io"
	existing := &domain.SupportedDeploymentCombo{
		ArtifactKind:      domain.DeployArtifactContainer,
		HostingPlatformID: flyID,
		Notes:             &notes,
		Guidance:          []byte(`{}`),
	}
	if err := db.CreateSupportedDeploymentCombo(ctx, existing); err != nil {
		t.Fatalf("seed existing combo: %v", err)
	}

	// Seeding is a no-op on a non-empty table, which is the whole problem.
	seeded, err := hosting.SeedCombosIfEmpty(ctx, db)
	if err != nil {
		t.Fatalf("seed combos: %v", err)
	}
	if seeded != 0 {
		t.Fatalf("seeded %d combos into a non-empty table, want 0", seeded)
	}
	if hasGKECombo(t, db) {
		t.Fatal("gke combo present before refresh; the test no longer reproduces the gap")
	}

	if _, err := hosting.RefreshCombos(ctx, db); err != nil {
		t.Fatalf("refresh combos: %v", err)
	}
	if !hasGKECombo(t, db) {
		t.Error("refresh did not add the kubernetes/gke combo")
	}

	// Refresh has to be safe to run repeatedly; the unique constraint on
	// (artifact_kind, hosting_platform_id) would otherwise turn it into an error.
	if _, err := hosting.RefreshCombos(ctx, db); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	var gkeCount int
	if err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM supported_deployment_combos c
		JOIN hosting_platforms hp ON c.hosting_platform_id = hp.id
		WHERE hp.slug = 'gke'`).Scan(&gkeCount); err != nil {
		t.Fatalf("count gke combos: %v", err)
	}
	if gkeCount != 1 {
		t.Errorf("gke combo row count = %d after two refreshes, want 1", gkeCount)
	}
}

func hasGKECombo(t *testing.T, db *DB) bool {
	t.Helper()
	var exists bool
	if err := db.Pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM supported_deployment_combos c
			JOIN hosting_platforms hp ON c.hosting_platform_id = hp.id
			WHERE hp.slug = 'gke' AND c.artifact_kind = 'kubernetes'
		)`).Scan(&exists); err != nil {
		t.Fatalf("check gke combo: %v", err)
	}
	return exists
}

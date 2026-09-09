package db

import (
	"context"
	"testing"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
)

// deployer opens deployments through one channel, the way a project really
// has one: project_deployment_channels allows a single active channel per
// project, so every deployment in a test hangs off the same one.
func deployer(t *testing.T, db *DB, projectID uuid.UUID) func(domain.HostingDeploymentKind, *uuid.UUID, string) *domain.HostingDeployment {
	t.Helper()
	channelID := seedDeploymentChannel(t, db, projectID)

	return func(kind domain.HostingDeploymentKind, variationID *uuid.UUID, appName string) *domain.HostingDeployment {
		t.Helper()
		d := &domain.HostingDeployment{
			ProjectID:   projectID,
			ChannelID:   channelID,
			Kind:        kind,
			VariationID: variationID,
			AppName:     appName,
		}
		if err := db.CreateHostingDeployment(context.Background(), d); err != nil {
			t.Fatalf("create deployment: %v", err)
		}
		return d
	}
}

// The column the hosting meter reads to decide whether something is still
// costing money. It used to be set the moment a deploy succeeded, which read as
// "this deployment stopped" to the only code that consults it. Nothing else in
// the tree ever set it, so every deployment's billable window closed at the
// instant it opened.
func TestADeploymentThatIsUpHasNotFinished(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	open := deployer(t, db, projectID)

	d := open(domain.HostingDeploymentKindProd, nil, "acme-prod")
	if err := db.CompleteHostingDeployment(ctx, d.ID, "https://acme.example", "true"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	got, err := db.GetHostingDeployment(ctx, d.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Status != domain.HostingDeploymentStatusRunning {
		t.Errorf("a completed deploy is not running: %q", got.Status)
	}
	if got.FinishedAt != nil {
		t.Error("a deployment that is up and serving was recorded as finished")
	}
}

// The behaviour asked of the whole change, asserted against real SQL: a demo
// that has been stopped drops out of the metering window instead of billing to
// the current instant forever.
func TestAStoppedDemoStopsBeingMetered(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	open := deployer(t, db, projectID)

	variationID := seedVariation(t, db, projectID, "pending")
	d := open(domain.HostingDeploymentKindDemo, &variationID, "acme-demo")
	if err := db.CompleteHostingDeployment(ctx, d.ID, "https://demo.example", "true"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// While it is up it bills to now, and that window grows with the clock.
	row := meterRowFor(t, db, d.ID)
	now := time.Now()
	if !row.BillableThrough(now).Equal(now) {
		t.Error("a running demo does not bill to now")
	}

	if err := db.TerminateHostingDeployment(ctx, d.ID, ""); err != nil {
		t.Fatalf("stop the demo: %v", err)
	}

	stopped, err := db.GetHostingDeployment(ctx, d.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stopped.Status != domain.HostingDeploymentStatusTerminated {
		t.Errorf("a stopped demo is not terminated: %q", stopped.Status)
	}
	if stopped.FinishedAt == nil {
		t.Fatal("a stopped demo has no finished_at, so it would meter forever")
	}

	row = meterRowFor(t, db, d.ID)
	later := time.Now().Add(72 * time.Hour)
	if row.BillableThrough(later).After(*stopped.FinishedAt) {
		t.Error("a stopped demo's billable window went on growing with the clock")
	}
}

// Terminating twice describes one ending, so the second call must not move the
// instant the first recorded. A demo stopped an hour ago that is stopped again
// today did not run for that hour in between.
func TestStoppingTwiceKeepsTheFirstEnding(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	open := deployer(t, db, projectID)

	variationID := seedVariation(t, db, projectID, "pending")
	d := open(domain.HostingDeploymentKindDemo, &variationID, "acme-demo")
	db.CompleteHostingDeployment(ctx, d.ID, "https://demo.example", "true")

	if err := db.TerminateHostingDeployment(ctx, d.ID, ""); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	first, _ := db.GetHostingDeployment(ctx, d.ID)

	if err := db.TerminateHostingDeployment(ctx, d.ID, ""); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	second, _ := db.GetHostingDeployment(ctx, d.ID)

	if !second.FinishedAt.Equal(*first.FinishedAt) {
		t.Errorf("stopping twice moved the ending: %v then %v", *first.FinishedAt, *second.FinishedAt)
	}
}

// A teardown that errored stopped nothing. The deployment is still up, still
// billing, and still Mendel's to take down, so recording the failure must not
// close the row -- which would silence the meter for an app that is running.
func TestAFailedTeardownLeavesTheDeploymentBillable(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	open := deployer(t, db, projectID)

	variationID := seedVariation(t, db, projectID, "pending")
	d := open(domain.HostingDeploymentKindDemo, &variationID, "acme-demo")
	db.CompleteHostingDeployment(ctx, d.ID, "https://demo.example", "true")

	if err := db.NoteHostingDeploymentError(ctx, d.ID, "Teardown failed: exit status 1"); err != nil {
		t.Fatalf("note the failure: %v", err)
	}

	got, _ := db.GetHostingDeployment(ctx, d.ID)
	if got.Status != domain.HostingDeploymentStatusRunning || got.FinishedAt != nil {
		t.Error("a failed teardown closed the deployment, so it stopped being billed while still up")
	}
	if got.ErrorMessage == nil {
		t.Error("a failed teardown left no trace of why")
	}
	if _, ok := meterRowsByID(t, db)[d.ID]; !ok {
		t.Error("a deployment whose teardown failed dropped out of the hosting meter")
	}
}

// A second deploy lands on the same app name, so the platform replaced what was
// there. Leaving both rows open would have the meter bill one app twice for
// every hour after the redeploy.
func TestARedeploySupersedesTheDeploymentItReplaced(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	open := deployer(t, db, projectID)

	first := open(domain.HostingDeploymentKindProd, nil, "acme-prod")
	db.CompleteHostingDeployment(ctx, first.ID, "https://acme.example", "true")

	second := open(domain.HostingDeploymentKindProd, nil, "acme-prod")
	db.CompleteHostingDeployment(ctx, second.ID, "https://acme.example", "true")
	if err := db.TerminateSupersededDeployments(ctx, second.ID); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	oldRow, _ := db.GetHostingDeployment(ctx, first.ID)
	if oldRow.Status != domain.HostingDeploymentStatusTerminated || oldRow.FinishedAt == nil {
		t.Error("the superseded deployment is still open, so the app is metered twice")
	}
	newRow, _ := db.GetHostingDeployment(ctx, second.ID)
	if newRow.Status != domain.HostingDeploymentStatusRunning || newRow.FinishedAt != nil {
		t.Error("superseding closed the deployment that replaced it")
	}

	metered := meterRowsByID(t, db)
	if _, ok := metered[first.ID]; ok {
		if metered[first.ID].FinishedAt == nil {
			t.Error("the superseded deployment still meters open-endedly")
		}
	}
}

// A deployment of a different app is not superseded by this one. The rule is
// "same app name", and a project's demo and its production deploy are different
// apps that must both go on being metered.
func TestSupersedingLeavesOtherAppsAlone(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	open := deployer(t, db, projectID)

	variationID := seedVariation(t, db, projectID, "pending")
	demo := open(domain.HostingDeploymentKindDemo, &variationID, "acme-demo")
	db.CompleteHostingDeployment(ctx, demo.ID, "https://demo.example", "true")

	prod := open(domain.HostingDeploymentKindProd, nil, "acme-prod")
	db.CompleteHostingDeployment(ctx, prod.ID, "https://acme.example", "true")
	if err := db.TerminateSupersededDeployments(ctx, prod.ID); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	stillUp, _ := db.GetHostingDeployment(ctx, demo.ID)
	if stillUp.Status != domain.HostingDeploymentStatusRunning {
		t.Error("deploying production took a running demo down with it")
	}
}

// The restart sweep must leave serving deployments alone. A demo runs in the
// user's cloud, not in this process, and Mendel restarting says nothing about
// whether it is up -- but a deploy still in flight had its goroutine killed and
// will never be advanced by anything.
func TestOnlyInFlightDeploysAreTreatedAsInterrupted(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	open := deployer(t, db, projectID)

	variationID := seedVariation(t, db, projectID, "pending")

	serving := open(domain.HostingDeploymentKindDemo, &variationID, "acme-serving")
	db.CompleteHostingDeployment(ctx, serving.ID, "https://demo.example", "true")

	provisioning := open(domain.HostingDeploymentKindProd, nil, "acme-provisioning")
	db.MarkHostingDeploymentProvisioning(ctx, provisioning.ID, "https://acme.example", "true")

	inFlight := open(domain.HostingDeploymentKindProd, nil, "acme-in-flight")

	interrupted, err := db.InterruptedDeployments(ctx)
	if err != nil {
		t.Fatalf("read interrupted: %v", err)
	}
	if len(interrupted) != 1 {
		t.Fatalf("expected exactly the in-flight deploy, got %d", len(interrupted))
	}
	if interrupted[0].ID != inFlight.ID {
		t.Errorf("the wrong deploy was treated as interrupted: %s", interrupted[0].AppName)
	}
}

// A failed deploy is over. It is excluded from metering anyway, but leaving
// finished_at null would make "never came up" indistinguishable from "still up"
// to anything reading the row afterwards.
func TestAFailedDeployIsClosed(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	open := deployer(t, db, projectID)

	d := open(domain.HostingDeploymentKindProd, nil, "acme-prod")
	if err := db.FailHostingDeploymentWithFix(ctx, d.ID, "no credentials", "add GCP_REGION"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	got, _ := db.GetHostingDeployment(ctx, d.ID)
	if got.Status != domain.HostingDeploymentStatusFailed || got.FinishedAt == nil {
		t.Error("a failed deploy was left open")
	}
	if got.SuggestedFix == nil || *got.SuggestedFix != "add GCP_REGION" {
		t.Error("the proposed fix was not kept, so nothing can offer a retry with it")
	}
	if _, ok := meterRowsByID(t, db)[d.ID]; ok {
		t.Error("a failed deploy is being metered; its wall-clock is deploy time, not runtime")
	}
}

// meterRowFor returns the metering view of one deployment, failing if the
// hosting meter cannot see it at all.
func meterRowFor(t *testing.T, db *DB, id uuid.UUID) DeploymentMeterRow {
	t.Helper()
	row, ok := meterRowsByID(t, db)[id]
	if !ok {
		t.Fatalf("deployment %s is not visible to the hosting meter", id)
	}
	return row
}

func meterRowsByID(t *testing.T, db *DB) map[uuid.UUID]DeploymentMeterRow {
	t.Helper()
	rows, err := db.GetHostingDeploymentsToMeter(context.Background())
	if err != nil {
		t.Fatalf("read metering rows: %v", err)
	}
	out := make(map[uuid.UUID]DeploymentMeterRow, len(rows))
	for _, r := range rows {
		out[r.DeploymentID] = r
	}
	return out
}

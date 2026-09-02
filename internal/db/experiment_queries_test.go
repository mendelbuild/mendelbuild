package db

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/experiment"
)

// seedHopAndVariations gives an experiment something to hang from.
func seedHopAndVariations(t *testing.T, db *DB, projectID uuid.UUID, n int) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	var strategyID uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`SELECT id FROM strategies WHERE project_id = $1`, projectID).Scan(&strategyID); err != nil {
		t.Fatalf("find strategy: %v", err)
	}

	hopID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO hops (id, strategy_id, name, commentary) VALUES ($1,$2,'hop','why')`,
		hopID, strategyID); err != nil {
		t.Fatalf("seed hop: %v", err)
	}

	var variations []uuid.UUID
	for i := 0; i < n; i++ {
		id := uuid.New()
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO variations (id, hop_id, name) VALUES ($1,$2,$3)`,
			id, hopID, "variation"); err != nil {
			t.Fatalf("seed variation: %v", err)
		}
		variations = append(variations, id)
	}
	return hopID, variations
}

func float64p(f float64) *float64 { return &f }
func intp(i int) *int             { return &i }

// The round trip that makes internal/experiment reachable: an experiment, its
// Arms, and a verdict written down where something other than a test can find
// it.
func TestExperimentRoundTrip(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	hopID, variations := seedHopAndVariations(t, db, projectID, 2)

	e := &domain.Experiment{
		ProjectID:               projectID,
		HopID:                   hopID,
		AssignmentUnit:          domain.AssignmentUnitUser,
		AssignmentKeySource:     domain.AssignmentKeyCookie,
		AssignmentKeyName:       "sid",
		MinimumDetectableEffect: float64p(0.02),
		StoppingRule:            domain.StoppingFixedHorizon,
		PlannedDurationHours:    intp(336),
	}
	if err := db.CreateExperiment(ctx, e); err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	arms := []domain.ExperimentArm{
		{ExperimentID: e.ID, Slug: domain.MainlineSlug, AllocationWeight: 50},
		{ExperimentID: e.ID, VariationID: &variations[0], Slug: "a", AllocationWeight: 25},
		{ExperimentID: e.ID, VariationID: &variations[1], Slug: "b", AllocationWeight: 25},
	}
	for i := range arms {
		if err := db.CreateExperimentArm(ctx, &arms[i]); err != nil {
			t.Fatalf("create arm %s: %v", arms[i].Slug, err)
		}
	}

	got, err := db.GetExperiment(ctx, e.ID)
	if err != nil || got == nil {
		t.Fatalf("get experiment: %v", err)
	}
	if len(got.Arms) != 3 {
		t.Fatalf("want three Arms, got %d", len(got.Arms))
	}
	// Mainline first, because it is the thing everything else is compared to.
	if !got.Arms[0].IsMainline() {
		t.Errorf("mainline should sort first, got %q", got.Arms[0].Slug)
	}
	if msg := domain.ValidateAllocation(got.Arms); msg != "" {
		t.Errorf("a valid allocation was rejected: %s", msg)
	}

	// The seam this whole migration exists for: a verdict from
	// internal/experiment, written where a later reader can find it.
	adm := &experiment.Admission{
		Migration: experiment.Migration{
			ArmID: "a",
			Up:    "ALTER TABLE orders ADD COLUMN mendel_exp_a_score INT;",
			Down:  "ALTER TABLE orders DROP COLUMN mendel_exp_a_score;",
		},
		Delta: experiment.Delta{Added: []experiment.Object{
			{Kind: experiment.ObjectField, Collection: "orders", Name: "mendel_exp_a_score"},
		}},
		Shapes: map[string]experiment.TableSchema{
			"orders": {"id": "uuid", "mendel_exp_a_score": "integer"},
		},
	}
	rec, err := db.RecordAdmission(ctx, got.Arms[1].ID, adm, domain.AdmissionAdmitted, "")
	if err != nil {
		t.Fatalf("record admission: %v", err)
	}
	if rec.AdmittedAt.IsZero() {
		t.Error("an admission with no time cannot be compared against schema drift later")
	}

	back, err := db.GetArmAdmissions(ctx, got.Arms[1].ID)
	if err != nil || len(back) != 1 {
		t.Fatalf("read admissions: %v (%d rows)", err, len(back))
	}

	// Stored as the package's own JSON, so the record does not drift as those
	// types gain fields.
	var delta experiment.Delta
	if err := json.Unmarshal(back[0].Delta, &delta); err != nil {
		t.Fatalf("the recorded delta does not survive the round trip: %v", err)
	}
	if len(delta.Added) != 1 || delta.Added[0].Name != "mendel_exp_a_score" {
		t.Errorf("delta came back as %+v", delta)
	}
	if !delta.PurelyAdditive() {
		t.Error("an additive delta came back looking non-additive")
	}
}

// The statistics have to be pre-registered, and "before it runs" is the whole
// point: an agent that picks its stopping rule after seeing the data has a
// badly inflated false-positive rate. So the database refuses, rather than
// trusting every caller to check.
func TestRunningRequiresPreRegisteredStatistics(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	hopID, _ := seedHopAndVariations(t, db, projectID, 1)

	bare := &domain.Experiment{
		ProjectID: projectID, HopID: hopID,
		AssignmentUnit:      domain.AssignmentUnitUser,
		AssignmentKeySource: domain.AssignmentKeyCookie,
		AssignmentKeyName:   "sid",
	}
	if err := db.CreateExperiment(ctx, bare); err != nil {
		t.Fatalf("a draft may be incomplete: %v", err)
	}
	if msg := bare.NotReadyToStart(); msg == "" {
		t.Error("an experiment with no MDE should say why it cannot start")
	}

	if err := db.SetExperimentStatus(ctx, bare.ID, domain.ExperimentRunning); err == nil {
		t.Error("an experiment with no stopping rule was allowed to run")
	}

	// Declared, and it starts.
	full := &domain.Experiment{
		ProjectID: projectID, HopID: hopID,
		AssignmentUnit:          domain.AssignmentUnitUser,
		AssignmentKeySource:     domain.AssignmentKeyCookie,
		AssignmentKeyName:       "sid",
		MinimumDetectableEffect: float64p(0.02),
		StoppingRule:            domain.StoppingSequential,
		PlannedDurationHours:    intp(168),
	}
	if err := db.CreateExperiment(ctx, full); err != nil {
		t.Fatalf("create: %v", err)
	}
	if msg := full.NotReadyToStart(); msg != "" {
		t.Errorf("a fully declared experiment was held back: %s", msg)
	}
	if err := db.SetExperimentStatus(ctx, full.ID, domain.ExperimentRunning); err != nil {
		t.Fatalf("a declared experiment should run: %v", err)
	}

	got, _ := db.GetExperiment(ctx, full.ID)
	if got.StartedAt == nil {
		t.Error("running without a start time leaves the duration unmeasurable")
	}
}

// A described dissonance must be acknowledged before traffic, and the phrase is
// stored verbatim because the record is keyed by the exact string confirmed.
func TestDissonanceMustBeAcknowledgedBeforeTraffic(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	hopID, _ := seedHopAndVariations(t, db, projectID, 1)

	userID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO users (id, email, name) VALUES ($1,'a@b.c','tester')`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	e := &domain.Experiment{
		ProjectID: projectID, HopID: hopID,
		AssignmentUnit:          domain.AssignmentUnitUser,
		AssignmentKeySource:     domain.AssignmentKeyCookie,
		AssignmentKeyName:       "sid",
		MinimumDetectableEffect: float64p(0.02),
		StoppingRule:            domain.StoppingFixedHorizon,
		PlannedDurationHours:    intp(168),
		DissonanceDescription:   "Saved drafts written by this Arm stop appearing.",
		DissonancePhrase:        domain.DefaultDissonancePhrase,
	}
	if err := db.CreateExperiment(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := db.SetExperimentStatus(ctx, e.ID, domain.ExperimentRunning); err == nil {
		t.Error("an unacknowledged dissonance was allowed to take traffic")
	}

	// Near enough is not enough: the row is keyed by the exact string.
	if err := db.AcknowledgeDissonance(ctx, e.ID, "i understand", userID); err == nil {
		t.Error("a phrase that does not match was accepted")
	}
	if err := db.AcknowledgeDissonance(ctx, e.ID, "  I understand  ", userID); err != nil {
		t.Errorf("surrounding whitespace is a copying artefact, not a mismatch: %v", err)
	}
	if err := db.SetExperimentStatus(ctx, e.ID, domain.ExperimentRunning); err != nil {
		t.Errorf("an acknowledged experiment should run: %v", err)
	}
}

// Two mainlines would make "the control" ambiguous in the one place the
// comparison depends on it not being.
func TestOnlyOneMainlineArm(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()
	hopID, _ := seedHopAndVariations(t, db, projectID, 1)

	e := &domain.Experiment{
		ProjectID: projectID, HopID: hopID,
		AssignmentUnit:      domain.AssignmentUnitUser,
		AssignmentKeySource: domain.AssignmentKeyCookie,
		AssignmentKeyName:   "sid",
	}
	if err := db.CreateExperiment(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}
	first := &domain.ExperimentArm{ExperimentID: e.ID, Slug: domain.MainlineSlug}
	if err := db.CreateExperimentArm(ctx, first); err != nil {
		t.Fatalf("first control: %v", err)
	}
	second := &domain.ExperimentArm{ExperimentID: e.ID, Slug: "also-mainline"}
	if err := db.CreateExperimentArm(ctx, second); err == nil {
		t.Error("a second mainline control was accepted")
	}
}

// Per-request assignment means one person meets both Arms, so the same row gets
// written by two Arms' logic. That this is inadmissible is a derivation from the
// Assignment Unit, not a rule sitting beside it.
func TestPerRequestAssignmentForbidsDurableWrites(t *testing.T) {
	perRequest := &domain.Experiment{AssignmentUnit: domain.AssignmentUnitRequest}
	if perRequest.PermitsDurableWrites() {
		t.Error("per-request assignment cannot admit per-Assignment-Unit durable writes")
	}
	for _, unit := range []domain.AssignmentUnit{
		domain.AssignmentUnitUser, domain.AssignmentUnitSession, domain.AssignmentUnitTenant,
	} {
		if !(&domain.Experiment{AssignmentUnit: unit}).PermitsDurableWrites() {
			t.Errorf("%s assignment is stable per participant and should permit writes", unit)
		}
	}
}

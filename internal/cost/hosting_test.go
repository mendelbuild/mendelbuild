package cost

import (
	"context"
	"testing"
	"time"

	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
)

// meterFake stands in for the database so the settlement loop can be driven
// through a deployment's whole life in a few microseconds. What it records is
// the ledger, which is the thing under test: hosting is metered in chunks, so
// what matters is not any single reading but that the readings stop.
type meterFake struct {
	rows    []db.DeploymentMeterRow
	entries []*domain.CostEntry
}

func (m *meterFake) GetModelRateCard(context.Context, string, time.Time) (*domain.ModelRateCard, error) {
	return nil, nil
}

func (m *meterFake) GetHostingRateCard(context.Context, string, string, time.Time) (*domain.HostingRateCard, error) {
	return &domain.HostingRateCard{USDPerHour: 1, BillsWhenIdle: true}, nil
}

func (m *meterFake) RecordCostEntry(_ context.Context, e *domain.CostEntry) error {
	m.entries = append(m.entries, e)
	// The real recorder stamps occurred_at as it writes, and the meter reads
	// the latest of those back as "billed through". Mirroring that here is what
	// makes a second settle of the same deployment behave as it does in
	// production rather than re-billing from the start.
	m.rows[0].MeteredThrough = &e.OccurredAt
	return nil
}

func (m *meterFake) GetHostingDeploymentsToMeter(context.Context) ([]db.DeploymentMeterRow, error) {
	return m.rows, nil
}

func (m *meterFake) totalUSD() float64 {
	var sum float64
	for _, e := range m.entries {
		sum += e.AmountUSD
	}
	return sum
}

// The behaviour the whole lifecycle change exists for: a demo that has been
// stopped stops costing.
//
// Before hosting deployments were ever closed out, every deployment that had
// succeeded stayed 'running' with no finished_at, so BillableThrough fell back
// to now on every reading and the bill grew for as long as Mendel stayed up.
// Stopping the demo did not change that, because stopping it wrote to a
// different table altogether.
func TestAStoppedDemoStopsAccruingHostingSpend(t *testing.T) {
	ctx := context.Background()
	started := time.Now().Add(-2 * time.Hour)

	m := &meterFake{rows: []db.DeploymentMeterRow{{
		DeploymentID: uuid.New(),
		ProjectID:    uuid.New(),
		PlatformSlug: "test-platform",
		StartedAt:    started,
		Status:       string(domain.HostingDeploymentStatusRunning),
	}}}

	// While it is up, each settle charges for the time since the last one.
	if n, err := SettleHostingSpend(ctx, m); err != nil || n != 1 {
		t.Fatalf("metering a running deployment: %v, recorded %d", err, n)
	}
	whileRunning := m.totalUSD()
	if whileRunning <= 0 {
		t.Fatal("a running deployment accrued nothing")
	}

	// It is stopped an hour after it started. Everything after that instant is
	// somebody else's hour, not this deployment's.
	stopped := started.Add(time.Hour)
	m.rows[0].Status = string(domain.HostingDeploymentStatusTerminated)
	m.rows[0].FinishedAt = &stopped

	for i := 0; i < 5; i++ {
		if _, err := SettleHostingSpend(ctx, m); err != nil {
			t.Fatalf("metering a stopped deployment: %v", err)
		}
	}

	if got := m.totalUSD(); got != whileRunning {
		t.Errorf("a stopped deployment kept accruing: %.6f then %.6f", whileRunning, got)
	}
}

// A deployment that is still up must go on being metered while it is up. The
// failure this guards against is over-correcting: a fix that stops billing
// stopped deployments by stopping billing everything would hide exactly the
// spend a project is most likely to lose track of.
func TestARunningDeploymentKeepsAccruing(t *testing.T) {
	ctx := context.Background()

	m := &meterFake{rows: []db.DeploymentMeterRow{{
		DeploymentID: uuid.New(),
		ProjectID:    uuid.New(),
		PlatformSlug: "test-platform",
		StartedAt:    time.Now().Add(-time.Hour),
		Status:       string(domain.HostingDeploymentStatusRunning),
	}}}

	if _, err := SettleHostingSpend(ctx, m); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	first := m.totalUSD()

	// Wind the clock back on what has been billed, standing in for time passing
	// between two readings of a deployment that is still up.
	billed := m.rows[0].MeteredThrough.Add(-30 * time.Minute)
	m.rows[0].MeteredThrough = &billed

	if _, err := SettleHostingSpend(ctx, m); err != nil {
		t.Fatalf("second settle: %v", err)
	}
	if m.totalUSD() <= first {
		t.Error("a deployment that is still up stopped accruing")
	}
}

// BillableThrough is where "stopped" turns into "stops costing", so it is
// asserted directly as well: an open-ended deployment bills to now, a closed
// one bills to when it closed and not a second further.
func TestBillableThroughStopsAtTheEnding(t *testing.T) {
	started := time.Now().Add(-3 * time.Hour)
	stopped := started.Add(time.Hour)
	now := time.Now()

	open := db.DeploymentMeterRow{StartedAt: started}
	if got := open.BillableThrough(now); !got.Equal(now) {
		t.Errorf("a deployment that has not stopped should bill to now, got %v", got)
	}

	closed := db.DeploymentMeterRow{StartedAt: started, FinishedAt: &stopped}
	if got := closed.BillableThrough(now); !got.Equal(stopped) {
		t.Errorf("a stopped deployment should bill to when it stopped, got %v", got)
	}
	if got := closed.BillableThrough(now.Add(100 * time.Hour)); !got.Equal(stopped) {
		t.Error("a stopped deployment's billable window grew with the clock")
	}
}

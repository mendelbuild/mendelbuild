package cost

import (
	"context"
	"time"

	"github.com/bhs/mendelbuild/internal/db"
)

// defaultMachineShape is what deployments are metered against until Mendel
// learns what a deploy script actually provisioned. It is recorded on every
// hosting ledger row, so the assumption travels with the figure.
const defaultMachineShape = "default"

// minBillableInterval avoids writing a ledger row for every few seconds of
// uptime. Hosting accrues continuously, so it is metered in chunks; a running
// deployment simply shows slightly less than it has truly accrued between
// readings.
const minBillableInterval = 5 * time.Minute

// HostingMeterDB is the persistence surface hosting settlement needs.
type HostingMeterDB interface {
	LedgerDB
	GetHostingDeploymentsToMeter(ctx context.Context) ([]db.DeploymentMeterRow, error)
}

// SettleHostingSpend tops up the ledger with hosting cost accrued since each
// deployment was last metered.
//
// Hosting is not a one-off charge like an API call: an app left running keeps
// costing money whether or not anyone looks at it, and that is exactly the
// spend a project is most likely to lose track of. Metering periodically means
// a running deployment's cost is visible while it is still running, rather than
// only appearing after teardown.
//
// This is an estimate from list prices and wall-clock, never a provider
// invoice. On platforms that bill per request, the rate card says so and the
// charge is zero.
func SettleHostingSpend(ctx context.Context, database HostingMeterDB) (int, error) {
	deployments, err := database.GetHostingDeploymentsToMeter(ctx)
	if err != nil {
		return 0, err
	}

	recorder := NewRecorder(database)
	now := time.Now()
	recorded := 0

	for _, d := range deployments {
		since := d.UnbilledSince()
		through := d.BillableThrough(now)

		billable := through.Sub(since)
		if billable < minBillableInterval {
			continue
		}

		entry, err := recorder.RecordDeployment(ctx, Attribution{
			ProjectID:   d.ProjectID,
			StrategyID:  d.StrategyID,
			HopID:       d.HopID,
			VariationID: d.VariationID,
		}, d.DeploymentID, d.PlatformSlug, defaultMachineShape, billable)
		if err != nil {
			return recorded, err
		}
		if entry != nil {
			recorded++
		}
	}

	return recorded, nil
}

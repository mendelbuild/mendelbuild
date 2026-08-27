package cost

import (
	"context"
	"time"

	"github.com/bhs/mendelbuild/internal/domain"
)

// Cache and batch multipliers are uniform across current Anthropic models:
// a cache read costs a tenth of an input token, a 5-minute cache write costs a
// 25% premium over one (a 1-hour write is 2x, which Mendel does not use), and
// the Batch API halves the whole request.
const (
	cacheReadMultiplier  = 0.1
	cacheWriteMultiplier = 1.25
	batchMultiplier      = 0.5
)

// anthropicPricingSource records where the seeded model rates came from, so a
// reviewer can check a cost figure against a citable list price rather than
// trusting a number compiled into the binary.
const anthropicPricingSource = "Anthropic list pricing, docs.anthropic.com/en/docs/about-claude/models (captured 2026-06-24)"

// DefaultModelRates returns the model rate cards to seed on startup.
//
// These go stale as Anthropic ships models and changes prices, which is exactly
// why they live in the database and are refreshable via `mendel rates refresh`
// rather than being consulted from Go at pricing time.
func DefaultModelRates() []domain.ModelRateCard {
	rate := func(model string, in, out float64) domain.ModelRateCard {
		return domain.ModelRateCard{
			Model:                model,
			InputUSDPerMTok:      in,
			OutputUSDPerMTok:     out,
			CacheReadMultiplier:  cacheReadMultiplier,
			CacheWriteMultiplier: cacheWriteMultiplier,
			BatchMultiplier:      batchMultiplier,
			Source:               anthropicPricingSource,
		}
	}

	return []domain.ModelRateCard{
		rate("claude-fable-5", 10.00, 50.00),
		rate("claude-opus-5", 5.00, 25.00),
		rate("claude-opus-4-8", 5.00, 25.00),
		rate("claude-opus-4-7", 5.00, 25.00),
		rate("claude-opus-4-6", 5.00, 25.00),
		rate("claude-sonnet-5", 2.00, 10.00),
		rate("claude-sonnet-4-6", 3.00, 15.00),
		rate("claude-haiku-4-5", 1.00, 5.00),
	}
}

// hostingPricingSource marks hosting rates as list-price approximations. Mendel
// prices deployments from machine shape x wall-clock; it never sees a provider
// invoice, so everything derived from these is an estimate and is labeled as
// one in the UI.
const hostingPricingSource = "platform list pricing, approximate; refresh with `mendel rates refresh`"

// DefaultHostingRates returns the hosting rate cards to seed on startup.
//
// Scale-to-zero platforms are seeded with bills_when_idle = false: Mendel can
// see how long a deployment existed but not how long it served traffic, and
// billing idle wall-clock on those would overstate spend by orders of
// magnitude. Those deployments are reported as tracked-but-unpriced rather
// than given a confident wrong number.
func DefaultHostingRates() []domain.HostingRateCard {
	always := func(platform, shape string, perHour float64) domain.HostingRateCard {
		return domain.HostingRateCard{
			PlatformSlug:  platform,
			MachineShape:  shape,
			USDPerHour:    perHour,
			BillsWhenIdle: true,
			Source:        hostingPricingSource,
		}
	}
	scaleToZero := func(platform, shape string) domain.HostingRateCard {
		return domain.HostingRateCard{
			PlatformSlug:  platform,
			MachineShape:  shape,
			USDPerHour:    0,
			BillsWhenIdle: false,
			Source:        hostingPricingSource + "; billed per request, not per hour",
		}
	}

	return []domain.HostingRateCard{
		// Mendel does not yet learn the machine shape a deploy script chose, so
		// every deployment is metered against its platform's "default" shape.
		// That assumption is recorded on each ledger row and shown in the UI,
		// rather than being buried in a total.
		always("fly-io", "default", 0.0027),
		always("gke", "default", 0.0670),

		// Fly.io machines run until stopped, so wall-clock is the right basis.
		always("fly-io", "shared-cpu-1x-256mb", 0.0027),
		always("fly-io", "shared-cpu-1x-512mb", 0.0043),
		always("fly-io", "shared-cpu-2x-1gb", 0.0113),
		always("fly-io", "performance-1x-2gb", 0.0431),

		// Cloud Run bills per request with scale-to-zero.
		scaleToZero("cloud-run", "default"),

		// GKE nodes are provisioned and billed continuously.
		always("gke", "e2-small", 0.0335),
		always("gke", "e2-medium", 0.0670),
		always("gke", "e2-standard-2", 0.1340),
	}
}

// DB is the persistence surface this package needs.
type DB interface {
	CountModelRateCards(ctx context.Context) (int, error)
	UpsertModelRateCard(ctx context.Context, c *domain.ModelRateCard) error
	CountHostingRateCards(ctx context.Context) (int, error)
	UpsertHostingRateCard(ctx context.Context, c *domain.HostingRateCard) error
}

// SeedIfEmpty seeds both rate card tables with defaults if they are empty.
// Returns the number of cards seeded (0 if both tables already had rows).
func SeedIfEmpty(ctx context.Context, db DB) (int, error) {
	seeded := 0

	modelCount, err := db.CountModelRateCards(ctx)
	if err != nil {
		return 0, err
	}
	if modelCount == 0 {
		n, err := seedModelRates(ctx, db)
		if err != nil {
			return seeded, err
		}
		seeded += n
	}

	hostingCount, err := db.CountHostingRateCards(ctx)
	if err != nil {
		return seeded, err
	}
	if hostingCount == 0 {
		n, err := seedHostingRates(ctx, db)
		if err != nil {
			return seeded, err
		}
		seeded += n
	}

	return seeded, nil
}

// RefreshAll writes the current defaults as newly effective rate cards.
//
// Existing cards are left in place rather than overwritten: a ledger entry
// points at the card that priced it, and rewriting history would make old
// figures unverifiable. New spend picks up the newest effective card.
func RefreshAll(ctx context.Context, db DB) (int, error) {
	n, err := seedModelRates(ctx, db)
	if err != nil {
		return n, err
	}
	m, err := seedHostingRates(ctx, db)
	return n + m, err
}

func seedModelRates(ctx context.Context, db DB) (int, error) {
	now := time.Now()
	cards := DefaultModelRates()
	for i := range cards {
		cards[i].EffectiveFrom = now
		if err := db.UpsertModelRateCard(ctx, &cards[i]); err != nil {
			return i, err
		}
	}
	return len(cards), nil
}

func seedHostingRates(ctx context.Context, db DB) (int, error) {
	now := time.Now()
	cards := DefaultHostingRates()
	for i := range cards {
		cards[i].EffectiveFrom = now
		if err := db.UpsertHostingRateCard(ctx, &cards[i]); err != nil {
			return i, err
		}
	}
	return len(cards), nil
}

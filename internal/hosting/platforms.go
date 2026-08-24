package hosting

import (
	"context"

	"github.com/bhs/mendelbuild/internal/domain"
)

// DefaultPlatforms returns the default hosting platforms to seed on startup.
// These can be refreshed via CLI to stay current with popular platforms.
func DefaultPlatforms() []domain.HostingPlatform {
	return []domain.HostingPlatform{
		{
			Slug:          "fly-io",
			Name:          "Fly.io",
			DeployerImage: "alpine:latest",
			Instructions: `Fly.io deployment:
- First install flyctl: curl -L https://fly.io/install.sh | sh && export PATH="$HOME/.fly/bin:$PATH"
- Use 'flyctl' CLI commands
- Required env var: FLY_API_TOKEN
- Deploy script should create app if needed, deploy using fly.toml or Dockerfile
- Teardown script should destroy the app`,
		},
		{
			Slug:          "cloud-run",
			Name:          "Google Cloud Run",
			DeployerImage: "google/cloud-sdk:slim",
			Instructions: `Google Cloud Run deployment:
- Use 'gcloud run deploy' command
- Required env vars: GCP_PROJECT_ID, GCP_SERVICE_ACCOUNT_KEY (JSON)
- Deploy script should authenticate with service account key, build and push to gcr.io, deploy to Cloud Run
- Teardown script should delete the Cloud Run service`,
		},
		{
			Slug:          "railway",
			Name:          "Railway",
			DeployerImage: "node:20-slim",
			Instructions: `Railway deployment:
- Use Railway CLI (install with: npm install -g @railway/cli)
- Required env var: RAILWAY_TOKEN
- Deploy script should link project and deploy
- Teardown script should remove the deployment`,
		},
		{
			Slug:          "vercel",
			Name:          "Vercel",
			DeployerImage: "node:20-slim",
			Instructions: `Vercel deployment:
- Use 'vercel' CLI (install with: npm install -g vercel)
- Required env vars: VERCEL_TOKEN, optionally VERCEL_ORG_ID
- Deploy script should deploy using vercel CLI
- Teardown script should remove the deployment`,
		},
		{
			Slug:          "render",
			Name:          "Render",
			DeployerImage: "alpine:latest",
			Instructions: `Render deployment:
- Install curl if needed: apk add --no-cache curl jq
- Use Render API with curl (render.com/docs/api)
- Required env var: RENDER_API_KEY
- Deploy script should create service via API and trigger deploy
- Teardown script should delete the service`,
		},
	}
}

// DB interface for hosting platform operations.
type DB interface {
	CountHostingPlatforms(ctx context.Context) (int, error)
	UpsertHostingPlatform(ctx context.Context, p *domain.HostingPlatform) error
	ListHostingPlatforms(ctx context.Context) ([]domain.HostingPlatform, error)
	GetHostingPlatformBySlug(ctx context.Context, slug string) (*domain.HostingPlatform, error)
}

// SeedIfEmpty seeds the hosting_platforms table with defaults if it's empty.
// Returns the number of platforms seeded (0 if table was not empty).
func SeedIfEmpty(ctx context.Context, db DB) (int, error) {
	count, err := db.CountHostingPlatforms(ctx)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		return 0, nil
	}

	defaults := DefaultPlatforms()
	for _, p := range defaults {
		if err := db.UpsertHostingPlatform(ctx, &p); err != nil {
			return 0, err
		}
	}
	return len(defaults), nil
}

// RefreshAll replaces all platforms with the defaults.
// Use this to reset to a clean state or update with new platform definitions.
func RefreshAll(ctx context.Context, db DB) (int, error) {
	defaults := DefaultPlatforms()
	for _, p := range defaults {
		if err := db.UpsertHostingPlatform(ctx, &p); err != nil {
			return 0, err
		}
	}
	return len(defaults), nil
}

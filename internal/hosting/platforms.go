package hosting

import (
	"context"
	"encoding/json"

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
		{
			Slug:          "gke",
			Name:          "Google Kubernetes Engine",
			DeployerImage: "google/cloud-sdk:slim",
			Instructions: `GKE Kubernetes deployment:
- Use 'gcloud container' and 'kubectl' commands
- Required env vars: GCP_PROJECT_ID, GCP_SERVICE_ACCOUNT_KEY (JSON), GKE_CLUSTER_NAME, GKE_ZONE
- Deploy script should authenticate, get cluster credentials, and apply k8s manifests
- Teardown script should delete the deployment/service resources
- Use 'kubectl apply -f' for manifests or 'helm install' for Helm charts`,
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

// ComboSpec defines a supported (artifact_kind, platform_slug) combination.
type ComboSpec struct {
	ArtifactKind        domain.DeployArtifactKind
	PlatformSlug        string
	Notes               string
	RequiredCredentials []string // Credential names that must exist for this combo
	Guidance            map[string]any
}

// DefaultCombos returns the initial sparse matrix of supported deployment combinations.
func DefaultCombos() []ComboSpec {
	return []ComboSpec{
		{
			ArtifactKind:        domain.DeployArtifactContainer,
			PlatformSlug:        "fly-io",
			Notes:               "Single container deployment to Fly.io",
			RequiredCredentials: []string{"FLY_API_TOKEN"},
			Guidance: map[string]any{
				"requires":    []string{"Dockerfile"},
				"healthCheck": "Use fly.toml [http_service.checks] for health checks",
				"tips":        []string{"fly launch auto-detects Dockerfile", "Use fly secrets for env vars"},
			},
		},
		{
			ArtifactKind:        domain.DeployArtifactContainer,
			PlatformSlug:        "cloud-run",
			Notes:               "Single container deployment to Google Cloud Run",
			RequiredCredentials: []string{"GCP_PROJECT_ID", "GCP_SERVICE_ACCOUNT_KEY"},
			Guidance: map[string]any{
				"requires":    []string{"Dockerfile", "GCP project with Cloud Run API enabled"},
				"healthCheck": "Cloud Run uses container PORT health by default",
				"tips":        []string{"Use Artifact Registry for images", "Set --allow-unauthenticated for public access"},
			},
		},
		{
			ArtifactKind:        domain.DeployArtifactKubernetes,
			PlatformSlug:        "gke",
			Notes:               "Kubernetes deployment to Google Kubernetes Engine",
			RequiredCredentials: []string{"GCP_PROJECT_ID", "GCP_SERVICE_ACCOUNT_KEY", "GKE_CLUSTER_NAME", "GKE_ZONE"},
			Guidance: map[string]any{
				"requires":    []string{"k8s manifests or Helm chart", "GKE cluster"},
				"healthCheck": "Use readiness/liveness probes in deployment spec",
				"tips":        []string{"Use Workload Identity for IAM", "Consider Autopilot for simpler ops"},
			},
		},
	}
}

// RequiredCredentialsForCombo returns the required credentials for a given combo.
func RequiredCredentialsForCombo(artifactKind domain.DeployArtifactKind, platformSlug string) []string {
	for _, spec := range DefaultCombos() {
		if spec.ArtifactKind == artifactKind && spec.PlatformSlug == platformSlug {
			return spec.RequiredCredentials
		}
	}
	return nil
}

// ComboDB interface for deployment combo operations.
type ComboDB interface {
	CountSupportedDeploymentCombos(ctx context.Context) (int, error)
	CreateSupportedDeploymentCombo(ctx context.Context, c *domain.SupportedDeploymentCombo) error
	GetHostingPlatformBySlug(ctx context.Context, slug string) (*domain.HostingPlatform, error)
}

// SeedCombosIfEmpty seeds the supported_deployment_combos table if empty.
func SeedCombosIfEmpty(ctx context.Context, db ComboDB) (int, error) {
	count, err := db.CountSupportedDeploymentCombos(ctx)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		return 0, nil
	}

	specs := DefaultCombos()
	seeded := 0
	for _, spec := range specs {
		platform, err := db.GetHostingPlatformBySlug(ctx, spec.PlatformSlug)
		if err != nil {
			continue // Skip if platform doesn't exist (e.g., GKE not seeded yet)
		}

		guidanceJSON, _ := json.Marshal(spec.Guidance)
		notes := spec.Notes

		combo := &domain.SupportedDeploymentCombo{
			ArtifactKind:      spec.ArtifactKind,
			HostingPlatformID: platform.ID,
			Notes:             &notes,
			Guidance:          guidanceJSON,
		}
		if err := db.CreateSupportedDeploymentCombo(ctx, combo); err != nil {
			return seeded, err
		}
		seeded++
	}
	return seeded, nil
}

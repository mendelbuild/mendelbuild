package hosting

import (
	"context"
	"encoding/json"

	"github.com/bhs/mendelbuild/internal/domain"
)

// ContainerPort is the port Mendel routes to inside a deployed container, and
// the value it puts in PORT so the app knows where to listen.
//
// The two must agree. Declaring a route to 8080 while the app defaults to 3000
// produces a container that starts cleanly and is unreachable, which the
// platform reports as a refused connection rather than as a misconfiguration.
//
// This is not a platform option in the sense the no-hardcoding rule covers: it
// is one number Mendel chooses and then announces, not a list of choices that
// drifts with the market.
const ContainerPort = 8080

// Namespace is where Mendel's deployments live in a user's Kubernetes cluster.
//
// The cluster belongs to the user and may run anything else; keeping Mendel's
// workloads in a namespace of their own means teardown deletes only what Mendel
// created, and gives the per-Arm NetworkPolicy work a boundary to attach to.
//
// Like ContainerPort, this is a value Mendel chooses and announces rather than a
// platform option that drifts.
const Namespace = "mendel-apps"

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
			Instructions: `Deploy to your own Google Kubernetes Engine cluster.

Mendel needs a service account key with enough access to build images and apply
workloads. Only you can mint that key, so run the following against your GCP
project; Mendel does the rest. Substitute your own project ID for PROJECT.

1. Enable the APIs Mendel uses:

   gcloud services enable container.googleapis.com cloudbuild.googleapis.com \
     artifactregistry.googleapis.com --project PROJECT

2. Create a service account for Mendel:

   gcloud iam service-accounts create mendel-deployer --project PROJECT \
     --display-name "Mendel Deployer"

3. Grant it the roles the deploy needs — cluster access, image build and push,
   the Cloud Build staging bucket, and permission to act as the build's own
   service account:

   for ROLE in container.developer cloudbuild.builds.editor \
     artifactregistry.writer storage.admin iam.serviceAccountUser; do
     gcloud projects add-iam-policy-binding PROJECT \
       --member serviceAccount:mendel-deployer@PROJECT.iam.gserviceaccount.com \
       --role roles/$ROLE --condition None
   done

4. Create the key and paste its contents into GCP_SERVICE_ACCOUNT_KEY:

   gcloud iam service-accounts keys create mendel-key.json \
     --iam-account mendel-deployer@PROJECT.iam.gserviceaccount.com

Then give Mendel these four values:
- GCP_PROJECT_ID          your project ID
- GCP_SERVICE_ACCOUNT_KEY the full contents of mendel-key.json
- GKE_CLUSTER_NAME        the cluster to deploy into
- GKE_ZONE                that cluster's location — a zone (us-central1-a) or a
                          region (us-central1) for a regional cluster

Mendel deploys into a namespace of its own, `+Namespace+`, creating it if
absent, and removes the Deployment, Service and Secret it created on teardown.`,
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

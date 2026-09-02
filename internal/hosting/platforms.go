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
			Instructions: `Deploy to your own Fly.io organization.

Before you start:
  1. Install flyctl — curl -L https://fly.io/install.sh | sh
     (then add $HOME/.fly/bin to your PATH)
  2. Sign in — flyctl auth login
  3. Know which organization Mendel should deploy into — flyctl orgs list

Mendel creates an app per demo and destroys it on teardown, so it needs a token
scoped to the organization rather than to one app. The script ends by printing
it under the name it goes in here:

  FLY_API_TOKEN

Each demo becomes its own Fly app, named after the project and variation, and
is destroyed when the demo stops.`,
			SetupScript: `# Replace <YOUR_FLY_ORG_HERE> with the slug from "flyctl orgs list" (often
# "personal"), angle brackets and all. Pasted unedited this line is a shell
# syntax error, which is deliberate: it stops here rather than running on.
export FLY_ORG=<YOUR_FLY_ORG_HERE>

# Nothing here destroys or replaces anything: creating a second token leaves the
# first one working, so re-running is safe and simply mints a fresh token.
flyctl orgs show "$FLY_ORG" > /dev/null

# Everything Mendel asks for, printed under the name Mendel asks for it under.
TOKEN=$(flyctl tokens create org --org "$FLY_ORG" --name "Mendel" --expiry 8760h)

echo
echo "================= paste these into Mendel ================="
echo
echo "FLY_API_TOKEN"
echo "  the whole line below, including the FlyV1 prefix"
echo "-----------------------------------------------------------"
printf '%s\n' "$TOKEN"
echo "-----------------------------------------------------------"`,
		},
		{
			Slug:          "cloud-run",
			Name:          "Google Cloud Run",
			DeployerImage: "google/cloud-sdk:slim",
			Instructions: `Deploy to your own Google Cloud Run project.

Before you start:
  1. Install the gcloud CLI — https://cloud.google.com/sdk/docs/install
  2. Sign in — gcloud auth login
  3. Have rights to manage IAM on the project you want to deploy into.

Mendel cannot mint a service account key on your behalf, so run the setup script
below. Replace the placeholder on its first line; everything after that pastes
as-is, and the whole thing is safe to run again if you lose the key or need to
repeat a step.

The script ends by printing both of these under the name it goes in here:

  GCP_PROJECT_ID
  GCP_SERVICE_ACCOUNT_KEY

Mendel deploys each demo as its own Cloud Run service in us-central1, built from
your Dockerfile, and deletes the service on teardown.`,
			SetupScript: `# Replace <YOUR_PROJECT_ID_HERE>, angle brackets and all. Pasted unedited
# this line is a shell syntax error, which is deliberate: it stops here rather
# than running the whole script against a project that does not exist.
export GCP_PROJECT=<YOUR_PROJECT_ID_HERE>

# Cloud Run itself, the build that produces the image, and the registry it is
# pushed to. Enabling an enabled API is a no-op.
gcloud services enable run.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com --project "$GCP_PROJECT"

# A service account for Mendel. Harmless if it already exists.
gcloud iam service-accounts create mendel-deployer --project "$GCP_PROJECT" --display-name "Mendel Deployer" || true

# Deploying services and making them public, building the image, pushing it, the
# Cloud Build staging bucket, and permission to act as the service's runtime
# identity. Adding a binding that is already there is a no-op.
for ROLE in run.admin cloudbuild.builds.editor artifactregistry.admin storage.admin iam.serviceAccountUser; do
  gcloud projects add-iam-policy-binding "$GCP_PROJECT" --member "serviceAccount:mendel-deployer@$GCP_PROJECT.iam.gserviceaccount.com" --role "roles/$ROLE" --condition None --quiet > /dev/null
done

# gcloud run deploy --source builds as the project's Compute Engine service
# account, so Mendel's account has to be allowed to act as it.
PROJECT_NUMBER=$(gcloud projects describe "$GCP_PROJECT" --format "value(projectNumber)")
gcloud iam service-accounts add-iam-policy-binding "$PROJECT_NUMBER-compute@developer.gserviceaccount.com" --project "$GCP_PROJECT" --member "serviceAccount:mendel-deployer@$GCP_PROJECT.iam.gserviceaccount.com" --role roles/iam.serviceAccountUser --quiet > /dev/null

# gcloud run deploy --source pushes the built image here. Creating the
# repository now means the first deploy does not have to, which also keeps it
# from racing the grants above: a token minted moments after a binding can still
# be refused, and the failure reads as a missing role rather than a stale token.
gcloud artifacts repositories create cloud-run-source-deploy --repository-format docker --location us-central1 --project "$GCP_PROJECT" --quiet 2> /dev/null || true

gcloud iam service-accounts keys create mendel-key.json --iam-account "mendel-deployer@$GCP_PROJECT.iam.gserviceaccount.com"

# Everything Mendel asks for, printed under the name Mendel asks for it under.
echo
echo "================= paste these into Mendel ================="
echo
echo "GCP_PROJECT_ID"
echo "  $GCP_PROJECT"
echo
echo "GCP_SERVICE_ACCOUNT_KEY"
echo "  everything between the two lines below, braces included"
echo "-----------------------------------------------------------"
cat mendel-key.json
echo "-----------------------------------------------------------"`,
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

Before you start:
  1. Install the gcloud CLI — https://cloud.google.com/sdk/docs/install
  2. Sign in — gcloud auth login
  3. Have a GKE cluster running, and rights to manage IAM on its project.

Mendel cannot mint a service account key on your behalf, so run the setup script
below. Replace the placeholder on its first line; everything after that pastes
as-is, and the whole thing is safe to run again if you lose the key or need to
repeat a step.

The script ends by printing each of these four values under the name it goes in
here, so there is nothing to work out from its output:

  GCP_PROJECT_ID
  GCP_SERVICE_ACCOUNT_KEY
  GKE_CLUSTER_NAME
  GKE_ZONE

If the project holds more than one cluster the script lists them and asks which,
since that is the one choice it cannot make for you.

Mendel deploys into a namespace of its own, ` + Namespace + `, creating it if
absent, and removes the Deployment, Service and Secret it created on teardown.`,
			SetupScript: `# Replace <YOUR_PROJECT_ID_HERE>, angle brackets and all. Pasted unedited
# this line is a shell syntax error, which is deliberate: it stops here rather
# than running the whole script against a project that does not exist.
export GCP_PROJECT=<YOUR_PROJECT_ID_HERE>

# The APIs Mendel uses: the cluster, the image build, and the image registry.
gcloud services enable container.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com --project "$GCP_PROJECT"

# A service account for Mendel. Harmless if it already exists.
gcloud iam service-accounts create mendel-deployer --project "$GCP_PROJECT" --display-name "Mendel Deployer" || true

# Cluster access, image build and push, the Cloud Build staging bucket, and
# permission to act as the build's own service account.
for ROLE in container.developer cloudbuild.builds.editor artifactregistry.writer storage.admin iam.serviceAccountUser; do
  gcloud projects add-iam-policy-binding "$GCP_PROJECT" --member "serviceAccount:mendel-deployer@$GCP_PROJECT.iam.gserviceaccount.com" --role "roles/$ROLE" --condition None --quiet > /dev/null
done

# The key itself.
gcloud iam service-accounts keys create mendel-key.json --iam-account "mendel-deployer@$GCP_PROJECT.iam.gserviceaccount.com"

# Everything Mendel asks for, printed under the name Mendel asks for it under.
# Reading a cluster table and a page of JSON and working out which part goes in
# which box is work the script can do instead.
CLUSTERS=$(gcloud container clusters list --project "$GCP_PROJECT" --format "value(name,location)")
CLUSTER_COUNT=$(printf '%s' "$CLUSTERS" | grep -c . || true)

echo
echo "================= paste these into Mendel ================="
echo
echo "GCP_PROJECT_ID"
echo "  $GCP_PROJECT"
echo
if [ "$CLUSTER_COUNT" -eq 1 ]; then
  echo "GKE_CLUSTER_NAME"
  printf '%s\n' "$CLUSTERS" | awk '{print "  " $1}'
  echo
  echo "GKE_ZONE"
  printf '%s\n' "$CLUSTERS" | awk '{print "  " $2}'
elif [ "$CLUSTER_COUNT" -eq 0 ]; then
  echo "GKE_CLUSTER_NAME / GKE_ZONE"
  echo "  No clusters in this project yet. Create one, then re-run this script."
else
  echo "GKE_CLUSTER_NAME / GKE_ZONE"
  echo "  $CLUSTER_COUNT clusters found. Pick the one to deploy into:"
  printf '%s\n' "$CLUSTERS" | awk '{print "    GKE_CLUSTER_NAME=" $1 "   GKE_ZONE=" $2}'
fi
echo
echo "GCP_SERVICE_ACCOUNT_KEY"
echo "  everything between the two lines below, braces included"
echo "-----------------------------------------------------------"
cat mendel-key.json
echo "-----------------------------------------------------------"`,
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
	UpsertSupportedDeploymentCombo(ctx context.Context, c *domain.SupportedDeploymentCombo) error
	GetHostingPlatformBySlug(ctx context.Context, slug string) (*domain.HostingPlatform, error)
}

// RefreshCombos writes every default combo, adding the ones an existing
// installation is missing and updating the rest.
//
// Platforms have had this since the beginning; combos only had a seed that runs
// on an empty table. So an installation seeded before a combo was added could
// never acquire it: the gke combo existed in this file and in no database, which
// left GKE unselectable in the UI with nothing to explain why.
func RefreshCombos(ctx context.Context, db ComboDB) (int, error) {
	refreshed := 0
	for _, spec := range DefaultCombos() {
		platform, err := db.GetHostingPlatformBySlug(ctx, spec.PlatformSlug)
		if err != nil {
			continue // Platform not present; refresh platforms first.
		}

		guidanceJSON, _ := json.Marshal(spec.Guidance)
		notes := spec.Notes

		combo := &domain.SupportedDeploymentCombo{
			ArtifactKind:      spec.ArtifactKind,
			HostingPlatformID: platform.ID,
			Notes:             &notes,
			Guidance:          guidanceJSON,
		}
		if err := db.UpsertSupportedDeploymentCombo(ctx, combo); err != nil {
			return refreshed, err
		}
		refreshed++
	}
	return refreshed, nil
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

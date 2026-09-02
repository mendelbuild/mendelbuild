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
			HostnameSource: domain.HostnameFromPlatform,
			Instructions: `Deploy to your own Fly.io organization.

Mendel creates an app per demo and destroys it on teardown, so it needs a token
scoped to the organization rather than to one app. Give the script your
organization slug and it is ready to paste as-is; it ends by printing the token
under the name it goes in here.

Each demo becomes its own Fly app, named after the project and variation, and is
destroyed when the demo stops.`,
			SetupPrerequisites: []string{
				"Install flyctl — curl -L https://fly.io/install.sh | sh, then add $HOME/.fly/bin to your PATH",
				"Sign in — flyctl auth login",
				"Know which organization to deploy into — flyctl orgs list",
			},
			SetupInputLabel: "Your Fly.io organization slug",
			SetupScript: `# The organization on the next line comes from the box above. Left unfilled it
# is a shell syntax error, deliberately: better to stop on line one than run on.
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
			HostnameSource: domain.HostnameFromPlatform,
			Instructions: `Deploy to your own Google Cloud Run project.

Mendel cannot mint a service account key on your behalf, so run the setup script
below. Give it your project ID and it is ready to paste as-is; the whole thing
is safe to run again if you lose the key or need to repeat a step.

It ends by printing every value below under the name it goes in here.

Mendel deploys each demo as its own Cloud Run service in the region you give it,
built from your Dockerfile, and deletes the service on teardown.`,
			SetupPrerequisites: []string{
				"Install the gcloud CLI — https://cloud.google.com/sdk/docs/install",
				"Sign in — gcloud auth login",
				"Have rights to manage IAM on the project you want to deploy into",
			},
			SetupInputLabel:      "Your GCP project ID",
			SetupInputCredential: "GCP_PROJECT_ID",
			SetupScript: `# The project ID on the next line comes from the box above. Left unfilled it is
# a shell syntax error, deliberately: better to stop on line one than run the
# whole script against a project that does not exist.
export GCP_PROJECT=<YOUR_PROJECT_ID_HERE>

# Where services go. Not Mendel's decision -- latency, data residency and cost
# all vary by region -- so take what gcloud is already configured with, and ask
# from the live list if there is nothing configured.
GCP_REGION=${GCP_REGION:-$(gcloud config get-value compute/region 2>/dev/null | grep -v '^(unset)$')}
if [ -z "$GCP_REGION" ]; then
  echo "No region configured. Pick one of these and re-run with it, for example:"
  gcloud compute regions list --project "$GCP_PROJECT" --format 'value(name)' | sed 's/^/  GCP_REGION=/'
  echo "Or set a default once with: gcloud config set compute/region <region>"
  return 2>/dev/null || exit 1
fi

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
gcloud artifacts repositories create cloud-run-source-deploy --repository-format docker --location "$GCP_REGION" --project "$GCP_PROJECT" --quiet 2> /dev/null || true

gcloud iam service-accounts keys create mendel-key.json --iam-account "mendel-deployer@$GCP_PROJECT.iam.gserviceaccount.com"

# Everything Mendel asks for, printed under the name Mendel asks for it under.
echo
echo "================= paste these into Mendel ================="
echo
echo "GCP_PROJECT_ID"
echo "  $GCP_PROJECT"
echo
echo "GCP_REGION"
echo "  $GCP_REGION"
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
			HostnameSource: domain.HostnameFromPlatform,
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
			HostnameSource: domain.HostnameFromPlatform,
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
			HostnameSource: domain.HostnameFromPlatform,
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
			HostnameSource: domain.HostnameFromUser,
			Instructions: `Deploy to your own Google Kubernetes Engine cluster.

Mendel cannot mint a service account key on your behalf, so run the setup script
below. Give it your project ID and it is ready to paste as-is; the whole thing
is safe to run again if you lose the key or need to repeat a step.

If the project has no cluster the script creates one, so there is nothing to set
up first; it says so before it starts, since a cluster takes a few minutes and
costs money while it runs. A project that already has exactly one is used as-is,
and one with several is listed for you to choose from.

It ends by printing every value below under the name it goes in here, so there
is nothing to work out from its output.

Mendel deploys into a namespace of its own, ` + Namespace + `, creating it if
absent, and removes what it created on teardown.

A cluster hands out addresses, not names, so a deployment here has no identity to
register anywhere: an OAuth redirect URI or a webhook target needs a host name
and https, and neither can be had for an IP. Supplying ` + domain.BaseDomainCredential + `
— a domain you control, with a wildcard record pointing at the cluster — gives
each demo a name of its own. It is optional: without it deployments still run and
are still reachable, and only the variations that must be registered by name are
affected.`,
			SetupPrerequisites: []string{
				"Install the gcloud CLI — https://cloud.google.com/sdk/docs/install",
				"Sign in — gcloud auth login",
				"Have rights to manage IAM and create clusters on the project — the script makes one if the project has none",
			},
			SetupInputLabel:      "Your GCP project ID",
			SetupInputCredential: "GCP_PROJECT_ID",
			SetupScript: `# The project ID on the next line comes from the box above. Left unfilled it is
# a shell syntax error, deliberately: better to stop on line one than run the
# whole script against a project that does not exist.
export GCP_PROJECT=<YOUR_PROJECT_ID_HERE>

# The APIs Mendel uses: the cluster, the image build, and the image registry.
gcloud services enable container.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com --project "$GCP_PROJECT"

# A service account for Mendel. Harmless if it already exists.
gcloud iam service-accounts create mendel-deployer --project "$GCP_PROJECT" --display-name "Mendel Deployer" || true

# Cluster access, image build and push, the Cloud Build staging bucket, permission
# to act as the build's own service account, reserving the static address the
# demos' DNS record points at, and requesting the certificate for their names.
# container.developer includes neither of those last two, and without them Mendel
# cannot tell you which records to create.
for ROLE in container.developer cloudbuild.builds.editor artifactregistry.writer storage.admin iam.serviceAccountUser compute.publicIpAdmin certificatemanager.editor; do
  gcloud projects add-iam-policy-binding "$GCP_PROJECT" --member "serviceAccount:mendel-deployer@$GCP_PROJECT.iam.gserviceaccount.com" --role "roles/$ROLE" --condition None --quiet > /dev/null
done

# The key itself.
gcloud iam service-accounts keys create mendel-key.json --iam-account "mendel-deployer@$GCP_PROJECT.iam.gserviceaccount.com"

# A cluster to deploy into. A project that already has one is left alone; only a
# project with none gets this, so re-running never makes a second cluster.
#
# Autopilot, because it needs no node pool sizing or upgrades to be decided by
# someone who came here to connect a deployment target, and it bills for what
# the demos actually request.
MENDEL_CLUSTER=${MENDEL_CLUSTER:-mendel-cluster}
# Where to put it is not Mendel's to decide: latency, data residency and cost all
# vary by region, and any default written into Mendel would be wrong for someone.
# So take the region gcloud is already configured with, and otherwise ask, having
# first read the live list out of GCP rather than reciting one.
MENDEL_REGION=${MENDEL_REGION:-$(gcloud config get-value compute/region 2>/dev/null | grep -v '^(unset)$')}
if [ -z "$(gcloud container clusters list --project "$GCP_PROJECT" --format 'value(name)')" ]; then
  if [ -z "$MENDEL_REGION" ]; then
    echo
    echo "No cluster in $GCP_PROJECT, and no region configured to put one in."
    echo "Pick one of these and re-run with it, for example:"
    echo
    gcloud compute regions list --project "$GCP_PROJECT" --format 'value(name)' | sed 's/^/  MENDEL_REGION=/'
    echo
    echo "Or set a default once with: gcloud config set compute/region <region>"
  else
    echo
    echo "No cluster in $GCP_PROJECT yet. Creating $MENDEL_CLUSTER in $MENDEL_REGION."
    echo "This takes a few minutes and the cluster costs money while it exists."
    echo "To use your own instead, stop here, create one, and re-run this script."
    echo
    # Guarded so a cluster created between the check and here is not an error.
    gcloud container clusters create-auto "$MENDEL_CLUSTER" --project "$GCP_PROJECT" --region "$MENDEL_REGION" || true
  fi
fi

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
  echo "  No cluster yet — see the message above for what to do."
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

	// OptionalCredentials unlock a capability without gating a deploy. A base
	// domain is the case this exists for: without one a Kubernetes deployment
	// still runs and is still reachable, and only the variations that must be
	// registered somewhere by name are affected.
	OptionalCredentials []string
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
			RequiredCredentials: []string{"GCP_PROJECT_ID", "GCP_REGION", "GCP_SERVICE_ACCOUNT_KEY"},
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

// OptionalCredentialsForCombo returns credentials that add a capability rather
// than gate a deployment.
func OptionalCredentialsForCombo(artifactKind domain.DeployArtifactKind, platformSlug string) []string {
	for _, spec := range DefaultCombos() {
		if spec.ArtifactKind == artifactKind && spec.PlatformSlug == platformSlug {
			return spec.OptionalCredentials
		}
	}
	return nil
}

// CredentialPurpose says what an optional credential buys, since nobody supplies
// one without knowing what it is for.
func CredentialPurpose(name string) string {
	if name == domain.BaseDomainCredential {
		return "A domain you control, with a wildcard record pointing at the cluster. " +
			"Deployments get a name under it instead of a bare address, which is what an " +
			"OAuth redirect URI or a webhook target needs. Without it they still run and " +
			"are still reachable."
	}
	return ""
}

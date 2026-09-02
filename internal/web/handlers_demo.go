package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bhs/mendelbuild/internal/codegen/executor"
	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/bhs/mendelbuild/internal/hosting"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// sanitizeAppName converts a project name to a DNS-safe app name.
// Fly.io app names must be lowercase, alphanumeric, and can contain hyphens.
func sanitizeAppName(name string) string {
	// Convert to lowercase
	name = strings.ToLower(name)
	// Replace spaces and underscores with hyphens
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")
	// Remove any characters that aren't alphanumeric or hyphens
	var result strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			result.WriteRune(c)
		}
	}
	// Trim hyphens from start and end
	return strings.Trim(result.String(), "-")
}

// demoAppName constructs a DNS-safe app name for a demo deployment.
func demoAppName(projectName string, variationID uuid.UUID) string {
	return fmt.Sprintf("%s-%s", projectName, variationID.String()[:8])
}

// prodAppName constructs a DNS-safe app name for a production deployment.
func prodAppName(projectName string) string {
	return fmt.Sprintf("%s-prod", projectName)
}

// teardownCommandFor returns the shell command that tears down a deployment of
// appName on the given platform. Stored with the deployment so teardown works
// even if Mendel restarts.
func teardownCommandFor(platformSlug, appName string, env map[string]string) string {
	switch platformSlug {
	case "fly-io":
		return fmt.Sprintf("flyctl apps destroy %s --yes", appName)
	case "cloud-run":
		// The region has to be captured here rather than assumed at teardown:
		// the service was created in the user's region, and a teardown aimed at
		// a different one silently deletes nothing.
		return fmt.Sprintf("gcloud run services delete %s --region %s --quiet", appName, env["GCP_REGION"])
	case "gke":
		// The Secret holding the app's required values is named after the
		// deployment and has to go with it; left behind, the user's cluster
		// accumulates the secrets of every variation ever demoed.
		// The HTTPRoute carries this demo's host rule; left behind, the hostname
		// goes on resolving to a backend that no longer exists. The Gateway is
		// shared and stays.
		return fmt.Sprintf("kubectl delete --namespace %s --ignore-not-found deployment,service,secret,httproute %s %s-env",
			hosting.Namespace, appName, appName)
	}
	return ""
}

// executeMigrationInstructions runs migration instructions via shell.
// Returns nil if instructions are empty or appear to be prose rather than commands.
// In the future, this could use Claude Code for more complex instructions.
func executeMigrationInstructions(ctx context.Context, workDir, instructions string) error {
	if instructions == "" {
		return nil
	}

	// Skip if instructions look like prose rather than commands
	// (e.g., starts with "Run", "Execute", "Apply", etc.)
	trimmed := strings.TrimSpace(instructions)
	proseIndicators := []string{"Run ", "Execute ", "Apply ", "To ", "First ", "Then ", "Note:", "WARNING"}
	for _, indicator := range proseIndicators {
		if strings.HasPrefix(trimmed, indicator) {
			fmt.Printf("[demo] Skipping migration: instructions appear to be prose, not commands\n")
			return nil
		}
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", instructions)
	cmd.Dir = workDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(output))
	}
	return nil
}

// handleStartDemo starts a demo instance for a variation.
// Uses cloud hosting via .mendel/demo-hosting.yml configuration.
func (s *Server) handleStartDemo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	// Check if project has a deployment channel that requires validation
	projectUUID, err := uuid.Parse(projectID)
	if err == nil {
		channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectUUID)
		if err == nil && channel != nil {
			// Channel exists - require demo validation
			if !channel.IsDemoValidated() {
				http.Error(w, "Demo deployment channel not validated. Go to Deployment settings to validate.", http.StatusBadRequest)
				return
			}

			// The variation's own requirements gate the deploy. Starting a
			// demo that cannot possibly work burns a deploy and produces a
			// failure whose cause is a missing value, not the code.
			deployURL := ""
			if channel.HostingPlatform != nil {
				project, err := s.db.GetProject(ctx, projectUUID)
				if err == nil {
					appName := demoAppName(sanitizeAppName(project.Name), variationID)
					deployURL = predictedDeployURL(channel.HostingPlatform.Slug, appName)
				}
			}
			statuses, err := s.variationRequirementStatus(ctx, projectUUID, variationID, deployURL)
			if err != nil {
				http.Error(w, "failed to check requirements: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if blocking := domain.BlockingRequirements(statuses); len(blocking) > 0 {
				http.Error(w, "This variation cannot run yet: "+domain.UnmetSummary(statuses)+
					". Provide them on the variation page.", http.StatusBadRequest)
				return
			}
		}
	}

	_, err = s.db.GetVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "variation not found", http.StatusNotFound)
		return
	}

	// Check if there's already a running or starting demo
	existingDemo, err := s.db.GetRunningDemoByVariation(ctx, variationID)
	if err == nil && existingDemo != nil {
		http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
		return
	}

	// Get the work directory for this variation
	workDir := git.WorkDirForVariation(projectID, variationID.String())

	// Create demo instance with "starting" status immediately
	// All validation happens in the background so errors get proper logging
	demoInstanceID := uuid.New()

	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir": workDir,
	})

	demoInstance := &domain.DemoInstance{
		ID:                   demoInstanceID,
		VariationID:          variationID,
		URL:                  "",
		TeardownInstructions: "", // Set by deployment after we know the resource names
		Status:               domain.DemoInstanceStatusStarting,
		ProcessInfo:          processInfo,
	}

	if err := s.db.CreateDemoInstance(ctx, demoInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Start the demo in a background goroutine - validation happens there
	go s.runDemoStartup(projectID, variationID, demoInstanceID, workDir)

	// Redirect immediately - user will see "starting" status
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// runDemoStartup runs the Docker-based demo startup process in the background.
// All validation happens here so errors get proper logging and suggested fixes.
func (s *Server) runDemoStartup(projectID string, variationID, demoInstanceID uuid.UUID, workDir string) {
	ctx := context.Background()

	// Helper to log with source tracking
	logInfo := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelInfo, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	logMilestone := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelMilestone, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	logError := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelError, msg, domain.SourceTypeDemo, &demoInstanceID)
	}

	// Helper to handle failures with suggested fix
	failDemoWithFix := func(errMsg, suggestedFix string) {
		logError(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, suggestedFix)
	}

	logMilestone("Checking demo configuration...")

	// Check if work directory exists - if not, try to recover from remote
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		// Check if the variation has a commit ref (was successfully pushed)
		variation, err := s.db.GetVariation(ctx, variationID)
		if err != nil || variation.CommitRef == nil || *variation.CommitRef == "" {
			failDemoWithFix(
				"Variation work directory not found - code may not be generated yet",
				"The variation's code hasn't been generated. Wait for code generation to complete, or check if there was an error during generation.",
			)
			return
		}

		// Code exists on remote - try to re-clone it
		logMilestone("Re-cloning variation branch from remote...")

		hop, err := s.db.GetHop(ctx, variation.HopID)
		if err != nil {
			failDemoWithFix(
				"Failed to look up hop for variation",
				"Internal error - try refreshing the page.",
			)
			return
		}

		projID, err := uuid.Parse(projectID)
		if err != nil {
			failDemoWithFix("Invalid project ID", "Internal error.")
			return
		}

		repo, err := s.db.GetRepositoryByProject(ctx, projID)
		if err != nil || repo.URL == nil {
			failDemoWithFix(
				"Repository URL not found",
				"Configure the repository URL in project settings.",
			)
			return
		}

		// Parse repo config for auth token
		var repoConfig struct {
			AuthToken string `json:"auth_token"`
		}
		if repo.Config != nil {
			json.Unmarshal(repo.Config, &repoConfig)
		}

		branchName := fmt.Sprintf("mendel/%s/%s", sanitizeBranchName(hop.Name), sanitizeBranchName(variation.Name))
		logInfo(fmt.Sprintf("Cloning branch %s...", branchName))

		gitClient := git.NewClient(workDir)
		if err := gitClient.Clone(ctx, *repo.URL, branchName, repoConfig.AuthToken); err != nil {
			failDemoWithFix(
				fmt.Sprintf("Failed to clone branch: %v", err),
				"The branch may have been deleted from the remote repository. You may need to regenerate this variation.",
			)
			return
		}
		logMilestone("Branch cloned successfully")
	}

	// Get deployment channel
	projID, _ := uuid.Parse(projectID)
	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projID)
	if err != nil || channel == nil {
		failDemoWithFix(
			"No deployment channel configured.",
			"Go to Deployment settings to configure how this project deploys.",
		)
		return
	}

	if !channel.IsDemoValidated() {
		failDemoWithFix(
			"Deployment channel not validated for demos.",
			"Go to Deployment settings and validate the demo deployment path.",
		)
		return
	}

	// Deploy using the channel
	s.runChannelDemoDeployment(ctx, projID, variationID, demoInstanceID, workDir, channel, logMilestone, logInfo, logError)
}

// runChannelDemoDeployment deploys a variation using the deployment channel.
// This replaces the old demo-hosting.yml script-based approach.
func (s *Server) runChannelDemoDeployment(
	ctx context.Context,
	projectID uuid.UUID,
	variationID uuid.UUID,
	demoInstanceID uuid.UUID,
	workDir string,
	channel *domain.ProjectDeploymentChannel,
	logMilestone func(string),
	logInfo func(string),
	logError func(string),
) {
	logMilestone("Deploying via " + channel.HostingPlatform.Name + "...")

	// Helper to fail the demo
	failDemo := func(errMsg string) {
		logError(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, "")
	}

	// Get project name for app naming
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		failDemo("Failed to get project: " + err.Error())
		return
	}
	projectName := sanitizeAppName(project.Name)

	// Get encryption key
	key, err := crypto.GetKey()
	if err != nil {
		failDemo("Encryption not configured: " + err.Error())
		return
	}

	// Get required credentials
	required := hosting.RequiredCredentialsForCombo(channel.ArtifactKind, channel.HostingPlatform.Slug)
	env := make(map[string]string)
	for _, name := range required {
		cred, err := s.db.GetProjectCredential(ctx, projectID, name)
		if err != nil {
			failDemo(fmt.Sprintf("Missing credential %s: %v", name, err))
			return
		}
		decrypted, err := crypto.Decrypt(cred.EncryptedValue, key)
		if err != nil {
			failDemo(fmt.Sprintf("Failed to decrypt %s: %v", name, err))
			return
		}
		env[name] = string(decrypted)
	}

	// Dispatch to platform-specific deployment
	appName := demoAppName(projectName, variationID)

	// The variation's requirements, checked here rather than only in
	// handleStartDemo so that restart and retry-with-fix are gated by the same
	// rule as a first start.
	statuses, err := s.variationRequirementStatus(ctx, projectID, variationID,
		predictedDeployURL(channel.HostingPlatform.Slug, appName))
	if err != nil {
		failDemo("Failed to read requirements: " + err.Error())
		return
	}
	if blocking := domain.BlockingRequirements(statuses); len(blocking) > 0 {
		failDemo("This variation cannot run yet: " + domain.UnmetSummary(statuses) +
			". Provide them on the variation page.")
		return
	}
	appSecrets, err := s.appSecretsFor(ctx, projectID, statuses)
	if err != nil {
		failDemo("Failed to read requirement values: " + err.Error())
		return
	}
	if len(appSecrets) > 0 {
		logInfo(fmt.Sprintf("Injecting %d required value(s) into the deployment", len(appSecrets)))
	}

	// Optional credentials add a capability rather than gate the deploy, so a
	// missing one is not an error -- but it has to be loaded, or the capability
	// it unlocks can never switch on.
	s.addOptionalCredentials(ctx, projectID, channel, key, env)

	var url string
	var deployErr error

	switch channel.HostingPlatform.Slug {
	case "fly-io":
		url, deployErr = s.deployToFlyIO(ctx, appName, workDir, env, appSecrets, logMilestone, logInfo)
	case "cloud-run":
		url, deployErr = s.deployToCloudRun(ctx, appName, workDir, env, appSecrets, logMilestone, logInfo)
	case "gke":
		url, deployErr = s.deployToGKE(ctx, projectID, false, appName, workDir, env, appSecrets, logMilestone, logInfo)
	default:
		failDemo("Unsupported platform: " + channel.HostingPlatform.Slug)
		return
	}

	// Still provisioning is not a failure. A demo whose load balancer has not
	// finished coming up is deployed correctly and will answer shortly, and
	// tearing it down as failed would destroy work that is fine.
	if deployErr != nil && !errors.Is(deployErr, errStillProvisioning) {
		failDemo(fmt.Sprintf("Deployment failed: %v", deployErr))
		return
	}

	if errors.Is(deployErr, errStillProvisioning) {
		logMilestone("Demo deployed at " + url + ", which is not serving yet while its " +
			"load balancer comes up.")
	} else {
		logMilestone("Demo deployed: " + url)
	}

	teardownCmd := teardownCommandFor(channel.HostingPlatform.Slug, appName, env)

	_, err = s.db.Pool.Exec(ctx, `
		UPDATE demo_instances
		SET url = $2, teardown_instructions = $3, status = $4
		WHERE id = $1
	`, demoInstanceID, url, teardownCmd, domain.DemoInstanceStatusRunning)
	if err != nil {
		logError("Failed to update demo status: " + err.Error())
	}
}

// flyTomlFor builds the fly.toml Mendel writes over whatever the repository
// had, so the app name is one Mendel controls.
//
// PORT is set alongside internal_port, and both come from the same constant.
// Declaring the port Fly routes to says nothing to the app about where to
// listen: an app that defaults to 3000 goes on listening on 3000, and Fly
// reports "instance refused connection" for a container that started perfectly
// well. Announcing the port through PORT is the convention every platform uses
// — Cloud Run injects exactly this variable — so it asks nothing of the user's
// repository that it was not already going to do.
func flyTomlFor(appName string) string {
	return fmt.Sprintf(`app = "%s"
primary_region = "iad"

[build]

[env]
  PORT = "%d"

[http_service]
  internal_port = %d
  force_https = true
  auto_stop_machines = "stop"
  auto_start_machines = true
  min_machines_running = 0
`, appName, hosting.ContainerPort, hosting.ContainerPort)
}

// deployToFlyIO deploys a working directory to Fly.io under the given app name.
func (s *Server) deployToFlyIO(
	ctx context.Context,
	appName string,
	workDir string,
	env map[string]string,
	appSecrets map[string]string,
	logMilestone func(string),
	logInfo func(string),
) (string, error) {
	// Check for Dockerfile
	dockerfilePath := filepath.Join(workDir, "Dockerfile")
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		return "", fmt.Errorf("no Dockerfile found in repository")
	}

	// Always write our fly.toml, so the app name is one Mendel controls.
	flyTomlPath := filepath.Join(workDir, "fly.toml")
	if err := os.WriteFile(flyTomlPath, []byte(flyTomlFor(appName)), 0644); err != nil {
		return "", fmt.Errorf("failed to write fly.toml: %w", err)
	}

	// Build environment
	cmdEnv := os.Environ()
	cmdEnv = append(cmdEnv, fmt.Sprintf("FLY_API_TOKEN=%s", env["FLY_API_TOKEN"]))

	// Check if app exists, create if not
	logInfo("Checking Fly.io app...")
	checkCmd := exec.CommandContext(ctx, "flyctl", "apps", "list", "--json")
	checkCmd.Env = cmdEnv
	checkOutput, _ := checkCmd.Output()

	appExists := strings.Contains(string(checkOutput), appName)

	if !appExists {
		logMilestone("Creating Fly.io app...")
		createCmd := exec.CommandContext(ctx, "flyctl", "apps", "create", appName)
		createCmd.Dir = workDir
		createCmd.Env = cmdEnv
		if output, err := createCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to create app: %s: %w", string(output), err)
		}
	}

	// Set the app's required values as Fly secrets before deploying, so the
	// first machine to start already has them. --stage holds them for the
	// deploy that follows rather than triggering one of its own.
	if len(appSecrets) > 0 {
		logInfo("Setting required values as Fly.io secrets...")
		args := []string{"secrets", "set", "--stage", "--app", appName}
		for name, value := range appSecrets {
			args = append(args, fmt.Sprintf("%s=%s", name, value))
		}
		secretsCmd := exec.CommandContext(ctx, "flyctl", args...)
		secretsCmd.Dir = workDir
		secretsCmd.Env = cmdEnv
		// Output may echo the names that were set; the values are not printed.
		if output, err := secretsCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to set secrets: %s: %w", string(output), err)
		}
	}

	// Deploy (--remote-only builds on Fly's infrastructure, avoiding local Docker)
	logMilestone("Deploying to Fly.io...")
	deployCmd := exec.CommandContext(ctx, "flyctl", "deploy", "--remote-only", "--wait-timeout", "300")
	deployCmd.Dir = workDir
	deployCmd.Env = cmdEnv
	output, err := deployCmd.CombinedOutput()
	logInfo(fmt.Sprintf("flyctl deploy output: %s", string(output)))
	if err != nil {
		return "", fmt.Errorf("deploy failed: %s: %w", string(output), err)
	}

	return flyDeployedURL(string(output), appName), nil
}

// flyVisitMarker precedes the deployed app's URL in flyctl's output.
var flyVisitMarker = regexp.MustCompile(`Visit your newly deployed app at\s+(https://\S+)`)

// flyAppHostPattern matches an app hostname on fly.dev, which is what a
// deployed app is reachable at. fly.io URLs (dashboard, monitoring, docs) are
// deliberately excluded.
var flyAppHostPattern = regexp.MustCompile(`https://([a-z0-9][a-z0-9-]*)\.fly\.dev\b/?`)

// flyDeployedURL extracts the deployed app's URL from flyctl deploy output.
//
// Taking the first https:// in the output does not work: flyctl prints a
// dashboard link (https://fly.io/apps/<name>/monitoring) before the app URL,
// and the build log carries whatever URLs the project's own toolchain emits,
// so that approach has recorded npm release notes as a demo URL.
func flyDeployedURL(output, appName string) string {
	// flyctl states the URL outright; prefer that over inference.
	if m := flyVisitMarker.FindStringSubmatch(output); m != nil {
		return strings.TrimSuffix(m[1], "/")
	}

	// Otherwise take a *.fly.dev host, preferring one that names this app.
	matches := flyAppHostPattern.FindAllStringSubmatch(output, -1)
	for _, m := range matches {
		if m[1] == appName {
			return strings.TrimSuffix(m[0], "/")
		}
	}
	if len(matches) > 0 {
		return strings.TrimSuffix(matches[0][0], "/")
	}

	// Nothing usable in the output; the app name determines the hostname.
	return fmt.Sprintf("https://%s.fly.dev", appName)
}

// deployToCloudRun deploys a working directory to Google Cloud Run under the given service name.
func (s *Server) deployToCloudRun(
	ctx context.Context,
	serviceName string,
	workDir string,
	env map[string]string,
	appSecrets map[string]string,
	logMilestone func(string),
	logInfo func(string),
) (string, error) {
	projectID := env["GCP_PROJECT_ID"]
	// The region is the user's, not Mendel's: a default written in here deploys
	// everyone's services to one place regardless of where their data or their
	// users are. The setup script reports it alongside the project.
	region := env["GCP_REGION"]
	if region == "" {
		return "", fmt.Errorf("GCP_REGION is not set; re-run the setup script, which reports it")
	}

	// Write service account key to temp file
	keyFile, err := os.CreateTemp("", "gcp-key-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create key file: %w", err)
	}
	defer os.Remove(keyFile.Name())
	if _, err := keyFile.WriteString(env["GCP_SERVICE_ACCOUNT_KEY"]); err != nil {
		keyFile.Close()
		return "", fmt.Errorf("failed to write key file: %w", err)
	}
	keyFile.Close()

	// Build environment
	cmdEnv := os.Environ()
	cmdEnv = append(cmdEnv, fmt.Sprintf("GOOGLE_APPLICATION_CREDENTIALS=%s", keyFile.Name()))

	// Authenticate
	logInfo("Authenticating with GCP...")
	authCmd := exec.CommandContext(ctx, "gcloud", "auth", "activate-service-account", "--key-file", keyFile.Name())
	authCmd.Env = cmdEnv
	if output, err := authCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("auth failed: %s: %w", string(output), err)
	}

	// Set project
	setProjectCmd := exec.CommandContext(ctx, "gcloud", "config", "set", "project", projectID)
	setProjectCmd.Env = cmdEnv
	setProjectCmd.CombinedOutput()

	// Build and deploy with Cloud Build
	logMilestone("Building and deploying to Cloud Run...")
	deployArgs := []string{"run", "deploy", serviceName,
		"--source", ".",
		"--region", region,
		"--allow-unauthenticated",
		"--quiet"}
	if len(appSecrets) > 0 {
		logInfo(fmt.Sprintf("Setting %d required value(s) on the service...", len(appSecrets)))
		// ^ names and values are joined with commas, so a value containing one
		// would be read as another assignment. The delimiter override tells
		// gcloud to split on a character no env var value realistically holds.
		pairs := "^|^"
		for name, value := range appSecrets {
			pairs += fmt.Sprintf("%s=%s|", name, value)
		}
		deployArgs = append(deployArgs, "--set-env-vars", strings.TrimSuffix(pairs, "|"))
	}
	deployCmd := exec.CommandContext(ctx, "gcloud", deployArgs...)
	deployCmd.Dir = workDir
	deployCmd.Env = cmdEnv
	output, err := deployCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("deploy failed: %s: %w", string(output), err)
	}

	// Get service URL
	urlCmd := exec.CommandContext(ctx, "gcloud", "run", "services", "describe", serviceName,
		"--region", region, "--format", "value(status.url)")
	urlCmd.Env = cmdEnv
	urlOutput, err := urlCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get URL: %w", err)
	}

	return strings.TrimSpace(string(urlOutput)), nil
}

// k8sManifestFor builds the Deployment and Service applied to GKE, and an
// Ingress when the deployment has a hostname to answer to.
//
// The container port, the Service's target, and PORT all come from the same
// constant: routing to a port the app was never told to listen on produces a
// pod that runs and a LoadBalancer that never answers.
//
// With a hostname the Service is a ClusterIP behind a shared Ingress that routes
// on Host, rather than a LoadBalancer of its own. That is what a wildcard DNS
// record requires -- every demo has to answer at one address for a single record
// to cover them all -- and it is cheaper besides, since a LoadBalancer bills per
// hour and there was previously one per demo. Without a hostname the old shape
// stands, so a project that has given Mendel no domain keeps working.
func k8sManifestFor(deploymentName, imageName, envFrom, hostname, staticIPName string) string {
	serviceType := "LoadBalancer"
	if hostname != "" {
		serviceType = "ClusterIP"
	}

	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
spec:
  replicas: 1
  # Keep the running pod until the new one can serve. With the default strategy
  # and a single replica, the old pod can go before the new one is answering, so
  # every redeploy has a hole in it -- short, but a hole.
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: app
        image: %s
        ports:
        - containerPort: %d
        env:
        - name: PORT
          value: "%d"%s
        # A TCP check rather than an HTTP path: Mendel does not know what the
        # user's app serves, or on what route, and guessing /health would fail
        # every app that does not have one. Accepting a connection is the most
        # that can be assumed of an arbitrary program.
        #
        # This also gates the load balancer. GKE holds a container-native NEG
        # pod unready until it is programmed, so with maxUnavailable: 0 the old
        # pod survives until the new one is genuinely receiving traffic.
        readinessProbe:
          tcpSocket:
            port: %d
          initialDelaySeconds: 2
          periodSeconds: 3
          failureThreshold: 20
---
apiVersion: v1
kind: Service
metadata:
  name: %s
spec:
  type: %s
  selector:
    app: %s
  ports:
  - port: 80
    targetPort: %d
`, deploymentName, deploymentName, deploymentName, imageName,
		hosting.ContainerPort, hosting.ContainerPort, envFrom, hosting.ContainerPort,
		deploymentName, serviceType, deploymentName, hosting.ContainerPort)

	if hostname == "" {
		return manifest
	}

	// kubectl warns that kubernetes.io/ingress.class is deprecated in favour of
	// spec.ingressClassName. Following that advice stops the Ingress working:
	// GKE ships no IngressClass resource, so ingressClassName: gce names a class
	// nothing provides, and the Ingress is never reconciled -- no address, and no
	// events at all, which leaves nothing to read that would explain it. Verified
	// both ways against a real cluster.
	//
	// One Ingress per deployment, all sharing a static address. Kubernetes
	// merges rules across Ingresses on the same class, so each demo brings and
	// removes its own rule and teardown needs no edit of a shared object --
	// which would otherwise be a read-modify-write race between demos starting
	// and stopping at once.
	// An HTTPRoute attached to the namespace's one Gateway, rather than an
	// Ingress of its own.
	//
	// An Ingress cannot carry the wildcard certificate these names need: applied
	// with networking.gke.io/certmap it provisions port 80 and no HTTPS listener,
	// with no error to read. Gateway API is the supported path for a Certificate
	// Manager certificate, and it is where the per-Arm routing in
	// 13_live_traffic_experiments.md is headed regardless.
	manifest += fmt.Sprintf(`---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: %s
spec:
  parentRefs:
  - name: %s
  hostnames:
  - %s
  rules:
  - backendRefs:
    - name: %s
      port: 80
`, deploymentName, gatewayName, hostname, deploymentName)
	return manifest
}

// gatewayName is the one Gateway every deployment in the namespace routes
// through. One address and one certificate serve all of them, which is what a
// single wildcard record and a single reserved address require.
const gatewayName = "mendel"

// gatewayManifest is that Gateway: the address the DNS records point at, and the
// certificate covering the names under them.
//
// The certificate is attached by annotation rather than by certificateRefs,
// because a Certificate Manager map is not a Kubernetes Secret and has no
// reference to point at.
func gatewayManifest(staticIPName, certMapName string) string {
	annotations := ""
	if certMapName != "" {
		annotations = fmt.Sprintf("\n  annotations:\n    networking.gke.io/certmap: %q", certMapName)
	}

	// Named, so the Gateway takes the address the user's records already name
	// rather than whichever one is free.
	addresses := ""
	if staticIPName != "" {
		addresses = fmt.Sprintf("\n  addresses:\n  - type: NamedAddress\n    value: %q", staticIPName)
	}

	// http always, https only once there is a certificate to serve.
	//
	// An https listener with nothing to present does not come up, and a Gateway
	// that does not come up routes nothing at all -- so emitting https alone
	// would mean a project with a domain and no certificate yet lost the plain
	// http it used to have. The names work as soon as the records exist, and
	// gain https when the certificate is issued.
	listeners := `
  - name: http
    protocol: HTTP
    port: 80
    allowedRoutes:
      namespaces:
        from: Same`
	if certMapName != "" {
		// No tls block, deliberately. The certificate comes from the certmap
		// annotation, and a listener that also declares `tls: mode: Terminate`
		// is rejected outright -- "certificateRefs or options must be specified
		// when mode is Terminate" -- because there is no Secret to reference.
		//
		// The Gateway then fails to apply, deployToGKE falls back to a bare
		// address, and production comes up on an IP with its name unused. Which
		// is what happened: the rejection was logged, the deploy reported
		// success, and the only symptom was a URL that was not the domain.
		listeners += `
  - name: https
    protocol: HTTPS
    port: 443
    allowedRoutes:
      namespaces:
        from: Same`
	}

	return fmt.Sprintf(`apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: %s%s
spec:
  gatewayClassName: gke-l7-global-external-managed%s
  listeners:%s
`, gatewayName, annotations, addresses, listeners)
}

// gkeSession is an authenticated, isolated CLI environment for one GKE operation.
//
// gcloud and kubectl both keep their state in files under the home directory. If
// operations share those files, two deployments aimed at different clusters race
// on one kubeconfig and whichever ran get-credentials last silently wins — so
// each session points them at a directory of its own instead.
type gkeSession struct {
	env       []string
	projectID string
	namespace string
	cleanup   func()
}

// newGCloudSession authenticates to the project and nothing more. The caller
// must call cleanup when finished.
//
// Most of what Mendel asks GCP is not about the cluster at all -- reserving an
// address, requesting a certificate, reading its state -- and fetching cluster
// credentials for those costs a GKE API round trip and a kubeconfig write to
// produce a file nothing then opens. That was roughly half the wait on every
// render of the Domain page.
//
// Use this whenever the work is `gcloud`. Use newGKESession only when something
// will actually run `kubectl`.
func newGCloudSession(ctx context.Context, env map[string]string) (*gkeSession, error) {
	var missing []string
	for _, name := range hosting.RequiredCredentialsForCombo(domain.DeployArtifactKubernetes, "gke") {
		if strings.TrimSpace(env[name]) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing GKE credentials: %s", strings.Join(missing, ", "))
	}

	dir, err := os.MkdirTemp("", "mendel-gke-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create session directory: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }

	keyPath := filepath.Join(dir, "key.json")
	if err := os.WriteFile(keyPath, []byte(env["GCP_SERVICE_ACCOUNT_KEY"]), 0600); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to write service account key: %w", err)
	}

	projectID := env["GCP_PROJECT_ID"]
	cmdEnv := append(os.Environ(),
		"GOOGLE_APPLICATION_CREDENTIALS="+keyPath,
		"CLOUDSDK_CONFIG="+filepath.Join(dir, "gcloud"),
		"KUBECONFIG="+filepath.Join(dir, "kubeconfig"),
		"CLOUDSDK_CORE_PROJECT="+projectID,
	)

	authCmd := exec.CommandContext(ctx, "gcloud", "auth", "activate-service-account", "--key-file", keyPath)
	authCmd.Env = cmdEnv
	if output, err := authCmd.CombinedOutput(); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to authenticate service account: %s: %w", strings.TrimSpace(string(output)), err)
	}

	return &gkeSession{env: cmdEnv, projectID: projectID, namespace: hosting.Namespace, cleanup: cleanup}, nil
}

// newGKESession adds cluster credentials, so kubectl has something to talk to.
func newGKESession(ctx context.Context, env map[string]string) (*gkeSession, error) {
	session, err := newGCloudSession(ctx, env)
	if err != nil {
		return nil, err
	}

	// --location accepts either a zone or a region, so a regional cluster works
	// through the same GKE_ZONE credential a zonal one uses.
	getCreds := exec.CommandContext(ctx, "gcloud", "container", "clusters", "get-credentials",
		env["GKE_CLUSTER_NAME"], "--location", env["GKE_ZONE"])
	getCreds.Env = session.env
	if output, err := getCreds.CombinedOutput(); err != nil {
		// Say which clusters the project does have. GKE_CLUSTER_NAME and
		// GKE_ZONE are transcribed by hand, and the two are easy to swap, so
		// the answer is nearly always in a list Mendel can already see -- and
		// "no cluster named us-central1" sends the reader looking for a fault
		// in the credentials rather than at the value in the box.
		hint := gkeClusterHint(ctx, session.env, session.projectID, env["GKE_CLUSTER_NAME"])
		session.cleanup()
		return nil, fmt.Errorf("failed to get cluster credentials: %s%s: %w",
			strings.TrimSpace(string(output)), hint, err)
	}

	return session, nil
}

// gkeClusterHint names the clusters the project actually holds, for an error
// about one that was not found. Best effort: if the listing fails too, the
// original error is still the useful one and this adds nothing.
func gkeClusterHint(ctx context.Context, cmdEnv []string, projectID, wanted string) string {
	list := exec.CommandContext(ctx, "gcloud", "container", "clusters", "list",
		"--project", projectID, "--format", "value(name,location)")
	list.Env = cmdEnv
	output, err := list.Output()
	if err != nil {
		return ""
	}

	var found []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			found = append(found, fmt.Sprintf("GKE_CLUSTER_NAME=%s with GKE_ZONE=%s", fields[0], fields[1]))
		}
	}
	if len(found) == 0 {
		return fmt.Sprintf("\nProject %s has no clusters at all, so there is nothing for %q to name yet.",
			projectID, wanted)
	}
	return fmt.Sprintf("\nProject %s has: %s.", projectID, strings.Join(found, "; "))
}

// kubectl builds a kubectl command scoped to Mendel's namespace.
func (g *gkeSession) kubectl(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--namespace", g.namespace}, args...)...)
	cmd.Env = g.env
	return cmd
}

// ensureNamespace creates Mendel's namespace if the user's cluster lacks it.
func (g *gkeSession) ensureNamespace(ctx context.Context) error {
	check := exec.CommandContext(ctx, "kubectl", "get", "namespace", g.namespace)
	check.Env = g.env
	if err := check.Run(); err == nil {
		return nil
	}
	create := exec.CommandContext(ctx, "kubectl", "create", "namespace", g.namespace)
	create.Env = g.env
	if output, err := create.CombinedOutput(); err != nil {
		// A concurrent deployment may have created it since the check above.
		if strings.Contains(string(output), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create namespace %s: %s: %w", g.namespace, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// buildImage builds workDir with Cloud Build and returns the pushed image
// reference.
//
// The tag is unique per build. With a fixed tag the Deployment's pod spec is
// byte-identical between deployments, so `kubectl apply` finds nothing to change
// and never rolls the pods over — the cluster would keep serving the previous
// variation's code from a container that merely shares its name.
func (g *gkeSession) buildImage(ctx context.Context, deploymentName, workDir string) (string, error) {
	imageName := fmt.Sprintf("gcr.io/%s/%s:%d", g.projectID, deploymentName, time.Now().Unix())

	// Cloud Build streams logs from its default bucket, which only a project
	// Viewer or Owner may read. A deployer service account holding just the
	// roles it needs is neither, and gcloud then exits non-zero over the logs
	// while the build itself succeeds. Writing logs to the project's own build
	// bucket keeps them readable and makes the exit code mean what it says.
	logDir := fmt.Sprintf("gs://%s_cloudbuild/mendel-logs", g.projectID)

	buildCmd := exec.CommandContext(ctx, "gcloud", "builds", "submit",
		"--gcs-log-dir", logDir, "--tag", imageName, ".")
	buildCmd.Dir = workDir
	buildCmd.Env = g.env
	if output, err := buildCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return imageName, nil
}

// deployToGKE deploys a working directory to Google Kubernetes Engine under the given deployment name.
func (s *Server) deployToGKE(
	ctx context.Context,
	projectID uuid.UUID,
	isProd bool,
	deploymentName string,
	workDir string,
	env map[string]string,
	appSecrets map[string]string,
	logMilestone func(string),
	logInfo func(string),
) (string, error) {
	logInfo("Getting GKE credentials...")
	session, err := newGKESession(ctx, env)
	if err != nil {
		return "", err
	}
	defer session.cleanup()

	if err := session.ensureNamespace(ctx); err != nil {
		return "", err
	}

	logMilestone("Building container image...")
	imageName, err := session.buildImage(ctx, deploymentName, workDir)
	if err != nil {
		return "", err
	}

	// Put the app's required values in a Secret the Deployment reads from.
	// The manifest holding them is written outside workDir and deleted once
	// applied, so the values never sit in the checked-out repository.
	envFrom := ""
	if len(appSecrets) > 0 {
		secretName := deploymentName + "-env"
		logInfo(fmt.Sprintf("Creating Secret %s with %d required value(s)...", secretName, len(appSecrets)))

		var sb strings.Builder
		fmt.Fprintf(&sb, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\ntype: Opaque\nstringData:\n", secretName)
		for name, value := range appSecrets {
			// Block scalar: the value is copied verbatim, whatever it contains.
			fmt.Fprintf(&sb, "  %s: |-\n    %s\n", name, strings.ReplaceAll(value, "\n", "\n    "))
		}

		secretFile, err := os.CreateTemp("", "mendel-secret-*.yaml")
		if err != nil {
			return "", fmt.Errorf("failed to create secret manifest: %w", err)
		}
		secretPath := secretFile.Name()
		defer os.Remove(secretPath)
		if _, err := secretFile.WriteString(sb.String()); err != nil {
			secretFile.Close()
			return "", fmt.Errorf("failed to write secret manifest: %w", err)
		}
		secretFile.Close()

		secretCmd := session.kubectl(ctx, "apply", "-f", secretPath)
		if output, err := secretCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to apply secret: %s: %w", strings.TrimSpace(string(output)), err)
		}

		envFrom = fmt.Sprintf("\n        envFrom:\n        - secretRef:\n            name: %s", secretName)
	}

	// A hostname only exists if the project has a domain to build one from, and
	// an address for its records to point at. Reserving the address here rather
	// than at channel setup means it exists by the time anything needs it, and
	// costs nothing on a project that never asks for a domain.
	hostname, ipName, servesHTTPS := "", "", false
	if pd, err := s.db.GetProjectDomain(ctx, projectID); err == nil && pd != nil && pd.BaseDomain != "" {
		if _, err := s.ensureStaticIP(ctx, projectID, session); err != nil {
			logInfo("Could not reserve an address, so this deployment keeps a bare one: " + err.Error())
		} else {
			// Production answers at the name the user chose for it; a demo gets
			// one invented under the demo wildcard. Giving production a demo
			// name would put it somewhere nobody was told to look.
			hostname = pd.DemoHost(deploymentName)
			if isProd {
				hostname = pd.ProdHost()
			}
			ipName = staticIPName(projectID)

			// Best effort: a deployment reachable over http is still deployed,
			// and the Domain page reports the certificate separately.
			if err := s.ensureCertificate(ctx, projectID, pd, session); err != nil {
				logInfo("Could not request a certificate: " + err.Error())
			}
			if hostname != "" {
				// The map, not the certificate. They are different resources with
				// different names, and a gateway pointed at a certmap that does
				// not exist serves no HTTPS and logs no event saying so -- which
				// is exactly how this went unnoticed.
				certMap := ""
				if fresh, err := s.db.GetProjectDomain(ctx, projectID); err == nil && fresh != nil {
					certMap = fresh.CertificateMapName
				}
				if err := s.ensureGateway(ctx, session, ipName, certMap); err != nil {
					// A milestone rather than a note: this changes the URL the
					// deployment ends up with, which is the thing the user came
					// to see. Logged quietly, it reads as an unexplained bare IP.
					logMilestone(fmt.Sprintf("Could not raise the gateway, so %s is not in use "+
						"and this deployment keeps a bare address", hostname))
					logInfo("Gateway error: " + err.Error())
					hostname, ipName = "", ""
				} else {
					// The gateway serves https only where there is a certificate
					// valid for *this* name. A certificate existing is not the
					// same as it covering the host being deployed: the demo
					// wildcard does not cover the production host, and reporting
					// https on the strength of the row existing hands out a URL
					// the browser rejects.
					if fresh, err := s.db.GetProjectDomain(ctx, projectID); err == nil {
						servesHTTPS = certMap != "" && fresh.CertificateCovers(hostname)
					}
				}
			}
		}
	}
	manifest := k8sManifestFor(deploymentName, imageName, envFrom, hostname, ipName)

	manifestPath := filepath.Join(workDir, "k8s-deploy.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		return "", fmt.Errorf("failed to write manifest: %w", err)
	}

	// Apply deployment
	logMilestone("Deploying to GKE...")
	applyCmd := session.kubectl(ctx, "apply", "-f", manifestPath)
	if output, err := applyCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("apply failed: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// Wait for the pods to come up before reporting a URL. Without this a
	// failing image reads as a successful deploy that merely has no address yet.
	rolloutCmd := session.kubectl(ctx, "rollout", "status", "deployment/"+deploymentName, "--timeout=180s")
	if output, err := rolloutCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("rollout failed: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// With a hostname the address is the wildcard record's, which already
	// resolves, so there is nothing to wait for and the URL is known in advance.
	// https because that is the whole point of having a name: a certificate can
	// be issued for it, which cannot be done for an address.
	if hostname != "" {
		// Report the scheme actually served. Saying https before a certificate
		// exists gives a URL that refuses to connect, while the http one works --
		// a broken link that looks like a broken deployment.
		scheme := "http"
		if servesHTTPS {
			scheme = "https"
		} else {
			logInfo("No certificate covering " + hostname + " yet, so this is http. " +
				"The Domain tab lists what is outstanding.")
		}
		url := scheme + "://" + hostname

		// Do not report a deployment as done while nothing answers at it.
		//
		// The first deployment onto a name provisions a global load balancer, and
		// Google takes several minutes to bring one up -- during which the URL
		// refuses connections. Reporting success then hands the user a link that
		// looks broken, and no way to tell "still coming up" from "broken".
		if !s.waitUntilServing(ctx, url, logMilestone, logInfo) {
			return url, errStillProvisioning
		}
		return url, nil
	}

	// Wait for external IP
	logInfo("Waiting for external IP...")
	for i := 0; i < 60; i++ {
		ipCmd := session.kubectl(ctx, "get", "service", deploymentName,
			"-o", "jsonpath={.status.loadBalancer.ingress[0].ip}")
		ipOutput, _ := ipCmd.Output()
		ip := strings.TrimSpace(string(ipOutput))
		if ip != "" {
			return fmt.Sprintf("http://%s", ip), nil
		}
		time.Sleep(5 * time.Second)
	}

	return "", fmt.Errorf("timed out waiting for LoadBalancer IP")
}


// handleStopDemo stops a running demo instance for a variation.
func (s *Server) handleStopDemo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	projID, err := uuid.Parse(projectID)
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// Find running demo
	demoInst, err := s.db.GetRunningDemoByVariation(ctx, variationID)
	if err != nil {
		http.Error(w, "no running demo found", http.StatusNotFound)
		return
	}

	// Run teardown with credentials
	teardownErr := s.runCloudTeardown(ctx, projID, variationID, demoInst)

	if teardownErr != nil {
		// Mark as error but continue
		errMsg := fmt.Sprintf("Teardown failed: %v", teardownErr)
		s.db.UpdateDemoInstanceStatus(ctx, demoInst.ID, domain.DemoInstanceStatusError, &errMsg)
	} else {
		// Mark as stopped
		s.db.UpdateDemoInstanceStatus(ctx, demoInst.ID, domain.DemoInstanceStatusStopped, nil)
	}

	// Revert migration if one was applied
	if err := s.revertVariationMigration(ctx, projectID, variationID); err != nil {
		// Log but don't fail - demo is already stopped
		fmt.Printf("[demo] Warning: failed to revert migration: %v\n", err)
	}

	// Redirect to variation detail
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// runCloudTeardown runs the teardown command stored in the demo instance.
func (s *Server) runCloudTeardown(ctx context.Context, projectID, variationID uuid.UUID, demoInst *domain.DemoInstance) error {
	if demoInst.TeardownInstructions == "" {
		return nil // No teardown needed
	}

	// Get deployment channel to know which credentials to use
	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil || channel == nil {
		return fmt.Errorf("no deployment channel found")
	}

	// Get encryption key
	key, err := crypto.GetKey()
	if err != nil {
		return fmt.Errorf("encryption not configured: %w", err)
	}

	// Get required credentials
	required := hosting.RequiredCredentialsForCombo(channel.ArtifactKind, channel.HostingPlatform.Slug)
	creds := map[string]string{}
	cmdEnv := os.Environ()
	for _, name := range required {
		cred, err := s.db.GetProjectCredential(ctx, projectID, name)
		if err != nil {
			continue // Credential might not exist, best effort
		}
		decrypted, err := crypto.Decrypt(cred.EncryptedValue, key)
		if err != nil {
			continue
		}
		creds[name] = string(decrypted)
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", name, string(decrypted)))
	}

	// kubectl reads a kubeconfig, not environment variables, so exporting the
	// GKE credentials is not enough to aim it anywhere. Inside a pod it would
	// otherwise fall back to the in-cluster service account and run the delete
	// against the cluster Mendel itself is running on. Authenticate first, and
	// let the session's KUBECONFIG point the command at the user's cluster.
	if channel.HostingPlatform.Slug == "gke" {
		session, err := newGKESession(ctx, creds)
		if err != nil {
			return fmt.Errorf("teardown could not reach the cluster: %w", err)
		}
		defer session.cleanup()
		cmdEnv = session.env
	}

	// Run the teardown command
	cmd := exec.CommandContext(ctx, "sh", "-c", demoInst.TeardownInstructions)
	cmd.Env = cmdEnv
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(output))
	}
	return nil
}

// revertVariationMigration reverts a variation's migration if it was applied and not yet reverted.
func (s *Server) revertVariationMigration(ctx context.Context, projectID string, variationID uuid.UUID) error {
	migration, err := s.db.GetVariationMigration(ctx, variationID)
	if err != nil {
		return nil // No migration exists, nothing to revert
	}

	// Only revert if applied and not already reverted
	if migration.AppliedAt == nil || migration.RevertedAt != nil {
		return nil
	}

	workDir := git.WorkDirForVariation(projectID, variationID.String())
	if err := executeMigrationInstructions(ctx, workDir, migration.DownInstructions); err != nil {
		return fmt.Errorf("execute down_instructions: %w", err)
	}

	if err := s.db.MarkVariationMigrationReverted(ctx, migration.ID); err != nil {
		return fmt.Errorf("mark reverted: %w", err)
	}

	return nil
}

// cleanupVariationWorkDir removes the work directory for a resolved variation.
// This should be called when a variation reaches a terminal state (merged, rejected, pruned).
func (s *Server) cleanupVariationWorkDir(projectID string, variationID uuid.UUID) error {
	workDir := git.WorkDirForVariation(projectID, variationID.String())
	return os.RemoveAll(workDir)
}

// apiGetDemoStatus returns the current status of a demo instance.
func (s *Server) apiGetDemoStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	demoID, err := uuid.Parse(chi.URLParam(r, "demoID"))
	if err != nil {
		http.Error(w, "invalid demo ID", http.StatusBadRequest)
		return
	}

	demo, err := s.db.GetDemoInstance(ctx, demoID)
	if err != nil {
		http.Error(w, "demo not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(demo)
}

// handleRetryDemo applies a fix via Claude Code and restarts the demo.
func (s *Server) handleRetryDemo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	fixPrompt := r.FormValue("fix_prompt")
	if fixPrompt == "" {
		http.Error(w, "fix prompt is required", http.StatusBadRequest)
		return
	}

	// Get work directory for this variation
	workDir := git.WorkDirForVariation(projectID, variationID.String())
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		// Try to re-clone from remote if the variation has a commit ref
		variation, err := s.db.GetVariation(ctx, variationID)
		if err != nil || variation.CommitRef == nil || *variation.CommitRef == "" {
			http.Error(w, "variation work directory not found and code not available on remote", http.StatusBadRequest)
			return
		}

		// Need to re-clone - redirect to restart which handles this
		http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s/restart-demo", projectID, variationID), http.StatusSeeOther)
		return
	}

	// Create a new demo instance for the fix attempt with "starting" status
	fixInstanceID := uuid.New()
	fixInstance := &domain.DemoInstance{
		ID:          fixInstanceID,
		VariationID: variationID,
		URL:         "",
		Status:      domain.DemoInstanceStatusStarting,
	}
	if err := s.db.CreateDemoInstance(ctx, fixInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Run the fix and demo startup in background
	go s.runFixAndDemo(projectID, variationID, fixInstanceID, workDir, fixPrompt)

	// Redirect immediately
	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// runFixAndDemo runs the executor to apply a fix, then starts the demo.
func (s *Server) runFixAndDemo(projectID string, variationID, demoInstanceID uuid.UUID, workDir, fixPrompt string) {
	ctx := context.Background()

	// Helper to log with source tracking (use "demo" so logs appear with the demo)
	logInfo := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelInfo, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	logMilestone := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelMilestone, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	logError := func(msg string) {
		s.db.CreateVariationLogWithSource(ctx, variationID, domain.LogLevelError, msg, domain.SourceTypeDemo, &demoInstanceID)
	}
	failDemo := func(errMsg string) {
		logError(errMsg)
		s.db.UpdateDemoInstanceWithSuggestedFix(ctx, demoInstanceID, errMsg, fixPrompt)
	}

	// Get API key from project config
	projID, err := uuid.Parse(projectID)
	if err != nil {
		failDemo("Invalid project ID")
		return
	}

	project, err := s.db.GetProject(ctx, projID)
	if err != nil {
		failDemo(fmt.Sprintf("Failed to get project: %v", err))
		return
	}

	var projectConfig struct {
		AnthropicAPIKey string `json:"anthropic_api_key"`
	}
	if project.Config != nil {
		json.Unmarshal(project.Config, &projectConfig)
	}
	apiKey := projectConfig.AnthropicAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		failDemo("No Anthropic API key configured")
		return
	}

	logMilestone("Applying fix via Claude Code...")
	logInfo(fmt.Sprintf("Prompt: %s", fixPrompt))

	// Run executor with the fix prompt
	exec := executor.New(apiKey, workDir).
		WithEventHandler(func(event executor.Event) {
			switch event.Type {
			case executor.EventToolCall:
				if event.ToolName == "Write" || event.ToolName == "Edit" {
					if path, ok := event.ToolInput["file_path"].(string); ok {
						logInfo(fmt.Sprintf("%s: %s", event.ToolName, path))
					}
				}
			case executor.EventComplete:
				logMilestone("Fix applied")
			}
		})

	result, err := exec.Run(ctx, executor.SystemPrompt(), fixPrompt)
	if err != nil {
		failDemo(fmt.Sprintf("Executor error: %v", err))
		return
	}
	s.recordVariationRunByID(ctx, variationID, result.Stats, "demo_fix")

	if !result.Success {
		failDemo(fmt.Sprintf("Fix failed: %v", result.Error))
		return
	}

	logMilestone("Fix applied, starting demo...")

	// Get deployment channel
	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projID)
	if err != nil || channel == nil {
		failDemo("No deployment channel configured. Go to Deployment settings.")
		return
	}

	if !channel.IsDemoValidated() {
		failDemo("Deployment channel not validated for demos. Go to Deployment settings.")
		return
	}

	// Run channel-based deployment
	s.runChannelDemoDeployment(ctx, projID, variationID, demoInstanceID, workDir, channel, logMilestone, logInfo, logError)
}

// handleRestartDemo stops any existing demo and starts fresh without code changes.
func (s *Server) handleRestartDemo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID := chi.URLParam(r, "projectID")

	variationID, err := uuid.Parse(chi.URLParam(r, "variationID"))
	if err != nil {
		http.Error(w, "invalid variation ID", http.StatusBadRequest)
		return
	}

	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	// Check if project has a deployment channel that requires validation
	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectUUID)
	if err != nil || channel == nil {
		http.Error(w, "No deployment channel configured. Go to Deployment settings.", http.StatusBadRequest)
		return
	}
	if !channel.IsDemoValidated() {
		http.Error(w, "Demo deployment channel not validated. Go to Deployment settings to validate.", http.StatusBadRequest)
		return
	}

	// Stop any existing running demo first
	existingDemo, _ := s.db.GetRunningDemoByVariation(ctx, variationID)
	if existingDemo != nil && existingDemo.TeardownInstructions != "" {
		s.runCloudTeardown(ctx, projectUUID, variationID, existingDemo)
		s.db.UpdateDemoInstanceStatus(ctx, existingDemo.ID, domain.DemoInstanceStatusStopped, nil)
	}

	// Get the work directory
	workDir := git.WorkDirForVariation(projectID, variationID.String())

	// Create new demo instance - validation happens in background
	demoInstanceID := uuid.New()
	processInfo, _ := json.Marshal(map[string]interface{}{
		"work_dir": workDir,
	})

	demoInstance := &domain.DemoInstance{
		ID:                   demoInstanceID,
		VariationID:          variationID,
		URL:                  "",
		TeardownInstructions: "", // Set by deployment after we know the resource names
		Status:               domain.DemoInstanceStatusStarting,
		ProcessInfo:          processInfo,
	}

	if err := s.db.CreateDemoInstance(ctx, demoInstance); err != nil {
		http.Error(w, "failed to create demo instance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Start demo in background - validation happens there
	go s.runDemoStartup(projectID, variationID, demoInstanceID, workDir)

	http.Redirect(w, r, fmt.Sprintf("/p/%s/variations/%s", projectID, variationID), http.StatusSeeOther)
}

// monitorClaudeProgress observes file system and git activity while Claude Code runs,
// logging progress updates every 2 seconds until the done channel is closed.
func monitorClaudeProgress(workDir string, logInfo func(string), done <-chan struct{}) {
	startTime := time.Now()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Track state for change detection
	lastCommit := getGitHead(workDir)
	lastModTimes := make(map[string]time.Time)

	for {
		select {
		case <-done:
			elapsed := time.Since(startTime).Round(time.Second)
			logInfo(fmt.Sprintf("Claude Code completed (%s)", elapsed))
			return
		case <-ticker.C:
			elapsed := time.Since(startTime).Round(time.Second)
			var updates []string

			// Check for new commits
			currentCommit := getGitHead(workDir)
			if currentCommit != lastCommit && currentCommit != "" {
				commitMsg := getGitCommitMessage(workDir, currentCommit)
				if commitMsg != "" {
					updates = append(updates, fmt.Sprintf("committed: %s", commitMsg))
				}
				lastCommit = currentCommit
			}

			// Check for recently modified files (ignore .git directory)
			modifiedFiles := getRecentlyModifiedFiles(workDir, lastModTimes)
			if len(modifiedFiles) > 0 {
				if len(modifiedFiles) <= 3 {
					updates = append(updates, fmt.Sprintf("modified: %s", strings.Join(modifiedFiles, ", ")))
				} else {
					updates = append(updates, fmt.Sprintf("modified: %s (+%d more)", strings.Join(modifiedFiles[:3], ", "), len(modifiedFiles)-3))
				}
			}

			// Log progress if there are updates, or every 30s as a heartbeat
			if len(updates) > 0 {
				logInfo(fmt.Sprintf("Claude Code running (%s): %s", elapsed, strings.Join(updates, "; ")))
			} else if elapsed.Seconds() > 0 && int(elapsed.Seconds())%30 == 0 {
				logInfo(fmt.Sprintf("Claude Code still running (%s)...", elapsed))
			}
		}
	}
}

// getGitHead returns the current HEAD commit hash.
func getGitHead(workDir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// getGitCommitMessage returns the first line of a commit message.
func getGitCommitMessage(workDir, commitHash string) string {
	cmd := exec.Command("git", "log", "-1", "--format=%s", commitHash)
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	msg := strings.TrimSpace(string(output))
	if len(msg) > 60 {
		msg = msg[:57] + "..."
	}
	return msg
}

// getRecentlyModifiedFiles returns files modified since last check, updating lastModTimes.
func getRecentlyModifiedFiles(workDir string, lastModTimes map[string]time.Time) []string {
	var modified []string

	// Walk top-level files and one level of subdirectories (skip .git)
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return nil
	}

	checkFile := func(path, name string) {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return
		}
		modTime := info.ModTime()
		if lastTime, exists := lastModTimes[path]; !exists || modTime.After(lastTime) {
			if exists { // Only report if we've seen it before (skip initial scan)
				modified = append(modified, name)
			}
			lastModTimes[path] = modTime
		}
	}

	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		path := fmt.Sprintf("%s/%s", workDir, entry.Name())
		if entry.IsDir() {
			// Check one level deep
			subEntries, err := os.ReadDir(path)
			if err != nil {
				continue
			}
			for _, subEntry := range subEntries {
				if !subEntry.IsDir() {
					subPath := fmt.Sprintf("%s/%s", path, subEntry.Name())
					checkFile(subPath, fmt.Sprintf("%s/%s", entry.Name(), subEntry.Name()))
				}
			}
		} else {
			checkFile(path, entry.Name())
		}
	}

	return modified
}

// addOptionalCredentials loads the credentials that add a capability rather than
// gate a deployment, and says nothing when they are absent.
//
// Loading them is easy to forget, because nothing fails without them: the
// deployment proceeds, the capability is simply never available, and the code
// reading env for the value sees an empty string that looks exactly like a user
// who chose not to supply one.
func (s *Server) addOptionalCredentials(
	ctx context.Context,
	projectID uuid.UUID,
	channel *domain.ProjectDeploymentChannel,
	key []byte,
	env map[string]string,
) {
	if channel == nil || channel.HostingPlatform == nil {
		return
	}
	for _, name := range hosting.OptionalCredentialsForCombo(channel.ArtifactKind, channel.HostingPlatform.Slug) {
		cred, err := s.db.GetProjectCredential(ctx, projectID, name)
		if err != nil {
			continue // Not supplied, which is what "optional" means.
		}
		decrypted, err := crypto.Decrypt(cred.EncryptedValue, key)
		if err != nil {
			log.Printf("deployment: could not decrypt optional credential %s: %v", name, err)
			continue
		}
		env[name] = string(decrypted)
	}
}

// staticIPName is what Mendel calls the address it reserves for a project. One
// per project, named after it, so a user looking at their GCP console can tell
// what it is for and Mendel can find it again without recording a lookup key.
func staticIPName(projectID uuid.UUID) string {
	return "mendel-" + projectID.String()[:8]
}

// ensureStaticIP reserves the address a project's DNS records point at, and
// records it so the Domain page can state the record.
//
// Reserved rather than left to the load balancer, for two reasons. An ephemeral
// address is not known until something has been deployed, which is backwards: the
// record has to exist before the first deployment is reachable. And it changes
// when the load balancer is recreated, which would silently break every record
// pointing at it.
//
// Global, because that is what a GKE Ingress binds to; a regional address is
// accepted by gcloud and then never used by the Ingress.
func (s *Server) ensureStaticIP(ctx context.Context, projectID uuid.UUID, session *gkeSession) (string, error) {
	if existing, err := s.db.GetProjectDomain(ctx, projectID); err == nil && existing != nil && existing.StaticIP != "" {
		return existing.StaticIP, nil
	}

	name := staticIPName(projectID)

	// Creating one that exists is not an error: this runs on every deploy, and
	// the address outlives any of them.
	create := exec.CommandContext(ctx, "gcloud", "compute", "addresses", "create", name,
		"--global", "--project", session.projectID)
	create.Env = session.env
	if out, err := create.CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
		return "", fmt.Errorf("could not reserve an address: %s: %w", strings.TrimSpace(string(out)), err)
	}

	describe := exec.CommandContext(ctx, "gcloud", "compute", "addresses", "describe", name,
		"--global", "--project", session.projectID, "--format", "value(address)")
	describe.Env = session.env
	out, err := describe.Output()
	if err != nil {
		return "", fmt.Errorf("reserved an address but could not read it back: %w", err)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("reserved address %s has no address", name)
	}

	if err := s.db.SetProjectStaticIP(ctx, projectID, ip, name); err != nil {
		return "", fmt.Errorf("could not record the reserved address: %w", err)
	}
	return ip, nil
}

// ensureCertificate asks Certificate Manager for a wildcard certificate covering
// the project's demo names, and records the ownership challenge it mints.
//
// The challenge is why this exists at all. The address records can be worked out
// from the domain, so Mendel can state them the moment it has an address; this
// one is generated by the certificate authority and is different for every
// domain, so the only way the Domain page can show it is to ask for it first.
//
// A wildcard authorization is created against the parent of the wildcard: for
// *.mendel-demos.example.com the domain authorized is mendel-demos.example.com.
// ensureGateway applies the namespace's one Gateway, so an HTTPRoute has
// something to attach to.
//
// Applied on every deploy rather than once: it is the same object each time, and
// a Gateway that has been deleted or predates a certificate would otherwise stay
// wrong until someone noticed.
func (s *Server) ensureGateway(ctx context.Context, session *gkeSession, staticIPName, certMapName string) error {
	path := filepath.Join(os.TempDir(), "mendel-gateway-"+staticIPName+".yaml")
	if err := os.WriteFile(path, []byte(gatewayManifest(staticIPName, certMapName)), 0600); err != nil {
		return fmt.Errorf("write gateway manifest: %w", err)
	}
	defer os.Remove(path)

	cmd := session.kubectl(ctx, "apply", "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply gateway: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (s *Server) ensureCertificate(ctx context.Context, projectID uuid.UUID, pd *domain.ProjectDomain, session *gkeSession) error {
	if pd == nil || pd.BaseDomain == "" {
		return nil
	}

	base := staticIPName(projectID) // One naming scheme for everything a project owns.
	zones := pd.CertificateZones()

	// One authorization per zone. Certificate Manager authorizes a single domain
	// name each, the sole exception being that a wildcard is authorized on its
	// parent -- which is why these are the zones rather than the wildcards.
	authNames := make([]string, 0, len(zones))
	for _, zone := range zones {
		name, record, value, found, err := findAuthorization(ctx, session, zone)
		if err != nil {
			return fmt.Errorf("could not check what already authorizes %s: %w", zone, err)
		}

		if !found {
			// Named after the zone, not its position in a list. An index is
			// reused when the user edits their demo subdomain, and a create on an
			// existing name fails with "already exists" -- which would be read as
			// success, then describe an authorization for whatever used to hold
			// that slot.
			name = authorizationName(base, zone)

			create := exec.CommandContext(ctx, "gcloud", "certificate-manager", "dns-authorizations",
				"create", name, "--domain", zone, "--project", session.projectID)
			create.Env = session.env
			if out, err := create.CombinedOutput(); err != nil {
				return fmt.Errorf("could not authorize %s: %s: %w", zone, strings.TrimSpace(string(out)), err)
			}

			record, value, err = describeAuthorization(ctx, session, name)
			if err != nil {
				return fmt.Errorf("authorized %s but could not read its record back: %w", zone, err)
			}
		}

		authNames = append(authNames, name)

		// Stored as each is settled rather than at the end, so a failure partway
		// leaves the user with the records that do exist instead of none.
		if err := s.db.SetProjectCertificateChallenge(ctx, projectID, domain.ACMEChallenge{
			Domain:      zone,
			RecordName:  strings.TrimSuffix(record, "."),
			RecordValue: value,
		}); err != nil {
			return err
		}
	}

	// The certificate's domain list is immutable, so its name carries a digest of
	// that list: changing the demo subdomain mints a new certificate beside the
	// old one rather than failing against a name that already exists with the
	// wrong contents. The map name stays put, so the gateway annotation does not
	// have to change when the certificate does.
	domains := pd.CertificateDomains()
	certName := fmt.Sprintf("%s-%s", base, digestOf(domains))

	cert := exec.CommandContext(ctx, "gcloud", "certificate-manager", "certificates", "create", certName,
		"--domains", strings.Join(domains, ","),
		"--dns-authorizations", strings.Join(authNames, ","),
		"--project", session.projectID)
	cert.Env = session.env
	if out, err := cert.CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("could not create the certificate: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// The gateway references a *map*, not a certificate. Annotating it with a
	// certificate name pointed at a map that did not exist, and a gateway given a
	// certmap it cannot resolve serves no HTTPS and logs no event about it.
	mkMap := exec.CommandContext(ctx, "gcloud", "certificate-manager", "maps", "create", base,
		"--project", session.projectID)
	mkMap.Env = session.env
	if out, err := mkMap.CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("could not create the certificate map: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// A primary entry is the map's fallback, and one certificate covers every
	// name Mendel uses, so a single entry serves all of them. Deleted first
	// because an entry's certificate list cannot be changed in place, and this
	// runs again whenever the certificate is replaced.
	entry := base + "-primary"
	del := exec.CommandContext(ctx, "gcloud", "certificate-manager", "maps", "entries", "delete", entry,
		"--map", base, "--project", session.projectID, "--quiet")
	del.Env = session.env
	_ = del.Run() // Absent is the expected case on a first run.

	mkEntry := exec.CommandContext(ctx, "gcloud", "certificate-manager", "maps", "entries", "create", entry,
		"--map", base, "--certificates", certName, "--set-primary", "--project", session.projectID)
	mkEntry.Env = session.env
	if out, err := mkEntry.CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("could not attach the certificate to its map: %s: %w",
			strings.TrimSpace(string(out)), err)
	}

	return s.db.SetProjectCertificate(ctx, projectID, certName, base)
}

// findAuthorization returns the authorization already covering a zone.
//
// Certificate Manager allows one authorization per domain, and refuses a second
// under a different name -- with a message about the domain, not the name, so a
// caller matching on "already exists" reads it as success and then describes an
// authorization that was never created. That is what left this project with the
// base zone authorized, the demo zone not, and no certificate at all.
//
// Reusing is also the kinder answer. A fresh authorization mints a fresh record
// value, so replacing one the user already created means sending them back to
// their DNS provider to change a record that was working.
func findAuthorization(ctx context.Context, session *gkeSession, zone string) (name, record, value string, found bool, err error) {
	list := exec.CommandContext(ctx, "gcloud", "certificate-manager", "dns-authorizations", "list",
		"--project", session.projectID,
		"--format", "value(name,domain,dnsResourceRecord.name,dnsResourceRecord.data)")
	list.Env = session.env
	out, err := list.Output()
	if err != nil {
		return "", "", "", false, err
	}

	name, record, value, found = matchAuthorization(string(out), zone)
	return name, record, value, found, nil
}

// matchAuthorization picks the row for a zone out of what gcloud printed.
//
// An exact match on the domain, never a suffix one: mendel-demos.example.com
// ends with example.com, and reusing the demo zone's authorization for the base
// domain would produce a certificate that cannot be validated and a record the
// user has already created, so nothing would look wrong.
func matchAuthorization(output, zone string) (name, record, value string, found bool) {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			continue
		}
		if strings.EqualFold(strings.TrimSuffix(fields[1], "."), zone) {
			return fields[0], fields[2], fields[3], true
		}
	}
	return "", "", "", false
}

// describeAuthorization reads back the record the authority minted, which is the
// only place its value can come from.
func describeAuthorization(ctx context.Context, session *gkeSession, name string) (record, value string, err error) {
	describe := exec.CommandContext(ctx, "gcloud", "certificate-manager", "dns-authorizations",
		"describe", name, "--project", session.projectID,
		"--format", "value(dnsResourceRecord.name,dnsResourceRecord.data)")
	describe.Env = session.env
	out, err := describe.Output()
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return "", "", fmt.Errorf("record came back as %q, which is not a name and a value",
			strings.TrimSpace(string(out)))
	}
	return fields[0], fields[1], nil
}

// authorizationName is stable for a zone and different for every other zone.
func authorizationName(base, zone string) string {
	return fmt.Sprintf("%s-%s", base, digestOf([]string{zone}))
}

// digestOf names a resource after its contents, so a resource that cannot be
// changed in place is replaced rather than skipped when the contents change.
func digestOf(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return hex.EncodeToString(sum[:])[:8]
}


// errStillProvisioning says the deployment is correct but not yet reachable.
//
// Not a failure: nothing needs fixing and nothing should be retried. It exists so
// a caller can record the deployment honestly rather than choosing between
// calling it done, which is untrue, and calling it failed, which is worse.
var errStillProvisioning = errors.New("deployed, but the load balancer is not serving yet")

// waitUntilServing blocks until the URL answers, reporting false on timeout.
//
// Any HTTP response counts. A 404 means the load balancer is routing, which is
// the question being asked; whether the app serves that particular path is the
// app's business and not something Mendel should have an opinion about.
func (s *Server) waitUntilServing(ctx context.Context, url string,
	logMilestone, logInfo func(string)) bool {

	logMilestone("Waiting for the load balancer to start serving " + url)
	logInfo("A first deployment onto a name has to provision one, which Google takes " +
		"5-20 minutes to finish. The name refuses connections until it does, and there " +
		"is nothing to fix in the meantime.")

	client := &http.Client{
		Timeout: 15 * time.Second,
		// Report on the deployment's own URL rather than wherever it redirects.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	deadline := time.Now().Add(20 * time.Minute)
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		if ctx.Err() != nil {
			return false
		}
		if resp, err := client.Get(url); err == nil {
			resp.Body.Close()
			logMilestone(fmt.Sprintf("Serving: %s answered %d", url, resp.StatusCode))
			return true
		}
		// Every couple of minutes, not every attempt: a log line per failed probe
		// buries the milestones in noise about a thing that is going fine.
		if attempt%8 == 0 {
			logInfo(fmt.Sprintf("Still provisioning after %d minutes.", attempt/4))
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(15 * time.Second):
		}
	}
	return false
}

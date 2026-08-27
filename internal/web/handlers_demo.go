package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
func teardownCommandFor(platformSlug, appName string) string {
	switch platformSlug {
	case "fly-io":
		return fmt.Sprintf("flyctl apps destroy %s --yes", appName)
	case "cloud-run":
		return fmt.Sprintf("gcloud run services delete %s --region us-central1 --quiet", appName)
	case "gke":
		return fmt.Sprintf("kubectl delete deployment,service %s", appName)
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
	var url string
	var deployErr error

	switch channel.HostingPlatform.Slug {
	case "fly-io":
		url, deployErr = s.deployToFlyIO(ctx, appName, workDir, env, logMilestone, logInfo)
	case "cloud-run":
		url, deployErr = s.deployToCloudRun(ctx, appName, workDir, env, logMilestone, logInfo)
	case "gke":
		url, deployErr = s.deployToGKE(ctx, appName, workDir, env, logMilestone, logInfo)
	default:
		failDemo("Unsupported platform: " + channel.HostingPlatform.Slug)
		return
	}

	if deployErr != nil {
		failDemo(fmt.Sprintf("Deployment failed: %v", deployErr))
		return
	}

	// Update demo instance with URL
	logMilestone("Demo deployed: " + url)

	teardownCmd := teardownCommandFor(channel.HostingPlatform.Slug, appName)

	_, err = s.db.Pool.Exec(ctx, `
		UPDATE demo_instances
		SET url = $2, teardown_instructions = $3, status = $4
		WHERE id = $1
	`, demoInstanceID, url, teardownCmd, domain.DemoInstanceStatusRunning)
	if err != nil {
		logError("Failed to update demo status: " + err.Error())
	}
}

// deployToFlyIO deploys a working directory to Fly.io under the given app name.
func (s *Server) deployToFlyIO(
	ctx context.Context,
	appName string,
	workDir string,
	env map[string]string,
	logMilestone func(string),
	logInfo func(string),
) (string, error) {
	// Check for Dockerfile
	dockerfilePath := filepath.Join(workDir, "Dockerfile")
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		return "", fmt.Errorf("no Dockerfile found in repository")
	}

	// Always write our fly.toml (ensures correct app name)
	// We use a Mendel-controlled app name to avoid conflicts
	flyTomlPath := filepath.Join(workDir, "fly.toml")
	flyToml := fmt.Sprintf(`app = "%s"
primary_region = "iad"

[build]

[http_service]
  internal_port = 8080
  force_https = true
  auto_stop_machines = "stop"
  auto_start_machines = true
  min_machines_running = 0
`, appName)
	if err := os.WriteFile(flyTomlPath, []byte(flyToml), 0644); err != nil {
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
	logMilestone func(string),
	logInfo func(string),
) (string, error) {
	projectID := env["GCP_PROJECT_ID"]
	region := "us-central1"

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
	deployCmd := exec.CommandContext(ctx, "gcloud", "run", "deploy", serviceName,
		"--source", ".",
		"--region", region,
		"--allow-unauthenticated",
		"--quiet")
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

// deployToGKE deploys a working directory to Google Kubernetes Engine under the given deployment name.
func (s *Server) deployToGKE(
	ctx context.Context,
	deploymentName string,
	workDir string,
	env map[string]string,
	logMilestone func(string),
	logInfo func(string),
) (string, error) {
	projectID := env["GCP_PROJECT_ID"]
	clusterName := env["GKE_CLUSTER_NAME"]
	zone := env["GKE_ZONE"]

	// Write service account key
	keyFile, err := os.CreateTemp("", "gcp-key-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create key file: %w", err)
	}
	defer os.Remove(keyFile.Name())
	keyFile.WriteString(env["GCP_SERVICE_ACCOUNT_KEY"])
	keyFile.Close()

	cmdEnv := os.Environ()
	cmdEnv = append(cmdEnv, fmt.Sprintf("GOOGLE_APPLICATION_CREDENTIALS=%s", keyFile.Name()))

	// Authenticate and get cluster credentials
	logInfo("Getting GKE credentials...")
	authCmd := exec.CommandContext(ctx, "gcloud", "auth", "activate-service-account", "--key-file", keyFile.Name())
	authCmd.Env = cmdEnv
	authCmd.CombinedOutput()

	setProjectCmd := exec.CommandContext(ctx, "gcloud", "config", "set", "project", projectID)
	setProjectCmd.Env = cmdEnv
	setProjectCmd.CombinedOutput()

	getCredsCmd := exec.CommandContext(ctx, "gcloud", "container", "clusters", "get-credentials", clusterName, "--zone", zone)
	getCredsCmd.Env = cmdEnv
	if output, err := getCredsCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to get cluster credentials: %s: %w", string(output), err)
	}

	// Build image using Cloud Build
	imageName := fmt.Sprintf("gcr.io/%s/%s:latest", projectID, deploymentName)
	logMilestone("Building container image...")
	buildCmd := exec.CommandContext(ctx, "gcloud", "builds", "submit", "--tag", imageName, ".")
	buildCmd.Dir = workDir
	buildCmd.Env = cmdEnv
	if output, err := buildCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build failed: %s: %w", string(output), err)
	}

	// Create k8s deployment manifest
	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
spec:
  replicas: 1
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
        - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: %s
spec:
  type: LoadBalancer
  selector:
    app: %s
  ports:
  - port: 80
    targetPort: 8080
`, deploymentName, deploymentName, deploymentName, imageName, deploymentName, deploymentName)

	manifestPath := filepath.Join(workDir, "k8s-deploy.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		return "", fmt.Errorf("failed to write manifest: %w", err)
	}

	// Apply deployment
	logMilestone("Deploying to GKE...")
	applyCmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", manifestPath)
	applyCmd.Env = cmdEnv
	if output, err := applyCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("apply failed: %s: %w", string(output), err)
	}

	// Wait for external IP
	logInfo("Waiting for external IP...")
	for i := 0; i < 30; i++ {
		ipCmd := exec.CommandContext(ctx, "kubectl", "get", "service", deploymentName,
			"-o", "jsonpath={.status.loadBalancer.ingress[0].ip}")
		ipCmd.Env = cmdEnv
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
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", name, string(decrypted)))
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

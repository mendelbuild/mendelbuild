package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/db"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
)

// Runner executes deployment scripts inside Docker containers.
type Runner struct {
	db        *db.DB
	workDir   string // Base directory for cloned repos
	imageName string // Docker image name for deploy tools
}

// NewRunner creates a new deploy runner.
func NewRunner(database *db.DB, workDir string) *Runner {
	return &Runner{
		db:        database,
		workDir:   workDir,
		imageName: "mendel-deploy-tools:latest",
	}
}

// DeployResult holds the result of a deployment.
type DeployResult struct {
	URL          string
	PublicURL    string
	InstanceInfo map[string]interface{}
	Logs         string
	Error        error
}

// Deploy deploys a variation to a cloud environment.
func (r *Runner) Deploy(ctx context.Context, variation *domain.Variation, repoPath string, cloudEcosystem string) (*DeployResult, error) {
	result := &DeployResult{}

	// Load deploy config
	config, err := LoadConfig(repoPath)
	if err != nil {
		result.Error = fmt.Errorf("load config: %w", err)
		return result, result.Error
	}

	// Get project ID from variation -> hop -> strategy -> project
	hop, err := r.db.GetHop(ctx, variation.HopID)
	if err != nil {
		result.Error = fmt.Errorf("get hop: %w", err)
		return result, result.Error
	}
	strategy, err := r.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		result.Error = fmt.Errorf("get strategy: %w", err)
		return result, result.Error
	}

	// Validate required credentials exist
	missingCreds, err := r.validateCredentials(ctx, strategy.ProjectID, config.Credentials)
	if err != nil {
		result.Error = fmt.Errorf("validate credentials: %w", err)
		return result, result.Error
	}
	if len(missingCreds) > 0 {
		result.Error = fmt.Errorf("missing required credentials: %s (add them in Project Settings)", strings.Join(missingCreds, ", "))
		return result, result.Error
	}

	// Create deployed instance record
	instance := &domain.DeployedInstance{
		ID:             uuid.New(),
		VariationID:    variation.ID,
		CloudEcosystem: cloudEcosystem,
		URL:            "", // Will be populated after deploy
		Status:         domain.DeployedInstanceStatusDeploying,
	}
	if err := r.db.CreateDeployedInstance(ctx, instance); err != nil {
		result.Error = fmt.Errorf("create deployed instance: %w", err)
		return result, result.Error
	}

	// Get decrypted credentials
	creds, err := r.getDecryptedCredentials(ctx, strategy.ProjectID, config.Credentials)
	if err != nil {
		r.updateInstanceStatus(ctx, instance.ID, domain.DeployedInstanceStatusFailed, err.Error())
		result.Error = fmt.Errorf("decrypt credentials: %w", err)
		return result, result.Error
	}

	// Run deploy script
	output, err := r.runDeployScript(ctx, repoPath, config, creds)
	result.Logs = output
	if err != nil {
		errMsg := err.Error()
		r.updateInstanceStatus(ctx, instance.ID, domain.DeployedInstanceStatusFailed, errMsg)
		result.Error = fmt.Errorf("deploy script failed: %w", err)
		return result, result.Error
	}

	// Parse output for URL and instance info
	url, publicURL, instanceInfo := r.parseDeployOutput(output, config)
	if url == "" {
		errMsg := "deploy script did not output MENDEL_URL"
		r.updateInstanceStatus(ctx, instance.ID, domain.DeployedInstanceStatusFailed, errMsg)
		result.Error = fmt.Errorf("%s", errMsg)
		return result, result.Error
	}

	result.URL = url
	result.PublicURL = publicURL
	result.InstanceInfo = instanceInfo

	// Update instance with results
	instanceInfoJSON, _ := json.Marshal(instanceInfo)
	_, err = r.db.Pool.Exec(ctx, `
		UPDATE deployed_instances
		SET url = $2, public_url = $3, instance_info = $4, status = 'running'
		WHERE id = $1
	`, instance.ID, url, publicURL, instanceInfoJSON)
	if err != nil {
		result.Error = fmt.Errorf("update deployed instance: %w", err)
		return result, result.Error
	}

	return result, nil
}

// Teardown tears down a deployed instance.
func (r *Runner) Teardown(ctx context.Context, instance *domain.DeployedInstance, repoPath string) error {
	config, err := LoadConfig(repoPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if config.Deploy.TeardownScript == "" {
		// No teardown script configured, just mark as terminated
		return r.updateInstanceStatus(ctx, instance.ID, domain.DeployedInstanceStatusTerminated, "")
	}

	// Get credentials for teardown
	variation, err := r.db.GetVariation(ctx, instance.VariationID)
	if err != nil {
		return fmt.Errorf("get variation: %w", err)
	}
	hop, err := r.db.GetHop(ctx, variation.HopID)
	if err != nil {
		return fmt.Errorf("get hop: %w", err)
	}
	strategy, err := r.db.GetStrategy(ctx, hop.StrategyID)
	if err != nil {
		return fmt.Errorf("get strategy: %w", err)
	}

	creds, err := r.getDecryptedCredentials(ctx, strategy.ProjectID, config.Credentials)
	if err != nil {
		return fmt.Errorf("decrypt credentials: %w", err)
	}

	// Run teardown script
	_, err = r.runScript(ctx, repoPath, config.Deploy.TeardownScript, config.Deploy.WorkingDir, creds)
	if err != nil {
		errMsg := err.Error()
		r.updateInstanceStatus(ctx, instance.ID, domain.DeployedInstanceStatusFailed, errMsg)
		return fmt.Errorf("teardown script failed: %w", err)
	}

	return r.updateInstanceStatus(ctx, instance.ID, domain.DeployedInstanceStatusTerminated, "")
}

func (r *Runner) validateCredentials(ctx context.Context, projectID uuid.UUID, required []string) ([]string, error) {
	var missing []string
	for _, name := range required {
		_, err := r.db.GetProjectCredential(ctx, projectID, name)
		if err != nil {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

func (r *Runner) getDecryptedCredentials(ctx context.Context, projectID uuid.UUID, names []string) (map[string]string, error) {
	key, err := crypto.GetKey()
	if err != nil {
		return nil, fmt.Errorf("get encryption key: %w", err)
	}

	creds := make(map[string]string)
	for _, name := range names {
		cred, err := r.db.GetProjectCredential(ctx, projectID, name)
		if err != nil {
			return nil, fmt.Errorf("get credential %s: %w", name, err)
		}

		value, err := crypto.DecryptString(cred.EncryptedValue, key)
		if err != nil {
			return nil, fmt.Errorf("decrypt credential %s: %w", name, err)
		}

		creds[name] = value
	}

	return creds, nil
}

func (r *Runner) runDeployScript(ctx context.Context, repoPath string, config *DeployConfig, creds map[string]string) (string, error) {
	return r.runScript(ctx, repoPath, config.Deploy.Script, config.Deploy.WorkingDir, creds)
}

func (r *Runner) runScript(ctx context.Context, repoPath, script, workDir string, creds map[string]string) (string, error) {
	// Resolve working directory
	containerWorkDir := "/workspace"
	if workDir != "" {
		containerWorkDir = filepath.Join("/workspace", workDir)
	}

	// Build docker command
	args := []string{
		"run", "--rm",
		"-v", fmt.Sprintf("%s:/workspace", repoPath),
		"-w", containerWorkDir,
	}

	// Add credentials as environment variables
	for name, value := range creds {
		args = append(args, "-e", fmt.Sprintf("%s=%s", name, value))
	}

	args = append(args, r.imageName, "bash", "-c", script)

	cmd := exec.CommandContext(ctx, "docker", args...)

	// Capture output
	var output strings.Builder
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start docker: %w", err)
	}

	// Read stdout and stderr concurrently
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			output.WriteString(scanner.Text() + "\n")
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			output.WriteString(scanner.Text() + "\n")
		}
	}()

	err := cmd.Wait()
	return output.String(), err
}

func (r *Runner) parseDeployOutput(output string, config *DeployConfig) (url, publicURL string, instanceInfo map[string]interface{}) {
	instanceInfo = make(map[string]interface{})

	// Parse URL
	urlRe := regexp.MustCompile(config.Deploy.Output.URLPattern)
	if matches := urlRe.FindStringSubmatch(output); len(matches) > 1 {
		url = strings.TrimSpace(matches[1])
	}

	// Parse public URL
	if config.Deploy.Output.PublicURLPattern != "" {
		publicRe := regexp.MustCompile(config.Deploy.Output.PublicURLPattern)
		if matches := publicRe.FindStringSubmatch(output); len(matches) > 1 {
			publicURL = strings.TrimSpace(matches[1])
		}
	}

	// Parse instance info
	if config.Deploy.Output.InstancePattern != "" {
		instanceRe := regexp.MustCompile(config.Deploy.Output.InstancePattern)
		if matches := instanceRe.FindStringSubmatch(output); len(matches) > 1 {
			instanceInfo["instance_id"] = strings.TrimSpace(matches[1])
		}
	}

	return
}

func (r *Runner) updateInstanceStatus(ctx context.Context, id uuid.UUID, status domain.DeployedInstanceStatus, errMsg string) error {
	var errMsgPtr *string
	if errMsg != "" {
		errMsgPtr = &errMsg
	}
	return r.db.UpdateDeployedInstanceStatus(ctx, id, status, errMsgPtr)
}

// HealthCheck performs health check on a deployed instance.
func (r *Runner) HealthCheck(ctx context.Context, instance *domain.DeployedInstance, config *DeployConfig) error {
	timeout := time.Duration(config.Health.Timeout) * time.Second
	interval := time.Duration(config.Health.Interval) * time.Second
	endpoint := config.Health.Endpoint

	deadline := time.Now().Add(timeout)
	url := instance.URL + endpoint

	for time.Now().Before(deadline) {
		// Simple HTTP GET check
		cmd := exec.CommandContext(ctx, "curl", "-sf", "-o", "/dev/null", url)
		if err := cmd.Run(); err == nil {
			return nil // Healthy
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
			// Continue checking
		}
	}

	return fmt.Errorf("health check timed out after %s", timeout)
}

// CheckDockerImage verifies the deploy tools image exists.
func (r *Runner) CheckDockerImage(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", r.imageName)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

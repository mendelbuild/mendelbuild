package web

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/bhs/mendelbuild/internal/hosting"
	"github.com/google/uuid"
)

// cloneMainForDeploy clones the project's main branch into a fresh temp directory.
// The caller is responsible for removing the returned directory.
func (s *Server) cloneMainForDeploy(ctx context.Context, projectID uuid.UUID) (workDir string, commitSHA string, err error) {
	repo, err := s.db.GetRepositoryByProject(ctx, projectID)
	if err != nil {
		return "", "", fmt.Errorf("get repository: %w", err)
	}
	if repo.URL == nil || *repo.URL == "" {
		return "", "", fmt.Errorf("project has no repository URL configured")
	}

	var repoConfig struct {
		MainBranch string `json:"main_branch"`
		AuthToken  string `json:"auth_token"`
	}
	if repo.Config != nil {
		json.Unmarshal(repo.Config, &repoConfig)
	}
	mainBranch := repoConfig.MainBranch
	if mainBranch == "" {
		mainBranch = "main"
	}

	tmpDir, err := os.MkdirTemp("", "mendel-prod-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp dir: %w", err)
	}

	// git.Client clones into workDir, which must not already exist as a git repo.
	cloneDir := filepath.Join(tmpDir, "repo")
	client := git.NewClient(cloneDir)
	if err := client.Clone(ctx, *repo.URL, mainBranch, repoConfig.AuthToken); err != nil {
		os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("clone %s: %w", mainBranch, err)
	}

	sha, err := client.GetCurrentCommit(ctx)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("get current commit: %w", err)
	}

	return tmpDir, sha, nil
}

// deployCredentialsForChannel decrypts the credentials the channel's platform requires.
func (s *Server) deployCredentialsForChannel(ctx context.Context, projectID uuid.UUID, channel *domain.ProjectDeploymentChannel) (map[string]string, error) {
	if channel.HostingPlatform == nil {
		return nil, fmt.Errorf("channel has no hosting platform")
	}

	key, err := crypto.GetKey()
	if err != nil {
		return nil, fmt.Errorf("encryption not configured: %w", err)
	}

	env := make(map[string]string)
	for _, name := range hosting.RequiredCredentialsForCombo(channel.ArtifactKind, channel.HostingPlatform.Slug) {
		cred, err := s.db.GetProjectCredential(ctx, projectID, name)
		if err != nil {
			return nil, fmt.Errorf("missing credential %s: %w", name, err)
		}
		decrypted, err := crypto.Decrypt(cred.EncryptedValue, key)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt %s: %w", name, err)
		}
		env[name] = string(decrypted)
	}
	return env, nil
}

// runChannelProdDeployment clones main and deploys it via the project's
// deployment channel. It records a hosting_deployments row for the attempt and
// writes deploy logs against it, so Mendel retains a durable record of what
// happened even when nothing surfaces the logs in the UI.
func (s *Server) runChannelProdDeployment(
	ctx context.Context,
	projectID uuid.UUID,
	channel *domain.ProjectDeploymentChannel,
) (*domain.HostingDeployment, error) {
	if channel.HostingPlatform == nil {
		return nil, fmt.Errorf("channel has no hosting platform")
	}
	if !channel.IsProdValidated() {
		return nil, fmt.Errorf("production deployment path is not validated")
	}

	project, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	appName := prodAppName(sanitizeAppName(project.Name))

	deployment := &domain.HostingDeployment{
		ProjectID: projectID,
		ChannelID: channel.ID,
		Kind:      domain.HostingDeploymentKindProd,
		AppName:   appName,
	}
	if err := s.db.CreateHostingDeployment(ctx, deployment); err != nil {
		return nil, fmt.Errorf("create deployment record: %w", err)
	}

	logAt := func(level domain.LogLevel) func(string) {
		return func(msg string) {
			fmt.Printf("[prod-deploy %s] %s\n", deployment.ID, msg)
			s.db.AppendHostingDeploymentLog(ctx, deployment.ID, level, msg)
		}
	}
	logMilestone := logAt(domain.LogLevelMilestone)
	logInfo := logAt(domain.LogLevelInfo)

	// fail records the error on the deployment row and returns it to the caller.
	fail := func(err error) (*domain.HostingDeployment, error) {
		logAt(domain.LogLevelError)(err.Error())
		s.db.FailHostingDeployment(ctx, deployment.ID, err.Error())
		return nil, err
	}

	env, err := s.deployCredentialsForChannel(ctx, projectID, channel)
	if err != nil {
		return fail(err)
	}

	// Production runs the merged code, so it needs whatever the merged
	// variations needed. Checking before the clone keeps a doomed deploy from
	// consuming a build.
	statuses, err := s.prodRequirementStatus(ctx, projectID,
		predictedDeployURL(channel.HostingPlatform.Slug, appName))
	if err != nil {
		return fail(err)
	}
	if blocking := domain.BlockingRequirements(statuses); len(blocking) > 0 {
		return fail(fmt.Errorf("production cannot run yet: %s", domain.UnmetSummary(statuses)))
	}
	appSecrets, err := s.appSecretsFor(ctx, projectID, statuses)
	if err != nil {
		return fail(err)
	}

	logMilestone("Cloning main branch...")
	tmpDir, commitSHA, err := s.cloneMainForDeploy(ctx, projectID)
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(tmpDir)
	workDir := filepath.Join(tmpDir, "repo")

	deployment.CommitSHA = &commitSHA
	if err := s.db.SetHostingDeploymentCommit(ctx, deployment.ID, commitSHA); err != nil {
		return fail(fmt.Errorf("record commit: %w", err))
	}

	logMilestone(fmt.Sprintf("Deploying %s to %s...", deployment.ShortCommit(), channel.HostingPlatform.Name))

	var url string
	switch channel.HostingPlatform.Slug {
	case "fly-io":
		url, err = s.deployToFlyIO(ctx, appName, workDir, env, appSecrets, logMilestone, logInfo)
	case "cloud-run":
		url, err = s.deployToCloudRun(ctx, appName, workDir, env, appSecrets, logMilestone, logInfo)
	case "gke":
		url, err = s.deployToGKE(ctx, appName, workDir, env, appSecrets, logMilestone, logInfo)
	default:
		return fail(fmt.Errorf("unsupported platform: %s", channel.HostingPlatform.Slug))
	}
	if err != nil {
		return fail(fmt.Errorf("deploy failed: %w", err))
	}

	teardown := teardownCommandFor(channel.HostingPlatform.Slug, appName)
	if err := s.db.CompleteHostingDeployment(ctx, deployment.ID, url, teardown); err != nil {
		return fail(fmt.Errorf("record deployment: %w", err))
	}

	logMilestone("Production deployed: " + url)

	deployment.URL = &url
	deployment.Status = domain.HostingDeploymentStatusRunning
	return deployment, nil
}

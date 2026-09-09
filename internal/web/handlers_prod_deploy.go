package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/bhs/mendelbuild/internal/hosting"
	"github.com/go-chi/chi/v5"
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
	s.addOptionalCredentials(ctx, projectID, channel, key, env)
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

	// Everything production needs, judged before a deployment row exists.
	// Before this it was two checks either side of the record: an unvalidated
	// channel refused without one, unmet requirements recorded a failed
	// deployment that had never begun. Neither is an attempt worth keeping, and
	// both now say the same thing the checklist says.
	a, obs := s.assessDeployAreaWith(ctx, domain.AreaProd, projectID, uuid.Nil)
	if !a.Available {
		return nil, fmt.Errorf("%s", declineWithChecklist(a, projectID))
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

	// The requirements the assessment judged, not a fresh read: a second lookup
	// could disagree with the gate that has just allowed this.
	appSecrets, err := s.appSecretsFor(ctx, projectID, obs.Requirements)
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
		url, err = s.deployToGKE(ctx, projectID, true, appName, workDir, env, appSecrets, logMilestone, logInfo)
	default:
		return fail(fmt.Errorf("unsupported platform: %s", channel.HostingPlatform.Slug))
	}
	// Correct but not yet reachable is neither success nor failure. Recording it
	// as running would hand over a link that refuses to connect; recording it as
	// failed would say something is wrong when nothing is.
	provisioning := errors.Is(err, errStillProvisioning)
	if err != nil && !provisioning {
		return fail(fmt.Errorf("deploy failed: %w", err))
	}

	teardown := teardownCommandFor(channel.HostingPlatform.Slug, appName, env)

	// This deploy landed on the app name the last one occupies, so the last one
	// is gone whether or not anything says so. Close its row here rather than
	// leaving two open rows claiming the same app -- which is how a redeploy
	// came to double the app's metered hosting cost for every hour afterwards.
	if err := s.db.TerminateSupersededDeployments(ctx, deployment.ID); err != nil {
		logInfo("Could not close out the superseded deployment: " + err.Error())
	}

	if provisioning {
		if err := s.db.MarkHostingDeploymentProvisioning(ctx, deployment.ID, url, teardown); err != nil {
			return fail(fmt.Errorf("record deployment: %w", err))
		}
		logMilestone("Deployed, but " + url + " is not serving yet. The load balancer is " +
			"still coming up; nothing else is needed and it will start answering on its own.")
		deployment.URL = &url
		deployment.Status = domain.HostingDeploymentStatusDeploying
		return deployment, nil
	}

	if err := s.db.CompleteHostingDeployment(ctx, deployment.ID, url, teardown); err != nil {
		return fail(fmt.Errorf("record deployment: %w", err))
	}

	logMilestone("Production deployed: " + url)

	deployment.URL = &url
	deployment.Status = domain.HostingDeploymentStatusRunning
	return deployment, nil
}

// handleTeardownProd takes a project's production deployment down.
//
// This route exists because project deletion needs it to. Retiring a project
// with production still up leaves an app serving and billing against a project
// nothing in Mendel lists any more, so that has to be a gate -- and a gate is
// only legitimate when there is a way to satisfy it. Before this there was
// none: Mendel could deploy production and had no route that stopped it, which
// is exactly why a running production deployment could only ever be a warning.
//
// It is the same teardown command demos use, stored in the same column, run by
// the same function. Only the button is new.
func (s *Server) handleTeardownProd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectID, err := uuid.Parse(chi.URLParam(r, "projectID"))
	if err != nil {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	deployment, err := s.db.GetActiveProdDeployment(ctx, projectID)
	if err != nil {
		http.Error(w, "could not read the production deployment", http.StatusInternalServerError)
		return
	}
	if deployment == nil {
		http.Redirect(w, r, "/p/"+projectID.String()+"/deployment", http.StatusSeeOther)
		return
	}

	// A deployment Mendel never learned a teardown command for is one it cannot
	// stop, and saying so is better than marking it terminated and quietly
	// leaving the app running.
	if deployment.Teardown() == "" {
		msg := "Mendel has no teardown command for " + deployment.AppName +
			", so it cannot take this deployment down. It landed before Mendel " +
			"recorded one, or the deploy did not get far enough to have one. " +
			"Remove it on " + s.platformNameFor(ctx, projectID) + " and redeploy to " +
			"give Mendel a deployment it can manage."
		s.db.NoteHostingDeploymentError(ctx, deployment.ID, msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	logAt := func(level domain.LogLevel, msg string) {
		s.db.AppendHostingDeploymentLog(ctx, deployment.ID, level, msg)
	}
	logAt(domain.LogLevelMilestone, "Tearing down "+deployment.AppName+"...")

	if err := s.runCloudTeardown(ctx, projectID, deployment); err != nil {
		// As with a demo: a teardown that failed stopped nothing, so the row
		// stays open and goes on being metered. It is still up.
		msg := fmt.Sprintf("Teardown failed: %v", err)
		logAt(domain.LogLevelError, msg)
		s.db.NoteHostingDeploymentError(ctx, deployment.ID, msg)
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}

	logAt(domain.LogLevelMilestone, "Production taken down.")
	if err := s.db.TerminateHostingDeployment(ctx, deployment.ID, ""); err != nil {
		http.Error(w, "torn down, but the record could not be updated: "+err.Error(),
			http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/p/"+projectID.String()+"/deployment", http.StatusSeeOther)
}

// platformNameFor names a project's hosting platform, for a message telling the
// reader where to go and clean up by hand. Falls back to "the hosting platform"
// rather than failing the message it is part of.
func (s *Server) platformNameFor(ctx context.Context, projectID uuid.UUID) string {
	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil || channel == nil || channel.HostingPlatform == nil {
		return "the hosting platform"
	}
	return channel.HostingPlatform.Name
}

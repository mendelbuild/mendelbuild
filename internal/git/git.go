package git

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Client handles git operations via os/exec.
type Client struct {
	workDir string
}

// NewClient creates a new git client for operations in the given directory.
func NewClient(workDir string) *Client {
	return &Client{workDir: workDir}
}

// Clone clones a repository to the work directory.
// If authToken is provided, it will be embedded in the URL for HTTPS authentication.
func (c *Client) Clone(ctx context.Context, repoURL, branch, authToken string) error {
	// Embed auth token in URL if provided (works for GitHub, GitLab, etc.)
	cloneURL := repoURL
	if authToken != "" {
		cloneURL = embedAuthToken(repoURL, authToken)
	}

	args := []string{"clone", "--branch", branch, "--single-branch", cloneURL, c.workDir}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		if isGitHubAuthError(stderrStr, repoURL) {
			return &GitHubAuthError{
				RemoteURL: repoURL,
				RawError:  stderrStr,
			}
		}
		return fmt.Errorf("git clone: %w: %s", err, stderrStr)
	}
	return nil
}

// CreateBranch creates a new branch and checks it out.
func (c *Client) CreateBranch(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", "-b", branchName)
	cmd.Dir = c.workDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git checkout -b: %w: %s", err, stderr.String())
	}
	return nil
}

// Checkout switches to an existing branch.
func (c *Client) Checkout(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", branchName)
	cmd.Dir = c.workDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git checkout: %w: %s", err, stderr.String())
	}
	return nil
}

// CommitAll stages all changes and commits with the given message.
func (c *Client) CommitAll(ctx context.Context, message string) error {
	// Stage all changes
	addCmd := exec.CommandContext(ctx, "git", "add", "-A")
	addCmd.Dir = c.workDir
	var stderr bytes.Buffer
	addCmd.Stderr = &stderr
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, stderr.String())
	}

	// Check if there are changes to commit
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = c.workDir
	output, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if len(output) == 0 {
		// Nothing to commit
		return nil
	}

	// Commit (use -c flags to set identity without requiring global git config)
	commitCmd := exec.CommandContext(ctx, "git",
		"-c", "user.email=mendel@mendel.build",
		"-c", "user.name=MendelBuild",
		"commit", "-m", message)
	commitCmd.Dir = c.workDir
	stderr.Reset()
	commitCmd.Stderr = &stderr
	if err := commitCmd.Run(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, stderr.String())
	}
	return nil
}

// Push pushes the current branch to the remote.
func (c *Client) Push(ctx context.Context, authToken string) error {
	// Get current branch name
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = c.workDir
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return fmt.Errorf("get current branch: %w", err)
	}
	branchName := strings.TrimSpace(string(branchOutput))

	// Get remote URL
	remoteCmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	remoteCmd.Dir = c.workDir
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		return fmt.Errorf("get remote url: %w", err)
	}
	remoteURL := strings.TrimSpace(string(remoteOutput))

	// If auth token provided, update remote URL temporarily
	if authToken != "" {
		authURL := embedAuthToken(remoteURL, authToken)
		setURLCmd := exec.CommandContext(ctx, "git", "remote", "set-url", "origin", authURL)
		setURLCmd.Dir = c.workDir
		if err := setURLCmd.Run(); err != nil {
			return fmt.Errorf("set remote url: %w", err)
		}
		// Restore original URL after push
		defer func() {
			restoreCmd := exec.CommandContext(context.Background(), "git", "remote", "set-url", "origin", remoteURL)
			restoreCmd.Dir = c.workDir
			restoreCmd.Run()
		}()
	}

	// Use --force because Mendel-managed branches may need to be overwritten on retry
	cmd := exec.CommandContext(ctx, "git", "push", "--force", "-u", "origin", branchName)
	cmd.Dir = c.workDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		// Detect GitHub auth failures and provide helpful message
		if isGitHubAuthError(stderrStr, remoteURL) {
			return &GitHubAuthError{
				RemoteURL: remoteURL,
				RawError:  stderrStr,
			}
		}
		return fmt.Errorf("git push: %w: %s", err, stderrStr)
	}
	return nil
}

// GetCurrentCommit returns the current commit SHA.
func (c *Client) GetCurrentCommit(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = c.workDir

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Fetch fetches remote branches.
func (c *Client) Fetch(ctx context.Context, authToken string) error {
	// Get remote URL
	remoteCmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	remoteCmd.Dir = c.workDir
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		return fmt.Errorf("get remote url: %w", err)
	}
	remoteURL := strings.TrimSpace(string(remoteOutput))

	// If auth token provided, update remote URL temporarily
	if authToken != "" {
		authURL := embedAuthToken(remoteURL, authToken)
		setURLCmd := exec.CommandContext(ctx, "git", "remote", "set-url", "origin", authURL)
		setURLCmd.Dir = c.workDir
		if err := setURLCmd.Run(); err != nil {
			return fmt.Errorf("set remote url: %w", err)
		}
		defer func() {
			restoreCmd := exec.CommandContext(context.Background(), "git", "remote", "set-url", "origin", remoteURL)
			restoreCmd.Dir = c.workDir
			restoreCmd.Run()
		}()
	}

	cmd := exec.CommandContext(ctx, "git", "fetch", "origin")
	cmd.Dir = c.workDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		if isGitHubAuthError(stderrStr, remoteURL) {
			return &GitHubAuthError{
				RemoteURL: remoteURL,
				RawError:  stderrStr,
			}
		}
		return fmt.Errorf("git fetch: %w: %s", err, stderrStr)
	}
	return nil
}

// MergeBranch merges a branch into the current branch.
func (c *Client) MergeBranch(ctx context.Context, branchName string) error {
	// Use --no-ff to create a merge commit even if fast-forward is possible
	// Use -c flags to set identity without requiring global git config
	cmd := exec.CommandContext(ctx, "git",
		"-c", "user.email=mendel@mendel.build",
		"-c", "user.name=MendelBuild",
		"merge", "--no-ff", "-m",
		fmt.Sprintf("Merge branch '%s'", branchName), branchName)
	cmd.Dir = c.workDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git merge: %w: %s", err, stderr.String())
	}
	return nil
}

// RebaseOnto rebases the current branch onto a target branch (typically main).
// Fetches the target branch first, then rebases. Returns an error if there are conflicts.
func (c *Client) RebaseOnto(ctx context.Context, targetBranch, authToken string) error {
	// Fetch the target branch first
	if err := c.FetchBranch(ctx, targetBranch, authToken); err != nil {
		return fmt.Errorf("fetch target branch: %w", err)
	}

	// Rebase onto the remote tracking branch
	remoteBranch := fmt.Sprintf("origin/%s", targetBranch)
	cmd := exec.CommandContext(ctx, "git",
		"-c", "user.email=mendel@mendel.build",
		"-c", "user.name=MendelBuild",
		"rebase", remoteBranch)
	cmd.Dir = c.workDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Abort rebase on failure to leave repo in clean state
		abortCmd := exec.CommandContext(ctx, "git", "rebase", "--abort")
		abortCmd.Dir = c.workDir
		abortCmd.Run() // Ignore abort errors

		return fmt.Errorf("git rebase: %w: %s", err, stderr.String())
	}
	return nil
}

// MergeRemoteBranch fetches a specific remote branch and merges it into the current branch.
func (c *Client) MergeRemoteBranch(ctx context.Context, branchName, authToken string) error {
	// Fetch the specific branch (needed because we clone with --single-branch)
	if err := c.FetchBranch(ctx, branchName, authToken); err != nil {
		return fmt.Errorf("fetch branch: %w", err)
	}

	// Merge the remote tracking branch
	// Use -c flags to set identity without requiring global git config
	remoteBranch := fmt.Sprintf("origin/%s", branchName)
	cmd := exec.CommandContext(ctx, "git",
		"-c", "user.email=mendel@mendel.build",
		"-c", "user.name=MendelBuild",
		"merge", "--no-ff", "-m",
		fmt.Sprintf("Merge branch '%s' [MendelBuild]", branchName), remoteBranch)
	cmd.Dir = c.workDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git merge: %w: %s", err, stderr.String())
	}
	return nil
}

// FetchBranch fetches a specific branch from origin.
func (c *Client) FetchBranch(ctx context.Context, branchName, authToken string) error {
	// Get remote URL
	remoteCmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	remoteCmd.Dir = c.workDir
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		return fmt.Errorf("get remote url: %w", err)
	}
	remoteURL := strings.TrimSpace(string(remoteOutput))

	// If auth token provided, update remote URL temporarily
	if authToken != "" {
		authURL := embedAuthToken(remoteURL, authToken)
		setURLCmd := exec.CommandContext(ctx, "git", "remote", "set-url", "origin", authURL)
		setURLCmd.Dir = c.workDir
		if err := setURLCmd.Run(); err != nil {
			return fmt.Errorf("set remote url: %w", err)
		}
		defer func() {
			restoreCmd := exec.CommandContext(context.Background(), "git", "remote", "set-url", "origin", remoteURL)
			restoreCmd.Dir = c.workDir
			restoreCmd.Run()
		}()
	}

	// Fetch the specific branch: git fetch origin <branch>:<remote-tracking-branch>
	refspec := fmt.Sprintf("%s:refs/remotes/origin/%s", branchName, branchName)
	cmd := exec.CommandContext(ctx, "git", "fetch", "origin", refspec)
	cmd.Dir = c.workDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		if isGitHubAuthError(stderrStr, remoteURL) {
			return &GitHubAuthError{
				RemoteURL: remoteURL,
				RawError:  stderrStr,
			}
		}
		return fmt.Errorf("git fetch origin %s: %w: %s", branchName, err, stderrStr)
	}
	return nil
}

// GetWorkDir returns the working directory for this client.
func (c *Client) GetWorkDir() string {
	return c.workDir
}

// DiffStats holds statistics about differences between two refs.
type DiffStats struct {
	FilesChanged int
	Additions    int
	Deletions    int
}

// parseDiffNumstat parses the output of git diff --numstat.
// Format: additions<TAB>deletions<TAB>filename (one per line)
// Binary files show "-" for additions/deletions.
func parseDiffNumstat(output string) *DiffStats {
	stats := &DiffStats{}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		stats.FilesChanged++

		// Binary files show "-" for additions/deletions
		if parts[0] != "-" {
			var add int
			fmt.Sscanf(parts[0], "%d", &add)
			stats.Additions += add
		}
		if parts[1] != "-" {
			var del int
			fmt.Sscanf(parts[1], "%d", &del)
			stats.Deletions += del
		}
	}
	return stats
}

// GetDiffStats returns diff statistics between a base ref and the current HEAD.
// Useful for comparing a variation branch against main.
func (c *Client) GetDiffStats(ctx context.Context, baseRef string) (*DiffStats, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--numstat", baseRef+"...HEAD")
	cmd.Dir = c.workDir

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w", err)
	}

	return parseDiffNumstat(string(output)), nil
}

// GetDiffStatsForBranch fetches a remote branch and returns diff stats against base.
// This is useful when you don't have the branch checked out locally.
func (c *Client) GetDiffStatsForBranch(ctx context.Context, branchName, baseRef, authToken string) (*DiffStats, error) {
	// Fetch the branch first
	if err := c.FetchBranch(ctx, branchName, authToken); err != nil {
		return nil, fmt.Errorf("fetch branch: %w", err)
	}

	// Compare base to the remote tracking branch
	remoteBranch := fmt.Sprintf("origin/%s", branchName)
	cmd := exec.CommandContext(ctx, "git", "diff", "--numstat", baseRef+"..."+remoteBranch)
	cmd.Dir = c.workDir

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w", err)
	}

	return parseDiffNumstat(string(output)), nil
}

// WorkDirForVariation returns the working directory path for a variation.
// Path structure: {MENDEL_WORK_DIR}/{projectID}/{variationID}/
func WorkDirForVariation(projectID, variationID string) string {
	baseDir := os.Getenv("MENDEL_WORK_DIR")
	if baseDir == "" {
		// Default to ~/.mendel/work for persistence (macOS clears /tmp/ periodically)
		if home, err := os.UserHomeDir(); err == nil {
			baseDir = filepath.Join(home, ".mendel", "work")
		} else {
			baseDir = "/tmp/mendel"
		}
	}
	return filepath.Join(baseDir, projectID, variationID)
}

// embedAuthToken embeds an auth token in an HTTPS URL.
// Works for GitHub, GitLab, and other git hosts that support token auth.
func embedAuthToken(repoURL, token string) string {
	u, err := url.Parse(repoURL)
	if err != nil {
		return repoURL
	}

	if u.Scheme != "https" {
		return repoURL
	}

	// GitHub fine-grained PATs (github_pat_*) and classic PATs (ghp_*) both work
	// with the x-access-token format. GitLab and others also support this.
	// Format: https://x-access-token:token@host/path
	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}

// DeleteRemoteBranch deletes a branch from the remote.
func (c *Client) DeleteRemoteBranch(ctx context.Context, branchName, authToken string) error {
	// Get remote URL
	remoteCmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	remoteCmd.Dir = c.workDir
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		return fmt.Errorf("get remote url: %w", err)
	}
	remoteURL := strings.TrimSpace(string(remoteOutput))

	// If auth token provided, update remote URL temporarily
	if authToken != "" {
		authURL := embedAuthToken(remoteURL, authToken)
		setURLCmd := exec.CommandContext(ctx, "git", "remote", "set-url", "origin", authURL)
		setURLCmd.Dir = c.workDir
		if err := setURLCmd.Run(); err != nil {
			return fmt.Errorf("set remote url: %w", err)
		}
		defer func() {
			restoreCmd := exec.CommandContext(context.Background(), "git", "remote", "set-url", "origin", remoteURL)
			restoreCmd.Dir = c.workDir
			restoreCmd.Run()
		}()
	}

	cmd := exec.CommandContext(ctx, "git", "push", "origin", "--delete", branchName)
	cmd.Dir = c.workDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("delete remote branch: %w: %s", err, stderr.String())
	}
	return nil
}

// Cleanup removes the work directory.
func (c *Client) Cleanup() error {
	return os.RemoveAll(c.workDir)
}

// GitHubAuthError represents an authentication failure with GitHub.
type GitHubAuthError struct {
	RemoteURL string
	RawError  string
}

func (e *GitHubAuthError) Error() string {
	return fmt.Sprintf("GitHub authentication failed. Your token may be expired or lack push permissions.\n\n"+
		"To fix: Update your GitHub token at https://github.com/settings/tokens\n"+
		"Then update the token in Mendel project settings.\n\n"+
		"Raw error: %s", e.RawError)
}

// isGitHubAuthError checks if a git error is a GitHub authentication failure.
func isGitHubAuthError(stderr, remoteURL string) bool {
	if !strings.Contains(remoteURL, "github.com") {
		return false
	}
	authErrorPatterns := []string{
		"Invalid username or token",
		"Authentication failed",
		"could not read Password",
		"invalid credentials",
		"Bad credentials",
	}
	for _, pattern := range authErrorPatterns {
		if strings.Contains(stderr, pattern) {
			return true
		}
	}
	return false
}

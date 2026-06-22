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
		return fmt.Errorf("git clone: %w: %s", err, stderr.String())
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

	// Commit
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
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

	cmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", branchName)
	cmd.Dir = c.workDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git push: %w: %s", err, stderr.String())
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

// GetWorkDir returns the working directory for this client.
func (c *Client) GetWorkDir() string {
	return c.workDir
}

// WorkDirForVariation returns the working directory path for a variation.
func WorkDirForVariation(variationID string) string {
	baseDir := os.Getenv("MENDEL_WORK_DIR")
	if baseDir == "" {
		baseDir = "/tmp/mendel"
	}
	return filepath.Join(baseDir, variationID)
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

	// Format: https://token@host/path
	u.User = url.User(token)
	return u.String()
}

// Cleanup removes the work directory.
func (c *Client) Cleanup() error {
	return os.RemoveAll(c.workDir)
}

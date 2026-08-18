package deploy

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DeployConfig defines how Mendel deploys and manages cloud instances.
// Loaded from .mendel/deploy-config.yml in user repositories.
type DeployConfig struct {
	Version int `yaml:"version"` // Schema version, currently 1

	Deploy DeploySettings `yaml:"deploy"` // Deployment script configuration

	Health HealthSettings `yaml:"health"` // Health check configuration

	// Credential names required for deployment. Values are stored
	// encrypted in Mendel's database and injected as env vars.
	Credentials []string `yaml:"credentials"`

	Envoy EnvoySettings `yaml:"envoy"` // Optional Envoy-specific overrides
}

type DeploySettings struct {
	// Path to deployment script, relative to repo root.
	// Script receives credentials as env vars and should output
	// MENDEL_URL=<url> on success.
	Script string `yaml:"script"`

	// Path to teardown script for cleanup on variation termination.
	TeardownScript string `yaml:"teardown_script"`

	// Working directory for scripts. Defaults to repo root.
	WorkingDir string `yaml:"working_dir,omitempty"`

	Output OutputPatterns `yaml:"output"` // Patterns to parse script output
}

type OutputPatterns struct {
	// Regex to extract internal service URL (for Envoy routing).
	// Script should print: MENDEL_URL=https://my-service.run.app
	URLPattern string `yaml:"url_pattern"`

	// Regex to extract public URL (optional, for direct access).
	PublicURLPattern string `yaml:"public_url_pattern,omitempty"`

	// Regex to extract cloud-specific instance identifier.
	// e.g., Cloud Run revision, ECS task ARN
	InstancePattern string `yaml:"instance_pattern,omitempty"`
}

type HealthSettings struct {
	// HTTP path for health checks. Defaults to "/health".
	Endpoint string `yaml:"endpoint"`

	// Seconds to wait for healthy status after deploy. Defaults to 120.
	Timeout int `yaml:"timeout"`

	// Seconds between health check attempts. Defaults to 5.
	Interval int `yaml:"interval"`
}

type EnvoySettings struct {
	// HTTP header used for consistent hashing. Defaults to "X-User-ID".
	HashHeader string `yaml:"hash_header,omitempty"`

	// Health check path for Envoy backend checks. Defaults to "/health".
	HealthPath string `yaml:"health_path,omitempty"`
}

// LoadConfig loads a deploy config from the given repo path.
func LoadConfig(repoPath string) (*DeployConfig, error) {
	configPath := filepath.Join(repoPath, ".mendel", "deploy-config.yml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read deploy-config.yml: %w", err)
	}

	var config DeployConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse deploy-config.yml: %w", err)
	}

	// Apply defaults
	config.applyDefaults()

	// Validate
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate deploy-config.yml: %w", err)
	}

	return &config, nil
}

func (c *DeployConfig) applyDefaults() {
	if c.Health.Endpoint == "" {
		c.Health.Endpoint = "/health"
	}
	if c.Health.Timeout == 0 {
		c.Health.Timeout = 120
	}
	if c.Health.Interval == 0 {
		c.Health.Interval = 5
	}
	if c.Envoy.HashHeader == "" {
		c.Envoy.HashHeader = "X-User-ID"
	}
	if c.Envoy.HealthPath == "" {
		c.Envoy.HealthPath = "/health"
	}
	if c.Deploy.Output.URLPattern == "" {
		c.Deploy.Output.URLPattern = `MENDEL_URL=(.*)`
	}
}

func (c *DeployConfig) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version: %d (expected 1)", c.Version)
	}
	if c.Deploy.Script == "" {
		return fmt.Errorf("deploy.script is required")
	}
	return nil
}

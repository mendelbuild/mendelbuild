package demo

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the .mendel/demo.yaml configuration file.
type Config struct {
	Version int `yaml:"version"`

	// Deployment type: "local" (default) or "cloud"
	Type string `yaml:"type"`

	// Command to start the service (required)
	// Variables: ${PORT}, ${VARIATION_ID}, and for cloud: output captured for ${DEPLOY_URL}
	Start string `yaml:"start"`

	// Command to stop the service (required)
	// Variables: ${PORT}, ${VARIATION_ID}, ${DEPLOY_URL} (cloud only)
	Stop string `yaml:"stop"`

	// URL to check for health (required)
	// Variables: ${PORT}, ${DEPLOY_URL}
	HealthURL string `yaml:"health_url"`

	// Seconds to wait for health check (default: 60)
	HealthTimeout int `yaml:"health_timeout"`

	// Seconds between health check attempts (default: 2)
	HealthInterval int `yaml:"health_interval"`

	// Port configuration (local mode only)
	Port PortConfig `yaml:"port"`

	// Regex to extract deployed URL from start command output (cloud mode only)
	// If empty, uses built-in fallback (first https:// URL)
	URLPattern string `yaml:"url_pattern"`

	// Commands to run before start, in order (optional)
	Setup []string `yaml:"setup"`

	// File to copy to .env before start (optional)
	EnvFile string `yaml:"env_file"`
}

// PortConfig holds port allocation settings for local mode.
type PortConfig struct {
	Default int `yaml:"default"`
	Range   int `yaml:"range"`
}

// LoadConfig reads and parses .mendel/demo.yaml from the given directory.
func LoadConfig(workDir string) (*Config, error) {
	configPath := filepath.Join(workDir, ".mendel", "demo.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("demo config not found: %s", configPath)
		}
		return nil, fmt.Errorf("read demo config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse demo config: %w", err)
	}

	// Apply defaults
	cfg.applyDefaults()

	// Validate
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid demo config: %w", err)
	}

	return &cfg, nil
}

// applyDefaults sets default values for unspecified fields.
func (c *Config) applyDefaults() {
	if c.Type == "" {
		c.Type = "local"
	}
	if c.HealthTimeout == 0 {
		c.HealthTimeout = 60
	}
	if c.HealthInterval == 0 {
		c.HealthInterval = 2
	}
	if c.Port.Default == 0 {
		c.Port.Default = 3000
	}
	if c.Port.Range == 0 {
		c.Port.Range = 100
	}
}

// validate checks that required fields are present.
func (c *Config) validate() error {
	if c.Start == "" {
		return fmt.Errorf("start command is required")
	}
	if c.Stop == "" {
		return fmt.Errorf("stop command is required")
	}
	if c.HealthURL == "" {
		return fmt.Errorf("health_url is required")
	}
	if c.Type != "local" && c.Type != "cloud" {
		return fmt.Errorf("type must be 'local' or 'cloud', got '%s'", c.Type)
	}
	return nil
}

// SubstituteVariables replaces ${VAR} placeholders in a string.
func SubstituteVariables(s string, vars map[string]string) string {
	result := s
	for k, v := range vars {
		result = strings.ReplaceAll(result, "${"+k+"}", v)
	}
	return result
}

// ExtractURL extracts a URL from command output.
// If pattern is empty, uses a built-in fallback to find the first https:// URL.
func ExtractURL(output, pattern string) string {
	var re *regexp.Regexp
	if pattern != "" {
		re = regexp.MustCompile(pattern)
	} else {
		// Built-in fallback: match first https:// URL
		re = regexp.MustCompile(`https://[^\s"'<>]+`)
	}

	match := re.FindString(output)
	return match
}

// AllocatePort returns a port based on the variation ID and port config.
// Uses a simple hash to spread variations across the port range.
func AllocatePort(variationID string, portCfg PortConfig) int {
	if len(variationID) == 0 {
		return portCfg.Default
	}

	// Simple hash: sum of bytes mod range
	var hash int
	for _, b := range []byte(variationID) {
		hash += int(b)
	}

	return portCfg.Default + (hash % portCfg.Range)
}

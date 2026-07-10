package demo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the .mendel/demo.yaml configuration file.
// This is a minimal config that works alongside .mendel/docker-compose.yml.
type Config struct {
	Version int `yaml:"version"`

	// Which docker-compose service to expose as the demo (required)
	Service string `yaml:"service"`

	// The port inside the container that the service listens on (required)
	// Mendel will map this to a host port dynamically
	ContainerPort int `yaml:"container_port"`

	// Path to health check endpoint (e.g., "/health", "/api/health")
	// Combined with allocated host port to form full health URL
	HealthPath string `yaml:"health_path"`

	// Seconds to wait for health check (default: 60)
	HealthTimeout int `yaml:"health_timeout"`

	// Seconds between health check attempts (default: 2)
	HealthInterval int `yaml:"health_interval"`

	// Commands to run after containers are up (migrations, seed data, etc.)
	// These run from the .mendel directory
	AfterUp []string `yaml:"after_up"`

	// Commands to run before docker-compose down (cleanup, etc.)
	BeforeDown []string `yaml:"before_down"`
}

// LoadConfig reads and parses .mendel/demo.yaml from the given directory.
func LoadConfig(workDir string) (*Config, error) {
	configPath := filepath.Join(workDir, ".mendel", "demo.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("demo config not found: %s - create .mendel/demo.yaml and .mendel/docker-compose.yml", configPath)
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
	if c.HealthTimeout == 0 {
		c.HealthTimeout = 60
	}
	if c.HealthInterval == 0 {
		c.HealthInterval = 2
	}
	if c.HealthPath == "" {
		c.HealthPath = "/"
	}
	if c.ContainerPort == 0 {
		c.ContainerPort = 3000
	}
}

// validate checks that required fields are present.
func (c *Config) validate() error {
	if c.Service == "" {
		return fmt.Errorf("service is required (which docker-compose service to expose)")
	}
	return nil
}

// HasDockerCompose checks if .mendel/docker-compose.yml exists.
func HasDockerCompose(workDir string) bool {
	composePath := filepath.Join(workDir, ".mendel", "docker-compose.yml")
	_, err := os.Stat(composePath)
	return err == nil
}

// DockerComposeUp runs docker-compose up in the .mendel directory.
func DockerComposeUp(workDir string) (string, error) {
	mendelDir := filepath.Join(workDir, ".mendel")
	cmd := exec.Command("docker-compose", "up", "-d", "--build", "--wait")
	cmd.Dir = mendelDir
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// DockerComposeDown runs docker-compose down in the .mendel directory.
func DockerComposeDown(workDir string, removeVolumes bool) (string, error) {
	mendelDir := filepath.Join(workDir, ".mendel")
	args := []string{"down"}
	if removeVolumes {
		args = append(args, "-v")
	}
	cmd := exec.Command("docker-compose", args...)
	cmd.Dir = mendelDir
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// GetServicePort gets the host port mapped to a service's container port.
func GetServicePort(workDir, serviceName string, containerPort int) (int, error) {
	mendelDir := filepath.Join(workDir, ".mendel")
	// docker-compose port <service> <container_port> returns "0.0.0.0:12345" or similar
	cmd := exec.Command("docker-compose", "port", serviceName, strconv.Itoa(containerPort))
	cmd.Dir = mendelDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("get service port: %w - output: %s", err, string(output))
	}

	// Parse "0.0.0.0:12345" or "[::]:12345"
	parts := strings.Split(strings.TrimSpace(string(output)), ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("unexpected port output: %s", string(output))
	}
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, fmt.Errorf("parse port: %w", err)
	}
	return port, nil
}

// RunScript runs a shell command in the .mendel directory.
func RunScript(workDir, script string) (string, error) {
	mendelDir := filepath.Join(workDir, ".mendel")
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = mendelDir
	output, err := cmd.CombinedOutput()
	return string(output), err
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

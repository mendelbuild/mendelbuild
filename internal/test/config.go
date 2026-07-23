package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the .mendel/test-config.yml configuration file.
// This defines how to run tests inside a Docker environment.
type Config struct {
	Version int `yaml:"version"`

	// Which docker-compose service to run tests in (required)
	// This service should have the test dependencies and source code
	Service string `yaml:"service"`

	// Command to run inside the service container (required)
	// e.g., "npm test", "go test ./...", "pytest"
	TestCommand string `yaml:"test_command"`

	// Seconds to wait for test services to be healthy (default: 60)
	StartupTimeout int `yaml:"startup_timeout"`
}

// LoadConfig reads and parses .mendel/test-config.yml from the given directory.
func LoadConfig(workDir string) (*Config, error) {
	configPath := filepath.Join(workDir, ".mendel", "test-config.yml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No test config is valid - tests may not need Docker
		}
		return nil, fmt.Errorf("read test config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse test config: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid test config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.StartupTimeout == 0 {
		c.StartupTimeout = 60
	}
}

func (c *Config) validate() error {
	if c.Service == "" {
		return fmt.Errorf("service is required (which container to run tests in)")
	}
	if c.TestCommand == "" {
		return fmt.Errorf("test_command is required (e.g., 'npm test')")
	}
	return nil
}

// HasTestCompose checks if .mendel/docker-compose.test.yml exists.
func HasTestCompose(workDir string) bool {
	composePath := filepath.Join(workDir, ".mendel", "docker-compose.test.yml")
	_, err := os.Stat(composePath)
	return err == nil
}

// CreateDefaultConfig creates a default test-config.yml that passes immediately.
// Used when no test configuration exists.
func CreateDefaultConfig(workDir string) error {
	mendelDir := filepath.Join(workDir, ".mendel")
	if err := os.MkdirAll(mendelDir, 0755); err != nil {
		return fmt.Errorf("create .mendel dir: %w", err)
	}

	configPath := filepath.Join(mendelDir, "test-config.yml")
	content := `# Default test configuration (auto-generated)
# Replace with actual test command when tests are added
version: 1
service: test
test_command: "echo 'No tests configured - passing by default'"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write test-config.yml: %w", err)
	}
	return nil
}

// CreateDefaultCompose creates a minimal docker-compose.test.yml that just runs a shell.
// Used when no test compose file exists.
func CreateDefaultCompose(workDir string) error {
	mendelDir := filepath.Join(workDir, ".mendel")
	if err := os.MkdirAll(mendelDir, 0755); err != nil {
		return fmt.Errorf("create .mendel dir: %w", err)
	}

	composePath := filepath.Join(mendelDir, "docker-compose.test.yml")
	content := `# Default test infrastructure (auto-generated)
# Replace with actual test services when tests are added
services:
  test:
    image: alpine:latest
    command: ["sleep", "infinity"]
`
	if err := os.WriteFile(composePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write docker-compose.test.yml: %w", err)
	}
	return nil
}

// RunTests runs the test suite inside the Docker environment.
// Returns nil if tests pass, error otherwise.
func RunTests(workDir string, cfg *Config) error {
	mendelDir := filepath.Join(workDir, ".mendel")

	// Start test infrastructure
	upCmd := exec.Command("docker-compose", "-f", "docker-compose.test.yml", "up", "-d", "--build", "--wait")
	upCmd.Dir = mendelDir
	upOutput, err := upCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker-compose up failed: %w\n%s", err, string(upOutput))
	}

	// Run tests inside the container
	// Use sh -c to handle complex commands with pipes, etc.
	execCmd := exec.Command("docker-compose", "-f", "docker-compose.test.yml", "exec", "-T", cfg.Service, "sh", "-c", cfg.TestCommand)
	execCmd.Dir = mendelDir
	testOutput, testErr := execCmd.CombinedOutput()

	// Always clean up, regardless of test result
	downCmd := exec.Command("docker-compose", "-f", "docker-compose.test.yml", "down", "-v")
	downCmd.Dir = mendelDir
	downCmd.Run() // Ignore cleanup errors

	if testErr != nil {
		return fmt.Errorf("tests failed: %w\n%s", testErr, string(testOutput))
	}

	return nil
}

// TestOutput holds the result of a test run.
type TestOutput struct {
	Passed bool
	Output string
	Error  string
}

// RunTestsWithOutput runs tests and returns detailed output.
func RunTestsWithOutput(workDir string, cfg *Config) *TestOutput {
	mendelDir := filepath.Join(workDir, ".mendel")
	result := &TestOutput{}

	// Start test infrastructure
	upCmd := exec.Command("docker-compose", "-f", "docker-compose.test.yml", "up", "-d", "--build", "--wait")
	upCmd.Dir = mendelDir
	upOutput, err := upCmd.CombinedOutput()
	if err != nil {
		result.Error = fmt.Sprintf("docker-compose up failed: %v\n%s", err, string(upOutput))
		return result
	}

	// Run tests inside the container
	execCmd := exec.Command("docker-compose", "-f", "docker-compose.test.yml", "exec", "-T", cfg.Service, "sh", "-c", cfg.TestCommand)
	execCmd.Dir = mendelDir
	testOutput, testErr := execCmd.CombinedOutput()
	result.Output = string(testOutput)

	// Always clean up
	downCmd := exec.Command("docker-compose", "-f", "docker-compose.test.yml", "down", "-v")
	downCmd.Dir = mendelDir
	downCmd.Run()

	if testErr != nil {
		result.Error = testErr.Error()
		result.Passed = false
	} else {
		result.Passed = true
	}

	return result
}

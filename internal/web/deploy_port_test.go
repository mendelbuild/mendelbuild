package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/codegen"
	"github.com/bhs/mendelbuild/internal/hosting"
)

// The port Mendel routes to and the port it tells the app to listen on must be
// the same number. They were not: fly.toml declared internal_port 8080 while
// nothing set PORT, so a generated app that defaulted to 3000 started cleanly
// and was unreachable, which Fly reported as "instance refused connection".
//
// The two halves live in different files and neither reads the other, which is
// how they drifted. This test is what holds them together.
func TestFlyTomlRoutesToThePortTheAppIsTold(t *testing.T) {
	toml := flyTomlFor("pong-game-0e30d7df")

	wantPort := fmt.Sprintf(`internal_port = %d`, hosting.ContainerPort)
	if !strings.Contains(toml, wantPort) {
		t.Errorf("fly.toml should route to the container port:\nwant %q\nin:\n%s", wantPort, toml)
	}

	wantEnv := fmt.Sprintf(`PORT = "%d"`, hosting.ContainerPort)
	if !strings.Contains(toml, wantEnv) {
		t.Errorf("fly.toml must tell the app where to listen:\nwant %q\nin:\n%s", wantEnv, toml)
	}

	if !strings.Contains(toml, `app = "pong-game-0e30d7df"`) {
		t.Error("fly.toml should carry the Mendel-controlled app name")
	}
}

func TestK8sManifestRoutesToThePortTheAppIsTold(t *testing.T) {
	manifest := k8sManifestFor("pong-game", "gcr.io/x/pong-game:latest", "")

	for _, want := range []string{
		fmt.Sprintf("containerPort: %d", hosting.ContainerPort),
		fmt.Sprintf("targetPort: %d", hosting.ContainerPort),
		fmt.Sprintf(`value: "%d"`, hosting.ContainerPort), // the PORT env var
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("k8s manifest missing %q:\n%s", want, manifest)
		}
	}

	// The Secret carrying the app's required values is wired in where the
	// container spec expects it, not appended after the document separator.
	withSecret := k8sManifestFor("pong-game", "img", "\n        envFrom:\n        - secretRef:\n            name: pong-game-env")
	envFromAt := strings.Index(withSecret, "envFrom:")
	separatorAt := strings.Index(withSecret, "---")
	if envFromAt < 0 || envFromAt > separatorAt {
		t.Error("envFrom must land inside the Deployment, before the Service document")
	}
}

// Code generation is told which port to listen on, and the deployers route to
// it. A prompt naming a different port is how the app came to listen on 3000
// while Fly waited on 8080.
func TestCodegenPromptNamesTheDeployedPort(t *testing.T) {
	prompt := codegen.BuildImplementationPrompt("email-password-auth", "google-oauth", "Wire up Google sign-in.", "container")

	if !strings.Contains(prompt, "PORT") {
		t.Error("the container prompt must tell the app to read PORT")
	}
	if !strings.Contains(prompt, fmt.Sprintf("%d", hosting.ContainerPort)) {
		t.Errorf("the container prompt should name the port deployments set (%d)", hosting.ContainerPort)
	}
	// The old prompt offered a choice; the deployer does not.
	if strings.Contains(prompt, "typically 8080 or 3000") {
		t.Error("the prompt must not offer a port the deployer will not route to")
	}
}

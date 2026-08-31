package web

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestGKEDeployRoundTrip runs the real deploy path against a real GKE cluster:
// build, apply, roll out, reach the app over its LoadBalancer, then tear it
// down and confirm nothing is left behind.
//
// Opt-in, because it needs a cluster, a service account and several minutes.
// Set MENDEL_GKE_INTEGRATION=1 along with the four credentials the gke channel
// requires; GCP_SERVICE_ACCOUNT_KEY may be the key itself or a path to it.
//
// This exists because the gke channel shipped without ever having run. Every
// stage of it was broken in a way only execution would reveal, so the guard
// against that recurring is a test that actually executes it.
func TestGKEDeployRoundTrip(t *testing.T) {
	if os.Getenv("MENDEL_GKE_INTEGRATION") != "1" {
		t.Skip("set MENDEL_GKE_INTEGRATION=1 to run the GKE round trip")
	}

	workDir := os.Getenv("MENDEL_GKE_TEST_WORKDIR")
	if workDir == "" {
		t.Fatal("set MENDEL_GKE_TEST_WORKDIR to a checkout with a Dockerfile")
	}

	env := map[string]string{}
	for _, name := range []string{"GCP_PROJECT_ID", "GCP_SERVICE_ACCOUNT_KEY", "GKE_CLUSTER_NAME", "GKE_ZONE"} {
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("%s is not set", name)
		}
		env[name] = value
	}
	// Accept a path so the key never has to be pasted into a shell history.
	if key, err := os.ReadFile(env["GCP_SERVICE_ACCOUNT_KEY"]); err == nil {
		env["GCP_SERVICE_ACCOUNT_KEY"] = string(key)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	deploymentName := "mendel-itest-" + strings.ToLower(time.Now().Format("150405"))
	logf := func(msg string) { t.Log(msg) }

	// A non-empty secret map exercises the Secret and envFrom wiring, which is
	// how a variation's required values reach the container.
	appSecrets := map[string]string{"MENDEL_ITEST_MARKER": "present"}

	srv := &Server{}
	teardown := teardownCommandFor("gke", deploymentName)
	t.Cleanup(func() {
		session, err := newGKESession(context.Background(), env)
		if err != nil {
			t.Errorf("cleanup session: %v", err)
			return
		}
		defer session.cleanup()
		cmd := exec.Command("sh", "-c", teardown)
		cmd.Env = session.env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("teardown failed: %s: %v", strings.TrimSpace(string(out)), err)
		}
	})

	url, err := srv.deployToGKE(ctx, deploymentName, workDir, env, appSecrets, logf, logf)
	if err != nil {
		t.Fatalf("deployToGKE: %v", err)
	}
	t.Logf("deployed to %s", url)

	// The LoadBalancer answers before its backends finish health checking, so
	// a single request here would be flaky for reasons that are not the code's.
	var lastErr error
	for i := 0; i < 30; i++ {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				lastErr = nil
				break
			}
			lastErr = errStatus(resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Second)
	}
	if lastErr != nil {
		t.Fatalf("deployed app never became reachable at %s: %v", url, lastErr)
	}
}

type errStatus int

func (e errStatus) Error() string { return "unexpected status " + http.StatusText(int(e)) }

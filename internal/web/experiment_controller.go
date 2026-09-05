package web

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// Installing the gateway controller experiments need.
//
// GKE's managed controller matches headers Exact only, so it cannot pick one
// cookie out of the several a visitor carries. Mendel keeps it at the edge,
// where it holds the address and the certificate, and installs one behind it
// that can match.

// EnvoyGatewayVersion is pinned deliberately.
//
// A controller that changed under a running experiment could change how traffic
// is routed mid-comparison, so the version moves when someone decides it should
// and not when a release happens. Bump it here, and read the release notes
// first.
const EnvoyGatewayVersion = "v1.9.1"

func envoyGatewayManifestURL() string {
	return "https://github.com/envoyproxy/gateway/releases/download/" +
		EnvoyGatewayVersion + "/install.yaml"
}

// installControllerCommand is what an administrator runs when Mendel may not.
func installControllerCommand() string {
	// --server-side so re-running is an apply rather than a conflict, which
	// matters because re-running is the normal case.
	return "kubectl apply --server-side -f " + envoyGatewayManifestURL()
}

// canInstallController asks the cluster whether Mendel's credentials may do it.
//
// Asking is exact where inferring is not: a service account's rights are the
// union of its IAM role and any RBAC bound to it, so a role name says nothing
// definite. Envoy Gateway brings custom resource definitions and cluster roles,
// which are the two most privileged things in its manifest -- if those are
// permitted the rest follows, and if they are not the install fails partway,
// which is worse than not starting.
func canInstallController(ctx context.Context, session *gkeSession) domain.Fact {
	for _, resource := range []string{"customresourcedefinitions", "clusterroles"} {
		cmd := exec.CommandContext(ctx, "kubectl", "auth", "can-i", "create", resource)
		cmd.Env = session.env
		// Output rather than CombinedOutput: for a cluster-scoped resource
		// kubectl writes "Warning: resource ... is not namespace scoped" to
		// stderr, and folding that in with the answer means the answer matches
		// neither yes nor no. The verdict is on stdout alone.
		out, err := cmd.Output()

		switch interpretCanI(string(out), err) {
		case domain.FactTrue:
			continue
		case domain.FactFalse:
			return domain.FactFalse
		default:
			return domain.FactUnknown
		}
	}
	return domain.FactTrue
}

// interpretCanI reads kubectl's verdict.
//
// `can-i` answers on stdout and exits non-zero for "no", so a non-zero exit is
// an answer rather than a failure -- and anything that is neither word is a
// failure to ask, which must not be read as permission either way.
func interpretCanI(stdout string, err error) domain.Fact {
	lines := strings.Fields(strings.TrimSpace(stdout))
	if len(lines) == 0 {
		return domain.FactUnknown
	}
	switch strings.ToLower(lines[len(lines)-1]) {
	case "yes":
		return domain.FactTrue
	case "no":
		return domain.FactFalse
	}
	if err != nil {
		return domain.FactUnknown
	}
	return domain.FactUnknown
}

// installExperimentController installs Envoy Gateway and waits for its
// GatewayClass to be accepted.
//
// Waiting is the point. An apply that returns cleanly means the objects are
// stored, not that a controller is running and has claimed its class -- and this
// whole area's recurring failure is treating the first as the second.
func (s *Server) installExperimentController(ctx context.Context, projectID uuid.UUID,
	logInfo func(string)) error {

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil || channel == nil || channel.HostingPlatform == nil ||
		channel.ArtifactKind != domain.DeployArtifactKubernetes {
		return fmt.Errorf("this project has no Kubernetes channel to install into")
	}
	env, err := s.deployCredentialsForChannel(ctx, projectID, channel)
	if err != nil {
		return fmt.Errorf("the channel's credentials are not available: %w", err)
	}

	session, err := newGKESession(ctx, env)
	if err != nil {
		return fmt.Errorf("could not reach the cluster: %w", err)
	}
	defer session.cleanup()

	if allowed := canInstallController(ctx, session); allowed == domain.FactFalse {
		return fmt.Errorf("these credentials may not create custom resource definitions or "+
			"cluster roles, so Mendel cannot install the controller. An administrator can run:\n  %s",
			installControllerCommand())
	}

	logInfo("Installing Envoy Gateway " + EnvoyGatewayVersion)
	apply := exec.CommandContext(ctx, "kubectl", "apply", "--server-side",
		"-f", envoyGatewayManifestURL())
	apply.Env = session.env
	if out, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("installing the controller failed: %s: %w",
			strings.TrimSpace(string(out)), err)
	}

	logInfo("Waiting for the controller to claim its gateway class")
	if err := waitForGatewayClass(ctx, session, ExperimentGatewayClass, logInfo); err != nil {
		return err
	}

	logInfo("Envoy Gateway is installed and can match cookies")
	s.invalidateExperimentObservation(projectID)
	return nil
}

// waitForGatewayClass blocks until the class reports Accepted.
func waitForGatewayClass(ctx context.Context, session *gkeSession, class string,
	logInfo func(string)) error {

	deadline := time.Now().Add(3 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		cmd := exec.CommandContext(ctx, "kubectl", "get", "gatewayclass", class,
			"-o", "jsonpath={.status.conditions[?(@.type=='Accepted')].status}")
		cmd.Env = session.env
		out, err := cmd.CombinedOutput()
		last = strings.TrimSpace(string(out))
		if err == nil && strings.EqualFold(last, "True") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("the controller was applied but %s never reported Accepted (last saw %q); "+
		"the objects exist and nothing is serving them", class, last)
}

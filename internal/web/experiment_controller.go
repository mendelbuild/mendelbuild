package web

import (
	"context"
	"errors"
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
//
// Two lines rather than one, and both matter. The first fetches credentials,
// because the place this is most likely to be run is a fresh Cloud Shell with no
// kubeconfig at all. The second carries --context, because a bare kubectl
// installs into whatever cluster the reader's terminal points at -- which for
// anyone running Mendel is quite likely Mendel's own, and a command that does
// the right thing on the author's machine and the wrong thing on the reader's is
// worse than no command.
func installControllerCommand(env map[string]string) string {
	apply := "kubectl apply --server-side -f " + envoyGatewayManifestURL()

	project, cluster, zone := env["GCP_PROJECT_ID"], env["GKE_CLUSTER_NAME"], env["GKE_ZONE"]
	if project == "" || cluster == "" || zone == "" {
		return apply
	}
	return fmt.Sprintf(
		"gcloud container clusters get-credentials %s --location %s --project %s\n"+
			"kubectl --context gke_%s_%s_%s apply --server-side -f %s",
		cluster, zone, project,
		project, zone, cluster, envoyGatewayManifestURL())
}

// cloudShellURL opens a terminal in the browser, signed in as whoever follows
// the link, with gcloud and kubectl already installed.
//
// The point is that this step requires no software on anybody's laptop. A person
// who has to learn about Homebrew before their first experiment will not have a
// first experiment, and asking someone non-technical to install a Kubernetes CLI
// is a good way to lose them at exactly the wrong moment.
//
// Deliberately not an "Open in Cloud Shell" deep link. That form requires
// cloudshell_git_repo, and a repository Google has not allow-listed opens a
// temporary environment *without the user's credentials* -- so the one thing the
// link exists to make easy is the thing it would break. The command is copied
// instead, which is why it carries its own get-credentials line.
func cloudShellURL(env map[string]string) string {
	if project := env["GCP_PROJECT_ID"]; project != "" {
		return "https://shell.cloud.google.com/?show=terminal&project=" + project
	}
	return "https://shell.cloud.google.com/?show=terminal"
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
	verdict, _ := canInstallControllerWhy(ctx, session)
	return verdict
}

// installNeeds are the privileged resources the manifest touches, with the verb
// it touches them with.
//
// patch, not create. `apply --server-side` is a PATCH whatever the object's
// current state, so an account permitted to create and not to patch fails on the
// very first apply -- which is exactly what GKE's container.developer allows,
// and exactly what this probe missed when it asked about create.
var installNeeds = []string{
	"customresourcedefinitions",
	"clusterroles",
	"clusterrolebindings",
	"roles",
	"rolebindings",
	"mutatingwebhookconfigurations",
}

// canInstallControllerWhy also returns what went wrong when the answer is
// unknown, so an inconclusive probe leaves a trail instead of a shrug.
func canInstallControllerWhy(ctx context.Context, session *gkeSession) (domain.Fact, string) {
	for _, resource := range installNeeds {
		cmd := exec.CommandContext(ctx, "kubectl", "auth", "can-i", "patch", resource)
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
			return domain.FactFalse, ""
		default:
			why := fmt.Sprintf("asking whether %s may be patched gave %q", resource, strings.TrimSpace(string(out)))
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				why += ": " + strings.TrimSpace(string(exit.Stderr))
			} else if err != nil {
				why += ": " + err.Error()
			}
			return domain.FactUnknown, why
		}
	}
	return domain.FactTrue, ""
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

	// Only a definite no stops this. If Mendel cannot tell whether it is
	// allowed, the informative thing is to try: the API server's refusal names
	// the exact permission that is missing, where "could not tell" names
	// nothing and leaves the user to run a command Mendel could have run.
	if canInstallController(ctx, session) == domain.FactFalse {
		return fmt.Errorf("these credentials may not modify cluster roles and webhooks, so "+
			"Mendel cannot install the controller. An administrator can run:\n  %s",
			installControllerCommand(env))
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

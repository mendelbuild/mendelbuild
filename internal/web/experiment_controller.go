package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
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

// gatewayAPICRD matches a Gateway API custom resource definition, as opposed to
// Envoy Gateway's own.
var gatewayAPICRD = regexp.MustCompile(`(?m)^\s*name:\s*[a-z]+\.gateway\.networking\.k8s\.io\s*$`)

// installableManifest is the controller's manifest with the Gateway API custom
// resource definitions removed.
//
// Envoy Gateway bundles its own copy of all ten, and on GKE they are already
// installed and managed by the platform. Applying them over the top fails twice
// over: server-side apply reports a field-manager conflict with
// kube-addon-manager, and Autopilot's enforce-gateway-standard-channel policy
// refuses the experimental-channel ones outright -- grpcroutes, listenersets,
// tcproutes, udproutes.
//
// Removing them is not a workaround for the policy, it is the correct thing to
// install: the cluster's own definitions are the ones every controller on it
// must agree about, and a second copy is what the conflict is warning about.
// Envoy Gateway's own definitions -- the gateway.envoyproxy.io and
// gateway.networking.x-k8s.io groups -- are kept, since nothing else provides
// them.
func installableManifest(raw string) string {
	var keep []string
	for _, doc := range strings.Split(raw, "\n---") {
		if strings.Contains(doc, "kind: CustomResourceDefinition") && gatewayAPICRD.MatchString(doc) {
			continue
		}
		if strings.TrimSpace(doc) == "" {
			continue
		}
		keep = append(keep, doc)
	}
	return strings.Join(keep, "\n---") + "\n"
}

// gatewayClassManifest points a GatewayClass at the controller.
//
// install.yaml contains no GatewayClass at all -- Envoy Gateway leaves that to
// whoever installs it, and its quickstart creates one separately. Waiting for a
// class the manifest never creates would have waited forever, on an install that
// had entirely succeeded.
func gatewayClassManifest() string {
	return fmt.Sprintf(`apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: %s
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
`, ExperimentGatewayClass)
}

// canInstallController asks the cluster by attempting the thing.
//
// `apply --server-side --dry-run=server` sends the real objects through the real
// admission chain -- authentication, authorization, admission webhooks and
// policies -- and persists nothing. The exit code is the answer.
//
// This replaced a `kubectl auth can-i` probe over a hand-written list of
// resources and verbs, which was a *model* of what the manifest does, and was
// wrong three ways at once. It asked about create where a server-side apply
// patches. It listed six resources where the manifest touches more. And it could
// only ever see RBAC -- the thing that actually blocked this install was an
// Autopilot ValidatingAdmissionPolicy, which no permission question would have
// revealed.
//
// The dry run has no model. It is the operation.
func canInstallController(ctx context.Context, session *gkeSession, manifest string) (domain.Fact, string) {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "--dry-run=server", "-f", "-")
	cmd.Env = session.env
	cmd.Stdin = strings.NewReader(manifest)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return domain.FactTrue, ""
	}

	// Forbidden is the cluster saying no, whether the source is RBAC or a
	// policy. Anything else is a failure to ask -- a network error, a manifest
	// that could not be fetched -- and must not be reported as a refusal.
	if strings.Contains(string(out), "forbidden") || strings.Contains(string(out), "Forbidden") {
		return domain.FactFalse, strings.TrimSpace(string(out))
	}
	return domain.FactUnknown, strings.TrimSpace(string(out))
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

	// Only a definite refusal stops this. Where the dry run cannot tell -- a
	// network failure, a manifest that would not download -- the informative
	// thing is to try, because the real apply's error names the cause where
	// "could not tell" names nothing.
	manifest, err := fetchInstallManifest(ctx)
	if err != nil {
		return err
	}

	if verdict, why := canInstallController(ctx, session, manifest); verdict == domain.FactFalse {
		return fmt.Errorf("the cluster refused this install:\n%s\n\nSomeone with administrator "+
			"access can run:\n  %s", why, installControllerCommand(env))
	}

	logInfo("Installing Envoy Gateway " + EnvoyGatewayVersion)
	apply := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", "-")
	apply.Env = session.env
	apply.Stdin = strings.NewReader(manifest)
	if out, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("installing the controller failed: %s: %w",
			strings.TrimSpace(string(out)), err)
	}

	// The manifest creates no GatewayClass, so nothing yet points at the
	// controller that was just installed.
	logInfo("Pointing a gateway class at the controller")
	class := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", "-")
	class.Env = session.env
	class.Stdin = strings.NewReader(gatewayClassManifest())
	if out, err := class.CombinedOutput(); err != nil {
		return fmt.Errorf("could not create the gateway class: %s: %w",
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


// fetchInstallManifest downloads the controller manifest and removes what the
// platform already owns.
//
// Fetched here rather than handed to kubectl as a URL, because it has to be
// filtered before it is applied and dry-run against exactly what will be
// applied. A probe of a different manifest than the one that runs is not a probe.
func fetchInstallManifest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, envoyGatewayManifestURL(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not fetch the controller manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the controller manifest returned %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", fmt.Errorf("could not read the controller manifest: %w", err)
	}
	return installableManifest(string(raw)), nil
}

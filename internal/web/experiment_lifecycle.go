package web

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
	"github.com/bhs/mendelbuild/internal/hosting"
)

// Starting and stopping an experiment.
//
// The delicate part is the production hostname, which one route already serves.
// Adding a second route for the same host would not take effect: Gateway API
// ranks matches by path specificity, then method, then header count, and breaks
// the remaining tie by the older route -- and the existing production route is
// always older. Two routes would look applied and change nothing.
//
// So the experiment does not add a route at the edge. It repoints the one that
// is already there at the gateway that can match cookies, and points it back
// when the experiment stops. One object, no precedence to reason about, and a
// stop that restores exactly what was there.

// StartExperiment builds each Arm, applies the routing, and takes traffic.
func (s *Server) StartExperiment(ctx context.Context, experimentID uuid.UUID,
	logMilestone, logInfo func(string)) error {

	exp, err := s.db.GetExperiment(ctx, experimentID)
	if err != nil || exp == nil {
		return fmt.Errorf("no such experiment")
	}
	if msg := exp.NotReadyToStart(); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	if msg := domain.ValidateAllocation(exp.Arms); msg != "" {
		return fmt.Errorf("%s", msg)
	}

	// Recorded before the work begins, because the work takes minutes and a page
	// that cannot say so can only show what was true before the button was
	// pressed.
	if err := s.db.SetExperimentStatus(ctx, exp.ID, domain.ExperimentStarting); err != nil {
		return err
	}

	pd, err := s.db.GetProjectDomain(ctx, exp.ProjectID)
	if err != nil || pd == nil || pd.ProdHost() == "" {
		return fmt.Errorf("production has no hostname, so there is no route to attach arms to")
	}

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, exp.ProjectID)
	if err != nil || channel == nil {
		return fmt.Errorf("this project has no deployment channel")
	}
	env, err := s.deployCredentialsForChannel(ctx, exp.ProjectID, channel)
	if err != nil {
		return fmt.Errorf("the channel's credentials are not available: %w", err)
	}

	session, err := newGKESession(ctx, env)
	if err != nil {
		return err
	}
	defer session.cleanup()
	if err := session.ensureNamespace(ctx); err != nil {
		return err
	}

	// The route the ordinary production deploy created, named for the app. The
	// experiment repoints it rather than competing with it.
	prodRouteName, err := s.prodRouteName(ctx, exp.ProjectID)
	if err != nil {
		return err
	}

	deployment := ExperimentDeployment{
		Name:     experimentResourceName(exp),
		Hostname: pd.ProdHost(),
		// Only where production actually serves https. A cookie marked Secure on
		// an http site is never sent back, so every request would look
		// unassigned and nobody would stay in an Arm.
		Secure: pd.CertificateCovers(pd.ProdHost()),
	}

	for _, arm := range exp.Arms {
		if arm.IsMainline() {
			// Mainline keeps the Deployment it already has. Rebuilding it would
			// make the control a new thing, and the comparison would be against
			// something nobody had been running.
			deployment.Arms = append(deployment.Arms, ArmDeployment{
				Slug:    arm.Slug,
				Backend: prodRouteName,
				Weight:  arm.AllocationWeight,
			})
			continue
		}

		image, commit, err := s.buildArmImage(ctx, exp, arm, session, false, logInfo)
		if err != nil {
			return fmt.Errorf("building arm %s: %w", arm.Slug, err)
		}
		logMilestone("Built " + arm.Slug + " from " + short(commit))

		envFrom, err := s.applyArmEnvironment(ctx, exp, arm, pd.ProdHost(), session, logInfo)
		if err != nil {
			return fmt.Errorf("giving arm %s its environment: %w", arm.Slug, err)
		}

		deployment.Arms = append(deployment.Arms, ArmDeployment{
			Slug: arm.Slug, Image: image, Weight: arm.AllocationWeight, EnvFrom: envFrom,
		})
	}

	manifest, err := deployment.Manifest()
	if err != nil {
		return err
	}

	logMilestone("Applying the experiment's routing")
	if err := session.applyManifest(ctx, manifest); err != nil {
		return err
	}

	// Last, because until this happens nothing an experiment applied is serving
	// anybody -- which makes every step before it safely repeatable.
	// Envoy names the proxy it provisions after the Gateway plus a hash, so the
	// name cannot be written in advance -- it is discovered from the cluster that
	// just created it.
	proxy, err := session.experimentProxyService(ctx)
	if err != nil {
		return err
	}

	// Without this the edge gateway marks the proxy unhealthy and serves 503.
	// Its default health check probes the backend on the traffic port with no
	// Host header, and Envoy answers 404 to a request matching no route -- which
	// is correct of Envoy and fatal here. Envoy has a readiness endpoint on a
	// port of its own; the check is pointed at that instead.
	logMilestone("Teaching the edge gateway how to health check the proxy")
	if err := session.healthCheckProxy(ctx, proxy, experimentResourceName(exp)); err != nil {
		return err
	}

	logMilestone("Pointing production traffic at the experiment")
	if err := session.repointProdRoute(ctx, prodRouteName, proxy, ExperimentProxyNamespace); err != nil {
		return err
	}

	if err := s.db.SetExperimentStatus(ctx, exp.ID, domain.ExperimentRunning); err != nil {
		return err
	}
	s.recordExperimentEvent(ctx, exp.ID, domain.EventAllocationChanged, "Experiment started")
	return nil
}

// StopExperiment returns every visitor to mainline and removes what was built.
//
// The route goes back first. Until it does, traffic is still being split, and an
// experiment being stopped is usually being stopped for a reason -- so the
// fastest possible thing happens first and the tidying afterwards.
func (s *Server) StopExperiment(ctx context.Context, experimentID uuid.UUID,
	reason string, logInfo func(string)) error {

	exp, err := s.db.GetExperiment(ctx, experimentID)
	if err != nil || exp == nil {
		return fmt.Errorf("no such experiment")
	}

	if err := s.db.SetExperimentStatus(ctx, exp.ID, domain.ExperimentStopping); err != nil {
		return err
	}

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, exp.ProjectID)
	if err != nil || channel == nil {
		return fmt.Errorf("this project has no deployment channel")
	}
	env, err := s.deployCredentialsForChannel(ctx, exp.ProjectID, channel)
	if err != nil {
		return err
	}
	session, err := newGKESession(ctx, env)
	if err != nil {
		return err
	}
	defer session.cleanup()

	prod, err := s.prodRouteName(ctx, exp.ProjectID)
	if err != nil {
		return err
	}
	logInfo("Returning production traffic to mainline")
	if err := session.repointProdRoute(ctx, prod, prod, ""); err != nil {
		return err
	}

	// Everything the experiment created carries its label, so teardown finds
	// what it made without knowing what each object is.
	//
	// Two namespaces and two sets of kinds, because an experiment reaches into
	// the controller's namespace: the grant that lets the production route cross
	// the boundary, and the health check that keeps the load balancer believing
	// the proxy is up. Deleting only from mendel-apps left both behind on every
	// stop, and they accumulated.
	logInfo("Removing the experiment's resources")
	name := experimentResourceName(exp)
	for _, scope := range []struct{ namespace, kinds string }{
		{hosting.Namespace, "deployment,service,httproute,gateway,secret"},
		{ExperimentProxyNamespace, "referencegrant,healthcheckpolicy"},
	} {
		del := exec.CommandContext(ctx, "kubectl", "delete", scope.kinds,
			"-n", scope.namespace, "-l", "mendel-experiment="+name, "--ignore-not-found")
		del.Env = session.env
		if out, err := del.CombinedOutput(); err != nil {
			logInfo("Some resources were left behind in " + scope.namespace + ": " +
				strings.TrimSpace(string(out)))
		}
	}

	if err := s.db.SetExperimentStatus(ctx, exp.ID, domain.ExperimentStopped); err != nil {
		return err
	}
	s.recordExperimentEvent(ctx, exp.ID, domain.EventKillSwitchPulled, reason)
	return nil
}

// applyArmEnvironment gives an Arm the same environment production has, plus
// whatever its own Variation declared, and returns the fragment its pod spec
// needs to read it.
//
// An Arm with no environment is not a comparison. Both Arms of the first live
// experiment logged "Google OAuth: NOT configured" because production's secrets
// never reached them, so they differed from mainline in a way nobody chose and
// sign-in did not work on either -- which would have confounded any result the
// experiment produced. It also makes per-user assignment impossible, since that
// needs somebody to be able to log in.
//
// The union, not one or the other. An Arm runs its Variation's branch, which is
// mainline's code plus a change, so it needs everything production needs; and a
// Variation that introduced a new requirement needs that too. Where both declare
// the same name they are the same project-scoped value, so the union is not
// ambiguous.
func (s *Server) applyArmEnvironment(ctx context.Context, exp *domain.Experiment,
	arm domain.ExperimentArm, prodHost string, session *gkeSession,
	logInfo func(string)) (string, error) {

	deployURL := prodHost
	if deployURL != "" && !strings.HasPrefix(deployURL, "http") {
		deployURL = "https://" + deployURL
	}

	merged, err := s.prodRequirementStatus(ctx, exp.ProjectID, deployURL)
	if err != nil {
		return "", err
	}
	own, err := s.variationRequirementStatus(ctx, exp.ProjectID, *arm.VariationID, deployURL)
	if err != nil {
		return "", err
	}

	values, err := s.appSecretsFor(ctx, exp.ProjectID, append(merged, own...))
	if err != nil {
		return "", err
	}
	if len(values) == 0 {
		// Not a failure, and not silence either: an Arm that needs nothing and
		// an Arm whose values are missing look identical in the cluster, and the
		// log is the only place the difference is visible.
		logInfo("Arm " + arm.Slug + " needs no configured values")
		return "", nil
	}

	logInfo(fmt.Sprintf("Giving arm %s the %d value(s) production runs with", arm.Slug, len(values)))
	return session.applyEnvSecret(ctx, experimentArmResource(exp, arm)+"-env", values,
		map[string]string{"mendel-experiment": experimentResourceName(exp)})
}

// armBranch names the branch an Arm's Variation lives on.
//
// Derived the same way code generation derives it, because the two must agree:
// an Arm built from a branch nobody is writing to would be permanently, silently
// current.
func (s *Server) armBranch(ctx context.Context, exp *domain.Experiment,
	arm domain.ExperimentArm) (string, error) {

	if arm.VariationID == nil {
		return "", fmt.Errorf("mainline has no branch of its own")
	}
	variation, err := s.db.GetVariation(ctx, *arm.VariationID)
	if err != nil || variation == nil {
		return "", fmt.Errorf("variation not found")
	}
	hop, err := s.db.GetHop(ctx, exp.HopID)
	if err != nil || hop == nil {
		return "", fmt.Errorf("hop not found")
	}
	return fmt.Sprintf("mendel/%s/%s",
		sanitizeBranchName(hop.Name), sanitizeBranchName(variation.Name)), nil
}

// repoAccess is what reaching a project's repository takes.
type repoAccess struct {
	URL        string
	AuthToken  string
	MainBranch string
}

func (s *Server) repoAccessFor(ctx context.Context, projectID uuid.UUID) (repoAccess, error) {
	repo, err := s.db.GetRepositoryByProject(ctx, projectID)
	if err != nil || repo == nil || repo.URL == nil {
		return repoAccess{}, fmt.Errorf("this project has no repository URL")
	}
	var cfg struct {
		MainBranch string `json:"main_branch"`
		AuthToken  string `json:"auth_token"`
	}
	if repo.Config != nil {
		json.Unmarshal(repo.Config, &cfg)
	}
	if cfg.MainBranch == "" {
		cfg.MainBranch = "main"
	}
	return repoAccess{URL: *repo.URL, AuthToken: cfg.AuthToken, MainBranch: cfg.MainBranch}, nil
}

// buildArmImage checks out the Variation's branch, optionally rebases it onto
// main, and builds it -- recording what it was built from.
//
// The commit is recorded after the build succeeds, never before. A row written
// up front and left in place when the build failed would have the Arm claiming
// to serve code that was never produced, which is the same shape as reporting an
// action as the state of the world.
func (s *Server) buildArmImage(ctx context.Context, exp *domain.Experiment,
	arm domain.ExperimentArm, session *gkeSession, rebase bool,
	logInfo func(string)) (image, commit string, err error) {

	branch, err := s.armBranch(ctx, exp, arm)
	if err != nil {
		return "", "", err
	}
	access, err := s.repoAccessFor(ctx, exp.ProjectID)
	if err != nil {
		return "", "", err
	}

	workDir, err := os.MkdirTemp("", "mendel-arm-*")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(workDir)

	logInfo("Cloning " + branch)
	client := git.NewClient(workDir)
	if err := client.Clone(ctx, access.URL, branch, access.AuthToken); err != nil {
		return "", "", fmt.Errorf("could not clone %s: %w", branch, err)
	}

	if rebase {
		// Onto main, so an Arm is compared against a mainline that has moved
		// rather than against the one it was branched from. A conflict aborts
		// the rebase and leaves the branch as it was -- the build does not
		// continue from a half-resolved tree.
		logInfo("Rebasing " + branch + " onto " + access.MainBranch)
		if err := client.RebaseOnto(ctx, access.MainBranch, access.AuthToken); err != nil {
			return "", "", fmt.Errorf("could not rebase %s onto %s, so it was left as it was: %w",
				branch, access.MainBranch, err)
		}
		// Pushed, so the branch a later staleness check reads is the branch that
		// was built. Without this the rebase would live only in a temporary
		// directory and every subsequent check would report the Arm stale
		// against the un-rebased head it had just moved past.
		if err := client.Push(ctx, access.AuthToken); err != nil {
			return "", "", fmt.Errorf("rebased %s but could not push it: %w", branch, err)
		}
	}

	commit, err = client.GetCurrentCommit(ctx)
	if err != nil {
		// Not fatal to the build, and not quietly zero either. An empty commit
		// reads as "never built" everywhere else, which would be a lie about an
		// image that exists -- so this is the one case that refuses rather than
		// records something untrue.
		return "", "", fmt.Errorf("built nothing: could not read the commit of %s: %w", branch, err)
	}

	image, err = session.buildImage(ctx, experimentArmResource(exp, arm), workDir)
	if err != nil {
		return "", "", err
	}

	if err := s.db.RecordArmBuild(ctx, arm.ID, commit, image); err != nil {
		// The image exists whether or not Mendel managed to write this down, so
		// the build is not failed over it. But an unrecorded build is an Arm
		// that will report itself never built while serving traffic, which is
		// worth saying out loud.
		logInfo("Built " + arm.Slug + " but could not record what it was built from: " + err.Error())
	}
	return image, commit, nil
}

// applyManifest sends a rendered manifest to the cluster.
func (g *gkeSession) applyManifest(ctx context.Context, manifest string) error {
	// No -n. The manifest spans two namespaces -- the experiment's objects and
	// the grant that lets the production route reach the proxy -- and kubectl
	// refuses an object whose namespace differs from the flag. Every object names
	// its own instead, which is what a manifest crossing a boundary has to do.
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", "-")
	cmd.Env = g.env
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("applying the experiment failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// repointProdRoute sends the production hostname somewhere else.
//
// A patch rather than a re-apply, because the route belongs to the ordinary
// deployment path and everything else about it must survive untouched -- the
// hostname, the parent gateway, whatever a later deploy adds to it.
func (g *gkeSession) repointProdRoute(ctx context.Context, route, backend, namespace string) error {
	// Read first, so the patch describes a change from what is actually there.
	//
	// The previous version always emitted a remove for the namespace field and
	// tolerated the failure by matching the error text. The text was guessed and
	// was wrong -- kubectl says "The request is invalid: the server rejected our
	// request due to an error in our request", which names nothing -- so stopping
	// an experiment failed on its first step and left it running. An operation
	// that is only correct when a string comparison holds is not correct.
	current, err := g.routeBackendNamespace(ctx, route)
	if err != nil {
		return err
	}

	ops := []string{
		fmt.Sprintf(`{"op":"replace","path":"/spec/rules/0/backendRefs/0/name","value":%q}`, backend),
	}
	switch {
	case namespace != "":
		// add replaces an existing value and creates a missing one, so it is
		// right whether or not the field is there.
		ops = append(ops, fmt.Sprintf(
			`{"op":"add","path":"/spec/rules/0/backendRefs/0/namespace","value":%q}`, namespace))
	case current != "":
		// Only remove what exists. Leaving a stale namespace would send
		// production at a Service of that name in somebody else's namespace.
		ops = append(ops, `{"op":"remove","path":"/spec/rules/0/backendRefs/0/namespace"}`)
	}

	cmd := exec.CommandContext(ctx, "kubectl", "patch", "httproute", route,
		"-n", hosting.Namespace, "--type=json", "-p", "["+strings.Join(ops, ",")+"]")
	cmd.Env = g.env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("could not point %s at %s: %s: %w",
			route, backend, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// routeBackendNamespace reports the namespace the route's backend is in, empty
// when it names none.
func (g *gkeSession) routeBackendNamespace(ctx context.Context, route string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "httproute", route,
		"-n", hosting.Namespace, "-o", "jsonpath={.spec.rules[0].backendRefs[0].namespace}")
	cmd.Env = g.env
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not read the production route %s: %w", route, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// healthCheckProxy tells the edge gateway how to tell whether the proxy is up.
//
// A GKE-specific object, and unavoidably so: the health check belongs to the
// load balancer GKE provisions, and Gateway API has no portable way to describe
// one. It is applied here rather than rendered with the rest because it names
// the proxy Service, whose name Envoy chooses.
//
// The label value is quoted for the reason every other one is: YAML would read
// an experiment name like `2024` as a number, and the API server refuses the
// whole object over it.
func (g *gkeSession) healthCheckProxy(ctx context.Context, proxyService, experimentName string) error {
	manifest := fmt.Sprintf(`apiVersion: networking.gke.io/v1
kind: HealthCheckPolicy
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    mendel-experiment: %[4]q
spec:
  default:
    config:
      type: HTTP
      httpHealthCheck:
        port: %[3]d
        requestPath: /ready
  targetRef:
    group: ""
    kind: Service
    name: %[1]s
`, proxyService, ExperimentProxyNamespace, envoyReadinessPort, experimentName)

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-f", "-")
	cmd.Env = g.env
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("could not configure the proxy health check: %s: %w",
			strings.TrimSpace(string(out)), err)
	}
	return nil
}

// envoyReadinessPort is where the proxy answers /ready, separate from the port
// it serves traffic on.
const envoyReadinessPort = 19003

// experimentProxyService finds the Service Envoy created for the experiment
// gateway.
//
// Discovered rather than derived: Envoy names it after the Gateway plus a hash
// of its own choosing, which nothing outside Envoy can predict.
func (g *gkeSession) experimentProxyService(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "svc",
		"-n", ExperimentProxyNamespace,
		"-l", "gateway.envoyproxy.io/owning-gateway-name="+ExperimentGatewayName,
		"-o", "jsonpath={.items[0].metadata.name}")
	cmd.Env = g.env
	out, err := cmd.Output()
	name := strings.TrimSpace(string(out))
	if err != nil || name == "" {
		return "", fmt.Errorf("the experiment gateway has no proxy yet, so there is nothing to route to")
	}
	return name, nil
}

func (s *Server) recordExperimentEvent(ctx context.Context, experimentID uuid.UUID,
	kind domain.ExperimentEventKind, detail string) {
	s.db.RecordExperimentEvent(ctx, &domain.ExperimentEvent{
		ExperimentID: experimentID, Kind: kind, Detail: detail,
	})
}

// experimentResourceName prefixes everything one experiment owns.
func experimentResourceName(exp *domain.Experiment) string {
	return "mendel-exp-" + exp.ID.String()[:8]
}

// experimentArmResource names one Arm's objects.
func experimentArmResource(exp *domain.Experiment, arm domain.ExperimentArm) string {
	return experimentResourceName(exp) + "-" + arm.Slug
}


// prodRouteName is the HTTPRoute the ordinary production deploy created.
//
// Derived the same way that deploy derives it, from the project's name, because
// the two must agree: the experiment repoints that exact object and points it
// back afterwards, and a name that drifted would leave production routed to a
// gateway serving an experiment that no longer exists.
func (s *Server) prodRouteName(ctx context.Context, projectID uuid.UUID) (string, error) {
	project, err := s.db.GetProject(ctx, projectID)
	if err != nil || project == nil {
		return "", fmt.Errorf("project not found")
	}
	return prodAppName(sanitizeAppName(project.Name)), nil
}

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

		image, err := s.buildArmImage(ctx, exp, arm, session, logInfo)
		if err != nil {
			return fmt.Errorf("building arm %s: %w", arm.Slug, err)
		}
		logMilestone("Built " + arm.Slug)
		deployment.Arms = append(deployment.Arms, ArmDeployment{
			Slug: arm.Slug, Image: image, Weight: arm.AllocationWeight,
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
	logMilestone("Pointing production traffic at the experiment")
	if err := session.repointProdRoute(ctx, prodRouteName, ExperimentEdgeService); err != nil {
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
	if err := session.repointProdRoute(ctx, prod, prod); err != nil {
		return err
	}

	// Everything the experiment created carries its label, so teardown finds
	// what it made without knowing what each object is.
	logInfo("Removing the experiment's resources")
	name := experimentResourceName(exp)
	del := exec.CommandContext(ctx, "kubectl", "delete",
		"deployment,service,httproute,gateway", "-n", hosting.Namespace,
		"-l", "mendel-experiment="+name, "--ignore-not-found")
	del.Env = session.env
	if out, err := del.CombinedOutput(); err != nil {
		logInfo("Some resources were left behind: " + strings.TrimSpace(string(out)))
	}

	if err := s.db.SetExperimentStatus(ctx, exp.ID, domain.ExperimentStopped); err != nil {
		return err
	}
	s.recordExperimentEvent(ctx, exp.ID, domain.EventKillSwitchPulled, reason)
	return nil
}

// buildArmImage checks out the Variation's branch and builds it.
func (s *Server) buildArmImage(ctx context.Context, exp *domain.Experiment,
	arm domain.ExperimentArm, session *gkeSession, logInfo func(string)) (string, error) {

	variation, err := s.db.GetVariation(ctx, *arm.VariationID)
	if err != nil || variation == nil {
		return "", fmt.Errorf("variation not found")
	}
	hop, err := s.db.GetHop(ctx, exp.HopID)
	if err != nil || hop == nil {
		return "", fmt.Errorf("hop not found")
	}
	repo, err := s.db.GetRepositoryByProject(ctx, exp.ProjectID)
	if err != nil || repo == nil || repo.URL == nil {
		return "", fmt.Errorf("this project has no repository URL")
	}

	var repoConfig struct {
		AuthToken string `json:"auth_token"`
	}
	if repo.Config != nil {
		json.Unmarshal(repo.Config, &repoConfig)
	}

	workDir, err := os.MkdirTemp("", "mendel-arm-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workDir)

	branch := fmt.Sprintf("mendel/%s/%s", sanitizeBranchName(hop.Name), sanitizeBranchName(variation.Name))
	logInfo("Cloning " + branch)
	if err := git.NewClient(workDir).Clone(ctx, *repo.URL, branch, repoConfig.AuthToken); err != nil {
		return "", fmt.Errorf("could not clone %s: %w", branch, err)
	}

	return session.buildImage(ctx, experimentArmResource(exp, arm), workDir)
}

// applyManifest sends a rendered manifest to the cluster.
func (g *gkeSession) applyManifest(ctx context.Context, manifest string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "--server-side", "-n", hosting.Namespace, "-f", "-")
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
func (g *gkeSession) repointProdRoute(ctx context.Context, route, backend string) error {
	patch := fmt.Sprintf(`[{"op":"replace","path":"/spec/rules/0/backendRefs/0/name","value":%q}]`, backend)
	cmd := exec.CommandContext(ctx, "kubectl", "patch", "httproute", route,
		"-n", hosting.Namespace, "--type=json", "-p", patch)
	cmd.Env = g.env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("could not point %s at %s: %s: %w",
			route, backend, strings.TrimSpace(string(out)), err)
	}
	return nil
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

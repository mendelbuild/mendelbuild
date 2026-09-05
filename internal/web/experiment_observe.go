package web

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhs/mendelbuild/internal/crypto"
	"github.com/bhs/mendelbuild/internal/domain"
)

// VerifyDatastoreVar is where the connection to the non-production datastore is
// kept, encrypted, beside the project's other secrets.
const VerifyDatastoreVar = "MENDEL_EXPERIMENT_VERIFY_DATASTORE_URL"

// observeExperimentReadiness looks at what is actually true of the project.
//
// Every answer is a Fact rather than a bool, so a check Mendel could not perform
// is reported as unknown rather than as a property the user has failed to set
// up. Collapsing those is what makes a page flap between "ready" and "go fix
// this" while nothing about the project changes.
func (s *Server) observeExperimentReadiness(ctx context.Context, projectID uuid.UUID) domain.ExperimentObservation {
	var obs domain.ExperimentObservation

	pd, err := s.db.GetProjectDomain(ctx, projectID)
	switch {
	case err != nil:
		obs.ProdHostname, obs.ProdHTTPS = domain.FactUnknown, domain.FactUnknown
	case pd == nil || pd.ProdHost() == "":
		obs.ProdHostname, obs.ProdHTTPS = domain.FactFalse, domain.FactFalse
	default:
		obs.ProdHost = pd.ProdHost()
		obs.ProdHostname = domain.FactTrue

		// Observed here rather than read from the domain page's cache. That cache
		// is served for ten seconds, so by the time anyone opens this page it is
		// almost always cold -- and a cold entry is reported as "could not tell",
		// which would make the certificate step permanently undetermined however
		// healthy the certificate was. This path already runs in the background
		// with a minute to spend, so it can afford to look.
		//
		// The certificate has to cover this name, not merely exist: the demo
		// wildcard does not reach the production host.
		domainObs := s.observeDomain(ctx, projectID, pd)
		switch {
		case domainObs.CertificateState == "":
			obs.ProdHTTPS = domain.FactUnknown
		case domainObs.CertificateState == "ACTIVE" && pd.CertificateCovers(obs.ProdHost):
			obs.ProdHTTPS = domain.FactTrue
		default:
			obs.ProdHTTPS = domain.FactFalse
		}
	}

	// Left unknown deliberately: this page is about the project, and whether an
	// experiment changes the schema is a property of the experiment. Reporting
	// it as false here would tell someone they need nothing, and as true would
	// demand a database of a project that may never need one.
	obs.SchemaChanges = domain.FactUnknown

	obs.GatewayAPI, obs.EnableGatewayCommand, obs.CookieMatching,
		obs.CanInstallController, obs.InstallControllerHint = s.observeGatewayControllers(ctx, projectID)
	obs.VerifyDatastore, obs.VerifyReachable = s.observeVerifyDatastore(ctx, projectID)
	return obs
}

// observeGatewayAPI asks the cluster whether it can reconcile a Gateway.
//
// A read, not an install. On GKE the Gateway API controller is a managed cluster
// feature rather than something deployed into the cluster, so the remedy is one
// gcloud command and Mendel's job is to name it -- not to probe RBAC and install
// a controller, which is what a cluster without a managed one would need and
// which no selectable channel currently is.
func (s *Server) observeGatewayControllers(ctx context.Context, projectID uuid.UUID) (
	api domain.Fact, enable string, cookies domain.Fact, mayInstall domain.Fact, install string) {
	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil || channel == nil || channel.HostingPlatform == nil ||
		channel.ArtifactKind != domain.DeployArtifactKubernetes {
		// Not a Kubernetes channel, so the question does not arise yet.
		return domain.FactUnknown, "", domain.FactUnknown, domain.FactUnknown, ""
	}

	env, err := s.deployCredentialsForChannel(ctx, projectID, channel)
	if err != nil {
		return domain.FactUnknown, "", domain.FactUnknown, domain.FactUnknown, ""
	}

	command := fmt.Sprintf(
		"gcloud container clusters update %s --location %s --gateway-api=standard --project %s",
		env["GKE_CLUSTER_NAME"], env["GKE_ZONE"], env["GCP_PROJECT_ID"])

	// Idempotent, because a user re-running it is the normal case rather than
	// the exception.
	installEnvoy := installControllerCommand()

	session, err := newGKESession(ctx, env)
	if err != nil {
		return domain.FactUnknown, command, domain.FactUnknown, domain.FactUnknown, installEnvoy
	}
	defer session.cleanup()

	// A GatewayClass that exists but has not been accepted is as useless as none
	// at all, so ask for the condition rather than for the name.
	cmd := exec.CommandContext(ctx, "kubectl", "get", "gatewayclass",
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\" \"}{.status.conditions[?(@.type=='Accepted')].status}{\"\\n\"}{end}")
	cmd.Env = session.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		// The API not being installed is exactly what "no such resource type"
		// means, and it is the answer rather than a failure to get one.
		if strings.Contains(string(out), "doesn't have a resource type") ||
			strings.Contains(string(out), "the server could not find the requested resource") {
			// No Gateway API at all, so neither question has a yes.
			return domain.FactFalse, command, domain.FactFalse,
				canInstallController(ctx, session), installEnvoy
		}
		return domain.FactUnknown, command, domain.FactUnknown, domain.FactUnknown, installEnvoy
	}

	api, cookies = domain.FactFalse, domain.FactFalse
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.EqualFold(fields[1], "True") {
			continue
		}
		api = domain.FactTrue
		// GKE's own classes match headers Exact only. Any other accepted class
		// is one somebody installed, which is the only reason to install one.
		if !strings.HasPrefix(fields[0], "gke-") {
			cookies = domain.FactTrue
		}
	}
	// Only worth asking when it is the outstanding question; it costs two more
	// round trips to the cluster.
	mayInstall = domain.FactUnknown
	if cookies == domain.FactFalse {
		mayInstall = canInstallController(ctx, session)
	}
	return api, command, cookies, mayInstall, installEnvoy
}

// observeVerifyDatastore reports whether a non-production datastore has been
// given and whether Mendel can reach it.
func (s *Server) observeVerifyDatastore(ctx context.Context, projectID uuid.UUID) (set, reachable domain.Fact) {
	stored, err := s.db.GetProjectEnvVar(ctx, projectID, VerifyDatastoreVar)
	switch {
	case errors.Is(err, pgx.ErrNoRows), err == nil && stored == nil:
		// Not set is an answer, not a failure to look. The query reports a
		// missing row as an error, and reading that as "could not tell" told the
		// user Mendel had a problem when the truth was that they had not set one
		// yet -- which is the exact confusion Fact exists to prevent.
		return domain.FactFalse, domain.FactFalse
	case err != nil:
		return domain.FactUnknown, domain.FactUnknown
	}

	key, err := crypto.GetKey()
	if err != nil {
		return domain.FactTrue, domain.FactUnknown
	}
	conn, err := crypto.Decrypt(stored.EncryptedValue, key)
	if err != nil {
		return domain.FactTrue, domain.FactUnknown
	}

	dial, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	pool, err := pgxpool.New(dial, string(conn))
	if err != nil {
		return domain.FactTrue, domain.FactFalse
	}
	defer pool.Close()
	if err := pool.Ping(dial); err != nil {
		return domain.FactTrue, domain.FactFalse
	}
	return domain.FactTrue, domain.FactTrue
}

// --- Cache ---
//
// The same arrangement as the domain observation and for the same reason: this
// costs a kubectl call and a database dial, and a settings page should not wait
// on either. See domain_observe_cache.go.

const experimentObservationTTL = 30 * time.Second

type experimentObservation struct {
	obs        domain.ExperimentObservation
	at         time.Time
	refreshing bool
}

type experimentObservationCache struct {
	mu      sync.Mutex
	entries map[uuid.UUID]experimentObservation
}

func (s *Server) experimentObservationFor(projectID uuid.UUID) (domain.ExperimentObservation, time.Time) {
	entry, start := s.takeExperimentRefreshSlot(projectID)
	if start {
		go s.refreshExperimentObservation(projectID)
	}
	return entry.obs, entry.at
}

func (s *Server) takeExperimentRefreshSlot(projectID uuid.UUID) (experimentObservation, bool) {
	s.experimentObs.mu.Lock()
	defer s.experimentObs.mu.Unlock()

	if s.experimentObs.entries == nil {
		s.experimentObs.entries = make(map[uuid.UUID]experimentObservation)
	}
	entry := s.experimentObs.entries[projectID]
	if entry.refreshing || time.Since(entry.at) <= experimentObservationTTL {
		return entry, false
	}
	claimed := entry
	claimed.refreshing = true
	s.experimentObs.entries[projectID] = claimed
	return entry, true
}

func (s *Server) refreshExperimentObservation(projectID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	obs := s.observeExperimentReadiness(ctx, projectID)

	s.experimentObs.mu.Lock()
	defer s.experimentObs.mu.Unlock()
	s.experimentObs.entries[projectID] = experimentObservation{obs: obs, at: time.Now(), refreshing: false}
}

func (s *Server) invalidateExperimentObservation(projectID uuid.UUID) {
	s.experimentObs.mu.Lock()
	defer s.experimentObs.mu.Unlock()
	delete(s.experimentObs.entries, projectID)
}

package web

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/git"
)

// Whether an Arm is running the code its branch names, and bringing it up to
// date when it is not.
//
// The first live experiment had no answer to the first question. Code changed on
// both experiment branches while the experiment ran, and whether the running
// arms included it was unknowable -- they did, but only because the experiment
// had been restarted, which was luck rather than knowledge.
//
// Mendel does not act on staleness by itself. Rebuilding an Arm that is serving
// traffic changes what its participants see mid-experiment, which is a decision
// somebody makes rather than housekeeping Mendel does -- the same dissonance the
// kill switch is acknowledged for. So staleness is reported, and refreshing is a
// button.

// armBuildsFor says how each Arm's build stands against its branch.
//
// One ls-remote for the whole repository rather than one per Arm, and cached:
// this is asked on every render of the Hop page, and the first version put a
// network call to the user's git host on that path, so a slow GitHub made the
// page slow and an unreachable one made it hang for as long as the request
// lasted.
//
// A repository Mendel cannot reach yields unknown for every Arm, never stale.
// Telling somebody to rebuild an Arm that is already current wastes a build and,
// mid-experiment, changes what participants see for no reason at all.
func (s *Server) armBuildsFor(ctx context.Context, exp *domain.Experiment) map[uuid.UUID]domain.ArmBuild {
	builds := make(map[uuid.UUID]domain.ArmBuild, len(exp.Arms))
	heads := s.branchHeadsFor(ctx, exp.ProjectID)

	// Only a running experiment has Arms answering requests. Starting is
	// excluded on purpose: an Arm with no commit part-way through a start has
	// genuinely not been built yet, which is the state it should report.
	serving := exp.Status == domain.ExperimentRunning

	for _, arm := range exp.Arms {
		head := ""
		if branch, err := s.armBranch(ctx, exp, arm); err == nil {
			head = heads[branch]
		}
		builds[arm.ID] = domain.DescribeArmBuild(arm, head, serving)
	}
	return builds
}

// --- Branch heads, kept off the render path ---
//
// The same arrangement as the domain and experiment observations and for the
// same reason: this costs a round trip to somebody else's git host, and a page
// should not wait on one. See domain_observe_cache.go.
//
// It differs in one way, deliberately. Those caches return empty on a cold entry
// and the page polls a status endpoint until it fills; this one looks
// synchronously the first time, under a short timeout. The Hop page has no such
// poll, so a cold entry would report every Arm unchecked until somebody
// reloaded -- which is the state a reader is least able to act on, offered at
// exactly the moment they came to look.

const (
	// branchHeadTTL is how long a look is reused. Short enough that a push made
	// while somebody is working shows up without them wondering, long enough
	// that reading a page repeatedly does not hammer the remote.
	branchHeadTTL = 60 * time.Second

	// branchHeadTimeout bounds the one look a reader waits for. Past it the
	// answer is unknown, which is honest and is not stale -- the distinction
	// that keeps a timeout from asking for a needless rebuild.
	branchHeadTimeout = 5 * time.Second
)

type branchHeads struct {
	heads      map[string]string
	at         time.Time
	refreshing bool
}

type branchHeadCache struct {
	mu      sync.Mutex
	entries map[uuid.UUID]branchHeads
}

// branchHeadsFor returns the project's branch heads, looking them up only when
// there is nothing to serve.
func (s *Server) branchHeadsFor(ctx context.Context, projectID uuid.UUID) map[string]string {
	entry, cold, refresh := s.takeBranchHeadSlot(projectID)

	if cold {
		// Nothing to show, so this reader waits -- briefly, and for a bounded
		// time. Every later reader is served from what this one fetched.
		look, cancel := context.WithTimeout(ctx, branchHeadTimeout)
		defer cancel()
		return s.refreshBranchHeads(look, projectID)
	}
	if refresh {
		// Something to show and it is going stale. The reader gets the previous
		// answer, which is at most one TTL old, and the next one gets a fresh
		// look. A staleness verdict a minute behind is not a category of error;
		// a page that waits on a network call is.
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), branchHeadTimeout)
			defer cancel()
			s.refreshBranchHeads(bg, projectID)
		}()
	}
	return entry.heads
}

// takeBranchHeadSlot reports what is cached and who should go and look.
//
// cold means there is nothing to serve at all; refresh means what is there has
// aged out. At most one caller is given either, so a page opened in three tabs
// makes one request rather than three.
func (s *Server) takeBranchHeadSlot(projectID uuid.UUID) (entry branchHeads, cold, refresh bool) {
	s.branchHeads.mu.Lock()
	defer s.branchHeads.mu.Unlock()

	if s.branchHeads.entries == nil {
		s.branchHeads.entries = make(map[uuid.UUID]branchHeads)
	}
	entry = s.branchHeads.entries[projectID]

	if entry.refreshing || time.Since(entry.at) <= branchHeadTTL {
		return entry, false, false
	}
	claimed := entry
	claimed.refreshing = true
	s.branchHeads.entries[projectID] = claimed
	return entry, entry.at.IsZero(), !entry.at.IsZero()
}

// refreshBranchHeads looks, stores what it found, and returns it.
//
// A failed look is stored too, as an empty set with a fresh timestamp. Leaving
// the entry cold would send every subsequent reader to a remote that is not
// answering, one bounded wait each; storing it means one reader pays and the
// rest are told "unchecked" immediately, which is the same answer sooner.
func (s *Server) refreshBranchHeads(ctx context.Context, projectID uuid.UUID) map[string]string {
	var heads map[string]string
	if access, err := s.repoAccessFor(ctx, projectID); err == nil {
		if found, err := git.RemoteHeads(ctx, access.URL, access.AuthToken); err == nil {
			heads = found
		} else {
			log.Printf("experiment[%s]: could not read the repository's branches: %v", projectID, err)
		}
	}

	s.branchHeads.mu.Lock()
	defer s.branchHeads.mu.Unlock()
	s.branchHeads.entries[projectID] = branchHeads{heads: heads, at: time.Now()}
	return heads
}

// RefreshExperimentArms rebases each Arm onto main, rebuilds it, and -- when the
// experiment is running -- rolls the new image out to the visitors in it.
//
// armID selects one Arm; nil refreshes every Arm the experiment has. Both scopes
// exist because both are real: a change to one Variation deserves one rebuild,
// and mainline moving deserves all of them, since every Arm is now being
// compared against a control it no longer contains.
//
// Mainline is skipped in either case. It is not built by the experiment -- it
// keeps the Deployment the ordinary production deploy made -- so there is
// nothing here to bring up to date.
func (s *Server) RefreshExperimentArms(ctx context.Context, experimentID uuid.UUID,
	armID *uuid.UUID, logMilestone, logInfo func(string)) error {

	exp, err := s.db.GetExperiment(ctx, experimentID)
	if err != nil || exp == nil {
		return fmt.Errorf("no such experiment")
	}

	var wanted []domain.ExperimentArm
	for _, arm := range exp.Arms {
		if arm.IsMainline() {
			continue
		}
		if armID != nil && arm.ID != *armID {
			continue
		}
		wanted = append(wanted, arm)
	}
	if len(wanted) == 0 {
		return fmt.Errorf("no arm of this experiment can be refreshed")
	}

	// A stopped experiment has no Deployment to roll an image into, so building
	// one would be paying for a Cloud Build nothing could run. The rebase is
	// still worth doing -- it is the part that persists -- and starting the
	// experiment builds from the branch it leaves behind.
	deploying := exp.Status == domain.ExperimentRunning

	access, err := s.repoAccessFor(ctx, exp.ProjectID)
	if err != nil {
		return err
	}

	var session *gkeSession
	if deploying {
		channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, exp.ProjectID)
		if err != nil || channel == nil {
			return fmt.Errorf("this project has no deployment channel")
		}
		env, err := s.deployCredentialsForChannel(ctx, exp.ProjectID, channel)
		if err != nil {
			return fmt.Errorf("the channel's credentials are not available: %w", err)
		}
		if session, err = newGKESession(ctx, env); err != nil {
			return err
		}
		defer session.cleanup()
	}

	pd, _ := s.db.GetProjectDomain(ctx, exp.ProjectID)
	prodHost := ""
	if pd != nil {
		prodHost = pd.ProdHost()
	}

	for _, arm := range wanted {
		if !deploying {
			branch, err := s.armBranch(ctx, exp, arm)
			if err != nil {
				return err
			}
			if err := rebaseBranch(ctx, branch, access, logInfo); err != nil {
				return err
			}
			logMilestone(fmt.Sprintf("Rebased %s onto %s; it will be built when the experiment starts",
				arm.Slug, access.MainBranch))
			continue
		}

		image, commit, err := s.buildArmImage(ctx, exp, arm, session, true, logInfo)
		if err != nil {
			return fmt.Errorf("refreshing arm %s: %w", arm.Slug, err)
		}

		// Re-applied before the image changes, because the Variation may have
		// declared a requirement it did not have before. Pods read a Secret at
		// start, so the new image rolling is what picks the new values up --
		// doing this the other way round would leave the new code running on the
		// old environment until something else restarted it.
		if _, err := s.applyArmEnvironment(ctx, exp, arm, prodHost, session, logInfo); err != nil {
			return fmt.Errorf("refreshing arm %s environment: %w", arm.Slug, err)
		}

		if err := session.rolloutArmImage(ctx, experimentArmResource(exp, arm), image); err != nil {
			return fmt.Errorf("rolling out arm %s: %w", arm.Slug, err)
		}

		logMilestone("Arm " + arm.Slug + " is now serving " + short(commit))
		// Recorded against the Arm, because a participant's experience changed
		// part-way through: somebody who saw this Arm yesterday saw different
		// code from somebody who sees it now, and a result read without knowing
		// that is a result read wrong.
		s.db.RecordExperimentEvent(ctx, &domain.ExperimentEvent{
			ExperimentID: exp.ID, ArmID: &arm.ID,
			Kind: domain.EventAllocationChanged,
			Detail: fmt.Sprintf("Arm %s rebased onto %s and rebuilt at %s while serving traffic",
				arm.Slug, access.MainBranch, short(commit)),
		})
	}
	return nil
}

// rebaseBranch brings one branch onto main without building it.
func rebaseBranch(ctx context.Context, branch string, access repoAccess, logInfo func(string)) error {
	workDir, err := os.MkdirTemp("", "mendel-rebase-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	client := git.NewClient(workDir)
	logInfo("Cloning " + branch)
	if err := client.Clone(ctx, access.URL, branch, access.AuthToken); err != nil {
		return fmt.Errorf("could not clone %s: %w", branch, err)
	}
	logInfo("Rebasing " + branch + " onto " + access.MainBranch)
	if err := client.RebaseOnto(ctx, access.MainBranch, access.AuthToken); err != nil {
		return fmt.Errorf("could not rebase %s onto %s, so it was left as it was: %w",
			branch, access.MainBranch, err)
	}
	if err := client.Push(ctx, access.AuthToken); err != nil {
		return fmt.Errorf("rebased %s but could not push it: %w", branch, err)
	}
	return nil
}

// rolloutArmImage points a running Arm's Deployment at a new image.
//
// `set image` rather than re-applying the whole manifest: the routing, the
// weights and the gateway are all serving traffic correctly and none of them is
// what changed. Re-applying them to change one field is how an unrelated field
// gets reverted to whatever the renderer thought it should be.
//
// `rollout status` afterwards, because the point of this method is that the new
// code is serving -- and `set image` returning success means the API server
// accepted the object, not that a pod running it ever came up.
func (g *gkeSession) rolloutArmImage(ctx context.Context, deployment, image string) error {
	set := g.kubectl(ctx, "set", "image", "deployment/"+deployment, "app="+image)
	if out, err := set.CombinedOutput(); err != nil {
		return fmt.Errorf("could not set the image on %s: %s: %w",
			deployment, strings.TrimSpace(string(out)), err)
	}

	wait := g.kubectl(ctx, "rollout", "status", "deployment/"+deployment, "--timeout=5m")
	if out, err := wait.CombinedOutput(); err != nil {
		return fmt.Errorf("%s took the new image but no pod running it became ready: %s: %w",
			deployment, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// short renders a commit the length a person reads. The domain has its own; this
// is the web package's, so a template need not import one.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

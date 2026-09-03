package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// observeDomain looks at what is actually true, rather than at what anyone said.
//
// The DNS half needs no credentials at all: a name either resolves to the
// reserved address or it does not, and Mendel can settle that in a few
// milliseconds. That is the difference between a step the user ticks off and one
// Mendel can see, and it matters most for the case an acknowledgement handles
// worst -- a record created with a typo, which the user believes is done and
// which surfaces much later as a certificate that never issues.
func (s *Server) observeDomain(ctx context.Context, projectID uuid.UUID, pd *domain.ProjectDomain) domain.DomainObservation {
	var obs domain.DomainObservation
	if pd == nil || pd.BaseDomain == "" {
		return obs
	}

	resolver := net.DefaultResolver

	// A wildcard answers for any name beneath it, so asking about a name nothing
	// was ever deployed at is the cleanest way to test the record itself.
	lookupOne(ctx, pd.DemoHost("mendel-dns-check"), func(lookup context.Context, name string) error {
		addrs, err := resolver.LookupHost(lookup, name)
		if err == nil && len(addrs) > 0 {
			obs.WildcardTarget = addrs[0]
		}
		return err
	})

	// Each zone the certificate covers has its own record, and they are created
	// by hand in separate rows of a provider's form -- so one resolving says
	// nothing about the next. Reported per record for that reason.
	for _, c := range pd.Challenges {
		if c.RecordName == "" {
			continue
		}
		lookupOne(ctx, c.RecordName, func(lookup context.Context, name string) error {
			target, err := resolver.LookupCNAME(lookup, name)
			if err != nil {
				return err
			}
			if obs.ChallengeTargets == nil {
				obs.ChallengeTargets = make(map[string]string, len(pd.Challenges))
			}
			obs.ChallengeTargets[name] = target
			return nil
		})
	}

	lookupOne(ctx, pd.BaseDomain, func(lookup context.Context, name string) error {
		obs.Zone = zoneFor(lookup, resolver, name)
		return nil
	})

	state, err := s.certificateState(ctx, projectID, pd)
	if err != nil {
		// Logged rather than swallowed. Discarding this is what made the failure
		// invisible: nothing in the logs, and a page that quietly said the
		// certificate was outstanding.
		log.Printf("domain: could not determine the certificate state for %s: %v", pd.BaseDomain, err)
		obs.CertificateUnknown = true
	}
	obs.CertificateState = state
	obs.Known = true
	return obs
}

// lookupTimeout is how long any single DNS lookup gets.
//
// Per lookup rather than shared across all of them. A single budget over the
// whole set means each zone added shortens what the ones after it get: two zones
// is already three lookups plus the zone walk, and the ones at the end of the
// list -- always the same ones -- are the first to run out. A lookup that times
// out is indistinguishable from a name that does not resolve, which reads as the
// user's move and sends them to their DNS provider over a record that is fine.
const lookupTimeout = 4 * time.Second

// lookupOne runs a single lookup under its own deadline.
//
// A name that does not resolve is an ordinary answer -- usually a record the
// user has not created yet -- and says so on the ladder, so it is not logged. A
// timeout is not an answer at all: it is Mendel failing to look, reported as
// though the record were absent. Logged for that reason, and only for that
// reason, since the ladder cannot tell the difference.
func lookupOne(ctx context.Context, name string, fn func(context.Context, string) error) {
	lookup, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	err := fn(lookup, name)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsTimeout {
		log.Printf("domain: DNS lookup of %s timed out after %s; "+
			"it will be reported as not resolving", name, lookupTimeout)
	}
}

// certificateState asks the authority what it thinks, since only it knows.
//
// Returns the authority's word and no error when it answered; an empty state and
// no error when there is genuinely no certificate to ask about; and an error when
// Mendel could not find out. Those are three outcomes and the caller needs all
// three: "no certificate has been requested" is a fact about the project, while
// "gcloud would not run" is a fact about Mendel, and reporting the second as the
// first tells the user a certificate they already have is outstanding.
//
// Every step here is a way to not find out. The credentials come out of the
// database and are decrypted; the session writes a key file and spends a network
// round trip authenticating; the describe is another. Any of them can fail for a
// second and be fine on the next attempt, which is exactly why the failure must
// not be reported as an answer.
func (s *Server) certificateState(ctx context.Context, projectID uuid.UUID, pd *domain.ProjectDomain) (string, error) {
	if pd.CertificateName == "" {
		return "", nil // Nothing has been requested. Not a failure to look.
	}

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("get active deployment channel: %w", err)
	}
	if channel == nil || channel.HostingPlatform == nil {
		return "", fmt.Errorf("project has no active deployment channel")
	}
	if channel.HostingPlatform.HostnameSource != domain.HostnameFromUser {
		// The platform issues its own names, so there is no certificate of
		// Mendel's to ask about. A fact about the project, not a failure.
		return "", nil
	}
	env, err := s.deployCredentialsForChannel(ctx, projectID, channel)
	if err != nil {
		return "", fmt.Errorf("load credentials: %w", err)
	}
	session, err := newGCloudSession(ctx, env)
	if err != nil {
		return "", fmt.Errorf("start gcloud session: %w", err)
	}
	defer session.cleanup()

	cmd := exec.CommandContext(ctx, "gcloud", "certificate-manager", "certificates", "describe",
		pd.CertificateName, "--project", session.projectID, "--format", "value(managed.state)")
	cmd.Env = session.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("describe certificate %s: %s: %w",
			pd.CertificateName, strings.TrimSpace(stderr.String()), err)
	}
	state := strings.TrimSpace(string(out))
	if state == "" {
		// gcloud succeeded and said nothing, which is not a state. Treated as a
		// failure to determine rather than as an absent certificate.
		return "", fmt.Errorf("describe certificate %s returned no state", pd.CertificateName)
	}
	return state, nil
}

// zoneFor finds the DNS zone a name lives in: the closest ancestor that is
// delegated, which is the one whose provider the user is actually logged into.
//
// Needed because most providers ask for a host relative to the zone, and the
// zone is often not the domain the user gave Mendel. pong.mendel.build is not
// delegated -- mendel.build is -- so a record shown as relative to
// pong.mendel.build would be created one level too shallow, at
// mendel-demos.mendel.build, and would resolve for nobody while looking correct
// in the provider's list.
//
// Walking up stops before a single label: every TLD has NS records, so a domain
// that is delegated nowhere would otherwise report its zone as "com".
func zoneFor(ctx context.Context, resolver *net.Resolver, name string) string {
	for candidate := name; strings.Count(candidate, ".") >= 1; {
		if ns, err := resolver.LookupNS(ctx, candidate); err == nil && len(ns) > 0 {
			return candidate
		}
		cut := strings.Index(candidate, ".")
		if cut < 0 {
			break
		}
		candidate = candidate[cut+1:]
	}
	return ""
}

package web

import (
	"context"
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

	lookup, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	resolver := net.DefaultResolver

	// A wildcard answers for any name beneath it, so asking about a name nothing
	// was ever deployed at is the cleanest way to test the record itself.
	if addrs, err := resolver.LookupHost(lookup, pd.DemoHost("mendel-dns-check")); err == nil && len(addrs) > 0 {
		obs.WildcardTarget = addrs[0]
	}

	// Each zone the certificate covers has its own record, and they are created
	// by hand in separate rows of a provider's form -- so one resolving says
	// nothing about the next. Reported per record for that reason.
	for _, c := range pd.Challenges {
		if c.RecordName == "" {
			continue
		}
		if target, err := resolver.LookupCNAME(lookup, c.RecordName); err == nil {
			if obs.ChallengeTargets == nil {
				obs.ChallengeTargets = make(map[string]string, len(pd.Challenges))
			}
			obs.ChallengeTargets[c.RecordName] = target
		}
	}

	obs.Zone = zoneFor(lookup, resolver, pd.BaseDomain)
	obs.CertificateState = s.certificateState(ctx, projectID, pd)
	obs.Known = true
	return obs
}

// certificateState asks the authority what it thinks, since only it knows.
//
// Needs the channel's credentials, so unlike the DNS checks this is quiet when
// they are unavailable: an unknown state reads as "not issued yet", which is the
// safe way round -- it never claims a certificate exists.
func (s *Server) certificateState(ctx context.Context, projectID uuid.UUID, pd *domain.ProjectDomain) string {
	if pd.CertificateName == "" {
		return ""
	}

	channel, err := s.db.GetActiveProjectDeploymentChannel(ctx, projectID)
	if err != nil || channel == nil || channel.HostingPlatform == nil ||
		channel.HostingPlatform.HostnameSource != domain.HostnameFromUser {
		return ""
	}
	env, err := s.deployCredentialsForChannel(ctx, projectID, channel)
	if err != nil {
		return ""
	}
	session, err := newGCloudSession(ctx, env)
	if err != nil {
		return ""
	}
	defer session.cleanup()

	cmd := exec.CommandContext(ctx, "gcloud", "certificate-manager", "certificates", "describe",
		pd.CertificateName, "--project", session.projectID, "--format", "value(managed.state)")
	cmd.Env = session.env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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

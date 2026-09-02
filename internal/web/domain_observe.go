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

	if pd.ACMERecordName != "" {
		if target, err := resolver.LookupCNAME(lookup, pd.ACMERecordName); err == nil {
			obs.ChallengeTarget = target
		}
	}

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

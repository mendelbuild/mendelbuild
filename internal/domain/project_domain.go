package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ProjectDomain is where a project's deployments are reachable.
//
// Mendel never touches DNS. It is given a domain the user controls, invents
// names under it, and relies on records the user created once. That is the whole
// arrangement, and it works because a wildcard record answers for every name
// beneath it: the name Mendel picks already resolves before it picks.
type ProjectDomain struct {
	ProjectID uuid.UUID `json:"project_id"`

	// BaseDomain is the domain the user controls, e.g. example.com.
	BaseDomain string `json:"base_domain"`

	// DemoSubdomain is one label under it that demos live in. One label,
	// because a wildcard covers exactly one: *.mendel-demos.example.com answers
	// for pong-abc123.mendel-demos.example.com and nothing deeper.
	DemoSubdomain string `json:"demo_subdomain"`

	// ProdSubdomain is the label production answers at, empty when production
	// has no name yet.
	ProdSubdomain string `json:"prod_subdomain"`

	// StaticIP is the address the records point at. Mendel reserves it, so it is
	// known before anything is deployed -- which is the only reason Mendel can
	// state a record instead of telling the user to go and find an address.
	StaticIP     string `json:"static_ip"`
	StaticIPName string `json:"static_ip_name"`

	// ACMERecordName and ACMERecordValue are the ownership challenge for the
	// wildcard certificate. Minted by the certificate authority, so unlike the
	// address records these cannot be worked out from the domain: Mendel has to
	// ask for them and then repeat them back.
	ACMERecordName  string `json:"acme_record_name"`
	ACMERecordValue string `json:"acme_record_value"`
	CertificateName string `json:"certificate_name"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DefaultDemoSubdomain keeps demos in a label that says who made them, so a
// wildcard record the user creates is obviously Mendel's and can be removed
// without wondering what else it covered.
const DefaultDemoSubdomain = "mendel-demos"

// DemoWildcard is the name the user's wildcard record is created for.
func (d *ProjectDomain) DemoWildcard() string {
	if d == nil || d.BaseDomain == "" {
		return ""
	}
	return "*." + d.demoZone()
}

// DemoHost is where one deployment answers.
func (d *ProjectDomain) DemoHost(appName string) string {
	if d == nil || d.BaseDomain == "" || appName == "" {
		return ""
	}
	label := strings.Trim(strings.ToLower(appName), "-")
	if label == "" {
		return ""
	}
	return label + "." + d.demoZone()
}

// ProdHost is where production answers, empty when no label was given.
func (d *ProjectDomain) ProdHost() string {
	if d == nil || d.BaseDomain == "" || d.ProdSubdomain == "" {
		return ""
	}
	return d.ProdSubdomain + "." + d.BaseDomain
}

func (d *ProjectDomain) demoZone() string {
	sub := d.DemoSubdomain
	if sub == "" {
		sub = DefaultDemoSubdomain
	}
	return sub + "." + d.BaseDomain
}

// DNSRecordKind distinguishes the records a user has to create.
type DNSRecordKind string

const (
	DNSRecordDemo        DNSRecordKind = "demo"
	DNSRecordProd        DNSRecordKind = "prod"
	DNSRecordCertificate DNSRecordKind = "certificate"
)

// DNSRecord is one row a user types into their DNS provider.
//
// Named by the three fields every provider asks for, in the words providers use,
// because the value of writing them out at all is that they can be copied rather
// than worked out.
type DNSRecord struct {
	Kind  DNSRecordKind `json:"kind"`
	Type  string        `json:"type"`
	Name  string        `json:"name"`
	Value string        `json:"value"`
	Why   string        `json:"why"`
}

// DNSRecords is exactly what the user must create, or nil with a reason when
// Mendel cannot say yet.
//
// The address has to come first: a record pointing nowhere is worse than no
// record, because it looks done. So this returns nothing until Mendel has
// reserved one, and the caller says what is still outstanding.
func (d *ProjectDomain) DNSRecords() []DNSRecord {
	if d == nil || d.BaseDomain == "" || d.StaticIP == "" {
		return nil
	}

	records := []DNSRecord{{
		Kind:  DNSRecordDemo,
		Type:  "A",
		Name:  d.DemoWildcard(),
		Value: d.StaticIP,
		Why: "One record for every demo. The wildcard answers for whatever name Mendel " +
			"gives a deployment, so this is created once and never touched again.",
	}}

	if host := d.ProdHost(); host != "" {
		records = append(records, DNSRecord{
			Kind:  DNSRecordProd,
			Type:  "A",
			Name:  host,
			Value: d.StaticIP,
			Why: "Production answers at one name rather than a generated one, and shares " +
				"the same address as the demos.",
		})
	}

	if d.ACMERecordName != "" && d.ACMERecordValue != "" {
		records = append(records, DNSRecord{
			Kind:  DNSRecordCertificate,
			Type:  "CNAME",
			Name:  d.ACMERecordName,
			Value: d.ACMERecordValue,
			Why: "Proves the domain is yours so a certificate can be issued for it. " +
				"Without this the names resolve but only over http, and a sign-in " +
				"provider will not accept an http redirect URI.",
		})
	}
	return records
}

// CertificateOutstanding reports whether the names will resolve but not serve
// https yet, which is the state that breaks sign-in while looking like it works.
func (d *ProjectDomain) CertificateOutstanding() bool {
	return d != nil && d.BaseDomain != "" && d.StaticIP != "" && d.ACMERecordName == ""
}

// DomainBlocker explains what stands between the settings as they are and
// records the user could act on. Empty when the records are ready.
func (d *ProjectDomain) DomainBlocker(hasKubernetesChannel bool) string {
	if d == nil || strings.TrimSpace(d.BaseDomain) == "" {
		return "Give Mendel a domain you control and it will work out the records you need."
	}
	if d.StaticIP == "" {
		if !hasKubernetesChannel {
			return "Mendel reserves an address when a Kubernetes channel is set up, and the " +
				"records point at it. Choose that channel and provide its credentials first."
		}
		return "Mendel has not reserved an address yet. It does so on the next deploy or " +
			"validation of this channel, and the records will appear here once it has."
	}
	return ""
}

// NormalizeDomain tidies what someone typed into a domain. People paste URLs,
// trailing dots and stray capitals, and every one of those produces a record
// that looks right and resolves for nobody.
func NormalizeDomain(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.Trim(s, ".")
}

// NormalizeLabel tidies a single DNS label, which is what a subdomain must be.
func NormalizeLabel(s string) string {
	s = NormalizeDomain(s)
	// A label with a dot in it is not one label, and a wildcard would not cover
	// the name it produces.
	if i := strings.Index(s, "."); i >= 0 {
		s = s[:i]
	}
	return s
}

// ValidateDomain reports why a domain cannot be used, or "" when it can.
func ValidateDomain(base, demoLabel, prodLabel string) string {
	base = NormalizeDomain(base)
	if base == "" {
		return ""
	}
	if !strings.Contains(base, ".") {
		return fmt.Sprintf("%q is not a domain name. It needs at least one dot, as in example.com.", base)
	}
	for _, l := range []struct{ what, value string }{
		{"demo subdomain", demoLabel},
		{"production subdomain", prodLabel},
	} {
		if l.value == "" {
			continue
		}
		if cleaned := NormalizeDomain(l.value); strings.Contains(cleaned, ".") {
			// Name the label that was probably meant. Rejecting without saying
			// what would have been right leaves the reader guessing at which
			// part of what they typed was the offending one.
			return fmt.Sprintf("The %s is one label, not a whole name, because a wildcard record "+
				"covers one label only. You gave %q; did you mean %q?",
				l.what, l.value, NormalizeLabel(l.value))
		}
	}
	return ""
}

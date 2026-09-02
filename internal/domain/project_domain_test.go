package domain

import (
	"strings"
	"testing"
)

func TestDemoNamesStayOneLabelUnderTheWildcard(t *testing.T) {
	d := &ProjectDomain{BaseDomain: "example.com", DemoSubdomain: "mendel-demos", StaticIP: "34.1.2.3"}

	if got := d.DemoWildcard(); got != "*.mendel-demos.example.com" {
		t.Errorf("wildcard = %q", got)
	}
	host := d.DemoHost("pong-abc123")
	if host != "pong-abc123.mendel-demos.example.com" {
		t.Fatalf("host = %q", host)
	}
	// The wildcard covers one label. A host with an extra dot beneath the zone
	// would resolve for nobody, and would look like a broken deployment.
	beneath := strings.TrimSuffix(host, ".mendel-demos.example.com")
	if strings.Contains(beneath, ".") {
		t.Errorf("generated label %q is not a single label", beneath)
	}
}

func TestNoRecordsUntilThereIsAnAddress(t *testing.T) {
	// A record pointing nowhere is worse than no record, because it looks done.
	d := &ProjectDomain{BaseDomain: "example.com"}
	if got := d.DNSRecords(); got != nil {
		t.Errorf("records offered with no address: %+v", got)
	}
	if d.DomainBlocker(true) == "" {
		t.Error("no address and no explanation of why records are missing")
	}

	d.StaticIP = "34.1.2.3"
	if d.DomainBlocker(true) != "" {
		t.Error("address reserved, so nothing should be outstanding")
	}
	recs := d.DNSRecords()
	if len(recs) != 1 {
		t.Fatalf("want the demo wildcard alone, got %d", len(recs))
	}
	if recs[0].Type != "A" || recs[0].Name != "*.mendel-demos.example.com" || recs[0].Value != "34.1.2.3" {
		t.Errorf("record is %+v", recs[0])
	}
}

func TestProductionGetsARecordOnlyWhenNamed(t *testing.T) {
	d := &ProjectDomain{BaseDomain: "example.com", DemoSubdomain: "mendel-demos", StaticIP: "34.1.2.3"}
	if len(d.DNSRecords()) != 1 {
		t.Error("unnamed production should contribute no record")
	}

	d.ProdSubdomain = "pong"
	recs := d.DNSRecords()
	if len(recs) != 2 {
		t.Fatalf("want demo and production records, got %d", len(recs))
	}
	if recs[1].Name != "pong.example.com" {
		t.Errorf("production record names %q", recs[1].Name)
	}
	if d.ProdHost() != "pong.example.com" {
		t.Errorf("prod host = %q", d.ProdHost())
	}
}

// TestNormalizeDomain covers what people actually paste. Each of these produces
// a record that looks right and resolves for nobody.
func TestNormalizeDomain(t *testing.T) {
	for in, want := range map[string]string{
		"https://Example.com/":  "example.com",
		"  example.com.  ":      "example.com",
		"http://example.com?x=1": "example.com",
		"EXAMPLE.COM":           "example.com",
	} {
		if got := NormalizeDomain(in); got != want {
			t.Errorf("NormalizeDomain(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeLabel("demos.example.com"); got != "demos" {
		t.Errorf("NormalizeLabel kept more than one label: %q", got)
	}
}

func TestValidateDomainRejectsWhatCannotWork(t *testing.T) {
	if msg := ValidateDomain("example.com", "mendel-demos", "pong"); msg != "" {
		t.Errorf("valid settings rejected: %s", msg)
	}
	if msg := ValidateDomain("localhost", "", ""); msg == "" {
		t.Error("a name with no dot is not a domain")
	}
	// The one that matters: a multi-label subdomain silently defeats the
	// wildcard, so it has to be refused rather than normalised away.
	if msg := ValidateDomain("example.com", "a.b", ""); msg == "" {
		t.Error("a multi-label demo subdomain should be refused")
	}
}

// TestCertificateRecordIsListedWithTheOthers covers the record that is easiest
// to leave out, because unlike the address records its value cannot be worked
// out from the domain -- it is minted by the certificate authority.
//
// Leaving it out of this list because Mendel could not yet serve https would put
// the user in exactly the position the Domain page exists to prevent: needing a
// record, and having to be told about it somewhere other than where records are.
func TestCertificateRecordIsListedWithTheOthers(t *testing.T) {
	d := &ProjectDomain{
		BaseDomain: "example.com", DemoSubdomain: "mendel-demos", StaticIP: "34.1.2.3",
	}
	for _, r := range d.DNSRecords() {
		if r.Kind == DNSRecordCertificate {
			t.Fatal("a certificate record was offered before its value was known")
		}
	}

	d.Challenges = []ACMEChallenge{{
		Domain:      "mendel-demos.example.com",
		RecordName:  "_acme-challenge.mendel-demos.example.com",
		RecordValue: "bbeef8e0-9922-46f4-8537-2107e1d4d9b0.4.authorize.certificatemanager.goog.",
	}}

	recs := d.DNSRecords()
	if len(recs) != 2 {
		t.Fatalf("want the wildcard and the certificate record, got %d", len(recs))
	}
	cert := recs[1]
	if cert.Type != "CNAME" {
		t.Errorf("certificate record is a %s; providers need to be told CNAME", cert.Type)
	}
	if cert.Name != d.Challenges[0].RecordName || cert.Value != d.Challenges[0].RecordValue {
		t.Errorf("record does not repeat what the authority minted: %+v", cert)
	}
}

// TestRejectionNamesTheLabelMeant covers the message someone sees after pasting
// a whole domain into a label field, which is the mistake the field invites.
func TestRejectionNamesTheLabelMeant(t *testing.T) {
	msg := ValidateDomain("pong.mendel.build", "mendel-demos.pong.mendel.build", "")
	if msg == "" {
		t.Fatal("a whole domain in a label field should be refused")
	}
	if !strings.Contains(msg, `"mendel-demos"`) {
		t.Errorf("message does not suggest the label meant: %s", msg)
	}
}

// TestLimitationFollowsTheSchemeActuallyServed is the pairing that matters: a
// name served over http is still refused by a sign-in provider, so naming it is
// not enough on its own.
func TestLimitationFollowsTheSchemeActuallyServed(t *testing.T) {
	// A name without a certificate. Better than an address, still not
	// registerable, and Mendel has to keep saying so.
	if DeployURLLimitation("http://app.pong.mendel.build") == "" {
		t.Error("http on a real hostname is still refused; the limitation should stand")
	}
	// The same name once the certificate exists.
	if msg := DeployURLLimitation("https://app.pong.mendel.build"); msg != "" {
		t.Errorf("https on a real hostname is registerable, but got: %s", msg)
	}
}

// What the certificate covers, and what it must not be claimed to cover.
//
// A wildcard covers exactly one label, so no single one reaches both the
// production host and a demo two labels down. The certificate therefore carries
// one wildcard per zone -- and a host under neither is not covered, however much
// it looks like it belongs to the same domain.
//
// Worth pinning because the failure is invisible from inside Mendel: the
// certificate exists, goes ACTIVE, is attached to its map, and the name resolves.
// Only the browser disagrees, with ERR_CERT_COMMON_NAME_INVALID.
func TestCertificateCoversBothZonesAndNothingElse(t *testing.T) {
	d := &ProjectDomain{
		BaseDomain: "example.com", DemoSubdomain: "mendel-demos",
		ProdSubdomain: "app", CertificateName: "mendel-abc", CertificateMapName: "mendel-abc",
	}

	for _, host := range []string{d.DemoHost("pong-abc123"), d.ProdHost()} {
		if !d.CertificateCovers(host) {
			t.Errorf("%q should be covered by %v", host, d.CertificateDomains())
		}
	}

	// Two labels under a zone is past what a wildcard reaches.
	if d.CertificateCovers("a.b.mendel-demos.example.com") {
		t.Error("a wildcard covers exactly one label")
	}
	// The apex is not covered by a wildcard over it.
	if d.CertificateCovers("example.com") {
		t.Error("*.example.com does not cover example.com")
	}
	// Somebody else's domain entirely.
	if d.CertificateCovers("app.elsewhere.com") {
		t.Error("a host outside the base domain must never be covered")
	}
	// Nothing is covered before a certificate has been requested.
	none := &ProjectDomain{BaseDomain: "example.com", DemoSubdomain: "mendel-demos", ProdSubdomain: "app"}
	if none.CertificateCovers(none.ProdHost()) {
		t.Error("no certificate means nothing is covered")
	}
}

// Two zones means two records, and the user creates them by hand in separate
// rows of a provider's form. One of them resolving says nothing about the other.
func TestBothChallengeRecordsAreListed(t *testing.T) {
	d := &ProjectDomain{
		BaseDomain: "example.com", DemoSubdomain: "mendel-demos", ProdSubdomain: "app",
		StaticIP: "34.1.2.3",
	}
	if want := []string{"example.com", "mendel-demos.example.com"}; len(d.CertificateZones()) != 2 ||
		d.CertificateZones()[0] != want[0] || d.CertificateZones()[1] != want[1] {
		t.Fatalf("zones needing authorization: got %v, want %v", d.CertificateZones(), want)
	}

	for _, zone := range d.CertificateZones() {
		d.Challenges = append(d.Challenges, ACMEChallenge{
			Domain:      zone,
			RecordName:  "_acme-challenge." + zone,
			RecordValue: "target-for-" + zone + ".authorize.certificatemanager.goog.",
		})
	}

	var certRecords int
	for _, r := range d.DNSRecords() {
		if r.Kind == DNSRecordCertificate {
			certRecords++
			if r.Type != "CNAME" {
				t.Errorf("%s is a %s; providers need to be told CNAME", r.Name, r.Type)
			}
		}
	}
	if certRecords != 2 {
		t.Errorf("both challenge records must be listed, got %d", certRecords)
	}
}

// Most providers ask for a host relative to the zone they manage, and the zone
// is frequently not the domain the user gave Mendel.
//
// pong.mendel.build is not delegated; mendel.build is. So a record shown as
// relative to the base domain would be created a level too shallow, landing at
// mendel-demos.mendel.build -- correct-looking in the provider's list and
// resolving for nobody. That is what this page used to print.
func TestHostIsRelativeToTheZoneNotTheBaseDomain(t *testing.T) {
	d := &ProjectDomain{
		BaseDomain: "pong.mendel.build", DemoSubdomain: "mendel-demos",
		ProdSubdomain: "app", StaticIP: "34.1.2.3",
	}
	d.Challenges = []ACMEChallenge{{
		Domain:      "pong.mendel.build",
		RecordName:  "_acme-challenge.pong.mendel.build",
		RecordValue: "abc.authorize.certificatemanager.goog.",
	}}

	const zone = "mendel.build" // The delegated zone, not the base domain.

	want := map[string]string{
		"*.mendel-demos.pong.mendel.build":  "*.mendel-demos.pong",
		"app.pong.mendel.build":             "app.pong",
		"_acme-challenge.pong.mendel.build": "_acme-challenge.pong",
	}
	for _, r := range d.DNSRecords() {
		got := r.HostIn(zone)
		if expected, ok := want[r.Name]; !ok {
			t.Errorf("unexpected record %q", r.Name)
		} else if got != expected {
			t.Errorf("%s: host is %q, want %q", r.Name, got, expected)
		}
	}
}

func TestHostInDeclinesRatherThanGuesses(t *testing.T) {
	r := DNSRecord{Name: "app.example.com"}

	if got := r.HostIn(""); got != "" {
		t.Errorf("an unknown zone must produce no host, got %q", got)
	}
	// A record outside the zone cannot be expressed relative to it, and inventing
	// something would be worse than showing the full name.
	if got := r.HostIn("elsewhere.com"); got != "" {
		t.Errorf("a record outside the zone must produce no host, got %q", got)
	}
	// The zone's own name is @ everywhere.
	if got := (DNSRecord{Name: "example.com"}).HostIn("example.com"); got != "@" {
		t.Errorf("the zone apex should be @, got %q", got)
	}
	// A suffix that is not a label boundary is not a match.
	if got := (DNSRecord{Name: "notexample.com"}).HostIn("example.com"); got != "" {
		t.Errorf("%q is not inside example.com, got host %q", "notexample.com", got)
	}
	// Case is not significant in DNS.
	if got := (DNSRecord{Name: "App.Example.COM"}).HostIn("example.com"); got != "app" {
		t.Errorf("case should not matter, got %q", got)
	}
}

// A production deployment on a bare address while a name is configured means a
// deploy fell back, not that Mendel forgot the domain.
//
// The deployment URL is not rewritten after the fact, and should not be: it
// records what that deploy produced, and when the gateway fails to come up the
// deployment genuinely is only reachable at an address -- no route to the name
// exists. Which makes this the awkward case to report, because the URL is right
// and the situation is wrong.
func TestProdNameUnusedSpotsAFallbackDeploy(t *testing.T) {
	d := &ProjectDomain{
		BaseDomain: "pong.mendel.build", DemoSubdomain: "mendel-demos", ProdSubdomain: "app",
	}

	if !d.ProdNameUnused("http://34.56.24.112") {
		t.Error("a bare address while app.pong.mendel.build is configured is a fallback")
	}
	for _, using := range []string{
		"https://app.pong.mendel.build",
		"http://app.pong.mendel.build",
		"https://app.pong.mendel.build/",
		"https://APP.PONG.MENDEL.BUILD",
	} {
		if d.ProdNameUnused(using) {
			t.Errorf("%q is the configured name and must not be reported as unused", using)
		}
	}

	// Nothing to report when no name was asked for: a project with no production
	// subdomain is not missing anything.
	none := &ProjectDomain{BaseDomain: "pong.mendel.build", DemoSubdomain: "mendel-demos"}
	if none.ProdNameUnused("http://34.56.24.112") {
		t.Error("no production name configured, so an address is simply the answer")
	}
	// And nothing to report before anything is deployed.
	if d.ProdNameUnused("") {
		t.Error("no deployment yet is not a fallback")
	}
}

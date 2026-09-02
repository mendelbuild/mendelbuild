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
	if !d.CertificateOutstanding() {
		t.Error("no challenge minted yet, so the certificate is outstanding")
	}
	for _, r := range d.DNSRecords() {
		if r.Kind == DNSRecordCertificate {
			t.Fatal("a certificate record was offered before its value was known")
		}
	}

	d.ACMERecordName = "_acme-challenge.mendel-demos.example.com"
	d.ACMERecordValue = "bbeef8e0-9922-46f4-8537-2107e1d4d9b0.4.authorize.certificatemanager.goog."

	if d.CertificateOutstanding() {
		t.Error("the challenge is known, so nothing is outstanding")
	}
	recs := d.DNSRecords()
	if len(recs) != 2 {
		t.Fatalf("want the wildcard and the certificate record, got %d", len(recs))
	}
	cert := recs[1]
	if cert.Type != "CNAME" {
		t.Errorf("certificate record is a %s; providers need to be told CNAME", cert.Type)
	}
	if cert.Name != d.ACMERecordName || cert.Value != d.ACMERecordValue {
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

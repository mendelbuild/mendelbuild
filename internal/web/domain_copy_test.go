package web

import (
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
)

func renderDomainPageContent(t *testing.T, pd *domain.ProjectDomain, zone string) string {
	t.Helper()
	steps := pd.DomainReadiness(domain.DomainObservation{Known: true})
	headline, waiting := domain.DomainHeadline(steps)

	var out strings.Builder
	err := parsePageTemplate("project_domain.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"Steps": steps, "Headline": headline, "WaitingOnYou": waiting,
		"Checking": false, "CheckedLabel": "just now", "ProjectID": "abc",
		"Domain": pd, "Records": pd.DNSRecords(), "Blocker": "", "NeedsDomain": true,
		"DemoWildcard": pd.DemoWildcard(), "ProdHost": pd.ProdHost(),
		"ExampleHost": pd.DemoHost("pong-abc123"), "Zone": zone,
	})
	if err != nil {
		t.Fatalf("domain page does not render: %v", err)
	}
	return out.String()
}

func domainWithRecords() *domain.ProjectDomain {
	pd := &domain.ProjectDomain{
		BaseDomain: "example.com", DemoSubdomain: "mendel-demos", ProdSubdomain: "app",
		StaticIP: "34.1.2.3", CertificateName: "mendel-abc", CertificateMapName: "mendel-abc",
	}
	for _, zone := range pd.CertificateZones() {
		pd.Challenges = append(pd.Challenges, domain.ACMEChallenge{
			Domain:      zone,
			RecordName:  "_acme-challenge." + zone,
			RecordValue: "d34db33f-0000-4000-8000-000000000000.8.authorize.certificatemanager.goog.",
		})
	}
	return pd
}

// Every value the user has to retype into their DNS provider gets a copy button.
//
// These are long, and one wrong character produces a record that looks right and
// resolves for nobody -- a mistake that surfaces an hour later as a certificate
// that never issued, with nothing pointing back at the typo.
func TestEveryDNSValueIsCopyable(t *testing.T) {
	pd := domainWithRecords()
	html := renderDomainPageContent(t, pd, "")

	records := pd.DNSRecords()
	if len(records) < 4 {
		t.Fatalf("expected demo A, prod A and two challenge CNAMEs, got %d", len(records))
	}

	for _, r := range records {
		for _, value := range []string{r.Name, r.Value} {
			if !strings.Contains(html, `<code class="copy-value">`+value+`</code>`) {
				t.Errorf("%q is shown without a copy button", value)
			}
		}
	}
}

// A copy button with no handler behind it looks exactly like a working one.
//
// The page loads its scripts by name, so the button markup and the script that
// makes it do anything can drift apart without a single visible symptom. That is
// the failure this project keeps hitting: a feature that renders but cannot be
// used.
func TestCopyButtonsHaveTheirHandler(t *testing.T) {
	html := renderDomainPageContent(t, domainWithRecords(), "")

	if !strings.Contains(html, "data-copy-adjacent") {
		t.Fatal("no copy buttons on the domain page at all")
	}
	if !strings.Contains(html, "/static/js/copy-button.js") {
		t.Error("copy buttons are rendered but copy-button.js is not loaded, so they do nothing")
	}

	js, err := staticFS.ReadFile("static/js/copy-button.js")
	if err != nil {
		t.Fatalf("reading copy-button.js: %v", err)
	}
	for _, needed := range []string{"data-copy-adjacent", ".copyable", ".copy-value"} {
		if !strings.Contains(string(js), needed) {
			t.Errorf("copy-button.js does not handle %s, which the markup relies on", needed)
		}
	}
}

// The Host column must be relative to the delegated zone, and the full name has
// to stay reachable for the providers that want it.
func TestRecordsTableShowsHostAndFullName(t *testing.T) {
	pd := &domain.ProjectDomain{
		BaseDomain: "pong.mendel.build", DemoSubdomain: "mendel-demos",
		ProdSubdomain: "app", StaticIP: "34.1.2.3",
	}
	html := renderDomainPageContent(t, pd, "mendel.build")

	for _, want := range []string{
		`<code class="copy-value">*.mendel-demos.pong</code>`,       // what Namecheap wants
		`<code class="copy-value">*.mendel-demos.pong.mendel.build`, // still there for Cloudflare
		"<th>Host</th>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("records table is missing %s", want)
		}
	}

	// Relative to the base domain is the wrong cut and the bug this replaced: it
	// would put the record at mendel-demos.mendel.build.
	if strings.Contains(html, `<code class="copy-value">*.mendel-demos</code>`) {
		t.Error("host is relative to the base domain rather than the zone")
	}
}

// With no zone there is no honest way to shorten a name, so the page shows full
// names and says so instead of cutting at a guess.
func TestUnknownZoneShowsFullNamesOnly(t *testing.T) {
	pd := &domain.ProjectDomain{
		BaseDomain: "example.com", DemoSubdomain: "mendel-demos", StaticIP: "34.1.2.3",
	}
	html := renderDomainPageContent(t, pd, "")

	if strings.Contains(html, "<th>Host</th>") {
		t.Error("a Host column implies a zone Mendel does not know")
	}
	if !strings.Contains(html, `<code class="copy-value">*.mendel-demos.example.com</code>`) {
		t.Error("the full name should be shown when the zone is unknown")
	}
}

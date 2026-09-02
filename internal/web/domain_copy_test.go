package web

import (
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
)

func renderDomainPageContent(t *testing.T, pd *domain.ProjectDomain) string {
	t.Helper()
	steps := pd.DomainReadiness(domain.DomainObservation{Known: true})
	headline, waiting := domain.DomainHeadline(steps)

	var out strings.Builder
	err := parsePageTemplate("project_domain.html").ExecuteTemplate(&out, "page-content", map[string]interface{}{
		"Steps": steps, "Headline": headline, "WaitingOnYou": waiting,
		"Checking": false, "CheckedLabel": "just now", "ProjectID": "abc",
		"Domain": pd, "Records": pd.DNSRecords(), "Blocker": "", "NeedsDomain": true,
		"DemoWildcard": pd.DemoWildcard(), "ProdHost": pd.ProdHost(),
		"ExampleHost": pd.DemoHost("pong-abc123"),
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
	html := renderDomainPageContent(t, pd)

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
	html := renderDomainPageContent(t, domainWithRecords())

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

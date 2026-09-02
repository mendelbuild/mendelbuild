package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A gateway references a certificate *map*, not a certificate.
//
// They are separate GCP resources with separate names, and passing one where the
// other belongs is accepted without complaint: the annotation is written, the
// gateway programs itself, and it serves plain http. No error, no Kubernetes
// event, nothing in any log. The certificate meanwhile goes ACTIVE, so every
// surface Mendel can see reports success while no browser can reach the site
// over https.
//
// That shipped, and the only thing that would have caught it is a check that the
// name handed to the annotation came from the map field.
func TestGatewayIsGivenTheMapNotTheCertificate(t *testing.T) {
	src, err := os.ReadFile("handlers_demo.go")
	if err != nil {
		t.Fatal(err)
	}

	// Assigning the certificate name into the variable the gateway annotation is
	// built from is the regression, whatever the variable is called.
	wrong := regexp.MustCompile(`(?i)certmap\w*\s*[:=]+\s*\w+\.CertificateName\b`)
	for i, line := range strings.Split(string(src), "\n") {
		if wrong.MatchString(line) {
			t.Errorf("handlers_demo.go:%d gives the gateway a certificate name where a "+
				"certificate map name belongs; this fails silently:\n\t%s",
				i+1, strings.TrimSpace(line))
		}
	}

	if !strings.Contains(string(src), "certMap = fresh.CertificateMapName") {
		t.Error("deployToGKE no longer takes the gateway's certmap from CertificateMapName")
	}
}

// An authorization's name must follow the zone it is for, not its position in a
// list. Positions are reused when the user edits their demo subdomain, and
// `dns-authorizations create` on an existing name fails with "already exists" --
// which the caller treats as success, then reads back the authorization for the
// domain that used to occupy that slot and asks the user to create a record that
// can never resolve.
func TestAuthorizationNamesFollowTheZone(t *testing.T) {
	const base = "mendel-abc12345"

	demo := authorizationName(base, "mendel-demos.example.com")
	renamed := authorizationName(base, "demos.example.com")
	apex := authorizationName(base, "example.com")

	if demo == renamed || demo == apex || renamed == apex {
		t.Errorf("different zones share an authorization name: %s / %s / %s", demo, renamed, apex)
	}
	if demo != authorizationName(base, "mendel-demos.example.com") {
		t.Error("the same zone must always produce the same name, or re-running mints a duplicate")
	}
	if !strings.HasPrefix(demo, base) {
		t.Errorf("%q does not carry the project's naming scheme", demo)
	}
}

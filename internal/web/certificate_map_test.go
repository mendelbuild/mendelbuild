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

// Certificate Manager permits one authorization per domain and refuses a second
// under a different name -- with a message naming the *domain*, so a caller
// matching on "already exists" reads it as success and then describes an
// authorization that was never created. That is what left this project with its
// base zone authorized, its demo zone not, and no certificate at all.
//
// Reuse is also what spares the user a trip to their DNS provider: a fresh
// authorization mints a fresh value, so replacing one would invalidate a record
// that was already working.
func TestExistingAuthorizationIsReused(t *testing.T) {
	// Exactly what `gcloud ... dns-authorizations list --format=value(...)` prints.
	const listing = "mendel-d25227dc\tmendel-demos.pong.mendel.build.\t" +
		"_acme-challenge.mendel-demos.pong.mendel.build.\t9e54fd15.8.authorize.certificatemanager.goog.\n" +
		"mendel-d25227dc-39708d0f\tpong.mendel.build.\t" +
		"_acme-challenge.pong.mendel.build.\t0f0050eb.8.authorize.certificatemanager.goog."

	name, record, _, found := matchAuthorization(listing, "mendel-demos.pong.mendel.build")
	if !found {
		t.Fatal("the demo zone is authorized already and must be reused, not duplicated")
	}
	if name != "mendel-d25227dc" {
		t.Errorf("reused the wrong authorization: %q", name)
	}
	if record != "_acme-challenge.mendel-demos.pong.mendel.build." {
		t.Errorf("record does not match the authorization: %q", record)
	}

	// The base domain is a suffix of the demo zone. A suffix match would hand
	// back the demo zone's authorization and record -- which the user has already
	// created, so the certificate would sit unvalidated with nothing looking wrong.
	base, _, _, found := matchAuthorization(listing, "pong.mendel.build")
	if !found || base != "mendel-d25227dc-39708d0f" {
		t.Errorf("base domain matched %q, found=%v; suffix matching is not a match", base, found)
	}

	// A zone nobody has authorized must be created, not silently reused.
	if _, _, _, found := matchAuthorization(listing, "staging.pong.mendel.build"); found {
		t.Error("an unauthorized zone must not borrow another zone's authorization")
	}
	if _, _, _, found := matchAuthorization("", "pong.mendel.build"); found {
		t.Error("empty output cannot match anything")
	}
}

// An HTTPS listener using a certificate map must not also declare a tls block.
//
// The certificate comes from the networking.gke.io/certmap annotation, and
// `tls: mode: Terminate` without certificateRefs is rejected by the Gateway API
// webhook -- "certificateRefs or options must be specified when mode is
// Terminate" -- because there is no Secret to point at.
//
// The consequence is not an error the user sees. The Gateway fails to apply,
// deployToGKE falls back to a bare address, and production comes up on an IP
// with its own domain unused, reporting success the whole way.
func TestHTTPSListenerHasNoTLSBlockWithACertMap(t *testing.T) {
	withCert := gatewayManifest("mendel-ip", "mendel-map")

	if !strings.Contains(withCert, "networking.gke.io/certmap") {
		t.Fatal("the certificate map is not attached at all")
	}
	if !strings.Contains(withCert, "protocol: HTTPS") {
		t.Fatal("no HTTPS listener despite a certificate map")
	}
	if strings.Contains(withCert, "mode: Terminate") {
		t.Error("the HTTPS listener declares tls alongside a certmap, which the " +
			"Gateway API rejects; the certmap annotation supplies the certificate")
	}
	if strings.Contains(withCert, "tls:") {
		t.Error("no tls block belongs on a listener whose certificate comes from a certmap")
	}

	// Without a certificate there is nothing to present, so there must be no
	// HTTPS listener: a listener that cannot come up takes the whole Gateway
	// with it, and a project would lose the plain http it already had.
	withoutCert := gatewayManifest("mendel-ip", "")
	if strings.Contains(withoutCert, "HTTPS") {
		t.Error("an HTTPS listener with no certificate stops the Gateway coming up at all")
	}
	if !strings.Contains(withoutCert, "protocol: HTTP") {
		t.Error("http must survive when there is no certificate yet")
	}
}

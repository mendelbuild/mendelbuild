package web

import (
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
)

// TestManifestWithoutHostnameKeepsLoadBalancer covers the project that has given
// Mendel no domain. It still deploys and is still reachable; nothing about this
// change may require a domain to keep working.
func TestManifestWithoutHostnameKeepsLoadBalancer(t *testing.T) {
	m := k8sManifestFor("pong-abc123", "gcr.io/x/pong:1", "", "", "")

	if !strings.Contains(m, "type: LoadBalancer") {
		t.Error("without a hostname the Service should still be a LoadBalancer")
	}
	if strings.Contains(m, "kind: HTTPRoute") {
		t.Error("no hostname means there is no host to route on, so no HTTPRoute")
	}
}

// TestManifestWithHostnameRoutesByHost covers the shape a wildcard record needs:
// every demo answering at one address, told apart by Host.
//
// Confirmed against a real cluster rather than only asserted here: applied to
// GKE, the Ingress took an address and answered 200 for a request carrying the
// matching Host and 404 for one without it. The 404 is the half that matters --
// it shows the rule discriminates, which is what lets many demos share one
// address and one wildcard record.
func TestManifestWithHostnameRoutesByHost(t *testing.T) {
	host := "pong-abc123.demos.example.com"
	m := k8sManifestFor("pong-abc123", "gcr.io/x/pong:1", "", host, "")

	if !strings.Contains(m, "type: ClusterIP") {
		t.Error("a routed deployment needs no LoadBalancer of its own")
	}
	if strings.Contains(m, "type: LoadBalancer") {
		t.Error("a LoadBalancer per demo defeats the single address a wildcard record points at")
	}
	if !strings.Contains(m, "kind: HTTPRoute") {
		t.Fatal("no HTTPRoute, so nothing routes the hostname anywhere")
	}
	if !strings.Contains(m, "- "+host) {
		t.Errorf("HTTPRoute does not match on %s", host)
	}
	// The route has to attach to the shared Gateway and point back at this
	// deployment's own Service.
	if !strings.Contains(m, "name: "+gatewayName) {
		t.Error("HTTPRoute names no parent Gateway")
	}
	if !strings.Contains(m, "name: pong-abc123") {
		t.Error("HTTPRoute backend does not name the Service")
	}
}

// TestDeploymentHostnameIsOneLabel pins the constraint that makes the whole
// scheme work without Mendel touching DNS.
//
// A wildcard record covers exactly one label: *.demos.example.com answers for
// pong-abc123.demos.example.com and not for anything deeper. A name with a dot
// in it would resolve for nobody, and the failure would look like a broken
// deployment rather than a name the record never covered.
func TestDeploymentHostnameIsOneLabel(t *testing.T) {
	got := domain.DeploymentHostname("pong-abc123", "demos.example.com")
	if got != "pong-abc123.demos.example.com" {
		t.Fatalf("got %q", got)
	}
	if n := strings.Count(strings.TrimSuffix(got, ".demos.example.com"), "."); n != 0 {
		t.Errorf("the generated label contains %d dots; a wildcard covers one label only", n)
	}

	// A trailing dot or stray whitespace on the domain is the user's typing,
	// not a different domain.
	if domain.DeploymentHostname("pong", " demos.example.com. ") != "pong.demos.example.com" {
		t.Error("base domain should be normalised before use")
	}

	// Without a domain there is no hostname, which is what keeps the
	// LoadBalancer path in place.
	if domain.DeploymentHostname("pong", "") != "" {
		t.Error("no domain should mean no hostname")
	}
}

// TestGatewayCarriesTheAddressAndCertificate covers the two things that make a
// name usable: it answers at the address the DNS record points at, and it can
// prove itself over https.
//
// This replaced an Ingress because an Ingress cannot do the second. Applied with
// networking.gke.io/certmap it provisions port 80 and no HTTPS listener at all,
// with no error and no event -- verified on a real cluster, which is the only way
// that shows.
func TestGatewayCarriesTheAddressAndCertificate(t *testing.T) {
	g := gatewayManifest("mendel-1a2b3c4d", "mendel-1a2b3c4d")

	if !strings.Contains(g, "gatewayClassName: gke-l7-global-external-managed") {
		t.Error("no GKE gateway class, so nothing provisions this")
	}
	if !strings.Contains(g, `value: "mendel-1a2b3c4d"`) || !strings.Contains(g, "NamedAddress") {
		t.Error("gateway does not claim the reserved address the DNS records name")
	}
	if !strings.Contains(g, `networking.gke.io/certmap: "mendel-1a2b3c4d"`) {
		t.Error("no certificate map, so the listener has nothing to serve https with")
	}
	if !strings.Contains(g, "protocol: HTTPS") || !strings.Contains(g, "port: 443") {
		t.Error("gateway does not listen for https")
	}

	// Plain http is always served, so the names work as soon as the records
	// exist rather than waiting on a certificate.
	if !strings.Contains(g, "protocol: HTTP\n    port: 80") {
		t.Error("gateway does not listen on http")
	}

	// Before a certificate exists the Gateway still has to come up. An https
	// listener with nothing to present does not, and a Gateway that does not come
	// up routes nothing -- which would lose the http that used to work.
	bare := gatewayManifest("mendel-1a2b3c4d", "")
	if strings.Contains(bare, "certmap") || strings.Contains(bare, "HTTPS") {
		t.Error("offered https with no certificate to serve")
	}
	if !strings.Contains(bare, "protocol: HTTP\n    port: 80") {
		t.Error("without a certificate the gateway should still serve http")
	}
}

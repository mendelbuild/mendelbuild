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
	if strings.Contains(m, "kind: Ingress") {
		t.Error("no hostname means there is no host to route on, so no Ingress")
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
	if !strings.Contains(m, "kind: Ingress") {
		t.Fatal("no Ingress, so nothing routes the hostname anywhere")
	}
	if !strings.Contains(m, "host: "+host) {
		t.Errorf("Ingress does not match on %s", host)
	}
	// The rule has to point back at this deployment's own Service.
	if !strings.Contains(m, "name: pong-abc123") {
		t.Error("Ingress backend does not name the Service")
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

// TestIngressUsesTheClassAnnotation pins a line that looks like an oversight and
// is not.
//
// kubectl warns that kubernetes.io/ingress.class is deprecated in favour of
// spec.ingressClassName, and following that advice stops the Ingress working:
// GKE ships no IngressClass resource, so ingressClassName: gce names a class
// nothing provides. The Ingress is then never reconciled -- no address, and no
// events at all, so there is nothing to read that would explain it.
//
// Verified both ways against a real cluster: the annotation draws a "Scheduled
// for sync" from the load balancer controller within a minute and serves
// traffic; the documented replacement draws no events at all and never gets an
// address.
func TestIngressUsesTheClassAnnotation(t *testing.T) {
	m := k8sManifestFor("pong-abc123", "img", "", "pong-abc123.demos.example.com", "")

	if !strings.Contains(m, `kubernetes.io/ingress.class: "gce"`) {
		t.Error("the Ingress class annotation is missing; without it GKE never provisions the Ingress")
	}
	if strings.Contains(m, "ingressClassName") {
		t.Error("spec.ingressClassName names a class GKE does not create, and silently prevents provisioning")
	}
}

// TestIngressPinsToTheReservedAddress covers the link between the DNS record a
// user typed and the load balancer that answers it.
//
// The record points at one address. Without this annotation the load balancer
// takes whichever address is free, so the name resolves somewhere nothing is
// listening -- and the deployment itself is fine, which makes it look like a DNS
// mistake the user made.
func TestIngressPinsToTheReservedAddress(t *testing.T) {
	pinned := k8sManifestFor("pong-abc123", "img", "", "pong-abc123.demos.example.com", "mendel-1a2b3c4d")
	if !strings.Contains(pinned, `kubernetes.io/ingress.global-static-ip-name: "mendel-1a2b3c4d"`) {
		t.Error("Ingress is not pinned to the reserved address")
	}

	// A project with a hostname but no reserved address must not gain a broken
	// annotation naming an address that does not exist.
	unpinned := k8sManifestFor("pong-abc123", "img", "", "pong-abc123.demos.example.com", "")
	if strings.Contains(unpinned, "global-static-ip-name") {
		t.Error("named an address that was never reserved")
	}
}

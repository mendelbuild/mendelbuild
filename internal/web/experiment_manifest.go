package web

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bhs/mendelbuild/internal/assigner"
	"github.com/bhs/mendelbuild/internal/hosting"
)

// What an experiment needs in the cluster, rendered without one.
//
// A pure function for the same reason k8sManifestFor is: the resources can be
// read, diffed and tested without a cluster to apply them to, and the parts that
// have silently not worked on GKE before -- an annotation ignored, a class
// nothing provides -- are at least visible in a test's output.

// ArmDeployment is one Arm's share of the cluster.
type ArmDeployment struct {
	Slug string

	// Image is what this Arm runs. Empty for mainline, which keeps the
	// Deployment it already has: it is the code that was already there, and
	// redeploying it under a new name would make the control a new thing.
	Image string

	// Backend is the Service traffic for this Arm goes to. For mainline that is
	// the existing deployment's Service; for a Variation it is the one rendered
	// here.
	Backend string

	// Weight is this Arm's share of visitors who arrive without an assignment.
	Weight int
}

// ExperimentGatewayClass is the class of the controller that does Arm matching.
//
// Not GKE's. gke-l7-global-external-managed matches headers `Exact` only, and an
// Exact match on a Cookie header cannot pick one cookie out of the several a
// visitor carries -- so cookie-based Arm matching is impossible on it, whichever
// mechanism computes the Arm. Google's GatewayClass capabilities table is
// explicit about this, and a server-side dry run does not reveal it: the CRD
// accepts the regex and the controller ignores it.
const ExperimentGatewayClass = "eg"

// ExperimentGatewayName is the Gateway the Arm routes attach to.
const ExperimentGatewayName = "mendel-experiments"

// ExperimentProxyNamespace is where Envoy Gateway runs the proxies it
// provisions -- its own namespace, not the Gateway's.
//
// That is why the production route reaches it across a namespace boundary. The
// first version of this put a Service in mendel-apps selecting the proxy pods by
// label, which can never have endpoints: a Service selects pods in its own
// namespace only. It applied cleanly, reported ResolvedRefs=True because the
// Service existed, and pointed production traffic at nothing.
const ExperimentProxyNamespace = "envoy-gateway-system"

// ExperimentDeployment is everything an experiment puts in the cluster.
type ExperimentDeployment struct {
	// Name prefixes every resource, so one experiment's objects can be found and
	// removed without knowing what they are.
	Name string

	Hostname string
	Arms     []ArmDeployment

	// Secure marks the assignment cookie Secure, which requires https. Marking
	// it on an http site means the browser never sends it back, so every request
	// looks unassigned and nobody stays in an Arm.
	Secure bool

	EnvFrom string
}

// cookieMatch is the regular expression that recognises one Arm in a Cookie
// header.
//
// Gateway API matches headers, and a Cookie header carries every cookie the
// visitor has: "sid=abc; mendel_arm=treatment; theme=dark". So the match has to
// find one value inside a list, anchored at both ends of that value -- an
// unanchored match for "mendel_arm=a" would also fire on "mendel_arm=ab" and
// route one Arm's traffic into another, which is the kind of failure that looks
// like a surprising result rather than a bug.
func cookieMatch(slug string) string {
	return `(^|.*;\s*)` + regexp.QuoteMeta(assigner.CookieName) + `=` + regexp.QuoteMeta(slug) + `(\s*;.*|$)`
}

// Validate reports why this cannot be deployed, or "" when it can.
func (d ExperimentDeployment) Validate() string {
	if strings.TrimSpace(d.Name) == "" {
		return "an experiment needs a name to prefix its resources with"
	}
	if strings.TrimSpace(d.Hostname) == "" {
		// One Gateway serves every deployment, and the hostname is how their
		// traffic is told apart. Without one there is nothing to attach Arm
		// matching to.
		return "an experiment needs a hostname: the routes are matched on it"
	}
	weights := make([]assigner.ArmWeight, 0, len(d.Arms))
	mainline := 0
	for _, a := range d.Arms {
		weights = append(weights, assigner.ArmWeight{Slug: a.Slug, Weight: a.Weight})
		if strings.TrimSpace(a.Slug) == "" {
			return "an Arm with no slug cannot be named in a cookie or matched by a route"
		}
		if a.Slug == assigner.MainlineSlug {
			mainline++
			if a.Backend == "" {
				return "mainline must name the Service it already has; it is not redeployed"
			}
		} else if strings.TrimSpace(a.Image) == "" {
			return fmt.Sprintf("Arm %q has no image to run", a.Slug)
		}
	}
	if mainline != 1 {
		return fmt.Sprintf("exactly one Arm is mainline; this has %d", mainline)
	}
	if msg := assigner.ValidateAllocation(weights); msg != "" {
		return msg
	}
	return ""
}

// Manifest renders the experiment.
func (d ExperimentDeployment) Manifest() (string, error) {
	if msg := d.Validate(); msg != "" {
		return "", fmt.Errorf("%s", msg)
	}

	var b strings.Builder

	// One Deployment and Service per Arm that is not mainline.
	for _, arm := range d.Arms {
		if arm.Slug == assigner.MainlineSlug {
			continue
		}
		name := d.armResource(arm.Slug)
		fmt.Fprintf(&b, `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[7]s
  labels:
    mendel-experiment: %[4]s
    mendel-arm: %[5]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[1]s
  template:
    metadata:
      labels:
        app: %[1]s
        mendel-experiment: %[4]s
        mendel-arm: %[5]s
    spec:
      containers:
      - name: app
        image: %[2]s
        ports:
        - containerPort: %[3]d
        env:
        - name: PORT
          value: "%[3]d"%[6]s
---
apiVersion: v1
kind: Service
metadata:
  name: %[1]s
  namespace: %[7]s
  labels:
    mendel-experiment: %[4]s
spec:
  type: ClusterIP
  selector:
    app: %[1]s
  ports:
  - port: 80
    targetPort: %[3]d
---
`, name, arm.Image, hosting.ContainerPort, d.Name, arm.Slug, d.EnvFrom, hosting.Namespace)
	}

	// The Gateway that does the matching, and a stable Service in front of the
	// proxy it provisions.
	//
	// Two layers, because each does what the other cannot. GKE's Gateway holds
	// the reserved address, terminates TLS and carries the Certificate Manager
	// map -- none of which Envoy Gateway can be given, since it reads
	// certificates from Kubernetes Secrets and Certificate Manager will not
	// export a private key. Envoy Gateway can match a cookie, which GKE's class
	// cannot. So the first stays at the edge and hands the second the traffic.
	fmt.Fprintf(&b, `apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: %[1]s
  namespace: %[4]s
  labels:
    mendel-experiment: %[3]s
spec:
  gatewayClassName: %[2]s
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    allowedRoutes:
      namespaces:
        from: Same
---
`, ExperimentGatewayName, ExperimentGatewayClass, d.Name, hosting.Namespace)

	// Permission for the production route to reach the proxy across the
	// namespace boundary. Gateway API refuses a cross-namespace backend without
	// one, so this is what makes the repoint legal rather than merely intended.
	fmt.Fprintf(&b, `apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    mendel-experiment: %[1]s
spec:
  from:
  - group: gateway.networking.k8s.io
    kind: HTTPRoute
    namespace: %[3]s
  to:
  - group: ""
    kind: Service
---
`, d.Name, ExperimentProxyNamespace, hosting.Namespace)

	// No route is rendered for the production hostname. One already serves it,
	// created by the ordinary deployment, and a second would never take effect:
	// Gateway API ranks matches by path specificity, then method, then header
	// count, and breaks the remaining tie with the older route. The experiment
	// repoints the existing one instead -- see repointProdRoute.

	// The Arm routes, on the Gateway that can match them. An Arm's rule carries a
	// header match, so it outranks the fallback: Gateway API breaks ties by the
	// number of header matches, and the fallback has none.
	fmt.Fprintf(&b, `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: %s
  namespace: %s
  labels:
    mendel-experiment: %s
spec:
  parentRefs:
  - name: %s
  hostnames:
  - %s
  rules:
`, d.Name, hosting.Namespace, d.Name, ExperimentGatewayName, d.Hostname)

	for _, arm := range d.Arms {
		backend := arm.Backend
		if backend == "" {
			backend = d.armResource(arm.Slug)
		}
		fmt.Fprintf(&b, `  - matches:
    - headers:
      - type: RegularExpression
        name: Cookie
        value: %q
    backendRefs:
    - name: %s
      port: 80
`, cookieMatch(arm.Slug), backend)
	}

	// Anything with no Arm cookie is assigned here, by the same response that
	// serves it. The backends are weighted and each sets the cookie naming
	// itself, so the visitor is placed and told about it in one round trip --
	// with nothing to deploy, no redirect, and no request that cannot be
	// redirected because it carried a body.
	//
	// Last, and with no match of its own, so it is the least specific rule.
	fmt.Fprint(&b, "  - backendRefs:\n")
	for _, arm := range d.Arms {
		backend := arm.Backend
		if backend == "" {
			backend = d.armResource(arm.Slug)
		}
		fmt.Fprintf(&b, `    - name: %s
      port: 80
      weight: %d
      filters:
      - type: ResponseHeaderModifier
        responseHeaderModifier:
          add:
          - name: Set-Cookie
            value: %q
`, backend, arm.Weight, assigner.SetCookieValue(arm.Slug, d.Secure))
	}

	return b.String(), nil
}

// armResource names an Arm's Deployment and Service.
func (d ExperimentDeployment) armResource(slug string) string {
	return d.Name + "-" + slug
}

func indent(s, with string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = with + l
	}
	return strings.Join(lines, "\n")
}

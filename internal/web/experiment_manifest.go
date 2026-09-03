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
}

// ExperimentDeployment is everything an experiment puts in the cluster.
type ExperimentDeployment struct {
	// Name prefixes every resource, so one experiment's objects can be found and
	// removed without knowing what they are.
	Name string

	Hostname string
	Arms     []ArmDeployment

	// Allocation is what the assigner reads, as JSON. It lives in a ConfigMap
	// rather than being fetched from Mendel: production traffic must not depend
	// on Mendel being reachable.
	Allocation string

	AssignerImage string
	EnvFrom       string
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
	if strings.TrimSpace(d.Allocation) == "" {
		return "an experiment needs an allocation for the assigner to read"
	}
	if strings.TrimSpace(d.AssignerImage) == "" {
		return "an experiment needs the assigner image"
	}

	mainline := 0
	for _, a := range d.Arms {
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
	if len(d.Arms) < 2 {
		return "an experiment needs mainline and at least one Arm to compare against it"
	}
	return ""
}

// Manifest renders the experiment.
func (d ExperimentDeployment) Manifest() (string, error) {
	if msg := d.Validate(); msg != "" {
		return "", fmt.Errorf("%s", msg)
	}

	var b strings.Builder

	// The allocation, indented into the ConfigMap.
	fmt.Fprintf(&b, `apiVersion: v1
kind: ConfigMap
metadata:
  name: %s-allocation
data:
  allocation.json: |
%s
---
`, d.Name, indent(d.Allocation, "    "))

	// The assigner. One replica is not enough: it is on the path of every
	// visitor's first request, and a restart with one replica turns that into an
	// error rather than a redirect.
	fmt.Fprintf(&b, `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s-assigner
spec:
  replicas: 2
  selector:
    matchLabels:
      app: %[1]s-assigner
  template:
    metadata:
      labels:
        app: %[1]s-assigner
    spec:
      containers:
      - name: assigner
        image: %[2]s
        args: ["-addr=:%[3]d", "-allocation=/etc/mendel/allocation.json"]
        ports:
        - containerPort: %[3]d
        volumeMounts:
        - name: allocation
          mountPath: /etc/mendel
        readinessProbe:
          httpGet:
            path: /healthz
            port: %[3]d
      volumes:
      - name: allocation
        configMap:
          name: %[1]s-allocation
---
apiVersion: v1
kind: Service
metadata:
  name: %[1]s-assigner
spec:
  type: ClusterIP
  selector:
    app: %[1]s-assigner
  ports:
  - port: 80
    targetPort: %[3]d
---
`, d.Name, d.AssignerImage, hosting.ContainerPort)

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
`, name, arm.Image, hosting.ContainerPort, d.Name, arm.Slug, d.EnvFrom)
	}

	// The route. An Arm's rule carries a header match, so it outranks the
	// fallback: Gateway API breaks ties by the number of header matches, and the
	// fallback has none.
	fmt.Fprintf(&b, `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: %s
  labels:
    mendel-experiment: %s
spec:
  parentRefs:
  - name: %s
  hostnames:
  - %s
  rules:
`, d.Name, d.Name, gatewayName, d.Hostname)

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

	// Anything with no Arm cookie goes to the assigner, which gives it one and
	// sends it back. Last, and with no match, so it is the least specific rule.
	fmt.Fprintf(&b, `  - backendRefs:
    - name: %s-assigner
      port: 80
`, d.Name)

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

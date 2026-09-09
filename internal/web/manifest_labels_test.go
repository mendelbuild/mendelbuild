package web

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// labelBearingKeys are the places a Kubernetes object holds label values: the
// labels themselves, and the selectors that match them. Every one of those maps
// is map[string]string in the API types, so a value YAML reads as a boolean, a
// number or a timestamp fails the whole object -- "cannot unmarshal bool into
// ... labels of type string" -- rather than just the field.
var labelBearingKeys = map[string]bool{
	"labels":      true,
	"matchLabels": true,
	"selector":    true, // a Service's selector is a bare label map
}

// collectLabelValues walks a parsed manifest and returns every label value in
// it, keyed by its path, as it parsed. A value that YAML turned into something
// other than a string is reported as whatever it became, which is the only way
// to tell `true` from `"true"` -- the broken rendering is exactly the text its
// author meant to write.
func collectLabelValues(node any, path string, inLabels bool, out map[string]any) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			p := path + "." + k
			if inLabels {
				// A Deployment's selector holds matchLabels rather than labels
				// directly, so a map inside a label-bearing key is still a
				// label-bearing key; only scalars are values.
				if _, nested := child.(map[string]any); !nested {
					out[p] = child
					continue
				}
			}
			collectLabelValues(child, p, inLabels || labelBearingKeys[k], out)
		}
	case []any:
		for i, child := range v {
			collectLabelValues(child, fmt.Sprintf("%s[%d]", path, i), inLabels, out)
		}
	}
}

// parsedLabelValues reads a multi-document manifest the way the API server does
// and returns every label value across every document.
func parsedLabelValues(t *testing.T, manifest string) map[string]any {
	t.Helper()
	out := map[string]any{}
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	// Documents are numbered, because the same path appears in every one of
	// them and a shared key would let one object's labels hide another's.
	for i := 0; ; i++ {
		var doc any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		collectLabelValues(doc, fmt.Sprintf("doc[%d]", i), false, out)
	}
	if len(out) == 0 {
		t.Fatalf("no labels found at all in:\n%s", manifest)
	}
	return out
}

// assertAllStrings is the whole invariant: whatever a label value came from, it
// has to arrive as a string.
func assertAllStrings(t *testing.T, values map[string]any, manifest string) {
	t.Helper()
	bad := false
	for path, got := range values {
		if _, isString := got.(string); !isString {
			t.Errorf("label %s parsed as %T (%v), not a string -- the API server "+
				"refuses the whole object for this", path, got, got)
			bad = true
		}
	}
	if bad {
		t.Logf("manifest:\n%s", manifest)
	}
}

// requireLabelKeys guards against the sweep going vacuous: if the manifests are
// restructured and a label moves, the test should fail rather than quietly stop
// looking at it.
func requireLabelKeys(t *testing.T, values map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		found := false
		for path := range values {
			if strings.HasSuffix(path, "."+key) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no %s label was swept, so nothing here checks it", key)
		}
	}
}

// An Arm's slug is a Variation name, slugified, and it is rendered straight into
// mendel-arm. A Variation called "True" or "0755" is a perfectly ordinary name
// and a perfectly valid Kubernetes label value; unquoted, YAML makes one a
// boolean and the other an octal number, and the experiment fails to deploy with
// an error that points at Kubernetes rather than at the name.
func TestExperimentManifestLabelValuesStayStrings(t *testing.T) {
	d := experimentFixture()
	d.Arms[1].Slug = "true"
	d.Arms[2].Slug = "0755"

	manifest, err := d.Manifest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	values := parsedLabelValues(t, manifest)
	assertAllStrings(t, values, manifest)
	requireLabelKeys(t, values, "app", "mendel-arm", "mendel-experiment")

	// And they have to arrive as the right strings, not merely as strings.
	for _, slug := range []string{"true", "0755"} {
		if !hasLabelValue(values, "mendel-arm", slug) {
			t.Errorf("no mendel-arm label carries %q", slug)
		}
		if !hasLabelValue(values, "app", "exp-checkout-"+slug) {
			t.Errorf("no app label carries the Arm resource for %q", slug)
		}
	}
	if !hasLabelValue(values, "mendel-experiment", "exp-checkout") {
		t.Error("nothing carries the experiment label teardown finds objects by")
	}
}

// hasLabelValue reports whether some label named key came back as want.
func hasLabelValue(values map[string]any, key, want string) bool {
	for path, got := range values {
		if strings.HasSuffix(path, "."+key) && got == want {
			return true
		}
	}
	return false
}

// The ordinary deploy's manifest labels and selects on the deployment name. A
// deployment name is composed rather than typed, so it is not the likely
// offender -- but the renderer takes it as an argument, and its contract is the
// one every manifest has: hand it any valid name and the labels stay strings.
//
// This says nothing about metadata.name, which is substituted from the same
// argument and is equally a string field. That is a separate hole.
func TestDeploymentManifestLabelValuesStayStrings(t *testing.T) {
	for _, name := range []string{"pong-game-prod", "0755", "true"} {
		t.Run(name, func(t *testing.T) {
			manifest := k8sManifestFor(name, "gcr.io/x/pong:1", "", "", "")
			values := parsedLabelValues(t, manifest)
			assertAllStrings(t, values, manifest)
			requireLabelKeys(t, values, "app")
			if !hasLabelValue(values, "app", name) {
				t.Errorf("no app label carries %q", name)
			}
		})
	}
}

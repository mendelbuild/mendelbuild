package web

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// parseManifest reads a rendered manifest the way the API server does, which is
// the only reading that decides anything. Asserting on the text would have
// passed against the broken version -- `mendel-adapter: true` is exactly the
// string its author intended to write.
func parseManifest(t *testing.T, manifest string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal([]byte(manifest), &out); err != nil {
		t.Fatalf("rendered manifest is not YAML: %v\n%s", err, manifest)
	}
	return out
}

// The bug: `mendel-adapter: true` unquoted is a YAML boolean, and the API server
// refused the Secret with "cannot unmarshal bool into ... labels of type
// string". Every label value Kubernetes accepts must survive as a string.
func TestLabelValuesStayStrings(t *testing.T) {
	// All valid Kubernetes label values, and all things YAML reads as something
	// other than a string when left bare.
	awkward := map[string]string{
		"mendel-adapter": "true",
		"enabled":        "false",
		"short":          "y",
		"absent":         "null",
		"count":          "12",
		"version":        "1.20",
		"ordinary":       "mendel-exp-abc123",
	}

	manifest := envSecretManifest("some-env", map[string]string{"K": "v"}, awkward)
	doc := parseManifest(t, manifest)

	meta, ok := doc["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("no metadata in:\n%s", manifest)
	}
	labels, ok := meta["labels"].(map[string]any)
	if !ok {
		t.Fatalf("no labels in:\n%s", manifest)
	}

	for k, want := range awkward {
		got, present := labels[k]
		if !present {
			t.Errorf("label %q was not rendered", k)
			continue
		}
		s, isString := got.(string)
		if !isString {
			t.Errorf("label %q parsed as %T (%v), not a string -- the API server "+
				"refuses the whole Secret for this", k, got, got)
			continue
		}
		if s != want {
			t.Errorf("label %q = %q, want %q", k, s, want)
		}
	}
}

// A Secret with no labels is the ordinary deploy's case, and an empty `labels:`
// key is not the same object as no labels at all.
func TestNoLabelsRendersNoLabelsKey(t *testing.T) {
	doc := parseManifest(t, envSecretManifest("some-env", map[string]string{"K": "v"}, nil))
	meta := doc["metadata"].(map[string]any)
	if _, present := meta["labels"]; present {
		t.Error("an unlabelled Secret rendered a labels key")
	}
}

// Values are the reason the Secret exists, and they are arbitrary: a private key
// spans lines, a connection string is full of punctuation, and a password may be
// nothing but digits.
func TestValuesSurviveWhateverTheyContain(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL": "postgres://u:p@host:5432/db?sslmode=disable",
		"PRIVATE_KEY":  "-----BEGIN KEY-----\nabc\ndef\n-----END KEY-----",
		"NUMERIC":      "0123456",
		"YAMLISH":      "yes",
		"COLONS":       "a: b: c",
	}

	doc := parseManifest(t, envSecretManifest("some-env", values, nil))
	data, ok := doc["stringData"].(map[string]any)
	if !ok {
		t.Fatal("no stringData")
	}
	for k, want := range values {
		got, present := data[k]
		if !present {
			t.Errorf("value %q was not rendered", k)
			continue
		}
		if s, isString := got.(string); !isString {
			t.Errorf("value %q parsed as %T, not a string", k, got)
		} else if s != want {
			t.Errorf("value %q = %q, want %q", k, s, want)
		}
	}
}

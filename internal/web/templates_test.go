package web

import (
	"html/template"
	"io/fs"
	"strings"
	"testing"
)

// TestTemplatesParse parses every embedded template the same way the handlers
// do. Templates are parsed at request time via template.Must, so a syntax error
// does not fail the build — it panics on the first request to that page. This
// catches it at test time instead.
func TestTemplatesParse(t *testing.T) {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		t.Fatalf("reading embedded templates: %v", err)
	}

	found := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".html") {
			continue
		}
		found++

		switch name {
		case "layout.html":
			// Parsed as part of every page below.
			continue
		case "login.html":
			// Rendered standalone, without the layout.
			t.Run(name, func(t *testing.T) {
				if _, err := template.New("").ParseFS(templatesFS, "templates/"+name); err != nil {
					t.Errorf("parse: %v", err)
				}
			})
		default:
			t.Run(name, func(t *testing.T) {
				_, err := template.New("").Funcs(templateFuncs).
					ParseFS(templatesFS, "templates/layout.html", "templates/"+name)
				if err != nil {
					t.Errorf("parse: %v", err)
				}
			})
		}
	}

	if found == 0 {
		t.Fatal("no templates found in the embedded FS")
	}
}

// TestNoStatusRendersFailureAsSuccess guards a bug class that recurred in three
// separate templates: mapping a failed or losing Variation to status-resolved,
// which is success green. `terminated` is a code/test failure and `rejected`
// means another Variation won, so neither may share the winner's styling.
func TestNoStatusRendersFailureAsSuccess(t *testing.T) {
	banned := []struct {
		file    string
		snippet string
		why     string
	}{
		{"variation_detail.html", `"terminated"}}status-resolved`, "terminated is a code/test failure, not a success"},
		{"hop_detail.html", `"terminated"}}status-resolved`, "terminated is a code/test failure, not a success"},
		{"input_request_selection.html", `"rejected"}}status-resolved`, "a rejected Variation lost; it must not look like the winner"},
	}

	for _, b := range banned {
		data, err := fs.ReadFile(templatesFS, "templates/"+b.file)
		if err != nil {
			t.Fatalf("reading %s: %v", b.file, err)
		}
		if strings.Contains(string(data), b.snippet) {
			t.Errorf("%s: found %q — %s", b.file, b.snippet, b.why)
		}
	}
}

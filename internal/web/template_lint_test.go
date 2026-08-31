package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

)

// Lint rules for the templates.
//
// These are the reason the design system is a rev rather than a repaint. Each
// one states a rule that was broken everywhere before, and each is mechanical
// enough that nobody has to argue about a particular case: the answer is always
// "put it in the CSS", or "put it in the domain".
//
// They read the files on disk rather than the embedded copies, so a failure
// names a file you can open.

// templateSources returns every template as (path, contents).
func templateSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join("templates")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("reading templates: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("found no templates; the lint rules would pass vacuously")
	}
	return out
}

// reportLines prints each offending line with its number, so a failure is
// actionable without grepping.
func reportLines(t *testing.T, path, body string, re *regexp.Regexp, rule string) {
	t.Helper()
	for i, line := range strings.Split(body, "\n") {
		if re.MatchString(line) {
			t.Errorf("%s:%d: %s\n    %s", path, i+1, rule, strings.TrimSpace(line))
		}
	}
}

// A style attribute is a decision made in one place that cannot be reused,
// reviewed, or changed anywhere else. There were 683 of them.
//
// The rule is absolute deliberately. "Just this once, for a width" is how the
// last set accumulated, and a computed length has a way out: put it in a data
// attribute and let a script apply it (see the meter, in partials.html).
func TestTemplatesHaveNoInlineStyles(t *testing.T) {
	re := regexp.MustCompile(`\sstyle\s*=\s*"`)
	for path, body := range templateSources(t) {
		reportLines(t, path, body, re,
			`inline style attribute; use a class from components.css, or a data attribute the page's script reads`)
	}
}

// A colour literal in a template is a colour chosen by eye at that one site.
// That is how the app came to have about forty of them, saying nothing
// consistent. Colours live in tokens.css and are referenced by name.
//
// A <style> block inside a template is still allowed -- a page with genuinely
// page-specific structure is better off saying so next to the markup -- but it
// must express itself in tokens like everything else.
func TestTemplatesHaveNoColourLiterals(t *testing.T) {
	// Requires whitespace, an opening bracket or a comma before the hash, which
	// is what a colour looks like in CSS. It keeps href="#anchor" out of the
	// results without needing an exemption list.
	hex := regexp.MustCompile(`[\s(,]#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})\b`)
	fn := regexp.MustCompile(`\b(?:rgba?|hsla?)\s*\(`)

	for path, body := range templateSources(t) {
		reportLines(t, path, body, hex, "colour literal; use a token from tokens.css")
		reportLines(t, path, body, fn, "colour literal; use a token from tokens.css")
	}
}

// The rule internal/domain/lifecycle.go opens by stating, finally checkable.
//
// A status enum is a storage detail that conflates several concerns -- a
// Variation's mixes build progress, runtime state and adjudication outcome --
// so a template branching on one will sooner or later paint a failure the
// colour of a success. Every status a page shows has a plain-English reading in
// the domain: a Ribbon for the things with stages, a StatusView for the rest.
func TestTemplatesDoNotBranchOnRawStatus(t *testing.T) {
	re := regexp.MustCompile(`\b(?:eq|ne)\s+\.[A-Za-z0-9_.]*Status\b`)
	for path, body := range templateSources(t) {
		reportLines(t, path, body, re,
			"branches on a raw status string; read a domain.Ribbon or domain.StatusView instead")
	}
}

// Every class a template uses must exist in the stylesheets.
//
// A class name that matches nothing renders as unstyled markup, which looks
// like a layout bug and gets diagnosed as one. This catches the typo at the
// source -- and it is also what stops someone inventing a seventh tone:
// `badge-danger` is not a class anybody defined, so it fails here.
func TestTemplateClassesExistInTheStylesheets(t *testing.T) {
	var css strings.Builder
	for _, name := range []string{"tokens.css", "components.css"} {
		body, err := os.ReadFile(filepath.Join("static", "css", name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		css.Write(body)
	}
	// Templates may also carry a page-specific <style> block.
	for _, body := range templateSources(t) {
		css.WriteString(body)
	}
	stylesheet := css.String()

	// Classes applied by JavaScript rather than named in a stylesheet, plus the
	// hooks that scripts and tests select on.
	allowed := map[string]bool{
		"variation-checkbox": true, "winner-radio": true, "grade-cell": true,
		"grade-loading": true, "hop-status": true, "hop-objectives": true,
		"is-existing": true, "quality-badge-wrapper": true, "description-field": true,
		"target-field": true, "date-field": true, "btn-group": true, "active": true,
	}

	classAttr := regexp.MustCompile(`class="([^"{}]*)"`)
	seen := map[string][]string{}
	for path, body := range templateSources(t) {
		for _, m := range classAttr.FindAllStringSubmatch(body, -1) {
			for _, name := range strings.Fields(m[1]) {
				if allowed[name] || strings.Contains(stylesheet, "."+name) {
					continue
				}
				seen[name] = append(seen[name], path)
			}
		}
	}
	if len(seen) == 0 {
		return
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Errorf("class %q is used in %v but defined in no stylesheet", name, seen[name])
	}
}

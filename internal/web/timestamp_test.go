package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Instants are shown in the reader's zone; calendar days are not.
//
// Both halves matter and they fail in opposite directions. An instant left as a
// plain .Format is rendered in whatever zone the server happens to run in,
// which is how a laptop in California came to be told its own deploy started at
// six in the evening. A calendar day pushed through the same machinery is
// worse: "due 1 November" becomes 31 October for every reader west of UTC, an
// off-by-one on the very fact the row exists to state.
//
// The naming carries the distinction, so these rules maintain themselves. Every
// instant column in this schema is named for when something happened --
// created_at, resolved_at, measured_at -- and no calendar day is.

var (
	// A Go time being formatted in a template.
	templateFormat = regexp.MustCompile(`\{\{([A-Za-z0-9_.$]+)\.Format "`)
	// A value handed to the localtime helper.
	templateAt = regexp.MustCompile(`\{\{at ([A-Za-z0-9_.$]+) "([a-z]+)"\}\}`)
	// Fields that name a moment. Anything ending in At is one.
	instantField = regexp.MustCompile(`At$`)
)

// calendarDays are the columns that are a day rather than a moment. Kept as a
// list because there is no naming rule that separates them -- which is exactly
// why they need writing down somewhere a test can read.
var calendarDays = []string{"TargetDate", "PeriodStart", "PeriodEnd"}

func eachTemplate(t *testing.T, fn func(name, body string)) {
	t.Helper()
	entries, err := os.ReadDir("templates")
	if err != nil {
		t.Fatalf("reading templates: %v", err)
	}
	seen := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("templates", e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		seen++
		fn(e.Name(), string(body))
	}
	if seen == 0 {
		t.Fatal("found no templates; this check has stopped checking anything")
	}
}

// An instant rendered by the server is rendered in the server's zone, which is
// nobody's. They go through the helper, which hands the job to the browser.
func TestInstantsAreShownInTheReadersZone(t *testing.T) {
	eachTemplate(t, func(name, body string) {
		for i, line := range strings.Split(body, "\n") {
			for _, m := range templateFormat.FindAllStringSubmatch(line, -1) {
				field := m[1]
				if idx := strings.LastIndex(field, "."); idx >= 0 {
					field = field[idx+1:]
				}
				if instantField.MatchString(field) {
					t.Errorf(`%s:%d: %s.Format renders an instant in the server's zone; `+
						`use {{at %s "datetime"}} so the reader sees their own clock`,
						name, i+1, m[1], m[1])
				}
			}
		}
	})
}

// A day is a day everywhere. Shifting one by a zone moves the date it is about.
func TestCalendarDaysAreNotShifted(t *testing.T) {
	eachTemplate(t, func(name, body string) {
		for i, line := range strings.Split(body, "\n") {
			for _, m := range templateAt.FindAllStringSubmatch(line, -1) {
				for _, day := range calendarDays {
					if strings.Contains(m[1], day) {
						t.Errorf(`%s:%d: %s is a calendar day, not a moment; `+
							`shown in the reader's zone it moves to the day before `+
							`for anyone west of UTC`, name, i+1, m[1])
					}
				}
			}
		}
	})
}

// The helper only knows a few shapes, and a name it does not know silently
// becomes "datetime" at runtime -- a log line would quietly lose its seconds.
func TestOnlyKnownTimeShapesAreAskedFor(t *testing.T) {
	eachTemplate(t, func(name, body string) {
		for i, line := range strings.Split(body, "\n") {
			for _, m := range templateAt.FindAllStringSubmatch(line, -1) {
				if _, ok := timeShapes[m[2]]; !ok {
					t.Errorf("%s:%d: %q is not a time shape; the helper knows %v",
						name, i+1, m[2], shapeNames())
				}
			}
		}
	})
}

// The Go fallback and the browser script each hold their own copy of the
// shapes, because one is a Go layout and the other an Intl option set. They
// have to name the same list.
func TestGoAndBrowserAgreeOnTheShapes(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("static", "js", "localtime.js"))
	if err != nil {
		t.Fatalf("reading localtime.js: %v", err)
	}
	for name := range timeShapes {
		if !regexp.MustCompile(`\b` + name + `:`).Match(script) {
			t.Errorf("localtime.js has no %q shape, so the browser would fall back to "+
				"datetime and quietly render it in the wrong shape", name)
		}
	}
}

func shapeNames() []string {
	var out []string
	for name := range timeShapes {
		out = append(out, name)
	}
	return out
}

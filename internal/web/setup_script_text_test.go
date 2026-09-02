package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/hosting"
)

// A setup script is pasted into an interactive shell, and a final line with no
// newline is a line the shell has not been told is finished. Under bracketed
// paste that leaves the whole block sitting at a continuation prompt: the user
// presses Enter, sees a continuation marker, and nothing runs. The script parses
// perfectly, which is why this went unnoticed for as long as it did.
func TestSetupScriptTextEndsWithNewline(t *testing.T) {
	for _, p := range hosting.DefaultPlatforms() {
		if p.SetupScript == "" {
			continue
		}
		if got := setupScriptText(p.SetupScript); !strings.HasSuffix(got, "\n") {
			t.Errorf("%s: setup script does not end with a newline", p.Slug)
		}
	}

	if got := setupScriptText(""); got != "" {
		t.Errorf("empty script gained a newline: %q", got)
	}
	if got := setupScriptText("echo hi\n"); !strings.HasSuffix(got, "echo hi\n") {
		t.Errorf("a terminated script was terminated twice: %q", got)
	}
	if got := setupScriptText("echo hi"); !strings.HasSuffix(got, "echo hi\n") {
		t.Errorf("an unterminated script was not terminated: %q", got)
	}
}

// Defence in depth behind the setopt line, which is the actual fix.
//
// With comments enabled an apostrophe in one is harmless, so this is not what
// makes the scripts work. It governs how they fail if the setopt ever does not
// take: a comment zsh runs as a command is noise, and a comment that opens a
// quote hangs the terminal. Only one of those is recoverable by someone who does
// not already know what went wrong.
func TestSetupScriptCommentsCannotOpenAQuote(t *testing.T) {
	for _, p := range hosting.DefaultPlatforms() {
		for i, line := range strings.Split(p.SetupScript, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "#") {
				continue
			}
			if n := strings.IndexAny(trimmed, "'\"`"); n >= 0 {
				t.Errorf("%s:%d comment contains %q, which opens a quote in a shell that "+
					"does not treat # as a comment:\n\t%s", p.Slug, i+1, trimmed[n:n+1], trimmed)
			}
		}
	}
}

// The normalization only helps at the places the script actually reaches a
// template. Assigning the raw field instead is the regression, and it is
// invisible in every test that does not involve a real shell.
func TestSetupScriptReachesTemplatesNormalized(t *testing.T) {
	raw := regexp.MustCompile(`=\s*(?:channel\.HostingPlatform|p)\.SetupScript\b`)

	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range matches {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if raw.MatchString(line) {
				t.Errorf("%s:%d assigns SetupScript directly; wrap it in setupScriptText so the "+
					"script reaches the clipboard with a trailing newline:\n\t%s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

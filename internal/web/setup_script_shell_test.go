package web

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/hosting"
)

// A setup script is pasted into an interactive shell, and an interactive shell is
// not the thing any syntax check tests.
//
// zsh enables INTERACTIVE_COMMENTS unconditionally only in *non-interactive*
// shells, so `zsh -n` on a script full of comments passes while the same text
// pasted at a prompt does not: every comment runs as a command named #, and one
// containing an apostrophe opens a quote that never closes, leaving the block at
// a continuation prompt having executed nothing. That is not a hypothetical --
// it is what shipped, and it survived a syntax check, a code review and months of
// use because the only shell that reproduces it is a real interactive one.
//
// So use a real interactive one. The external commands are stubbed because the
// question is whether the shell will read the script, not whether the cloud
// accepts it.
func TestSetupScriptsSurviveAnInteractiveShell(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh on this host")
	}

	const stubs = `
gcloud() { : ; }
flyctl() { : ; }
fly() { : ; }
kubectl() { : ; }
`

	for _, p := range hosting.DefaultPlatforms() {
		if p.SetupScript == "" {
			continue
		}
		t.Run(p.Slug, func(t *testing.T) {
			script := setupScriptText(p.SetupScript)

			// The UI will not let the script be copied until the placeholder is
			// filled, so testing it unfilled would only prove the deliberate
			// syntax error still works.
			script = setupPlaceholder.ReplaceAllString(script, "placeholder-value")

			// -f skips the user's rc files: a framework that happens to enable
			// interactive_comments would hide exactly the bug being tested.
			cmd := exec.Command(zsh, "-f", "-i")
			cmd.Stdin = strings.NewReader(stubs + script)
			out, _ := cmd.CombinedOutput()

			for _, symptom := range []struct{ needle, means string }{
				{"quote>", "an unterminated quote left the shell at a continuation prompt"},
				{"command not found: #", "# was not treated as a comment"},
				{"parse error", "the shell could not parse the script"},
				{"unmatched", "an unmatched quote or bracket"},
			} {
				if strings.Contains(string(out), symptom.needle) {
					t.Errorf("%s: %s (found %q)\n%s",
						p.Slug, symptom.means, symptom.needle, tail(string(out)))
				}
			}
		})
	}
}

func tail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, "\n")
}

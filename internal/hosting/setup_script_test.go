package hosting

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSetupScriptsSurviveASecondRun guards the property CLAUDE.md requires of
// every channel's setup script: pasting it again must not fail partway.
//
// Re-running is the normal case — a lost key, a permission error fixed halfway
// down, a colleague repeating the setup. The line that makes this true is a
// guard on a creation step, and a guard looks like clutter to anyone tidying the
// script later. This is what makes removing one fail loudly.
func TestSetupScriptsSurviveASecondRun(t *testing.T) {
	// Creation commands that abort when the resource already exists. Each needs
	// a guard, because the second run is the one that hits them.
	needsGuard := []string{
		"service-accounts create",
		"clusters create",
		"repositories create",
		"apps create",
	}

	for _, platform := range DefaultPlatforms() {
		if strings.TrimSpace(platform.SetupScript) == "" {
			continue // Unselectable platforms carry no script; see CLAUDE.md.
		}

		for i, line := range strings.Split(platform.SetupScript, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			for _, cmd := range needsGuard {
				if !strings.Contains(trimmed, cmd) {
					continue
				}
				if !isGuarded(trimmed) {
					t.Errorf("%s setup script, line %d creates a resource that may already exist "+
						"but is not guarded, so a second run fails here:\n  %s",
						platform.Slug, i+1, trimmed)
				}
			}
		}
	}
}

// isGuarded reports whether a creation command tolerates the resource already
// existing.
func isGuarded(line string) bool {
	return strings.Contains(line, "|| true") ||
		strings.Contains(line, "||true") ||
		strings.Contains(line, "2>/dev/null ||") ||
		strings.Contains(line, "if !")
}

// TestSetupScriptsFailLoudlyWhenUnedited covers the one line the user must
// change before pasting.
//
// A placeholder that is merely a plausible-looking value gets pasted unedited,
// and the script then runs happily against a project that does not exist. The
// convention is a bracketed <YOUR_..._HERE> token, which bash refuses to parse,
// so an unedited paste stops on line one instead of part-way through.
//
// The syntax error is asserted by running bash over the line, not assumed:
// whether a given placeholder actually fails to parse is exactly the sort of
// thing that is obvious right up until it is wrong.
func TestSetupScriptsFailLoudlyWhenUnedited(t *testing.T) {
	placeholder := regexp.MustCompile(`<YOUR_[A-Z_]+_HERE>`)

	for _, platform := range DefaultPlatforms() {
		if strings.TrimSpace(platform.SetupScript) == "" {
			continue
		}

		// Comments may name the placeholder to explain it; what must be unique
		// is the line the user actually edits.
		var editLines []string
		for _, line := range strings.Split(platform.SetupScript, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if placeholder.MatchString(trimmed) {
				editLines = append(editLines, trimmed)
			}
		}
		if len(editLines) != 1 {
			t.Errorf("%s: %d lines carry a placeholder, want exactly one to edit: %v",
				platform.Slug, len(editLines), editLines)
			continue
		}
		editLine := editLines[0]
		var varName string
		if !strings.HasPrefix(editLine, "export ") {
			t.Errorf("%s: edit line should export the value so the rest of the script reads it: %s",
				platform.Slug, editLine)
			continue
		}
		varName = strings.TrimPrefix(strings.SplitN(editLine, "=", 2)[0], "export ")

		// Unedited, the line must not parse.
		if err := exec.Command("bash", "-n", "-c", editLine).Run(); err == nil {
			t.Errorf("%s: %q parses as valid shell, so pasting it unedited would run the "+
				"whole script against a placeholder", platform.Slug, editLine)
		}

		// The whole script must behave the same way, not just that line: unedited
		// it stops, and once the page has filled the value in it runs as pasted.
		// That second half is the point of collecting the value in the browser,
		// so it is worth asserting over the entire script rather than one line.
		if err := exec.Command("bash", "-n", scriptFile(t, platform.SetupScript)).Run(); err == nil {
			t.Errorf("%s: the unedited script parses, so nothing stops a blind paste", platform.Slug)
		}
		filled := placeholder.ReplaceAllString(platform.SetupScript, "a-real-value")
		if err := exec.Command("bash", "-n", scriptFile(t, filled)).Run(); err != nil {
			t.Errorf("%s: the script does not parse once the value is filled in, so what the "+
				"copy button hands over would not run: %v", platform.Slug, err)
		}

		// And the rest of the script has to read what was exported.
		rest := platform.SetupScript[strings.Index(platform.SetupScript, editLine)+len(editLine):]
		if !strings.Contains(rest, "$"+varName) {
			t.Errorf("%s: nothing after the edit line uses $%s, so editing it would change nothing",
				platform.Slug, varName)
		}
	}
}

// TestSelectableChannelsHaveASetupScript makes the rule enforceable rather than
// merely written down: a channel a user can actually choose must tell them how
// to obtain its credentials.
//
// Only platforms in DefaultCombos can be chosen. The rest are entries with no
// pairing, so they are exempt -- writing setup guidance nobody can reach, and
// nobody has run, is the failure this whole area already had once.
func TestSelectableChannelsHaveASetupScript(t *testing.T) {
	scripts := map[string]string{}
	for _, p := range DefaultPlatforms() {
		scripts[p.Slug] = p.SetupScript
	}

	for _, combo := range DefaultCombos() {
		script, known := scripts[combo.PlatformSlug]
		if !known {
			t.Errorf("combo %s/%s names a platform that does not exist",
				combo.ArtifactKind, combo.PlatformSlug)
			continue
		}
		if strings.TrimSpace(script) == "" {
			t.Errorf("%s is selectable as a %s channel but has no setup script, so a user "+
				"who picks it is told which credentials are needed and not how to get them",
				combo.PlatformSlug, combo.ArtifactKind)
		}
	}
}

// TestSetupScriptsPrintEveryRequiredCredential is the rule behind the whole
// point of these scripts: a user should finish one holding the values, not
// holding output they have to interpret.
//
// The GKE script once ended with a cluster table and a page of JSON, and left
// the reader to work out that NAME was GKE_CLUSTER_NAME, that LOCATION was
// GKE_ZONE, and that the entire JSON document was one field. Every name the
// channel will ask for must appear in the script that produces it.
func TestSetupScriptsPrintEveryRequiredCredential(t *testing.T) {
	scripts := map[string]string{}
	for _, p := range DefaultPlatforms() {
		scripts[p.Slug] = p.SetupScript
	}

	for _, combo := range DefaultCombos() {
		script := scripts[combo.PlatformSlug]
		if strings.TrimSpace(script) == "" {
			continue // Covered by TestSelectableChannelsHaveASetupScript.
		}
		for _, name := range combo.RequiredCredentials {
			if !strings.Contains(script, name) {
				t.Errorf("%s asks for %s but its setup script never names it, so the user "+
					"is left to work out which part of the output that is",
					combo.PlatformSlug, name)
			}
		}
	}
}

// TestSetupScriptsDeclareTheirInput ties the placeholder in a script to a prompt
// the page can ask for.
//
// The script cannot supply this value itself, so either the user edits the
// script or the page collects the value and completes it for them. The second
// only works if the platform says what to ask for, and an undeclared prompt
// silently falls back to the first.
func TestSetupScriptsDeclareTheirInput(t *testing.T) {
	placeholder := regexp.MustCompile(`<YOUR_[A-Z_]+_HERE>`)

	for _, p := range DefaultPlatforms() {
		if strings.TrimSpace(p.SetupScript) == "" {
			continue
		}
		if !placeholder.MatchString(p.SetupScript) {
			continue
		}
		if strings.TrimSpace(p.SetupInputLabel) == "" {
			t.Errorf("%s: script has a placeholder but no SetupInputLabel, so the page cannot "+
				"ask for the value and the user is left editing the script by hand", p.Slug)
		}
		if len(p.SetupPrerequisites) == 0 {
			t.Errorf("%s: no prerequisites declared; they were prose before and read as ragged "+
				"columns, which is the reason they are data now", p.Slug)
		}
	}
}


// scriptFile writes a script to a temp file so bash can parse it whole. Checking
// one line at a time would miss anything spanning lines, which is most of what a
// setup script is made of.
func scriptFile(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

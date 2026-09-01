package hosting

import (
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

// TestGKESetupScriptEditsOneLine checks the shape the instructions promise:
// edit the first line, paste the rest unchanged. If the placeholder moves or
// gains a second occurrence, the prose stops being true.
func TestGKESetupScriptEditsOneLine(t *testing.T) {
	var script string
	for _, p := range DefaultPlatforms() {
		if p.Slug == "gke" {
			script = p.SetupScript
		}
	}
	if script == "" {
		t.Fatal("the gke platform has no setup script")
	}

	if n := strings.Count(script, "your-project-id"); n != 1 {
		t.Errorf("placeholder appears %d times; the instructions promise a single edit", n)
	}

	// Everything after the assignment has to read the value rather than repeat
	// the literal, or editing one line would not be enough.
	body := script[strings.Index(script, "your-project-id"):]
	if !strings.Contains(body, `"$PROJECT"`) {
		t.Error("script does not use $PROJECT after the line the user edits")
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

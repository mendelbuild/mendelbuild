package codegen

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validDeclaration() DeclaredExperiment {
	return DeclaredExperiment{
		AssignmentUnit: "user",
		AssignmentKey:  DeclaredAssignmentKey{Source: "cookie", Name: "session_id"},
		Migration: &DeclaredMigration{
			Up:   "ALTER TABLE orders ADD COLUMN mendel_exp_b_score INT;",
			Down: "ALTER TABLE orders DROP COLUMN mendel_exp_b_score;",
		},
		Dissonance: "Scores shown against past orders disappear.",
	}
}

func TestValidDeclarationIsAccepted(t *testing.T) {
	d := validDeclaration()
	if msg := d.Validate(); msg != "" {
		t.Errorf("a well-formed declaration was rejected: %s", msg)
	}
	// A presentation-only experiment declares no migration and is still valid;
	// it simply needs strictly less than one that does.
	d.Migration = nil
	if msg := d.Validate(); msg != "" {
		t.Errorf("an experiment with no schema change was rejected: %s", msg)
	}
}

func TestDeclarationRejectsWhatCannotBeRun(t *testing.T) {
	for name, mutate := range map[string]func(*DeclaredExperiment){
		"no assignment unit":      func(d *DeclaredExperiment) { d.AssignmentUnit = "" },
		"invented assignment unit": func(d *DeclaredExperiment) { d.AssignmentUnit = "visitor" },
		"no key source":           func(d *DeclaredExperiment) { d.AssignmentKey.Source = "" },
		"invented key source":     func(d *DeclaredExperiment) { d.AssignmentKey.Source = "magic" },
		"no key name":             func(d *DeclaredExperiment) { d.AssignmentKey.Name = "  " },
		// An Arm that cannot be withdrawn cannot be run, so a missing down is
		// not a lesser version of a declaration -- it is not one.
		"no down migration": func(d *DeclaredExperiment) { d.Migration.Down = "" },
		"no up migration":   func(d *DeclaredExperiment) { d.Migration.Up = "" },
	} {
		t.Run(name, func(t *testing.T) {
			d := validDeclaration()
			mutate(&d)
			if msg := d.Validate(); msg == "" {
				t.Errorf("%s was accepted", name)
			}
		})
	}
}

// Concurrent Arms apply their migrations to one database at the same time, and
// namespacing is the only thing keeping two of them from colliding. Admission
// checks this properly against the objects the change actually created; this
// catches the case where nothing was even attempted, where the fix is in the
// generated code rather than in the database.
func TestUnnamespacedMigrationIsRejectedEarly(t *testing.T) {
	d := validDeclaration()
	d.Migration.Up = "ALTER TABLE orders ADD COLUMN score INT;"

	msg := d.Validate()
	if msg == "" {
		t.Fatal("an unnamespaced migration was accepted")
	}
	if !strings.Contains(msg, "mendel_exp_") {
		t.Errorf("the message should name the prefix that is missing: %s", msg)
	}
}

// Per-request assignment means one person meets both Arms, so the same row would
// be written by two Arms' logic. A derivation from the unit, not a rule beside
// it -- and it is caught here because the two halves are declared together.
func TestPerRequestAssignmentCannotCarryAMigration(t *testing.T) {
	d := validDeclaration()
	d.AssignmentUnit = "request"

	if msg := d.Validate(); msg == "" {
		t.Error("per-request assignment with durable writes was accepted")
	}

	// Without the migration it is a coherent presentation-only experiment.
	d.Migration = nil
	if msg := d.Validate(); msg != "" {
		t.Errorf("per-request assignment with no writes should be fine: %s", msg)
	}
}

// The slug appears in the assignment cookie and in the route match, so it has to
// be readable and it has to be unique. Variation names are neither.
func TestArmSlugIsReadableAndUnique(t *testing.T) {
	a, b := uuid.New(), uuid.New()

	first := armSlug("Google OAuth", a)
	second := armSlug("Google OAuth", b)
	if first == second {
		t.Error("two variations with one name produced one slug; the Arms would share a route")
	}
	if !strings.HasPrefix(first, "google-oauth-") {
		t.Errorf("slug should still read as the variation it is: %q", first)
	}

	// Whatever the name, the result has to be usable in a cookie and a hostname.
	for _, name := range []string{"", "   ", "!!!", "Ünïcødé Ñame", strings.Repeat("long", 40)} {
		got := armSlug(name, a)
		if got == "" || strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("armSlug(%q) = %q, which is not a usable label", name, got)
		}
		for _, r := range got {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
				t.Errorf("armSlug(%q) = %q contains %q", name, got, r)
				break
			}
		}
	}
}

// The prompt only asks for a declaration when a Hop wants one. A variation that
// declared an experiment nobody asked for would put real traffic on a comparison
// nobody designed.
func TestPromptAsksForADeclarationOnlyWhenWanted(t *testing.T) {
	without := BuildImplementationPrompt("hop", "var", "approach", "container", false)
	if strings.Contains(without, "experiment.json") {
		t.Error("an ordinary variation was told to declare a live experiment")
	}

	with := BuildImplementationPrompt("hop", "var", "approach", "container", true)
	for _, want := range []string{
		"experiment.json",
		"assignment_unit",
		"mendel_exp_",
		"purely additive",
	} {
		if !strings.Contains(with, want) {
			t.Errorf("an experiment prompt does not mention %q", want)
		}
	}
}

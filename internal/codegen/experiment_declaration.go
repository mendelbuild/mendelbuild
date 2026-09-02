package codegen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/bhs/mendelbuild/internal/experiment"
)

// DeclaredExperiment is the structure written to .mendel/experiment.json.
//
// Two halves, deliberately in one file. The assignment unit is a property of the
// *application* -- what a "user" is, and where the key is found -- and is the
// same for every Variation of it. The migration is a property of *this*
// Variation. Both are things only the code knows, and the repository is where
// the code says what it knows.
//
// Absent means no live experiment, which is the case for almost every Variation.
type DeclaredExperiment struct {
	// AssignmentUnit is user | session | request | tenant. The application tells
	// Mendel what a participant is rather than Mendel assuming.
	AssignmentUnit string `json:"assignment_unit"`

	// AssignmentKey is where the edge finds the participant's identity.
	AssignmentKey DeclaredAssignmentKey `json:"assignment_key"`

	// Migration is this Variation's additive schema change, if it has one. A
	// Variation with no migration is a presentation-only experiment and needs
	// strictly less than one with.
	Migration *DeclaredMigration `json:"migration,omitempty"`

	// Dissonance is what a person who experienced this Variation will feel when
	// its Arm stops serving.
	Dissonance string `json:"dissonance,omitempty"`
}

type DeclaredAssignmentKey struct {
	Source string `json:"source"` // cookie | header | jwt_claim | subdomain
	Name   string `json:"name"`
}

type DeclaredMigration struct {
	Up   string `json:"up"`
	Down string `json:"down"`
}

// Validate reports why a declaration cannot be used, or "" when it can.
//
// Structural only, on purpose. Whether the migration is genuinely additive is
// established by applying it to the user's datastore and diffing -- not by
// reading the text -- and the lints that read text are dialect-specific. Running
// a Postgres lint here would assume the user's database is Postgres, which is
// the assumption this codebase has been bitten by before. That judgment belongs
// at admission, where a real datastore is in hand.
func (d *DeclaredExperiment) Validate() string {
	if d == nil {
		return "there is no declaration"
	}

	switch domain.AssignmentUnit(d.AssignmentUnit) {
	case domain.AssignmentUnitUser, domain.AssignmentUnitSession,
		domain.AssignmentUnitRequest, domain.AssignmentUnitTenant:
	case "":
		return "assignment_unit is required: Mendel cannot guess what one participant is."
	default:
		return fmt.Sprintf("assignment_unit %q is not one of user, session, request, tenant.", d.AssignmentUnit)
	}

	switch domain.AssignmentKeySource(d.AssignmentKey.Source) {
	case domain.AssignmentKeyCookie, domain.AssignmentKeyHeader,
		domain.AssignmentKeyJWTClaim, domain.AssignmentKeySubdomain:
	case "":
		return "assignment_key.source is required: the edge has to know where to look."
	default:
		return fmt.Sprintf("assignment_key.source %q is not one of cookie, header, jwt_claim, subdomain.",
			d.AssignmentKey.Source)
	}
	if strings.TrimSpace(d.AssignmentKey.Name) == "" {
		return "assignment_key.name is required: which cookie, header or claim carries the key."
	}

	if d.Migration != nil {
		if strings.TrimSpace(d.Migration.Up) == "" || strings.TrimSpace(d.Migration.Down) == "" {
			return "A migration needs both up and down. The down is not optional: it is what " +
				"makes the Arm withdrawable, and an Arm that cannot be withdrawn cannot be run."
		}
		// The namespace is checked properly at admission, against the objects the
		// change actually created. This catches the case where nothing was even
		// attempted, which is worth saying early because the fix is in the code.
		if !strings.Contains(d.Migration.Up, experiment.NamespacePrefix) {
			return fmt.Sprintf("Nothing in the migration is namespaced. Every object an "+
				"experiment creates must be prefixed %q so concurrent Arms cannot collide.",
				experiment.NamespacePrefix)
		}
		// Per-request assignment means one person meets both Arms, so the same
		// row would be written by two Arms' logic. A derivation from the unit,
		// not a rule beside it.
		if domain.AssignmentUnit(d.AssignmentUnit) == domain.AssignmentUnitRequest {
			return "assignment_unit is request, so no per-participant durable writes are " +
				"admissible -- one person meets both Arms and the same row is written by both. " +
				"Either the unit is wrong or the migration is."
		}
	}

	return ""
}

// hopWantsExperiment reports whether this Hop's Variations take live traffic.
//
// Quiet on error: a lookup that fails means the prompt omits the experiment
// section, and a Variation that is merely deployed is the safe outcome. The
// opposite default would ask for a live experiment because a query timed out.
func (g *Generator) hopWantsExperiment(ctx context.Context, hopID uuid.UUID) bool {
	hop, err := g.db.GetHop(ctx, hopID)
	return err == nil && hop != nil && hop.LiveExperiment
}

// saveExperimentDeclaration reads .mendel/experiment.json and records what it
// says, creating the experiment and this Variation's Arm.
//
// This is the missing upstream half: internal/experiment could judge a migration
// and had no way to receive one, because nothing generated it. The declaration is
// how a Variation says it wants live traffic and what that will cost the schema.
func (g *Generator) saveExperimentDeclaration(ctx context.Context, workDir string,
	variation *domain.Variation, logger func(domain.LogLevel, string)) error {

	path := filepath.Join(workDir, ".mendel", "experiment.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no experiment declaration (this is fine)")
		}
		return fmt.Errorf("read experiment declaration: %w", err)
	}

	var decl DeclaredExperiment
	if err := json.Unmarshal(data, &decl); err != nil {
		return fmt.Errorf("parse experiment declaration: %w", err)
	}
	if msg := decl.Validate(); msg != "" {
		// Loud rather than silent: a Variation that asked for live traffic and
		// did not get it should say so where someone will read it.
		logger(domain.LogLevelError, "Experiment declaration rejected: "+msg)
		return fmt.Errorf("invalid experiment declaration: %s", msg)
	}

	projectID, err := g.db.GetProjectIDForVariation(ctx, variation.ID)
	if err != nil {
		return fmt.Errorf("find the project: %w", err)
	}

	// One experiment per Hop; each Variation is an Arm of it. The first
	// Variation to be generated creates it and the rest join.
	exp, err := g.db.GetExperimentForHop(ctx, variation.HopID)
	if err != nil {
		return fmt.Errorf("look for an existing experiment: %w", err)
	}
	if exp == nil {
		exp = &domain.Experiment{
			ProjectID:           projectID,
			HopID:               variation.HopID,
			AssignmentUnit:      domain.AssignmentUnit(decl.AssignmentUnit),
			AssignmentKeySource: domain.AssignmentKeySource(decl.AssignmentKey.Source),
			AssignmentKeyName:   decl.AssignmentKey.Name,
		}
		if err := g.db.CreateExperiment(ctx, exp); err != nil {
			return fmt.Errorf("create experiment: %w", err)
		}
		// Mainline exists from the start: it is the code that was already there,
		// and without it there is nothing to compare against.
		if err := g.db.CreateExperimentArm(ctx, &domain.ExperimentArm{
			ExperimentID: exp.ID, Slug: domain.MainlineSlug,
		}); err != nil {
			return fmt.Errorf("create the mainline arm: %w", err)
		}
		logger(domain.LogLevelMilestone, "Opened a live experiment for this hop")
	}

	// Two Variations of one Hop disagreeing about what a participant is would
	// make the comparison meaningless, and the mismatch is silent unless caught.
	if exp.AssignmentUnit != domain.AssignmentUnit(decl.AssignmentUnit) ||
		exp.AssignmentKeyName != decl.AssignmentKey.Name {
		return fmt.Errorf("this variation assigns by %s/%s but the experiment assigns by %s/%s; "+
			"Arms of one experiment must agree on what a participant is",
			decl.AssignmentUnit, decl.AssignmentKey.Name,
			exp.AssignmentUnit, exp.AssignmentKeyName)
	}

	if decl.Dissonance != "" && exp.DissonanceDescription == "" {
		if err := g.db.SetExperimentDissonance(ctx, exp.ID, decl.Dissonance,
			domain.DefaultDissonancePhrase); err != nil {
			return fmt.Errorf("record the dissonance: %w", err)
		}
	}

	arm := &domain.ExperimentArm{
		ExperimentID: exp.ID,
		VariationID:  &variation.ID,
		Slug:         armSlug(variation.Name, variation.ID),
	}
	if decl.Migration != nil {
		arm.DeclaredMigrationUp = decl.Migration.Up
		arm.DeclaredMigrationDown = decl.Migration.Down
	}
	if err := g.db.UpsertExperimentArm(ctx, arm); err != nil {
		return fmt.Errorf("create the arm: %w", err)
	}

	if decl.Migration == nil {
		logger(domain.LogLevelMilestone, "Declared a live experiment arm with no schema change")
	} else {
		logger(domain.LogLevelMilestone, "Declared a live experiment arm with an additive migration")
	}
	return nil
}

// armSlug is what the assignment cookie carries and the HTTPRoute matches.
//
// Derived from the Variation so a person reading a cookie or a route can tell
// which Arm it is, with the id's prefix appended because Variation names are not
// unique and a collision would route two Arms to one place.
func armSlug(name string, id uuid.UUID) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r == ' ' || r == '_' || r == '-':
			return '-'
		}
		return -1
	}, name)
	cleaned = strings.Trim(cleaned, "-")
	if len(cleaned) > 24 {
		cleaned = strings.Trim(cleaned[:24], "-")
	}
	if cleaned == "" {
		cleaned = "arm"
	}
	return cleaned + "-" + id.String()[:8]
}

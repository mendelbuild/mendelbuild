package codegen_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bhs/mendelbuild/internal/codegen"
	"github.com/bhs/mendelbuild/internal/testrepo"
	"github.com/bhs/mendelbuild/internal/test"
)

// The `.mendel/` specs read against real repositories rather than strings
// written next to the assertion.
//
// A test that constructs its own input tests the parser against what the author
// expected the file to look like. These read files that were written as files,
// which is the difference that catches a spec drifting from what anyone would
// actually put in a repository.

// Whatever a repository declares, it either parses or is refused with a reason
// somebody could act on. Never accepted quietly and never rejected silently.
func TestEveryFixtureDeclarationIsUnderstoodOrRefusedWithAReason(t *testing.T) {
	for _, name := range testrepo.All() {
		t.Run(name, func(t *testing.T) {
			dir := testrepo.Path(t, name)

			decl, err := codegen.ReadDeclaredExperiment(dir)
			switch {
			case errors.Is(err, codegen.ErrNoExperimentDeclared):
				return // Almost every Variation. Not a problem.
			case err != nil:
				if strings.TrimSpace(err.Error()) == "" {
					t.Error("a refused declaration must say why; it reaches a log someone reads")
				}
				return
			}
			if decl.AssignmentUnit == "" || decl.AssignmentKey.Name == "" {
				t.Errorf("a declaration that parsed says what a participant is and where the key "+
					"is found, got %+v", decl)
			}
		})
	}
}

// The simplest experiment there is -- two presentations and a cookie -- must not
// be made to satisfy the requirements of the hardest. A project with no database
// declaring no migration is the case that would break if the datastore
// machinery were ever required unconditionally.
func TestAProjectWithNoDatastoreDeclaresNoMigration(t *testing.T) {
	decl, err := codegen.ReadDeclaredExperiment(testrepo.Path(t, testrepo.Pong))
	if err != nil {
		t.Fatalf("pong declares a presentation-only experiment and it should parse: %v", err)
	}
	if decl.Migration != nil {
		t.Error("the no-datastore fixture has grown a migration, which is the case it exists to not be")
	}
}

// A migration has to be namespaced and reversible, and the fixture is where that
// is checked against a file rather than a literal.
func TestAFixtureMigrationIsNamespacedAndReversible(t *testing.T) {
	decl, err := codegen.ReadDeclaredExperiment(testrepo.Path(t, testrepo.Ledger))
	if err != nil {
		t.Fatalf("ledger declares a full experiment and it should parse: %v", err)
	}
	if decl.Migration == nil {
		t.Fatal("the full-path fixture has lost its migration")
	}
	if decl.Migration.Down == "" {
		t.Error("an Arm that cannot be withdrawn cannot be run")
	}
	if !strings.Contains(decl.Migration.Up, "mendel_exp_") {
		t.Error("an experiment's objects are namespaced so concurrent Arms cannot collide")
	}
}

// A repository's test configuration is optional and, where present, has to name
// both a service and a command -- neither is inferable.
func TestEveryFixtureTestConfigIsUsable(t *testing.T) {
	for _, name := range testrepo.All() {
		t.Run(name, func(t *testing.T) {
			cfg, err := test.LoadConfig(testrepo.Path(t, name))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg == nil {
				return // No Docker tests. Valid.
			}
			if cfg.Service == "" || cfg.TestCommand == "" {
				t.Errorf("a test config that loaded names a service and a command, got %+v", cfg)
			}
		})
	}
}

// A gap the fixtures found, recorded where someone will trip over it.
//
// §13 §14 says "Tier 1 offers only `device` (cookie-pinned) and `request`", and
// §16 D45's table gives `device` its own assignment mechanism. The code has
// user, session, request and tenant, and no `device` — so a repository written
// from the design is refused.
//
// Not fixed here because adding an assignment unit is not only a constant: it
// changes what PermitsDurableWrites derives, which mechanism §16 routes with,
// and what a Tier 1 experiment is allowed to declare. Recorded instead, and the
// pong fixture uses `session` with a cookie in the meantime, which is the
// nearest thing that exists and is not what §13 says.
//
// This test fails when `device` is added, which is the point: it asks to be
// deleted at the moment the divergence closes.
func TestDeviceIsSpecifiedAndNotImplemented(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".mendel"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	decl := `{"assignment_unit":"device","assignment_key":{"source":"cookie","name":"mendel_device"}}`
	if err := os.WriteFile(filepath.Join(dir, ".mendel", "experiment.json"), []byte(decl), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := codegen.ReadDeclaredExperiment(dir)
	if err == nil {
		t.Fatal("`device` is now accepted. §13 §14 and §16 D45 always said it should be, so this " +
			"test has done its job: delete it, and give the pong fixture back its `device` unit.")
	}
	if !strings.Contains(err.Error(), "device") {
		t.Errorf("the refusal should name the unit it did not recognise, got %v", err)
	}
}

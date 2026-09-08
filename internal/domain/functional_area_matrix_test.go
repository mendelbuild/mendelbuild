package domain

import (
	"os"
	"strings"
	"testing"
)

// planPath is the design document whose §4.2 table this catalogue generates.
const planPath = "../../dev/claude_plans/17_functional_area_matrix.md"

const (
	matrixBegin = "<!-- BEGIN GENERATED MATRIX -->"
	matrixEnd   = "<!-- END GENERATED MATRIX -->"
)

// The plan's table is generated from the catalogue, so the two cannot disagree.
//
// This is the drift problem the whole design is about, applied one level up. A
// document describing code it is not connected to drifts, and drift in that
// document means a reader reasoning about a matrix that no longer exists. Same
// discipline as schema/full.sql against the migrations, and for the same reason.
//
// Set MENDEL_UPDATE_MATRIX=1 to rewrite the block rather than fail.
func TestThePlanMatchesTheCatalogue(t *testing.T) {
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("reading the plan: %v", err)
	}
	doc := string(raw)

	before, rest, ok := strings.Cut(doc, matrixBegin)
	if !ok {
		t.Fatalf("%s has no %s marker, so the generated table has nowhere to go", planPath, matrixBegin)
	}
	_, after, ok := strings.Cut(rest, matrixEnd)
	if !ok {
		t.Fatalf("%s has no %s marker", planPath, matrixEnd)
	}

	want := "\n" + FunctionalAreas().MatrixMarkdown()
	rebuilt := before + matrixBegin + want + matrixEnd + after

	if rebuilt == doc {
		return
	}
	if os.Getenv("MENDEL_UPDATE_MATRIX") != "" {
		if err := os.WriteFile(planPath, []byte(rebuilt), 0o644); err != nil {
			t.Fatalf("rewriting the plan: %v", err)
		}
		t.Log("rewrote the generated matrix in " + planPath)
		return
	}
	t.Errorf("the plan's matrix is out of date with the catalogue.\n"+
		"Run: MENDEL_UPDATE_MATRIX=1 go test ./internal/domain/ -run %s", t.Name())
}

// Every area a reader can be sent to has to be in the table, or the document
// describes a product with rows the code does not have.
func TestEveryAreaAppearsInTheGeneratedMatrix(t *testing.T) {
	table := FunctionalAreas().MatrixMarkdown()
	for _, a := range FunctionalAreas().Areas() {
		if !strings.Contains(table, string(a.ID)) {
			t.Errorf("area %q has no column in the matrix", a.ID)
		}
	}
}

// A warning is rendered differently from a requirement, because a table where
// both look alike is a table that cannot be read for what gates anything.
func TestWarningsAreMarkedApartFromRequirements(t *testing.T) {
	var sawWarning bool
	for _, row := range FunctionalAreas().Matrix() {
		if len(row.Warned) > 0 {
			sawWarning = true
			if len(row.Required) > 0 {
				t.Errorf("%s both gates and warns; a condition does one or the other", row.Condition)
			}
		}
	}
	if !sawWarning {
		t.Skip("no warnings in the catalogue yet, so there is nothing to distinguish")
	}
	if !strings.Contains(FunctionalAreas().MatrixMarkdown(), "○") {
		t.Error("a warning should render differently from a requirement")
	}
}

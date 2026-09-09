package domain

import (
	"fmt"
	"sort"
	"strings"
)

// The matrix, rendered from the catalogue.
//
// dev/claude_plans/17_functional_area_matrix.md §4.2 is a table of which
// functional areas need which conditions, and it was hand-maintained -- which
// is the failure the whole design is about, one level up. A document describing
// code it is not connected to drifts, and drift here means a reader reasoning
// about a matrix that no longer exists.
//
// So the document's table is generated from this and checked by a test, the
// same discipline as schema/full.sql against the migrations. A condition added
// in Go without the document updating fails the suite.

// MatrixRow is one condition and which areas want it.
type MatrixRow struct {
	Condition   ConditionID
	Name        string
	Evidence    Evidence
	Remedy      Remedy
	DeclaredAt  Scope
	SatisfiedAt Scope

	// Required and Warned are the areas that gate on this and the areas that
	// merely mention it. An area appears in at most one.
	Required []AreaID
	Warned   []AreaID

	// Built is whether this condition has an evaluator. False means designed
	// and not written: it reports as `unimplemented`, which is neither
	// satisfied nor failed, and it renders differently in the table so a hole
	// cannot be read as a finished cell.
	Built bool
}

// Shared reports whether more than one area wants this condition. The shared
// ones are the reason the matrix is a table rather than one checklist per area,
// so they are what the document's table shows.
func (r MatrixRow) Shared() bool { return len(r.Required)+len(r.Warned) > 1 }

// Matrix returns every condition with the areas that want it, ordered by how
// widely shared it is and then by id, so the table opens with the rows that
// justify its existence.
func (c *Catalogue) Matrix() []MatrixRow {
	rows := make([]MatrixRow, 0, len(c.conditions))
	for _, id := range c.conditionIDs() {
		cond := c.conditions[id]
		row := MatrixRow{
			Condition:   id,
			Name:        cond.Name,
			Evidence:    cond.Evidence,
			Remedy:      cond.Remedy,
			DeclaredAt:  cond.DeclaredAt,
			SatisfiedAt: cond.SatisfiedAt,
			Built:       cond.Evaluate != nil,
		}
		for _, a := range c.Areas() {
			switch {
			case requires(a, id):
				row.Required = append(row.Required, a.ID)
			case warns(a, id):
				row.Warned = append(row.Warned, a.ID)
			}
		}
		rows = append(rows, row)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		ni, nj := len(rows[i].Required), len(rows[j].Required)
		if ni != nj {
			return ni > nj
		}
		return rows[i].Condition < rows[j].Condition
	})
	return rows
}

// MatrixMarkdown renders the matrix as the table the plan document carries.
//
// Deliberately the whole table rather than the shared rows only: the document
// used to list single-area conditions separately underneath, and keeping two
// renderings in step is the problem this exists to solve rather than an
// arrangement worth reproducing. One table, with the sharing visible in it.
func (c *Catalogue) MatrixMarkdown() string {
	areas := c.Areas()

	var b strings.Builder
	b.WriteString("| Functional Area Condition | Evidence | Remedy | Declared | Satisfied |")
	for _, a := range areas {
		fmt.Fprintf(&b, " %s |", a.ID)
	}
	b.WriteString("\n|---|---|---|---|---|")
	for range areas {
		b.WriteString(":-:|")
	}
	b.WriteString("\n")

	for _, row := range c.Matrix() {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |",
			row.Name, row.Evidence, row.Remedy, row.DeclaredAt, row.SatisfiedAt)
		for _, a := range areas {
			switch {
			case contains(row.Required, a.ID) && !row.Built:
				// A required condition nobody has written yet. Marked apart
				// from a built one because the alternative renders a hole as a
				// finished cell -- and this table is the thing a reader trusts
				// when deciding whether an area is covered. Six of these are
				// deferred on purpose; a reader who cannot see which is a
				// reader who has to be told, and telling does not survive.
				b.WriteString(" ◌ |")
			case contains(row.Required, a.ID):
				b.WriteString(" ● |")
			case contains(row.Warned, a.ID):
				b.WriteString(" ○ |")
			default:
				b.WriteString("  |")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func contains(ids []AreaID, id AreaID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

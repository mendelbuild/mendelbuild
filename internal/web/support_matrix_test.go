package web

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

func TestSupportMatrixPivot(t *testing.T) {
	fly := domain.HostingPlatform{ID: uuid.New(), Slug: "fly-io", Name: "Fly.io"}
	gke := domain.HostingPlatform{ID: uuid.New(), Slug: "gke", Name: "Google Kubernetes Engine"}
	note := "Fastest to a running demo."

	combos := []domain.SupportedDeploymentCombo{
		{ArtifactKind: "container", HostingPlatformID: fly.ID, Notes: &note},
		{ArtifactKind: "kubernetes", HostingPlatformID: gke.ID},
	}
	current := &domain.ProjectDeploymentChannel{ArtifactKind: "container", HostingPlatformID: fly.ID}

	m := pivotSupportMatrix([]domain.HostingPlatform{fly, gke}, combos, current)
	if !m.HasAny() {
		t.Fatal("expected a matrix")
	}

	// Columns are the kinds that actually appear, in a stable order.
	if got := m.ArtifactKinds; len(got) != 2 || got[0] != "container" || got[1] != "kubernetes" {
		t.Fatalf("ArtifactKinds = %v", got)
	}

	// Every platform gets a row, including one that supports nothing in a
	// given column — that absence is the information the grid exists to show.
	if len(m.Rows) != 2 {
		t.Fatalf("expected a row per platform, got %d", len(m.Rows))
	}
	flyRow, gkeRow := m.Rows[0], m.Rows[1]

	if !flyRow.Cells[0].Supported || !flyRow.Cells[0].Current {
		t.Error("Fly.io/container is supported and is this project's channel")
	}
	if flyRow.Cells[0].Note != note {
		t.Errorf("the combo's own note should reach the cell, got %q", flyRow.Cells[0].Note)
	}
	if flyRow.Cells[1].Supported {
		t.Error("Fly.io does not do kubernetes, and the grid must say so")
	}
	if !gkeRow.Cells[1].Supported {
		t.Error("GKE/kubernetes should be supported")
	}
	if gkeRow.Cells[1].Current {
		t.Error("only the project's actual channel is current")
	}
}

// A platform seeded with no combos still earns a row: "we know about this and
// cannot deploy to it" is a different answer from silence.
func TestSupportMatrixKeepsUnsupportedPlatforms(t *testing.T) {
	known := domain.HostingPlatform{ID: uuid.New(), Name: "Render"}
	fly := domain.HostingPlatform{ID: uuid.New(), Name: "Fly.io"}

	m := pivotSupportMatrix(
		[]domain.HostingPlatform{known, fly},
		[]domain.SupportedDeploymentCombo{{ArtifactKind: "container", HostingPlatformID: fly.ID}},
		nil,
	)
	if len(m.Rows) != 2 {
		t.Fatalf("expected both platforms, got %d rows", len(m.Rows))
	}
	if m.Rows[0].Cells[0].Supported {
		t.Error("Render supports nothing and the grid should show the gap")
	}
}

// Nothing seeded at all means no grid, rather than an empty one implying that
// Mendel can deploy nowhere.
func TestSupportMatrixEmptyWhenNoPlatforms(t *testing.T) {
	if m := pivotSupportMatrix(nil, nil, nil); m.HasAny() {
		t.Error("expected no matrix when no platforms are seeded")
	}
}

// And it has to render. This also puts the grid into the contact sheet, so it
// can be reviewed alongside every other state.
func TestSupportMatrixRenders(t *testing.T) {
	fly := domain.HostingPlatform{ID: uuid.New(), Name: "Fly.io"}
	gke := domain.HostingPlatform{ID: uuid.New(), Name: "Google Kubernetes Engine"}
	render := domain.HostingPlatform{ID: uuid.New(), Name: "Render"}
	note := "Fastest to a running demo."

	projectID := uuid.New()
	current := &domain.ProjectDeploymentChannel{ArtifactKind: "container", HostingPlatformID: fly.ID}

	body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
		"ProjectID":   projectID.String(),
		"Project":     &domain.Project{ID: projectID, Name: "pong"},
		"SettingsTab": "deployment",
		"SupportMatrix": pivotSupportMatrix(
			[]domain.HostingPlatform{fly, gke, render},
			[]domain.SupportedDeploymentCombo{
				{ArtifactKind: "container", HostingPlatformID: fly.ID, Notes: &note},
				{ArtifactKind: "container", HostingPlatformID: gke.ID},
				{ArtifactKind: "kubernetes", HostingPlatformID: gke.ID},
			},
			current),
	})

	for _, want := range []string{
		// The grid is the picker, so it is titled as one.
		"Change the channel",
		"Fly.io", "Google Kubernetes Engine", "Render",
		"container", "kubernetes",
		"in use",              // the project's own channel, marked in place
		`name="combo_id"`,     // and every supported cell is selectable
		"Update channel",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("support matrix missing %q", want)
		}
	}

	// Three supported pairings, so three radios -- and no more. An unsupported
	// pairing must not be selectable, which is the whole reason the grid can
	// replace the list rather than sit beside it.
	if n := strings.Count(body, `name="combo_id"`); n != 3 {
		t.Errorf("expected a radio for each supported pairing, got %d", n)
	}
}

// A project that has never chosen reads as a first choice, not a change.
func TestSupportMatrixBeforeAnyChannel(t *testing.T) {
	fly := domain.HostingPlatform{ID: uuid.New(), Name: "Fly.io"}
	projectID := uuid.New()

	m := pivotSupportMatrix([]domain.HostingPlatform{fly},
		[]domain.SupportedDeploymentCombo{{ArtifactKind: "container", HostingPlatformID: fly.ID}}, nil)
	m.ProjectID = projectID.String()

	body := renderPageForTest(t, "deployment_channel.html", map[string]interface{}{
		"ProjectID": projectID.String(), "SettingsTab": "deployment",
		"Project": &domain.Project{ID: projectID, Name: "pong"}, "SupportMatrix": m,
	})
	for _, want := range []string{"Choose a channel", "Set channel"} {
		if !strings.Contains(body, want) {
			t.Errorf("an unconfigured project missing %q", want)
		}
	}
	if strings.Contains(body, "in use") {
		t.Error("nothing is in use yet")
	}
}

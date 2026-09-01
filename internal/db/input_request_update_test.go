package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// TestUpdateInputRequestPersistsWhatItAsksFor covers the fields that describe
// the ask itself, as opposed to its progress through the queue.
//
// UpdateInputRequest wrote status and assignment but silently dropped
// instructions, link and required_capabilities. Nothing failed: the update
// returned nil and the row kept its old values. So a credential request filed
// before its channel knew what it needed could never be brought up to date, and
// the form went on offering one blank box however often the server recomputed
// the right answer.
func TestUpdateInputRequestPersistsWhatItAsksFor(t *testing.T) {
	db, projectID := testDB(t)
	ctx := context.Background()

	before := "Do the thing"
	req := &domain.InputRequest{
		ID:               uuid.New(),
		ProjectID:        projectID,
		Kind:             domain.InputRequestKindCredentialRequest,
		Title:            "Provide deployment credentials",
		Instructions:     &before,
		Status:           domain.InputRequestStatusNeedsAssignment,
		ObjectivityScore: 1,
		ImportanceScore:  1,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := db.CreateInputRequest(ctx, req); err != nil {
		t.Fatalf("create: %v", err)
	}

	after := "Run the setup script, then paste the key"
	link := "/p/" + projectID.String() + "/deployment"
	req.Instructions = &after
	req.Link = &link
	req.RequiredCapabilities = []string{"GCP_PROJECT_ID", "GKE_ZONE"}

	if err := db.UpdateInputRequest(ctx, req); err != nil {
		t.Fatalf("update: %v", err)
	}

	reloaded, err := db.GetInputRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if reloaded.Instructions == nil || *reloaded.Instructions != after {
		t.Errorf("instructions = %v, want %q", reloaded.Instructions, after)
	}
	if reloaded.Link == nil || *reloaded.Link != link {
		t.Errorf("link = %v, want %q", reloaded.Link, link)
	}
	if len(reloaded.RequiredCapabilities) != 2 {
		t.Fatalf("required_capabilities = %v, want two entries", reloaded.RequiredCapabilities)
	}
	for i, want := range []string{"GCP_PROJECT_ID", "GKE_ZONE"} {
		if reloaded.RequiredCapabilities[i] != want {
			t.Errorf("capability %d = %q, want %q", i, reloaded.RequiredCapabilities[i], want)
		}
	}
}

package codegen

import (
	"testing"

	"github.com/bhs/mendelbuild/internal/domain"
	"github.com/google/uuid"
)

// A declaration comes from a model writing JSON, so the checks here are what
// stands between a malformed entry and either a database error or a
// requirement that silently cannot be acted on.
func TestDeclaredRequirementValidation(t *testing.T) {
	variationID := uuid.New()

	cases := []struct {
		name    string
		decl    DeclaredRequirement
		wantErr bool
	}{
		{
			name: "secret needs only a name",
			decl: DeclaredRequirement{Kind: "secret", Name: "GOOGLE_CLIENT_SECRET"},
		},
		{
			name: "acknowledgement with instructions",
			decl: DeclaredRequirement{Kind: "acknowledgement", Name: "redirect-uri",
				Instructions: "Add {{deploy_url}}/cb to Authorized redirect URIs."},
		},
		{
			name:    "acknowledgement without instructions cannot be acted on",
			decl:    DeclaredRequirement{Kind: "acknowledgement", Name: "redirect-uri"},
			wantErr: true,
		},
		{
			name:    "unknown kind",
			decl:    DeclaredRequirement{Kind: "env", Name: "PORT"},
			wantErr: true,
		},
		{
			name:    "no name",
			decl:    DeclaredRequirement{Kind: "secret"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := tc.decl.toDomain(variationID)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.VariationID != variationID {
				t.Error("requirement should belong to the variation")
			}
			if req.Name != tc.decl.Name {
				t.Errorf("name = %q, want %q", req.Name, tc.decl.Name)
			}
		})
	}
}

// Empty optional fields must stay NULL rather than becoming empty strings: an
// acknowledgement's instructions are checked for NULL by the table, and a
// console URL that is "" would render as a link to nowhere.
func TestDeclaredRequirementOmitsEmptyOptionals(t *testing.T) {
	req, err := DeclaredRequirement{Kind: "secret", Name: "STRIPE_KEY"}.toDomain(uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Description != nil || req.Instructions != nil || req.ConsoleURL != nil {
		t.Error("unset optional fields should be nil, not pointers to empty strings")
	}
	if req.Kind != domain.RequirementKindSecret {
		t.Errorf("kind = %q", req.Kind)
	}
}

package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// Storage for adapter invocations.
//
// The token is the interesting part. A report arrives from a job running in the
// user's own environment, over the public internet, knowing only what Mendel
// gave it — so the token is what says which invocation is reporting, and §13 D10
// is the standing decision that identity comes from the credential and never
// from the payload. An endpoint that trusts what a body says about itself can
// be told anything.

// tokenBytes is the length of a minted token. Long enough that guessing is not
// a strategy; the endpoint it opens accepts findings about a datastore.
const tokenBytes = 32

// AdapterTokenTTL bounds how long a minted token is good for.
//
// A job that has not reported within it cannot report at all, which is the
// point: a token outliving its job is a standing credential. Generous enough to
// cover a slow schema copy on a large database, since the failure mode of too
// short is a phase that can never complete.
const AdapterTokenTTL = 30 * time.Minute

// HashAdapterToken is what is stored, never the token itself, so that reading
// the database does not let anyone forge a report.
func HashAdapterToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// CreateAdapterInvocation records an invocation and returns it with the token
// that authenticates its report.
//
// The token is returned and not stored, so this is the only moment it exists in
// a readable form. A caller that loses it has to mint a new invocation, which is
// the correct outcome — the alternative is a token that can be recovered later,
// which is the property being avoided.
func (db *DB) CreateAdapterInvocation(
	ctx context.Context,
	projectID uuid.UUID,
	phase string,
	instruction []byte,
) (*domain.AdapterInvocation, string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("mint an adapter token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	inv := &domain.AdapterInvocation{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Phase:       phase,
		ExpiresAt:   time.Now().Add(AdapterTokenTTL),
		Instruction: instruction,
	}

	err := db.Pool.QueryRow(ctx, `
		INSERT INTO adapter_invocations (id, project_id, phase, token_hash, expires_at, instruction)
		VALUES ($1, $2, $3, $4, $5, COALESCE($6, '{}'::jsonb))
		RETURNING created_at
	`, inv.ID, projectID, phase, HashAdapterToken(token), inv.ExpiresAt, instruction).Scan(&inv.CreatedAt)
	if err != nil {
		return nil, "", fmt.Errorf("record adapter invocation: %w", err)
	}
	return inv, token, nil
}

// AdapterInvocationByToken finds the invocation a report belongs to.
//
// Expiry is checked in the query rather than after it, so an expired token is
// indistinguishable from an unknown one to whoever presented it. The caller
// learns only that this is not a token that opens anything, which is all it is
// entitled to.
func (db *DB) AdapterInvocationByToken(ctx context.Context, token string) (*domain.AdapterInvocation, error) {
	inv := &domain.AdapterInvocation{}
	err := db.Pool.QueryRow(ctx, `
		SELECT id, project_id, phase, expires_at, instruction, result, COALESCE(outcome, ''), created_at, reported_at
		FROM adapter_invocations
		WHERE token_hash = $1 AND expires_at > NOW()
	`, HashAdapterToken(token)).Scan(
		&inv.ID, &inv.ProjectID, &inv.Phase, &inv.ExpiresAt,
		&inv.Instruction, &inv.Result, &inv.Outcome, &inv.CreatedAt, &inv.ReportedAt,
	)
	if err != nil {
		// Wrapped rather than flattened: the handler answers every failure here
		// with an unadorned 401 so a presented token cannot be used to learn
		// anything, but swallowing the cause here leaves nothing to debug from.
		return nil, fmt.Errorf("no live invocation for this token: %w", err)
	}
	return inv, nil
}

// RecordAdapterResult stores what came back, once.
//
// Once is enforced in the statement rather than checked first: two reports for
// one invocation is a retrying job or a replayed report, and the second must not
// overwrite the first. Which of them wins matters less than that it is decided
// here and not by whichever arrived last.
func (db *DB) RecordAdapterResult(ctx context.Context, id uuid.UUID, outcome string, result []byte) error {
	tag, err := db.Pool.Exec(ctx, `
		UPDATE adapter_invocations
		SET result = $2, outcome = $3, reported_at = NOW()
		WHERE id = $1 AND reported_at IS NULL
	`, id, result, outcome)
	if err != nil {
		return fmt.Errorf("record adapter result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("this invocation has already reported")
	}
	return nil
}

// LatestAdapterInvocation returns a project's most recent invocation of a
// phase, or nil when there has never been one.
//
// Nil is a third answer and the caller has to treat it as one: never asked is
// not the same as asked and told no, and a page that renders them alike sends
// someone to fix something nobody has looked at.
func (db *DB) LatestAdapterInvocation(ctx context.Context, projectID uuid.UUID, phase string) (*domain.AdapterInvocation, error) {
	inv := &domain.AdapterInvocation{}
	err := db.Pool.QueryRow(ctx, `
		SELECT id, project_id, phase, expires_at, instruction, result, COALESCE(outcome, ''), created_at, reported_at
		FROM adapter_invocations
		WHERE project_id = $1 AND phase = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, projectID, phase).Scan(
		&inv.ID, &inv.ProjectID, &inv.Phase, &inv.ExpiresAt,
		&inv.Instruction, &inv.Result, &inv.Outcome, &inv.CreatedAt, &inv.ReportedAt,
	)
	if err != nil {
		return nil, nil
	}
	return inv, nil
}

// SetAdapterInstruction records the question as asked, once the token that went
// with it has been handed to the job and not before.
//
// Separate from creating the invocation because the two carry different things.
// Creating mints a token; this stores the instruction with that token taken out,
// so the credential lives in exactly one place a job can read and nowhere a
// database can.
func (db *DB) SetAdapterInstruction(ctx context.Context, id uuid.UUID, instruction []byte) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE adapter_invocations SET instruction = $2 WHERE id = $1
	`, id, instruction)
	if err != nil {
		return fmt.Errorf("record the instruction: %w", err)
	}
	return nil
}

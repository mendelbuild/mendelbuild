package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bhs/mendelbuild/internal/domain"
)

// DraftOpenQuestion is one question as the drafting agent asked it, before
// anyone has answered.
type DraftOpenQuestion struct {
	Question         string
	SuggestedAnswers []string
}

// ReplaceUnansweredQuestions swaps a strategy's unanswered questions for a
// freshly drafted set, and leaves the answered ones alone.
//
// Answered ones survive because an answer is a fact about the project, not about
// the draft that prompted it. Clearing them on every redraft would ask someone
// what benchmark data they have every time Mendel rewrote an objective, and the
// drafting agent is told not to re-ask what it has been told -- which it can
// only do if the answers are still here to give it.
//
// The new questions are appended after the surviving answered ones, so position
// stays a total order over the list actually shown.
func (db *DB) ReplaceUnansweredQuestions(ctx context.Context, strategyID uuid.UUID,
	questions []DraftOpenQuestion) error {

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		DELETE FROM strategy_open_questions
		WHERE strategy_id = $1 AND answer IS NULL
	`, strategyID); err != nil {
		return fmt.Errorf("clear unanswered questions: %w", err)
	}

	var next int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(position) + 1, 0) FROM strategy_open_questions WHERE strategy_id = $1
	`, strategyID).Scan(&next); err != nil {
		return fmt.Errorf("read next position: %w", err)
	}

	now := time.Now()
	for i, q := range questions {
		suggestions, err := json.Marshal(nonNilStrings(q.SuggestedAnswers))
		if err != nil {
			return fmt.Errorf("encode suggested answers: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO strategy_open_questions
			    (id, strategy_id, question, suggested_answers, position, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)
		`, uuid.New(), strategyID, q.Question, suggestions, next+i, now); err != nil {
			return fmt.Errorf("insert open question: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// AnswerOpenQuestion records what the user said. An empty answer clears one,
// which puts the question back on screen unanswered.
func (db *DB) AnswerOpenQuestion(ctx context.Context, questionID uuid.UUID, answer string) error {
	if answer == "" {
		_, err := db.Pool.Exec(ctx, `
			UPDATE strategy_open_questions
			SET answer = NULL, answered_at = NULL, updated_at = NOW()
			WHERE id = $1
		`, questionID)
		return err
	}
	_, err := db.Pool.Exec(ctx, `
		UPDATE strategy_open_questions
		SET answer = $2, answered_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, questionID, answer)
	return err
}

// GetOpenQuestions returns a strategy's questions in the order they are shown.
func (db *DB) GetOpenQuestions(ctx context.Context, strategyID uuid.UUID) ([]domain.OpenQuestion, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, strategy_id, question, suggested_answers, answer, answered_at,
		       position, created_at, updated_at
		FROM strategy_open_questions
		WHERE strategy_id = $1
		ORDER BY position
	`, strategyID)
	if err != nil {
		return nil, fmt.Errorf("query open questions: %w", err)
	}
	defer rows.Close()

	var out []domain.OpenQuestion
	for rows.Next() {
		var q domain.OpenQuestion
		var suggestions []byte
		if err := rows.Scan(&q.ID, &q.StrategyID, &q.Question, &suggestions, &q.Answer,
			&q.AnsweredAt, &q.Position, &q.CreatedAt, &q.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan open question: %w", err)
		}
		if len(suggestions) > 0 {
			if err := json.Unmarshal(suggestions, &q.SuggestedAnswers); err != nil {
				return nil, fmt.Errorf("decode suggested answers: %w", err)
			}
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// nonNilStrings keeps a nil slice from being encoded as JSON null, which the
// column's default and every reader treat as a different thing from an empty
// list.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

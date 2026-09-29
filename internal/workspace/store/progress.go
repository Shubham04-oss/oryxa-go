package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

type CompletedOperation struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type ApplyAttempt struct {
	ID             string               `json:"id"`
	OrganizationID string               `json:"organization_id"`
	ActorID        string               `json:"actor_id"`
	Status         string               `json:"status"`
	Completed      []CompletedOperation `json:"completed"`
	CompletedCount int                  `json:"completed_count"`
	StartedAt      time.Time            `json:"started_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

var ErrAttemptNotFound = errors.New("workspace apply attempt not found")

// LatestAttempt caps returned operation details while retaining their total.
func (store *Store) LatestAttempt(ctx context.Context, organizationID string) (ApplyAttempt, error) {
	var attempt ApplyAttempt
	var completed []byte
	err := store.database.Pool.QueryRow(ctx, `SELECT id,organization_id,actor_id,status,
		jsonb_path_query_array(completed,'$[0 to 99]'),jsonb_array_length(completed),started_at,updated_at
		FROM workspace.apply_attempts WHERE organization_id=$1 ORDER BY updated_at DESC LIMIT 1`, organizationID).
		Scan(&attempt.ID, &attempt.OrganizationID, &attempt.ActorID, &attempt.Status, &completed, &attempt.CompletedCount, &attempt.StartedAt, &attempt.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApplyAttempt{}, ErrAttemptNotFound
	}
	if err != nil {
		return ApplyAttempt{}, fmt.Errorf("read workspace apply progress: %w", err)
	}
	if err := json.Unmarshal(completed, &attempt.Completed); err != nil {
		return ApplyAttempt{}, fmt.Errorf("decode workspace apply progress: %w", err)
	}
	return attempt, nil
}

func (store *Store) SaveAttempt(ctx context.Context, attempt ApplyAttempt) error {
	completed, err := json.Marshal(attempt.Completed)
	if err != nil {
		return err
	}
	_, err = store.database.Pool.Exec(ctx, `INSERT INTO workspace.apply_attempts
        (id,organization_id,actor_id,status,completed,started_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)
        ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,completed=EXCLUDED.completed,updated_at=EXCLUDED.updated_at
        WHERE apply_attempts.organization_id=EXCLUDED.organization_id AND apply_attempts.actor_id=EXCLUDED.actor_id`,
		attempt.ID, attempt.OrganizationID, attempt.ActorID, attempt.Status, completed, attempt.StartedAt, attempt.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save workspace apply progress: %w", err)
	}
	return nil
}

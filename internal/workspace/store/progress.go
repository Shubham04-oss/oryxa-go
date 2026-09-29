package store

import (
	"context"
	"encoding/json"
	"fmt"
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
	StartedAt      time.Time            `json:"started_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
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

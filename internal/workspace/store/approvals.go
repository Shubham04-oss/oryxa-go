package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrPlanNotFound = errors.New("reviewed workspace plan not found")

type ReviewedPlan struct {
	ID             string
	OrganizationID string
	ActorID        string
	DocumentHash   string
	BaseVersion    int64
	Plan           []byte
	ExpiresAt      time.Time
}

func (store *Store) SavePlan(ctx context.Context, plan ReviewedPlan) error {
	_, err := store.database.Pool.Exec(ctx, `INSERT INTO workspace.reviewed_plans
        (id, organization_id, actor_id, document_hash, base_version, plan, expires_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7)`, plan.ID, plan.OrganizationID, plan.ActorID,
		plan.DocumentHash, plan.BaseVersion, plan.Plan, plan.ExpiresAt)
	if err != nil {
		return fmt.Errorf("save reviewed workspace plan: %w", err)
	}
	return nil
}

func (store *Store) ReviewedPlan(ctx context.Context, organizationID, actorID, id string) (ReviewedPlan, error) {
	var plan ReviewedPlan
	err := store.database.Pool.QueryRow(ctx, `SELECT id, organization_id, actor_id, document_hash,
        base_version, plan, expires_at FROM workspace.reviewed_plans
        WHERE id=$1 AND organization_id=$2 AND actor_id=$3 AND expires_at > now()`,
		id, organizationID, actorID).Scan(&plan.ID, &plan.OrganizationID, &plan.ActorID,
		&plan.DocumentHash, &plan.BaseVersion, &plan.Plan, &plan.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewedPlan{}, ErrPlanNotFound
	}
	if err != nil {
		return ReviewedPlan{}, fmt.Errorf("read reviewed workspace plan: %w", err)
	}
	return plan, nil
}

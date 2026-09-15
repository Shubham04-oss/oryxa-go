package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// ReportConnection records that a caller saw the provider reject this
// connection's credential, and brings its next health check forward to at.
func (store *Store) ReportConnection(ctx context.Context, organizationID, connectionID string, at time.Time) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE connections SET last_reported_at = $3 WHERE organization_id = $1 AND id = $2`,
			organizationID, connectionID, at)
		if err != nil {
			return mapWriteError("report connection", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `
			UPDATE connections_health SET next_check_at = LEAST(next_check_at, $3)
			WHERE organization_id = $1 AND connection_id = $2`,
			organizationID, connectionID, at); err != nil {
			return mapWriteError("advance health check", err)
		}
		return nil
	})
}

// ClaimHealthChecks claims up to limit due, unlocked health rows of active
// connections whose credentials are not deleted, oldest first, so the
// heartbeat worker can check them without two workers claiming the same row.
func (store *Store) ClaimHealthChecks(ctx context.Context, now time.Time, limit int, token string, until time.Time) ([]domain.HealthCheck, error) {
	rows, err := store.pool.Query(ctx, `
		WITH picked AS (
			SELECT h.organization_id, h.connection_id
			FROM connections_health h
			JOIN connections c ON c.organization_id = h.organization_id AND c.id = h.connection_id
			WHERE h.next_check_at <= $1
			  AND (h.locked_until IS NULL OR h.locked_until <= $1)
			  AND c.status = 'active' AND c.credentials_deleted_at IS NULL
			ORDER BY h.next_check_at
			FOR UPDATE OF h SKIP LOCKED
			LIMIT $2
		)
		UPDATE connections_health AS h
		SET claim_token = $3, locked_until = $4
		FROM picked
		WHERE h.organization_id = picked.organization_id AND h.connection_id = picked.connection_id
		RETURNING h.organization_id, h.connection_id, h.state, h.reason, h.consecutive_failures, h.last_checked_at`,
		now, limit, token, until)
	if err != nil {
		return nil, fmt.Errorf("claim health checks: %w", err)
	}
	defer rows.Close()
	checks := []domain.HealthCheck{}
	for rows.Next() {
		var check domain.HealthCheck
		if err := rows.Scan(&check.OrganizationID, &check.ConnectionID, &check.State, &check.Reason,
			&check.ConsecutiveFailures, &check.LastCheckedAt); err != nil {
			return nil, fmt.Errorf("scan health check: %w", err)
		}
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim health checks: %w", err)
	}
	return checks, nil
}

// RecordHealthCheck saves what a claimed check found, and clears the claim.
// A token that no longer matches the row, because it was never claimed or
// another claim has since replaced it, answers ErrConflict.
func (store *Store) RecordHealthCheck(ctx context.Context, check domain.HealthCheck, token string, result domain.HealthResult, event *domain.Event) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		var lastSuccessAt *time.Time
		if result.Succeeded {
			lastSuccessAt = result.CheckedAt
		}
		tag, err := tx.Exec(ctx, `
			UPDATE connections_health
			SET state = $3, reason = $4, consecutive_failures = $5,
			    last_checked_at = COALESCE($6, last_checked_at),
			    last_success_at = COALESCE($7, last_success_at),
			    next_check_at = $8, claim_token = NULL, locked_until = NULL
			WHERE organization_id = $1 AND connection_id = $2 AND claim_token = $9`,
			check.OrganizationID, check.ConnectionID, result.State, result.Reason, result.ConsecutiveFailures,
			result.CheckedAt, lastSuccessAt, result.NextCheckAt, token)
		if err != nil {
			return mapWriteError("record health check", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrConflict
		}
		if event != nil {
			return insertEvents(ctx, tx, *event)
		}
		return nil
	})
}

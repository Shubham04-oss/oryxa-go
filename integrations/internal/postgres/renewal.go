package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// microsoftGraphRenewalAdvisoryLock is a fixed service-wide PostgreSQL lock
// key. It is intentionally independent of tenant IDs: one replica owns the
// bounded full renewal scan, preventing duplicate provider PATCH operations.
const microsoftGraphRenewalAdvisoryLock int64 = 4815162342

// WithMicrosoftGraphRenewalLease holds a session-level advisory lock on one
// acquired pgx connection for callback's entire scan. pgxpool query calls in
// the callback may use other connections; PostgreSQL's session lock still
// excludes every other broker replica connected to this database.
//
// Unlock deliberately uses a non-cancelled, short-lived context. If it cannot
// be confirmed, the physical connection is closed before returning it to the
// pool, ensuring a held session lock cannot leak into a later workload.
func (s *Store) WithMicrosoftGraphRenewalLease(ctx context.Context, callback func(context.Context) error) (acquired bool, resultErr error) {
	if callback == nil {
		return false, errors.New("Microsoft Graph renewal callback is required")
	}
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire Microsoft Graph renewal lease connection: %w", err)
	}
	defer connection.Release()
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, microsoftGraphRenewalAdvisoryLock).Scan(&acquired); err != nil {
		return false, fmt.Errorf("acquire Microsoft Graph renewal lease: %w", err)
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := connection.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, microsoftGraphRenewalAdvisoryLock).Scan(&unlocked)
		if unlockErr != nil || !unlocked {
			// A session-level lock remaining in a pooled connection would block
			// every later replica. Force the session closed before Release.
			_ = connection.Conn().Close(unlockCtx)
			if resultErr == nil {
				if unlockErr != nil {
					resultErr = fmt.Errorf("release Microsoft Graph renewal lease: %w", unlockErr)
				} else {
					resultErr = errors.New("release Microsoft Graph renewal lease: lock was not held")
				}
			}
		}
	}()
	return true, callback(ctx)
}

// ActiveMicrosoftGraphTriggers returns only the stable trigger references a
// renewal scheduler needs. Cursor values are the last organization and trigger
// IDs observed; callers must reuse the final item of a full page as the next
// cursor. The hard limit prevents an accidental unbounded database read.
func (s *Store) ActiveMicrosoftGraphTriggers(ctx context.Context, afterOrganizationID, afterTriggerID string, limit int) ([]domain.Trigger, error) {
	if limit <= 0 {
		return nil, errors.New("renewal trigger limit must be positive")
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.organization_id, t.id
		FROM integrations_triggers t
		JOIN integrations_connections c
		  ON c.organization_id = t.organization_id AND c.id = t.connection_id
		JOIN integrations_oauth_apps p
		  ON p.organization_id = c.organization_id AND p.id = c.profile_id
		WHERE t.provider = 'microsoft'
		  AND t.trigger_name IN ('new-email', 'calendar-event-changed')
		  AND t.status = 'active'
		  AND c.provider = 'microsoft'
		  AND c.status = 'active'
		  AND p.status = 'active'
		  AND (t.organization_id, t.id) > ($1, $2)
		ORDER BY t.organization_id, t.id
		LIMIT $3`, afterOrganizationID, afterTriggerID, limit)
	if err != nil {
		return nil, fmt.Errorf("query active Microsoft Graph triggers: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Trigger, 0, limit)
	for rows.Next() {
		var trigger domain.Trigger
		if err := rows.Scan(&trigger.OrganizationID, &trigger.ID); err != nil {
			return nil, fmt.Errorf("scan active Microsoft Graph trigger: %w", err)
		}
		result = append(result, trigger)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active Microsoft Graph triggers: %w", err)
	}
	return result, nil
}

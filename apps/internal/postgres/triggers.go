package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

const triggerColumns = `id, organization_id, member_id, connection_id, toolkit, slug, config,
	composio_trigger_id, status, status_reason, kind, manual_setup, delivery_count, last_delivery_at,
	created_at, updated_at`

// CreateTrigger records a trigger, unless a live one already answers the
// same Composio instance: Composio hands back the same trigger id for the
// same account, trigger and config, so a retry must not create a second row
// for it. The insert carries its own conflict check, targeting the same
// partial unique index the migration declares, so two callers racing to
// create the same instance cannot both believe they created it - the loser
// reads back what the winner inserted, inside the same transaction.
func (store *Store) CreateTrigger(ctx context.Context, trigger domain.Trigger, event domain.Event) (domain.Trigger, bool, error) {
	var result domain.Trigger
	created := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		triggers, err := queryTriggers(ctx, tx, `
			INSERT INTO apps_triggers (
				id, organization_id, member_id, connection_id, toolkit, slug, config,
				composio_trigger_id, status, status_reason, kind, manual_setup, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)
			ON CONFLICT (composio_trigger_id) WHERE status <> 'removed' DO NOTHING
			RETURNING `+triggerColumns,
			trigger.ID, trigger.OrganizationID, trigger.MemberID, trigger.ConnectionID, trigger.Toolkit,
			trigger.Slug, []byte(trigger.Config), trigger.ComposioTriggerID, string(trigger.Status),
			trigger.StatusReason, trigger.Kind, trigger.ManualSetup, trigger.CreatedAt)
		if err != nil {
			return mapWriteError("create trigger", err)
		}
		if len(triggers) == 1 {
			result = triggers[0]
			created = true
			// Only a subscription that is new announces one; the loser of a
			// race reads back the winner's and says nothing.
			return insertEvents(ctx, tx, event)
		}
		existing, err := queryTriggers(ctx, tx, `
			SELECT `+triggerColumns+`
			FROM apps_triggers
			WHERE composio_trigger_id = $1 AND status <> 'removed'`, trigger.ComposioTriggerID)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			return fmt.Errorf("create trigger: the conflicting instance vanished mid-transaction: %w", domain.ErrConflict)
		}
		result = existing[0]
		return nil
	})
	return result, created, err
}

// Trigger reads one of an organization's triggers, whatever its status.
func (store *Store) Trigger(ctx context.Context, organizationID, id string) (domain.Trigger, error) {
	triggers, err := queryTriggers(ctx, store.pool, `
		SELECT `+triggerColumns+`
		FROM apps_triggers
		WHERE organization_id = $1 AND id = $2`, organizationID, id)
	if err != nil {
		return domain.Trigger{}, err
	}
	if len(triggers) == 0 {
		return domain.Trigger{}, domain.ErrNotFound
	}
	return triggers[0], nil
}

// TriggerByComposioID reads the live trigger a Composio instance backs, for
// a webhook delivery that names the instance rather than apps' own id.
func (store *Store) TriggerByComposioID(ctx context.Context, composioTriggerID string) (domain.Trigger, error) {
	triggers, err := queryTriggers(ctx, store.pool, `
		SELECT `+triggerColumns+`
		FROM apps_triggers
		WHERE composio_trigger_id = $1 AND status <> 'removed'`, composioTriggerID)
	if err != nil {
		return domain.Trigger{}, err
	}
	if len(triggers) == 0 {
		return domain.Trigger{}, domain.ErrNotFound
	}
	return triggers[0], nil
}

// Triggers lists an organization's live triggers, newest first, optionally
// narrowed to one member's or one connection's.
func (store *Store) Triggers(ctx context.Context, organizationID string, query application.TriggerQuery) ([]domain.Trigger, error) {
	sql := `SELECT ` + triggerColumns + ` FROM apps_triggers WHERE organization_id = $1 AND status <> 'removed'`
	args := []any{organizationID}
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if query.MemberID != "" {
		sql += ` AND member_id = ` + bind(query.MemberID)
	}
	if query.ConnectionID != "" {
		sql += ` AND connection_id = ` + bind(query.ConnectionID)
	}
	sql += ` ORDER BY created_at DESC, id`
	return queryTriggers(ctx, store.pool, sql, args...)
}

// RemoveTrigger marks a trigger removed by itself, without touching the
// connection it is on. It answers false if it already was.
func (store *Store) RemoveTrigger(ctx context.Context, id string, now time.Time, event domain.Event) (bool, error) {
	removed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE apps_triggers SET status = 'removed', updated_at = $2
			WHERE id = $1 AND status <> 'removed'`, id, now)
		if err != nil {
			return mapWriteError("remove trigger", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		removed = true
		return insertEvents(ctx, tx, event)
	})
	return removed, err
}

// DisableTrigger marks an active trigger disabled and writes event. It
// answers false if the trigger was not active.
func (store *Store) DisableTrigger(ctx context.Context, id, reason string, now time.Time, event domain.Event) (bool, error) {
	disabled := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE apps_triggers
			SET status = 'disabled', status_reason = $2, updated_at = $3
			WHERE id = $1 AND status = 'active'`, id, reason, now)
		if err != nil {
			return mapWriteError("disable trigger", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		disabled = true
		return insertEvents(ctx, tx, event)
	})
	return disabled, err
}

func queryTriggers(ctx context.Context, db querier, sql string, args ...any) ([]domain.Trigger, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("query triggers", err)
	}
	defer rows.Close()
	triggers := []domain.Trigger{}
	for rows.Next() {
		trigger, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, trigger)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query triggers: %w", err)
	}
	return triggers, nil
}

func scanTrigger(rows pgx.Rows) (domain.Trigger, error) {
	var trigger domain.Trigger
	var config []byte
	var status string
	var lastDelivery *time.Time
	if err := rows.Scan(&trigger.ID, &trigger.OrganizationID, &trigger.MemberID, &trigger.ConnectionID,
		&trigger.Toolkit, &trigger.Slug, &config, &trigger.ComposioTriggerID, &status, &trigger.StatusReason,
		&trigger.Kind, &trigger.ManualSetup, &trigger.DeliveryCount, &lastDelivery,
		&trigger.CreatedAt, &trigger.UpdatedAt); err != nil {
		return domain.Trigger{}, fmt.Errorf("scan trigger: %w", err)
	}
	trigger.Config = json.RawMessage(config)
	trigger.Status = domain.TriggerStatus(status)
	if lastDelivery != nil {
		delivered := lastDelivery.UTC()
		trigger.LastDeliveryAt = &delivered
	}
	trigger.CreatedAt = trigger.CreatedAt.UTC()
	trigger.UpdatedAt = trigger.UpdatedAt.UTC()
	return trigger, nil
}

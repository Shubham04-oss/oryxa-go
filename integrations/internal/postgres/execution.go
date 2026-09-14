package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateTriggerWithEvent(ctx context.Context, trigger domain.Trigger, event domain.Event) error {
	if trigger.ID == "" || trigger.OrganizationID == "" || trigger.MemberID == "" || trigger.ConnectionID == "" || trigger.ProfileID == "" || event.EventID == "" || event.OrganizationID != trigger.OrganizationID {
		return errors.New("trigger and matching event identity are required")
	}
	props := trigger.Props
	if len(props) == 0 {
		props = json.RawMessage(`{}`)
	}
	detail, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("encode trigger event: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin trigger transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the exact connection and its OAuth profile before inserting. This
	// serializes trigger creation with disconnect and OAuth-profile disable: if
	// either transition commits first, no active row is visible; if this insert
	// commits first, their existing transactional trigger-disable update sees it.
	var locked int
	err = tx.QueryRow(ctx, `
		SELECT 1
		FROM integrations_connections c
		JOIN integrations_oauth_apps p
		  ON p.id = c.profile_id AND p.organization_id = c.organization_id
		WHERE c.organization_id = $1 AND c.id = $2 AND c.member_id = $3
		  AND c.profile_id = $4 AND c.status = 'active' AND p.status = 'active'
		FOR UPDATE OF c, p`, trigger.OrganizationID, trigger.ConnectionID, trigger.MemberID, trigger.ProfileID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock active trigger connection and OAuth profile: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO integrations_triggers (
			id, organization_id, member_id, connection_id, profile_id, provider,
			piece_name, piece_version, trigger_name, provider_identifier, props,
			status, version, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		trigger.ID, trigger.OrganizationID, trigger.MemberID, trigger.ConnectionID,
		trigger.ProfileID, trigger.Provider, trigger.PieceName, trigger.PieceVersion,
		trigger.TriggerName, trigger.ProviderIdentifier, props, trigger.Status,
		trigger.Version, trigger.CreatedAt, trigger.UpdatedAt)
	if err != nil {
		return mapWriteError("create trigger", err)
	}
	if event.EventID != "" {
		if err := insertOutbox(ctx, tx, event, detail); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit trigger transaction: %w", err)
	}
	return nil
}

func (s *Store) DisableTriggerWithEvent(ctx context.Context, organizationID, memberID, triggerID string, now time.Time, event domain.Event) error {
	detail, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("encode trigger event: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin disable trigger transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := enqueueMicrosoftGraphCleanupForTrigger(ctx, tx, organizationID, triggerID, now); err != nil {
		return err
	}
	if err := enqueueGitHubCleanupForTrigger(ctx, tx, organizationID, triggerID, now); err != nil {
		return err
	}
	if err := enqueueGoogleCleanupForTrigger(ctx, tx, organizationID, triggerID, now); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE integrations_triggers
		SET status = 'disabled', version = version + 1, updated_at = $4
		WHERE organization_id = $1 AND member_id = $2 AND id = $3 AND status = 'active'`,
		organizationID, memberID, triggerID, now)
	if err != nil {
		return fmt.Errorf("disable trigger: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	if event.EventID != "" {
		if err := insertOutbox(ctx, tx, event, detail); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit disable trigger transaction: %w", err)
	}
	return nil
}

func (s *Store) Trigger(ctx context.Context, organizationID, triggerID string) (domain.Trigger, error) {
	return scanTrigger(s.pool.QueryRow(ctx, `
		SELECT id, organization_id, member_id, connection_id, profile_id, provider,
		       piece_name, piece_version, trigger_name, provider_identifier, props,
		       status, version, created_at, updated_at
		FROM integrations_triggers WHERE organization_id = $1 AND id = $2`, organizationID, triggerID))
}

func (s *Store) RunnableTriggers(ctx context.Context, organizationID, profileID, pieceName, pieceVersion, triggerName, connectionID, providerIdentifier string) ([]domain.Trigger, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.organization_id, t.member_id, t.connection_id, t.profile_id,
		       t.provider, t.piece_name, t.piece_version, t.trigger_name,
		       t.provider_identifier, t.props, t.status, t.version,
		       t.created_at, t.updated_at
		FROM integrations_triggers t
		JOIN integrations_connections c
		  ON c.id = t.connection_id AND c.organization_id = t.organization_id
		JOIN integrations_oauth_apps p
		  ON p.id = c.profile_id AND p.organization_id = c.organization_id
		WHERE t.organization_id = $1 AND t.profile_id = $2
		  AND t.piece_name = $3 AND t.piece_version = $4
		  AND ($5 = $8 OR t.trigger_name = $5)
		  AND t.status = 'active' AND c.status = 'active' AND p.status = 'active'
		  AND ($6 = '' OR t.connection_id = $6)
		  AND ($7 = '' OR t.provider_identifier = $7)
		ORDER BY t.id`, organizationID, profileID, pieceName, pieceVersion, triggerName, connectionID, providerIdentifier, domain.SlackEventsRouteTrigger)
	if err != nil {
		return nil, fmt.Errorf("query runnable triggers: %w", err)
	}
	defer rows.Close()
	var result []domain.Trigger
	for rows.Next() {
		trigger, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, trigger)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runnable triggers: %w", err)
	}
	return result, nil
}

type rowScanner interface{ Scan(...any) error }

func scanTrigger(row rowScanner) (domain.Trigger, error) {
	var trigger domain.Trigger
	if err := row.Scan(&trigger.ID, &trigger.OrganizationID, &trigger.MemberID,
		&trigger.ConnectionID, &trigger.ProfileID, &trigger.Provider,
		&trigger.PieceName, &trigger.PieceVersion, &trigger.TriggerName,
		&trigger.ProviderIdentifier, &trigger.Props, &trigger.Status,
		&trigger.Version, &trigger.CreatedAt, &trigger.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Trigger{}, domain.ErrNotFound
		}
		return domain.Trigger{}, fmt.Errorf("scan trigger: %w", err)
	}
	return trigger, nil
}

func (s *Store) ClaimTriggerExecution(ctx context.Context, executionID, organizationID, triggerID, invocationID string, now, staleBefore time.Time) (domain.TriggerExecution, bool, error) {
	var execution domain.TriggerExecution
	err := s.pool.QueryRow(ctx, `
		INSERT INTO integrations_trigger_executions (
			id, organization_id, trigger_id, invocation_id, status, attempt, started_at
		) VALUES ($1,$2,$3,$4,'processing',1,$5)
		ON CONFLICT (trigger_id, invocation_id) DO UPDATE
		SET status = 'processing', attempt = integrations_trigger_executions.attempt + 1,
		    started_at = EXCLUDED.started_at, completed_at = NULL, last_error_code = ''
		WHERE integrations_trigger_executions.status = 'failed'
		   OR (integrations_trigger_executions.status = 'processing'
		       AND integrations_trigger_executions.started_at < $6)
		RETURNING id, organization_id, trigger_id, invocation_id, status, attempt,
		          started_at, completed_at`, executionID, organizationID, triggerID, invocationID, now, staleBefore).
		Scan(&execution.ID, &execution.OrganizationID, &execution.TriggerID,
			&execution.InvocationID, &execution.Status, &execution.Attempt,
			&execution.StartedAt, &execution.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.pool.QueryRow(ctx, `
			SELECT id, organization_id, trigger_id, invocation_id, status, attempt,
			       started_at, completed_at
			FROM integrations_trigger_executions
			WHERE trigger_id=$1 AND invocation_id=$2`, triggerID, invocationID).
			Scan(&execution.ID, &execution.OrganizationID, &execution.TriggerID,
				&execution.InvocationID, &execution.Status, &execution.Attempt,
				&execution.StartedAt, &execution.CompletedAt)
		if err != nil {
			return domain.TriggerExecution{}, false, fmt.Errorf("read existing trigger execution: %w", err)
		}
		return execution, false, nil
	}
	if err != nil {
		return domain.TriggerExecution{}, false, fmt.Errorf("claim trigger execution: %w", err)
	}
	return execution, true, nil
}

func (s *Store) CompleteTriggerExecutionWithEvent(ctx context.Context, execution domain.TriggerExecution, completedAt time.Time, event domain.Event) error {
	detail, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("encode trigger output event: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin complete execution transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE integrations_trigger_executions
		SET status = 'succeeded', completed_at = $4, last_error_code = ''
		WHERE id = $1 AND attempt = $2 AND status = 'processing' AND organization_id = $3`,
		execution.ID, execution.Attempt, execution.OrganizationID, completedAt)
	if err != nil {
		return fmt.Errorf("complete trigger execution: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConflict
	}
	if event.EventID != "" {
		if err := insertOutbox(ctx, tx, event, detail); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit trigger execution: %w", err)
	}
	return nil
}

func (s *Store) FailTriggerExecution(ctx context.Context, execution domain.TriggerExecution, completedAt time.Time, code string) error {
	if len(code) > 120 {
		code = code[:120]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE integrations_trigger_executions
		SET status = 'failed', completed_at = $4, last_error_code = $5
		WHERE id = $1 AND attempt = $2 AND organization_id = $3 AND status = 'processing'`,
		execution.ID, execution.Attempt, execution.OrganizationID, completedAt, code)
	if err != nil {
		return fmt.Errorf("fail trigger execution: %w", err)
	}
	return nil
}

func insertOutbox(ctx context.Context, tx pgx.Tx, event domain.Event, detail []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO integrations_outbox
			(id, source, detail_type, version, organization_id, detail, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, event.EventID, event.Source,
		event.DetailType, event.Version, event.OrganizationID, detail, event.OccurredAt)
	if err != nil {
		return mapWriteError("create outbox event", err)
	}
	return nil
}

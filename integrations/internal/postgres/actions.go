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

func (s *Store) ClaimActionExecution(ctx context.Context, execution domain.ActionExecution) (domain.ActionExecution, bool, error) {
	if execution.ID == "" || execution.OrganizationID == "" || execution.MemberID == "" || execution.ConnectionID == "" ||
		execution.ActionName == "" || len(execution.IdempotencyKeyHash) != 64 || len(execution.InputHash) != 64 {
		return domain.ActionExecution{}, false, errors.New("invalid action execution claim")
	}
	claimed := execution
	err := s.pool.QueryRow(ctx, `
		INSERT INTO integrations_action_executions (
			id, organization_id, member_id, connection_id, provider, piece_name,
			piece_version, action_name, idempotency_key_hash, input_hash,
			status, attempt, started_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'processing',1,$11)
		ON CONFLICT (organization_id, connection_id, action_name, idempotency_key_hash)
		DO NOTHING
		RETURNING id, organization_id, member_id, connection_id, provider,
		          piece_name, piece_version, action_name, idempotency_key_hash,
		          input_hash, status, attempt, safe_result, started_at, completed_at`,
		execution.ID, execution.OrganizationID, execution.MemberID,
		execution.ConnectionID, execution.Provider, execution.PieceName,
		execution.PieceVersion, execution.ActionName,
		execution.IdempotencyKeyHash, execution.InputHash, execution.StartedAt).
		Scan(&claimed.ID, &claimed.OrganizationID, &claimed.MemberID,
			&claimed.ConnectionID, &claimed.Provider, &claimed.PieceName,
			&claimed.PieceVersion, &claimed.ActionName,
			&claimed.IdempotencyKeyHash, &claimed.InputHash, &claimed.Status,
			&claimed.Attempt, &claimed.SafeResult, &claimed.StartedAt,
			&claimed.CompletedAt)
	if err == nil {
		return claimed, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ActionExecution{}, false, mapWriteError("claim action execution", err)
	}
	existing, err := s.actionExecution(ctx, execution.OrganizationID,
		execution.ConnectionID, execution.ActionName, execution.IdempotencyKeyHash)
	if err != nil {
		return domain.ActionExecution{}, false, err
	}
	return existing, false, nil
}

func (s *Store) actionExecution(ctx context.Context, organizationID, connectionID, actionName, keyHash string) (domain.ActionExecution, error) {
	var execution domain.ActionExecution
	err := s.pool.QueryRow(ctx, `
		SELECT id, organization_id, member_id, connection_id, provider,
		       piece_name, piece_version, action_name, idempotency_key_hash,
		       input_hash, status, attempt, safe_result, started_at, completed_at
		FROM integrations_action_executions
		WHERE organization_id=$1 AND connection_id=$2 AND action_name=$3
		  AND idempotency_key_hash=$4`, organizationID, connectionID, actionName, keyHash).
		Scan(&execution.ID, &execution.OrganizationID, &execution.MemberID,
			&execution.ConnectionID, &execution.Provider, &execution.PieceName,
			&execution.PieceVersion, &execution.ActionName,
			&execution.IdempotencyKeyHash, &execution.InputHash,
			&execution.Status, &execution.Attempt, &execution.SafeResult,
			&execution.StartedAt, &execution.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ActionExecution{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ActionExecution{}, fmt.Errorf("read action execution: %w", err)
	}
	return execution, nil
}

func (s *Store) CompleteActionExecutionWithEvent(ctx context.Context, execution domain.ActionExecution, safeResult json.RawMessage, completedAt time.Time, event domain.Event) error {
	if !json.Valid(safeResult) {
		return errors.New("safe action result must be valid JSON")
	}
	var detail []byte
	var err error
	if event.EventID != "" {
		detail, err = json.Marshal(event.Data)
		if err != nil {
			return fmt.Errorf("encode action event: %w", err)
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin complete action transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE integrations_action_executions
		SET status='succeeded', safe_result=$4, completed_at=$5,
		    last_error_code=''
		WHERE id=$1 AND organization_id=$2 AND attempt=$3 AND status='processing'`,
		execution.ID, execution.OrganizationID, execution.Attempt, safeResult, completedAt)
	if err != nil {
		return fmt.Errorf("complete action execution: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConflict
	}
	if event.EventID != "" {
		if event.OrganizationID != execution.OrganizationID {
			return errors.New("action event organization mismatch")
		}
		if err := insertOutbox(ctx, tx, event, detail); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit action execution: %w", err)
	}
	return nil
}

func (s *Store) FailActionExecution(ctx context.Context, execution domain.ActionExecution, completedAt time.Time, code string) error {
	if len(code) > 120 {
		code = code[:120]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE integrations_action_executions
		SET status='failed', completed_at=$4, last_error_code=$5
		WHERE id=$1 AND organization_id=$2 AND attempt=$3 AND status='processing'`,
		execution.ID, execution.OrganizationID, execution.Attempt, completedAt, code)
	if err != nil {
		return fmt.Errorf("fail action execution: %w", err)
	}
	return nil
}

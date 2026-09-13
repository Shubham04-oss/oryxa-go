package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// ClaimMicrosoftGraphCleanup claims a bounded batch of work. The returned
// connection can be disconnected or attached to a disabled OAuth profile:
// cleanup must use its previously sealed grant without making it executable
// for normal product traffic.
func (s *Store) ClaimMicrosoftGraphCleanup(ctx context.Context, limit int, leaseToken string, now, leaseUntil time.Time) ([]domain.MicrosoftGraphCleanupJob, error) {
	if limit < 1 || limit > 100 || leaseToken == "" || !leaseUntil.After(now) {
		return nil, errors.New("invalid Microsoft Graph cleanup claim")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin Microsoft Graph cleanup claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		WITH picked AS (
			SELECT organization_id, trigger_id
			FROM integrations_microsoft_graph_cleanup
			WHERE completed_at IS NULL AND available_at <= $1
			  AND (locked_until IS NULL OR locked_until < $1)
			ORDER BY created_at, trigger_id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		), claimed AS (
			UPDATE integrations_microsoft_graph_cleanup job
			SET attempts = job.attempts + 1, claim_token = $3, locked_until = $4
			FROM picked
			WHERE job.organization_id = picked.organization_id
			  AND job.trigger_id = picked.trigger_id
			RETURNING job.organization_id, job.trigger_id, job.connection_id,
				          job.profile_id, job.trigger_name, job.created_at, job.attempts, job.claim_token
		)
		SELECT claimed.organization_id, claimed.trigger_id, claimed.connection_id,
		       claimed.profile_id, claimed.trigger_name, claimed.created_at, claimed.attempts,
		       claimed.claim_token,
		       c.id, c.organization_id, c.member_id, c.profile_id, c.provider,
		       c.piece_name, c.piece_version, c.provider_subject_id,
		       c.provider_account_id, c.provider_account_name,
		       c.provider_tenant_id, c.provider_tenant_name, c.granted_scopes,
		       c.encrypted_credentials, c.status, c.version, c.created_at,
		       c.updated_at
		FROM claimed
		JOIN integrations_connections c
		  ON c.organization_id = claimed.organization_id
		 AND c.id = claimed.connection_id
		 AND c.profile_id = claimed.profile_id
		ORDER BY claimed.trigger_id`, now, limit, leaseToken, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("claim Microsoft Graph cleanup: %w", err)
	}
	defer rows.Close()
	jobs := make([]domain.MicrosoftGraphCleanupJob, 0, limit)
	for rows.Next() {
		job, err := scanMicrosoftGraphCleanupJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Microsoft Graph cleanup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit Microsoft Graph cleanup claim: %w", err)
	}
	return jobs, nil
}

func (s *Store) CompleteMicrosoftGraphCleanup(ctx context.Context, job domain.MicrosoftGraphCleanupJob, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE integrations_microsoft_graph_cleanup
		SET completed_at = $4, claim_token = NULL, locked_until = NULL, last_error = NULL
		WHERE organization_id = $1 AND trigger_id = $2 AND claim_token = $3
		  AND completed_at IS NULL`, job.OrganizationID, job.TriggerID, job.LeaseToken, now)
	if err != nil {
		return fmt.Errorf("complete Microsoft Graph cleanup: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) RetryMicrosoftGraphCleanup(ctx context.Context, job domain.MicrosoftGraphCleanupJob, availableAt time.Time, safeCode string) error {
	if safeCode == "" {
		safeCode = "cleanup_failed"
	}
	tag, err := s.pool.Exec(ctx, `UPDATE integrations_microsoft_graph_cleanup
		SET available_at = $4, claim_token = NULL, locked_until = NULL,
		    last_error = left($5, 80)
		WHERE organization_id = $1 AND trigger_id = $2 AND claim_token = $3
		  AND completed_at IS NULL`, job.OrganizationID, job.TriggerID, job.LeaseToken, availableAt, safeCode)
	if err != nil {
		return fmt.Errorf("retry Microsoft Graph cleanup: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}

type cleanupRowScanner interface{ Scan(...any) error }

func scanMicrosoftGraphCleanupJob(row cleanupRowScanner) (domain.MicrosoftGraphCleanupJob, error) {
	var job domain.MicrosoftGraphCleanupJob
	var credentials []byte
	err := row.Scan(
		&job.OrganizationID, &job.TriggerID, &job.ConnectionID, &job.ProfileID,
		&job.TriggerName, &job.CreatedAt, &job.Attempt, &job.LeaseToken,
		&job.Connection.ID, &job.Connection.OrganizationID, &job.Connection.MemberID,
		&job.Connection.ProfileID, &job.Connection.Provider, &job.Connection.PieceName,
		&job.Connection.PieceVersion, &job.Connection.Identity.SubjectID,
		&job.Connection.Identity.AccountID, &job.Connection.Identity.AccountName,
		&job.Connection.Identity.TenantID, &job.Connection.Identity.TenantName,
		&job.Connection.GrantedScopes, &credentials, &job.Connection.Status,
		&job.Connection.Version, &job.Connection.CreatedAt, &job.Connection.UpdatedAt,
	)
	if err != nil {
		return domain.MicrosoftGraphCleanupJob{}, fmt.Errorf("scan Microsoft Graph cleanup job: %w", err)
	}
	if err := decodeSealed(credentials, &job.Connection.EncryptedCredentials); err != nil {
		return domain.MicrosoftGraphCleanupJob{}, fmt.Errorf("decode Microsoft Graph cleanup credentials: %w", err)
	}
	return job, nil
}

func decodeSealed(value []byte, destination *domain.SealedData) error {
	// Keep JSON decoding beside the other postgres storage code, while making
	// the claim scanner small and ensuring sealed data never leaves this process
	// except through the worker's in-memory KMS open call.
	if err := json.Unmarshal(value, destination); err != nil {
		return err
	}
	return nil
}

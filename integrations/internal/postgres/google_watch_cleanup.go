package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func (s *Store) ClaimGoogleCleanup(ctx context.Context, limit int, leaseToken string, now, leaseUntil time.Time) ([]domain.GoogleCleanupJob, error) {
	if limit < 1 || limit > 100 || leaseToken == "" || !leaseUntil.After(now) {
		return nil, errors.New("invalid Google cleanup claim")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin Google cleanup claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		WITH picked AS (
			SELECT organization_id, trigger_id
			FROM integrations_google_watch_cleanup
			WHERE completed_at IS NULL AND available_at <= $1
			  AND (locked_until IS NULL OR locked_until < $1)
			ORDER BY created_at, trigger_id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		), claimed AS (
			UPDATE integrations_google_watch_cleanup job
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
		return nil, fmt.Errorf("claim Google cleanup: %w", err)
	}
	defer rows.Close()
	jobs := make([]domain.GoogleCleanupJob, 0, limit)
	for rows.Next() {
		job, err := scanGoogleCleanupJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Google cleanup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit Google cleanup claim: %w", err)
	}
	return jobs, nil
}

func (s *Store) CompleteGoogleCleanup(ctx context.Context, job domain.GoogleCleanupJob, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE integrations_google_watch_cleanup
		SET completed_at = $4, claim_token = NULL, locked_until = NULL, last_error = NULL
		WHERE organization_id = $1 AND trigger_id = $2 AND claim_token = $3
		  AND completed_at IS NULL`, job.OrganizationID, job.TriggerID, job.LeaseToken, now)
	if err != nil {
		return fmt.Errorf("complete Google cleanup: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) RetryGoogleCleanup(ctx context.Context, job domain.GoogleCleanupJob, availableAt time.Time, safeCode string) error {
	if safeCode == "" {
		safeCode = "cleanup_failed"
	}
	tag, err := s.pool.Exec(ctx, `UPDATE integrations_google_watch_cleanup
		SET available_at = $4, claim_token = NULL, locked_until = NULL,
		    last_error = left($5, 80)
		WHERE organization_id = $1 AND trigger_id = $2 AND claim_token = $3
		  AND completed_at IS NULL`, job.OrganizationID, job.TriggerID, job.LeaseToken, availableAt, safeCode)
	if err != nil {
		return fmt.Errorf("retry Google cleanup: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) ActiveGoogleWatchTriggers(ctx context.Context, afterOrganizationID, afterTriggerID string, limit int) ([]domain.Trigger, error) {
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
		WHERE t.provider = 'google'
		  AND t.trigger_name IN ('new-email', 'calendar-event-changed')
		  AND t.status = 'active'
		  AND c.provider = 'google'
		  AND c.status = 'active'
		  AND p.status = 'active'
		  AND (t.organization_id, t.id) > ($1, $2)
		ORDER BY t.organization_id, t.id
		LIMIT $3`, afterOrganizationID, afterTriggerID, limit)
	if err != nil {
		return nil, fmt.Errorf("query active Google watch triggers: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Trigger, 0, limit)
	for rows.Next() {
		var trigger domain.Trigger
		if err := rows.Scan(&trigger.OrganizationID, &trigger.ID); err != nil {
			return nil, fmt.Errorf("scan active Google watch trigger: %w", err)
		}
		result = append(result, trigger)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active Google watch triggers: %w", err)
	}
	return result, nil
}

func (s *Store) ActiveGoogleGmailTriggers(ctx context.Context, email string) ([]domain.Trigger, error) {
	if email == "" {
		return nil, errors.New("Gmail mailbox email is required")
	}
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
		WHERE t.provider = 'google' AND t.trigger_name = 'new-email' AND t.status = 'active'
		  AND c.provider = 'google' AND c.status = 'active' AND p.status = 'active'
		  AND lower(c.provider_account_id) = lower($1)
		ORDER BY t.organization_id, t.id`, email)
	if err != nil {
		return nil, fmt.Errorf("query Gmail mailbox triggers: %w", err)
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
		return nil, fmt.Errorf("iterate Gmail mailbox triggers: %w", err)
	}
	return result, nil
}

func (s *Store) ActiveGoogleGmailTriggerCount(ctx context.Context, email string) (int, error) {
	if email == "" {
		return 0, errors.New("Gmail mailbox email is required")
	}
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM integrations_triggers t
		JOIN integrations_connections c
		  ON c.id = t.connection_id AND c.organization_id = t.organization_id
		JOIN integrations_oauth_apps p
		  ON p.id = c.profile_id AND p.organization_id = c.organization_id
		WHERE t.provider = 'google' AND t.trigger_name = 'new-email' AND t.status = 'active'
		  AND c.provider = 'google' AND c.status = 'active' AND p.status = 'active'
		  AND lower(c.provider_account_id) = lower($1)`, email).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count Gmail mailbox triggers: %w", err)
	}
	return count, nil
}

func scanGoogleCleanupJob(row cleanupRowScanner) (domain.GoogleCleanupJob, error) {
	var job domain.GoogleCleanupJob
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
		return domain.GoogleCleanupJob{}, fmt.Errorf("scan Google cleanup job: %w", err)
	}
	if err := decodeSealed(credentials, &job.Connection.EncryptedCredentials); err != nil {
		return domain.GoogleCleanupJob{}, fmt.Errorf("decode Google cleanup credentials: %w", err)
	}
	return job, nil
}

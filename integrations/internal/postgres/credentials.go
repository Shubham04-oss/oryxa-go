package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func (s *Store) AcquireConnectionRefreshLease(ctx context.Context, organizationID, connectionID string, expectedVersion int64, token string, now, until time.Time) (bool, error) {
	if organizationID == "" || connectionID == "" || expectedVersion < 1 || token == "" || !until.After(now) {
		return false, errors.New("invalid credential refresh lease")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE integrations_connections
		SET refresh_lease_token=$4, refresh_lease_until=$5
		WHERE organization_id=$1 AND id=$2 AND version=$3 AND status='active'
		  AND (refresh_lease_until IS NULL OR refresh_lease_until < $6)`,
		organizationID, connectionID, expectedVersion, token, until, now)
	if err != nil {
		return false, fmt.Errorf("acquire credential refresh lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) ReleaseConnectionRefreshLease(ctx context.Context, organizationID, connectionID, token string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE integrations_connections
		SET refresh_lease_token=NULL, refresh_lease_until=NULL
		WHERE organization_id=$1 AND id=$2 AND refresh_lease_token=$3`,
		organizationID, connectionID, token)
	if err != nil {
		return fmt.Errorf("release credential refresh lease: %w", err)
	}
	return nil
}

func (s *Store) UpdateConnectionCredentials(ctx context.Context, organizationID, connectionID, token string, expectedVersion int64, encrypted domain.SealedData, scopes []string, now time.Time) error {
	encoded, err := json.Marshal(encrypted)
	if err != nil {
		return fmt.Errorf("encode refreshed credentials: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE integrations_connections
		SET encrypted_credentials=$5, granted_scopes=$6, version=version+1,
		    updated_at=$7, refresh_lease_token=NULL, refresh_lease_until=NULL
		WHERE organization_id=$1 AND id=$2 AND refresh_lease_token=$3
		  AND version=$4 AND status='active'`,
		organizationID, connectionID, token, expectedVersion, encoded, scopes, now)
	if err != nil {
		return fmt.Errorf("update refreshed credentials: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) MarkConnectionReauthorizationWithEvent(ctx context.Context, organizationID, connectionID, token string, expectedVersion int64, now time.Time, event domain.Event) error {
	if event.EventID == "" || event.OrganizationID != organizationID {
		return errors.New("matching reauthorization event is required")
	}
	detail, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("encode reauthorization event: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reauthorization transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE integrations_connections
		SET status='reauthorization_required', version=version+1, updated_at=$5,
		    refresh_lease_token=NULL, refresh_lease_until=NULL
		WHERE organization_id=$1 AND id=$2 AND refresh_lease_token=$3
		  AND version=$4 AND status='active'`,
		organizationID, connectionID, token, expectedVersion, now)
	if err != nil {
		return fmt.Errorf("mark connection for reauthorization: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `
		UPDATE integrations_triggers
		SET status='disabled', version=version+1, updated_at=$3
		WHERE organization_id=$1 AND connection_id=$2 AND status='active'`,
		organizationID, connectionID, now); err != nil {
		return fmt.Errorf("disable triggers requiring reauthorization: %w", err)
	}
	if err := insertOutbox(ctx, tx, event, detail); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reauthorization transaction: %w", err)
	}
	return nil
}

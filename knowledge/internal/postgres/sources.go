package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

const sourceColumns = `id, organization_id, connection_id, app_id, provider, target, scope, team_id, member_id,
	created_by, status, reason, interval_seconds, sync_at, last_synced_at, attempts,
	lease_token, lease_until, version, created_at, updated_at`

// SourceQuery narrows a listing. An org admin sees the organization's sources;
// anyone else sees the ones they registered.
type SourceQuery struct {
	CreatedBy    string
	ConnectionID string
	Provider     string
	Admin        bool
}

func (store *Store) CreateSource(ctx context.Context, source domain.Source, event *domain.Event) error {
	target, err := encodeMap(source.Target)
	if err != nil {
		return err
	}
	return store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO knowledge_sources (
				id, organization_id, connection_id, app_id, provider, target, scope, team_id, member_id,
				created_by, status, reason, interval_seconds, sync_at, attempts, version, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 0, 1, $15, $15)`,
			source.ID, source.OrganizationID, source.ConnectionID, source.AppID, source.Provider, target,
			string(source.Scope), nullable(source.TeamID), nullable(source.MemberID), source.CreatedBy,
			string(source.Status), source.Reason, int(source.Interval/time.Second), source.SyncAt,
			source.CreatedAt); err != nil {
			return mapWriteError("create source", err)
		}
		if event != nil {
			return insertEvents(ctx, tx, *event)
		}
		return nil
	})
}

func (store *Store) Source(ctx context.Context, organizationID, sourceID string) (domain.Source, error) {
	sources, err := querySources(ctx, store.pool, `
		SELECT `+sourceColumns+`
		FROM knowledge_sources
		WHERE organization_id = $1 AND id = $2`, organizationID, sourceID)
	if err != nil {
		return domain.Source{}, err
	}
	if len(sources) == 0 {
		return domain.Source{}, domain.ErrNotFound
	}
	return sources[0], nil
}

func (store *Store) Sources(ctx context.Context, organizationID string, query SourceQuery) ([]domain.Source, error) {
	sql := `SELECT ` + sourceColumns + ` FROM knowledge_sources WHERE organization_id = $1 AND status <> 'removed'`
	args := []any{organizationID}
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if !query.Admin {
		sql += ` AND created_by = ` + bind(query.CreatedBy)
	}
	if query.ConnectionID != "" {
		sql += ` AND connection_id = ` + bind(query.ConnectionID)
	}
	if query.Provider != "" {
		sql += ` AND provider = ` + bind(query.Provider)
	}
	sql += ` ORDER BY created_at DESC, id`
	return querySources(ctx, store.pool, sql, args...)
}

// ClaimDueSources leases the sources whose next run is due. Every replica may
// run this: SKIP LOCKED hands each row to one of them, and the lease outlives
// the claim so a replica that dies does not strand its work.
func (store *Store) ClaimDueSources(ctx context.Context, now time.Time, limit int, leaseToken string, leaseUntil time.Time) ([]domain.Source, error) {
	return querySources(ctx, store.pool, `
		WITH picked AS (
			SELECT id
			FROM knowledge_sources
			WHERE status = 'active'
			  AND sync_at IS NOT NULL AND sync_at <= $1
			  AND (lease_until IS NULL OR lease_until < $1)
			ORDER BY sync_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE knowledge_sources AS source
		SET lease_token = $3, lease_until = $4
		FROM picked
		WHERE source.id = picked.id
		RETURNING `+prefixed("source", sourceColumns), now, limit, leaseToken, leaseUntil)
}

// RenewLease extends a claim mid-run. Extraction can take longer than one
// lease window, so a run holds its claim by saying it is still working. It
// answers false when the lease is no longer this run's, which is the signal to
// stop rather than write what another replica now owns.
func (store *Store) RenewLease(ctx context.Context, sourceID, leaseToken string, leaseUntil time.Time) (bool, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE knowledge_sources
		SET lease_until = $3
		WHERE id = $1 AND lease_token = $2`, sourceID, leaseToken, leaseUntil)
	if err != nil {
		return false, fmt.Errorf("renew source lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// FinishSource schedules the next run and releases the claim. It answers false
// when the lease has moved on, so a run that lost it changes nothing.
func (store *Store) FinishSource(ctx context.Context, sourceID, leaseToken string, now time.Time, nextSync time.Time, event *domain.Event) (bool, error) {
	claimed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE knowledge_sources
			SET last_synced_at = $3, sync_at = $4, attempts = 0, reason = '',
			    lease_token = NULL, lease_until = NULL, updated_at = $3, version = version + 1
			WHERE id = $1 AND lease_token = $2`, sourceID, leaseToken, now, nextSync)
		if err != nil {
			return fmt.Errorf("finish source: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		claimed = true
		if event != nil {
			return insertEvents(ctx, tx, *event)
		}
		return nil
	})
	return claimed, err
}

// FailSource backs the source off and releases the claim, keeping the reason
// so a member can see why nothing new arrived.
func (store *Store) FailSource(ctx context.Context, sourceID, leaseToken, reason string, now time.Time, retryAt time.Time, event *domain.Event) (bool, error) {
	claimed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE knowledge_sources
			SET attempts = attempts + 1, sync_at = $4, reason = left($5, 200),
			    lease_token = NULL, lease_until = NULL, updated_at = $3, version = version + 1
			WHERE id = $1 AND lease_token = $2`, sourceID, leaseToken, now, retryAt, reason)
		if err != nil {
			return fmt.Errorf("fail source: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		claimed = true
		if event != nil {
			return insertEvents(ctx, tx, *event)
		}
		return nil
	})
	return claimed, err
}

// PauseSources stops every live source reading through what went away, and
// writes one event for each. Running it again pauses nothing and writes
// nothing, so a redelivered event is harmless.
func (store *Store) PauseSources(ctx context.Context, column, value, reason string, now time.Time, event func(domain.Source) (domain.Event, error)) ([]domain.Source, error) {
	if column != "connection_id" && column != "app_id" {
		return nil, fmt.Errorf("sources are paused by connection or app, not %q", column)
	}
	var paused []domain.Source
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		sources, err := querySources(ctx, tx, `
			UPDATE knowledge_sources
			SET status = 'paused', reason = $2, sync_at = NULL,
			    lease_token = NULL, lease_until = NULL, updated_at = $3, version = version + 1
			WHERE `+column+` = $1 AND status = 'active'
			RETURNING `+sourceColumns, value, reason, now)
		if err != nil {
			return err
		}
		for _, source := range sources {
			if event == nil {
				continue
			}
			recorded, err := event(source)
			if err != nil {
				return err
			}
			if err := insertEvents(ctx, tx, recorded); err != nil {
				return err
			}
		}
		paused = sources
		return nil
	})
	return paused, err
}

// RemoveSources retires every source an organization, or one of its members,
// no longer has, and retracts what they held in the same transaction.
func (store *Store) RemoveSources(ctx context.Context, organizationID, createdBy string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error) {
	sql := `
		UPDATE knowledge_sources
		SET status = 'removed', sync_at = NULL, lease_token = NULL, lease_until = NULL,
		    updated_at = $2, version = version + 1
		WHERE organization_id = $1 AND status <> 'removed'`
	args := []any{organizationID, now}
	if createdBy != "" {
		sql += ` AND created_by = $3`
		args = append(args, createdBy)
	}
	sql += ` RETURNING ` + sourceColumns

	var removed []domain.Source
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		sources, err := querySources(ctx, tx, sql, args...)
		if err != nil {
			return err
		}
		for _, source := range sources {
			documents, err := retractDocuments(ctx, tx, source.ID, now)
			if err != nil {
				return err
			}
			if events == nil {
				continue
			}
			recorded, err := events(source, documents)
			if err != nil {
				return err
			}
			if err := insertEvents(ctx, tx, recorded...); err != nil {
				return err
			}
		}
		removed = sources
		return nil
	})
	return removed, err
}

// RemoveSource retires one source a member asked to stop indexing, retracting
// its documents with it.
func (store *Store) RemoveSource(ctx context.Context, organizationID, sourceID string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		sources, err := querySources(ctx, tx, `
			UPDATE knowledge_sources
			SET status = 'removed', sync_at = NULL, lease_token = NULL, lease_until = NULL,
			    updated_at = $3, version = version + 1
			WHERE organization_id = $1 AND id = $2 AND status <> 'removed'
			RETURNING `+sourceColumns, organizationID, sourceID, now)
		if err != nil {
			return err
		}
		if len(sources) == 0 {
			return domain.ErrNotFound
		}
		documents, err := retractDocuments(ctx, tx, sourceID, now)
		if err != nil {
			return err
		}
		if events == nil {
			return nil
		}
		recorded, err := events(sources[0], documents)
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, recorded...)
	})
}

// querier is what both a pool and a transaction answer, so a statement reads
// the same inside one as outside.
type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func querySources(ctx context.Context, db querier, sql string, args ...any) ([]domain.Source, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("query sources", err)
	}
	defer rows.Close()
	sources := []domain.Source{}
	for rows.Next() {
		source, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query sources: %w", err)
	}
	return sources, nil
}

func scanSource(rows pgx.Rows) (domain.Source, error) {
	var source domain.Source
	var target []byte
	var scope, status string
	var teamID, memberID, leaseToken *string
	var intervalSeconds int
	if err := rows.Scan(&source.ID, &source.OrganizationID, &source.ConnectionID, &source.AppID, &source.Provider,
		&target, &scope, &teamID, &memberID, &source.CreatedBy, &status, &source.Reason, &intervalSeconds,
		&source.SyncAt, &source.LastSyncedAt, &source.Attempts, &leaseToken, &source.LeaseUntil,
		&source.Version, &source.CreatedAt, &source.UpdatedAt); err != nil {
		return domain.Source{}, fmt.Errorf("scan source: %w", err)
	}
	decoded, err := decodeMap(target)
	if err != nil {
		return domain.Source{}, err
	}
	source.Target = decoded
	source.Scope = domain.Scope(scope)
	source.Status = domain.Status(status)
	source.TeamID = text(teamID)
	source.MemberID = text(memberID)
	source.LeaseToken = text(leaseToken)
	source.Interval = time.Duration(intervalSeconds) * time.Second
	source.CreatedAt = source.CreatedAt.UTC()
	source.UpdatedAt = source.UpdatedAt.UTC()
	return source, nil
}

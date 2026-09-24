package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

const sourceColumns = `id, organization_id, connection_id, app_id, composio_account_id, provider, target, scope, team_id, member_id,
	created_by, status, reason, interval_seconds, sync_at, last_synced_at, attempts,
	lease_token, lease_until, version, created_at, updated_at, full_sweep_at, include_kinds, kinds_pending`

func (store *Store) CreateSource(ctx context.Context, source domain.Source, event *domain.Event) error {
	target, err := encodeMap(source.Target)
	if err != nil {
		return err
	}
	return store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO knowledge_sources (
				id, organization_id, connection_id, app_id, composio_account_id, provider, target, scope, team_id, member_id,
				created_by, status, reason, interval_seconds, sync_at, attempts, version, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 0, 1, $16, $16)`,
			source.ID, source.OrganizationID, source.ConnectionID, source.AppID, source.ComposioAccountID, source.Provider,
			target, string(source.Scope), nullable(source.TeamID), nullable(source.MemberID), source.CreatedBy,
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

func (store *Store) Sources(ctx context.Context, organizationID string, query domain.SourceQuery) ([]domain.Source, error) {
	sql := `SELECT ` + sourceColumns + ` FROM knowledge_sources WHERE organization_id = $1`
	if !query.IncludeRemoved {
		sql += ` AND status <> 'removed'`
	}
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
	if placement := query.Placement; placement != nil {
		sql += ` AND scope = ` + bind(string(placement.Scope))
		switch placement.Scope {
		case domain.ScopeTeam:
			sql += ` AND team_id = ` + bind(placement.TeamID)
		case domain.ScopeMember:
			sql += ` AND member_id = ` + bind(placement.MemberID)
		}
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
//
// claimedDue is when the source was due as the run claimed it. A sync_at
// later than that was asked for while the run went on (see HastenSource), and
// the sooner of it and nextSync stands, so the run's own schedule does not
// erase a request it may have listed too early to satisfy. Nil schedules
// nextSync regardless.
//
// fullSweptAt is set when this pass read the source from the beginning and
// drained: it becomes the new full_sweep_at. Nil leaves the column as it was,
// which is what keeps an ordinary incremental pass from erasing a sweep an
// earlier pass recorded.
func (store *Store) FinishSource(ctx context.Context, sourceID, leaseToken string, now, nextSync time.Time, claimedDue, fullSweptAt *time.Time, event *domain.Event) (bool, error) {
	claimed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE knowledge_sources
			SET last_synced_at = $3,
			    sync_at = CASE WHEN $6::timestamptz IS NOT NULL AND sync_at > $6 THEN LEAST(sync_at, $4) ELSE $4 END,
			    attempts = 0, reason = '',
			    lease_token = NULL, lease_until = NULL, updated_at = $3, version = version + 1,
			    full_sweep_at = COALESCE($5, full_sweep_at)
			WHERE id = $1 AND lease_token = $2`, sourceID, leaseToken, now, nextSync, fullSweptAt, claimedDue)
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

// RemoveSources retires every source an organization, or one of its members,
// no longer has, and retracts what they held in the same transaction. reason
// is recorded on each: a source retired because its member left is one the
// next uploader may take over, unlike one a member removed.
func (store *Store) RemoveSources(ctx context.Context, organizationID, createdBy, reason string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error) {
	sql := `
		UPDATE knowledge_sources
		SET status = 'removed', reason = $3, sync_at = NULL, lease_token = NULL, lease_until = NULL,
		    updated_at = $2, version = version + 1
		WHERE organization_id = $1 AND status <> 'removed'`
	args := []any{organizationID, now, reason}
	if createdBy != "" {
		sql += ` AND created_by = $4`
		args = append(args, createdBy)
	}
	return store.removeSources(ctx, sql+` RETURNING `+sourceColumns, args, now, events)
}

// RemoveSourcesByConnection retires every source reading through one
// connection, and retracts what they held in the same transaction. Unlike
// RemoveSources, which retires by organization or creator, this runs when the
// connection itself is gone for good: apps.connection.removed.v1.
func (store *Store) RemoveSourcesByConnection(ctx context.Context, connectionID, reason string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error) {
	return store.removeSources(ctx, `
		UPDATE knowledge_sources
		SET status = 'removed', reason = $3, sync_at = NULL, lease_token = NULL, lease_until = NULL,
		    updated_at = $2, version = version + 1
		WHERE connection_id = $1 AND status <> 'removed'
		RETURNING `+sourceColumns, []any{connectionID, now, reason}, now, events)
}

// RemoveSourcesByTeam retires every source a deleted team held, and retracts
// what they held in the same transaction: teams.team.deleted.v1. Left
// running, a deleted team's sources would go on publishing its documents,
// and rag — which removes the team's corpora when the team goes — would build
// it a new one from them.
func (store *Store) RemoveSourcesByTeam(ctx context.Context, organizationID, teamID, reason string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error) {
	return store.removeSources(ctx, `
		UPDATE knowledge_sources
		SET status = 'removed', reason = $4, sync_at = NULL, lease_token = NULL, lease_until = NULL,
		    updated_at = $2, version = version + 1
		WHERE organization_id = $1 AND scope = 'team' AND team_id = $3 AND status <> 'removed'
		RETURNING `+sourceColumns, []any{organizationID, now, teamID, reason}, now, events)
}

// RetireSource retires a source the run holding its lease found can no longer
// be read, records reason on it, and retracts what it held in the same
// transaction. It answers false, and changes nothing, once the lease has
// moved on: like every write that ends a run, it is the lease holder's alone.
func (store *Store) RetireSource(ctx context.Context, sourceID, leaseToken, reason string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) (bool, error) {
	removed, err := store.removeSources(ctx, `
		UPDATE knowledge_sources
		SET status = 'removed', reason = $3, sync_at = NULL, lease_token = NULL, lease_until = NULL,
		    updated_at = $4, version = version + 1
		WHERE id = $1 AND lease_token = $2 AND status <> 'removed'
		RETURNING `+sourceColumns, []any{sourceID, leaseToken, reason, now}, now, events)
	return len(removed) == 1, err
}

// removeSources runs a statement that retires sources and returns them, then
// retracts each one's documents and records its events, all in one
// transaction, so a source is never retired with its documents still live.
func (store *Store) removeSources(ctx context.Context, statement string, args []any, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error) {
	var removed []domain.Source
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		sources, err := querySources(ctx, tx, statement, args...)
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

// HastenSource brings forward the next sweep of one source, because someone
// asked for it. Unlike the trigger-driven sweep it does not check when the
// source was last read: a person asking has a reason the schedule does not
// know — a permission they just fixed, something they just changed at the
// provider — and refusing them because a pass ran a minute ago would make
// the button a lie. It answers false for a source that is not active, or one
// already due sooner.
//
// A source whose pass is under way may already have read past what the
// asking is about, such as a file uploaded after the pass listed its
// placement. Its sync_at, still the past time it was claimed at, is moved to
// at, so FinishSource sees it was asked for again and runs it once more
// rather than an interval later.
func (store *Store) HastenSource(ctx context.Context, organizationID, sourceID string, at time.Time) (bool, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE knowledge_sources
		SET sync_at = $3, updated_at = $3, version = version + 1
		WHERE organization_id = $1 AND id = $2 AND status = 'active' AND sync_at IS NOT NULL
		  AND (sync_at > $3 OR (lease_until > $3 AND sync_at < $3))`, organizationID, sourceID, at)
	if err != nil {
		return false, fmt.Errorf("hasten a source: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// HastenSources brings forward the next sweep of every healthy source on a
// connection, because the app said something in it changed.
//
// It never delays one already due sooner. A source swept within floor is
// brought forward to floor after that sweep rather than to now: a busy
// channel delivers faster than any provider should be read, but what it
// reported should not then wait a whole interval. And a source that is
// failing keeps its backoff — last_synced_at is its last success, however
// long ago, so the floor alone would let every delivery hammer a provider
// that is refusing it. Answers how many moved.
func (store *Store) HastenSources(ctx context.Context, connectionID string, at time.Time, floor time.Duration) (int64, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE knowledge_sources
		SET sync_at = GREATEST($2, COALESCE(last_synced_at + make_interval(secs => $3), $2)),
		    updated_at = $2, version = version + 1
		WHERE connection_id = $1 AND status = 'active' AND attempts = 0
		  AND sync_at IS NOT NULL
		  AND sync_at > GREATEST($2, COALESCE(last_synced_at + make_interval(secs => $3), $2))`,
		connectionID, at, floor.Seconds())
	if err != nil {
		return 0, fmt.Errorf("hasten sources: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PauseSources stops every live source reading through one connection, and
// writes one event for each. Running it again pauses nothing and writes
// nothing, so a redelivered event is harmless.
func (store *Store) PauseSources(ctx context.Context, connectionID, reason string, now time.Time, event func(domain.Source) (domain.Event, error)) ([]domain.Source, error) {
	var paused []domain.Source
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		sources, err := querySources(ctx, tx, `
			UPDATE knowledge_sources
			SET status = 'paused', reason = $2, sync_at = NULL,
			    lease_token = NULL, lease_until = NULL, updated_at = $3, version = version + 1
			WHERE connection_id = $1 AND status = 'active'
			RETURNING `+sourceColumns, connectionID, reason, now)
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

// RemoveSource retires one source a member asked to stop indexing, retracting
// its documents with it, and records reason on it.
func (store *Store) RemoveSource(ctx context.Context, organizationID, sourceID, reason string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		sources, err := querySources(ctx, tx, `
			UPDATE knowledge_sources
			SET status = 'removed', reason = $4, sync_at = NULL, lease_token = NULL, lease_until = NULL,
			    updated_at = $3, version = version + 1
			WHERE organization_id = $1 AND id = $2 AND status <> 'removed'
			RETURNING `+sourceColumns, organizationID, sourceID, now, reason)
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
	if err := rows.Scan(&source.ID, &source.OrganizationID, &source.ConnectionID, &source.AppID, &source.ComposioAccountID,
		&source.Provider, &target, &scope, &teamID, &memberID, &source.CreatedBy, &status, &source.Reason, &intervalSeconds,
		&source.SyncAt, &source.LastSyncedAt, &source.Attempts, &leaseToken, &source.LeaseUntil,
		&source.Version, &source.CreatedAt, &source.UpdatedAt, &source.FullSweepAt, &source.IncludeKinds,
		&source.KindsPending); err != nil {
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

// SetSourceKinds records what kinds of document a source indexes (nil: the
// default) and marks its documents to be decided again, bringing its next
// pass forward to at. Answers whether there was a source to change.
func (store *Store) SetSourceKinds(ctx context.Context, organizationID, sourceID string, include []string, at time.Time) (bool, error) {
	var kinds any
	if include != nil {
		kinds = include
	}
	tag, err := store.pool.Exec(ctx, `
		UPDATE knowledge_sources
		SET include_kinds = $3, kinds_pending = true, sync_at = least(coalesce(sync_at, $4), $4),
		    updated_at = $4, version = version + 1
		WHERE organization_id = $1 AND id = $2 AND status <> 'removed'`, organizationID, sourceID, kinds, at)
	if err != nil {
		return false, mapWriteError("set source kinds", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ClearKindsPending records that a source's documents have been decided
// again for its current choice of kinds, by the run holding its lease.
func (store *Store) ClearKindsPending(ctx context.Context, sourceID, lease string) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE knowledge_sources SET kinds_pending = false
		WHERE id = $1 AND lease_token = $2`, sourceID, lease); err != nil {
		return fmt.Errorf("clear kinds pending: %w", err)
	}
	return nil
}

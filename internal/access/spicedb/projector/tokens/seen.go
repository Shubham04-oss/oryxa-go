package tokens

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seenLockSpace keeps the per-key advisory locks apart from other users of
// hashtextextended locks.
const seenLockSpace = 7263511301

// Seen remembers, per relationship key, when the last event the projector
// applied to it happened, in access.spicedb_seen.
type Seen struct{ pool *pgxpool.Pool }

func NewSeen(pool *pgxpool.Pool) *Seen { return &Seen{pool: pool} }

// Guard runs apply unless an event newer than at already touched one of
// check, then records at against record. Every key is locked for the whole
// call, so two tasks handling events about the same relationship take turns.
// applied is false when the event was older and apply never ran.
func (seen *Seen) Guard(ctx context.Context, check, record []string, at time.Time, apply func(context.Context) error) (applied bool, err error) {
	keys := slices.Concat(check, record)
	slices.Sort(keys) // one order, so two calls can't deadlock
	keys = slices.Compact(keys)
	// One change often names a pair twice (a file's owner and created_by),
	// and an upsert may not touch the same row twice.
	record = slices.Clone(record)
	slices.Sort(record)
	record = slices.Compact(record)
	err = pgx.BeginFunc(ctx, seen.pool, func(tx pgx.Tx) error {
		for _, key := range keys {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, key, int64(seenLockSpace)); err != nil {
				return fmt.Errorf("lock %s: %w", key, err)
			}
		}
		var newest *time.Time
		if err := tx.QueryRow(ctx, `SELECT max(occurred_at) FROM access.spicedb_seen WHERE key = ANY($1)`, check).Scan(&newest); err != nil {
			return fmt.Errorf("read seen: %w", err)
		}
		if newest != nil && newest.After(at) {
			return nil
		}
		if err := apply(ctx); err != nil {
			return err
		}
		applied = true
		_, err := tx.Exec(ctx, `INSERT INTO access.spicedb_seen (key, occurred_at)
			SELECT key, $2 FROM unnest($1::text[]) AS key
			ON CONFLICT (key) DO UPDATE SET occurred_at = GREATEST(access.spicedb_seen.occurred_at, EXCLUDED.occurred_at)`,
			record, at)
		if err != nil {
			return fmt.Errorf("record seen: %w", err)
		}
		return nil
	})
	return applied, err
}

// Purge forgets keys last touched before before. An event older than SQS
// keeps messages can't arrive, so the window only needs to outlast that.
func (seen *Seen) Purge(ctx context.Context, before time.Time) (int64, error) {
	tag, err := seen.pool.Exec(ctx, `DELETE FROM access.spicedb_seen WHERE occurred_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("purge seen: %w", err)
	}
	return tag.RowsAffected(), nil
}

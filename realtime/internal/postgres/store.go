// Package postgres keeps realtime's transactional outbox, and the ids of the
// Worker's events realtime has accepted, so that an event the Worker sends
// again is published once.
package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	realtimev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/realtime/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock keeps replicas that start together from installing
// realtime's tables at once. The other services take theirs from 7263510400
// to batching's 7263510901.
const migrationsAdvisoryLock int64 = 7263511000

// OutboxTable is realtime's outbox, for internal/outbox to publish from.
const OutboxTable = "realtime_outbox"

// AcceptedRetention is how long an id of the Worker's is remembered once
// realtime has accepted its event. The Worker gives up on an event seven days
// after it recorded it, which was before realtime accepted it, so no retry of
// an accepted event can arrive once its id is forgotten.
const AcceptedRetention = 7 * 24 * time.Hour

// Store is realtime's PostgreSQL.
type Store struct{ database *postgreskit.Database }

// New returns the store over database.
func New(database *postgreskit.Database) *Store { return &Store{database: database} }

// Pool is the pool the store uses, for the outbox's dispatcher.
func (store *Store) Pool() *pgxpool.Pool { return store.database.Pool }

// Migrate installs realtime's tables. Running it again is harmless.
func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "realtime", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Received is one event the Worker sent, checked, and written as realtime
// publishes it.
type Received struct {
	// WorkerID is the id the Worker gave the event when it recorded it, and
	// keeps across its retries.
	WorkerID   string
	DetailType string
	Data       json.RawMessage
	// OccurredAt is when the message was posted, or the conversation read.
	OccurredAt time.Time
}

// accept records one event in the outbox unless its Worker id was accepted
// already. The published event's id is derived from the Worker's, so the
// outbox, too, takes it once for as long as it keeps it.
const accept = `
	WITH accepted AS (
		INSERT INTO realtime_accepted_events (id) VALUES ($1)
		ON CONFLICT (id) DO NOTHING
		RETURNING id
	)
	INSERT INTO realtime_outbox (id, detail_type, detail, occurred_at, trace_context)
	SELECT $2::text, $3::text, $4::jsonb, $5::timestamptz, NULLIF($6::text, '') FROM accepted
	ON CONFLICT (id) DO NOTHING`

// Accept records events in the outbox, all of them or none, in one
// transaction. An event whose Worker id was accepted before, by an earlier
// call within AcceptedRetention or earlier in this one, is not recorded
// again. It reports how many events it recorded.
//
// Two calls carrying one id at once record it once: the second waits on the
// first's id until the first commits, and then finds it taken, or, if the
// first rolled back, takes it itself.
//
// Every call takes its ids in their own sorted order, whatever order the
// Worker sent them in. Two calls carrying the same ids then only ever wait on
// one another in turn. Taken in the Worker's order, a call holding b while it
// waited for a, beside one holding a while it waited for b, would deadlock,
// and PostgreSQL would fail one of them. The sort is stable, so of an id a
// call carries twice, the first is still the one recorded.
func (store *Store) Accept(ctx context.Context, events []Received) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	events = slices.Clone(events)
	slices.SortStableFunc(events, func(a, b Received) int { return strings.Compare(a.WorkerID, b.WorkerID) })
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	batch := &pgx.Batch{}
	for _, event := range events {
		if event.WorkerID == "" || event.DetailType == "" || len(event.Data) == 0 || event.OccurredAt.IsZero() {
			return 0, errors.New("an event needs the Worker's id, a detail type, its data and when it happened")
		}
		batch.Queue(accept, event.WorkerID, WorkerEventID(event.WorkerID), event.DetailType, event.Data,
			event.OccurredAt.UTC(), trace)
	}
	recorded := 0
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		results := tx.SendBatch(ctx, batch)
		for range events {
			tag, err := results.Exec()
			if err != nil {
				_ = results.Close()
				return err
			}
			recorded += int(tag.RowsAffected())
		}
		return results.Close()
	})
	if err != nil {
		return 0, fmt.Errorf("accept the Worker's events: %w", err)
	}
	return recorded, nil
}

// RecordPurgeCompleted records that the Worker did the purge the event
// completed.SourceEventID, auth's or agents', asked for. Its event's id is
// derived from that event's, so recording it again, as a purge delivered
// twice is, records nothing new while the outbox keeps the first. It reports
// whether it recorded one.
func (store *Store) RecordPurgeCompleted(ctx context.Context, completed realtimev1.PurgeCompleted) (bool, error) {
	if completed.SourceEventID == "" || completed.OrganizationID == "" || completed.CompletedAt.IsZero() {
		return false, errors.New("a completed purge names the event that asked for it, the organization and when it was done")
	}
	completed.RemovedAt, completed.CompletedAt = completed.RemovedAt.UTC(), completed.CompletedAt.UTC()
	data, err := json.Marshal(completed)
	if err != nil {
		return false, fmt.Errorf("encode the completed purge: %w", err)
	}
	tag, err := store.database.Pool.Exec(ctx, `
		INSERT INTO realtime_outbox (id, detail_type, detail, occurred_at, trace_context)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT (id) DO NOTHING`,
		PurgeCompletedEventID(completed.SourceEventID), realtimev1.PurgeCompletedDetailType, data,
		completed.CompletedAt, observability.CaptureTraceCarrier(ctx).Encode())
	if err != nil {
		return false, fmt.Errorf("record the completed purge: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ForgetAccepted deletes up to limit of the Worker's ids accepted before
// before, and reports how many it deleted.
func (store *Store) ForgetAccepted(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := store.database.Pool.Exec(ctx, `
		DELETE FROM realtime_accepted_events
		WHERE id IN (SELECT id FROM realtime_accepted_events WHERE accepted_at < $1 LIMIT $2)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("forget accepted ids: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// WorkerEventID is the id realtime publishes the Worker's event workerID
// under.
func WorkerEventID(workerID string) string { return eventID("worker", workerID) }

// PurgeCompletedEventID is the id realtime publishes the completion of the
// purge the event sourceEventID asked for under. Auth and agents each draw
// their events' ids at random, so neither's is ever the other's.
func PurgeCompletedEventID(sourceEventID string) string { return eventID("purge", sourceEventID) }

// eventID derives an event's id from what the event is about, rather than
// drawing it at random as other publishers do, so the same event recorded
// twice is one row in the outbox and one event_id to a consumer that
// deduplicates by it. It looks like any other: evt_ and 22 characters.
// namespace keeps ids made from the Worker's ids apart from those made from
// the ids of the events that asked for purges.
func eventID(namespace, key string) string {
	sum := sha256.Sum256([]byte("oryxa.realtime\x00" + namespace + "\x00" + key))
	return "evt_" + base64.RawURLEncoding.EncodeToString(sum[:16])
}

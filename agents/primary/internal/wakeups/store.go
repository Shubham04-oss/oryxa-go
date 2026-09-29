package wakeups

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock keeps replicas that start together from installing
// the tables at once. The runs table uses 7263510700.
const migrationsAdvisoryLock int64 = 7263510701

// Event is one thing a member's app reported.
type Event struct {
	ID             string
	OrganizationID string
	MemberID       string
	Toolkit        string
	Trigger        string
	// Data is the app's own payload. DataOmitted says it was too large to
	// travel with the event.
	Data        json.RawMessage
	DataOmitted bool
	ReceivedAt  time.Time
}

// Batch is the events one wake-up hands to Krude: one member's, in one
// organization, oldest first.
type Batch struct {
	ID             string
	OrganizationID string
	MemberID       string
	Events         []Event
}

// Rule is when a member's waiting events make a batch: once Size of them
// wait, or the oldest has waited MaxWait, whichever comes first, so a lone
// event is never held for ever. Largest bounds one batch; the rest wait for
// the next.
type Rule struct {
	Size    int
	MaxWait time.Duration
	Largest int
}

// Store keeps the events waiting to wake Krude.
type Store struct {
	database *postgreskit.Database
	policy   interface {
		Get(context.Context, string) (policy.Settings, error)
	}
}

// NewStore returns the store over database. Its tables are named with their
// schema, so the pool's search_path does not matter.
func NewStore(database *postgreskit.Database) *Store {
	return &Store{database: database}
}

func NewStoreWithPolicy(database *postgreskit.Database, provider interface {
	Get(context.Context, string) (policy.Settings, error)
}) *Store {
	return &Store{database: database, policy: provider}
}

// Migrate installs the tables. Running it twice is harmless.
func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agents-wakeups", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// appUpdatesOn holds, in a statement whose $2 and $3 are an event's
// organization and member, unless that member turned updates from their apps
// off: what arrives while they are off is not kept to tell them later.
const appUpdatesOn = `COALESCE((
	SELECT preference.app_updates FROM agents_primary.wakeup_preferences preference
	WHERE preference.organization_id = $2 AND preference.member_id = $3), $9)`

func (store *Store) defaultAppUpdates(ctx context.Context, organizationID string) (bool, error) {
	if store.policy == nil {
		return true, nil
	}
	settings, err := store.policy.Get(ctx, organizationID)
	if err != nil {
		return false, fmt.Errorf("read organization app update policy: %w", err)
	}
	return settings.Proactive != "off", nil
}

// Record keeps an event until it is handed to Krude. It reports false for an
// event it already has, which the bus may deliver more than once, and for one
// whose member turned updates from their apps off.
func (store *Store) Record(ctx context.Context, event Event) (bool, error) {
	if event.ID == "" || event.OrganizationID == "" || event.MemberID == "" {
		return false, errors.New("a wake-up event needs its id, organization and member")
	}
	var data any
	if len(event.Data) > 0 {
		data = event.Data
	}
	defaultOn, err := store.defaultAppUpdates(ctx, event.OrganizationID)
	if err != nil {
		return false, err
	}
	tag, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.wakeup_events
			(id, organization_id, member_id, toolkit, trigger, data, data_omitted, received_at)
		SELECT $1, $2, $3, $4, $5, $6::jsonb, $7, $8
		WHERE `+appUpdatesOn+`
		ON CONFLICT (id) DO NOTHING`,
		event.ID, event.OrganizationID, event.MemberID, event.Toolkit, event.Trigger, data, event.DataOmitted, event.ReceivedAt, defaultOn)
	if err != nil {
		return false, fmt.Errorf("record wake-up event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RecordBatch keeps a batch's events, all of them or none, in one
// transaction: a wake-up claiming between two of them would otherwise hand
// Krude part of the batch now and the rest later. It answers how many were
// new; the bus may deliver a batch, or a trigger in it, more than once, and a
// member who turned updates from their apps off has none kept.
func (store *Store) RecordBatch(ctx context.Context, events []Event) (int, error) {
	defaults := map[string]bool{}
	for _, event := range events {
		if event.ID == "" || event.OrganizationID == "" || event.MemberID == "" {
			return 0, errors.New("a wake-up event needs its id, organization and member")
		}
		if _, known := defaults[event.OrganizationID]; !known {
			on, err := store.defaultAppUpdates(ctx, event.OrganizationID)
			if err != nil {
				return 0, err
			}
			defaults[event.OrganizationID] = on
		}
	}
	recorded := 0
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		for _, event := range events {
			var data any
			if len(event.Data) > 0 {
				data = event.Data
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO agents_primary.wakeup_events
					(id, organization_id, member_id, toolkit, trigger, data, data_omitted, received_at)
				SELECT $1, $2, $3, $4, $5, $6::jsonb, $7, $8
				WHERE `+appUpdatesOn+`
				ON CONFLICT (id) DO NOTHING`,
				event.ID, event.OrganizationID, event.MemberID, event.Toolkit, event.Trigger, data, event.DataOmitted, event.ReceivedAt, defaults[event.OrganizationID])
			if err != nil {
				return fmt.Errorf("record wake-up event: %w", err)
			}
			recorded += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return recorded, nil
}

// Claim takes the batches that are due, at most limit members' worth, and
// holds them for lease: long enough for a run to hand them to Krude. A batch
// whose lease runs out, because its replica died, is due again.
func (store *Store) Claim(ctx context.Context, rule Rule, lease time.Duration, limit int) ([]Batch, error) {
	if rule.Size < 1 || rule.MaxWait <= 0 || rule.Largest < 1 || lease <= 0 || limit < 1 {
		return nil, errors.New("claiming wake-ups needs a positive size, wait, largest batch, lease and limit")
	}
	rows, err := store.database.Pool.Query(ctx, `
		WITH due AS (
			SELECT organization_id, member_id,
				'wb_' || md5(organization_id || ':' || member_id || ':' || clock_timestamp()::text || random()::text) AS batch_id
			FROM agents_primary.wakeup_events
			WHERE handled_at IS NULL AND (batch_id IS NULL OR claimed_until < now())
			GROUP BY organization_id, member_id
			HAVING count(*) >= $1 OR min(recorded_at) <= now() - make_interval(secs => $2)
			ORDER BY min(recorded_at)
			LIMIT $3
		),
		picked AS (
			SELECT id, batch_id FROM (
				SELECT waiting.id, due.batch_id,
					row_number() OVER (PARTITION BY waiting.organization_id, waiting.member_id
						ORDER BY waiting.recorded_at, waiting.id) AS position
				FROM agents_primary.wakeup_events waiting
				JOIN due ON due.organization_id = waiting.organization_id AND due.member_id = waiting.member_id
				WHERE waiting.handled_at IS NULL AND (waiting.batch_id IS NULL OR waiting.claimed_until < now())
			) ranked
			WHERE position <= $4
		)
		UPDATE agents_primary.wakeup_events claimed
		SET batch_id = picked.batch_id, claimed_until = now() + make_interval(secs => $5)
		FROM picked
		WHERE claimed.id = picked.id AND claimed.handled_at IS NULL
			AND (claimed.batch_id IS NULL OR claimed.claimed_until < now())
		RETURNING claimed.batch_id, claimed.id, claimed.organization_id, claimed.member_id, claimed.toolkit,
			claimed.trigger, claimed.data, claimed.data_omitted, claimed.received_at`,
		rule.Size, rule.MaxWait.Seconds(), limit, rule.Largest, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim wake-ups: %w", err)
	}
	type claimedEvent struct {
		batchID string
		event   Event
	}
	claimed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (claimedEvent, error) {
		var found claimedEvent
		var data []byte
		err := row.Scan(&found.batchID, &found.event.ID, &found.event.OrganizationID, &found.event.MemberID,
			&found.event.Toolkit, &found.event.Trigger, &data, &found.event.DataOmitted, &found.event.ReceivedAt)
		found.event.Data = data
		return found, err
	})
	if err != nil {
		return nil, fmt.Errorf("read claimed wake-ups: %w", err)
	}

	batches := map[string]*Batch{}
	var order []string
	for _, found := range claimed {
		batch, ok := batches[found.batchID]
		if !ok {
			batch = &Batch{ID: found.batchID, OrganizationID: found.event.OrganizationID, MemberID: found.event.MemberID}
			batches[found.batchID] = batch
			order = append(order, found.batchID)
		}
		batch.Events = append(batch.Events, found.event)
	}
	result := make([]Batch, 0, len(order))
	for _, id := range order {
		batch := batches[id]
		// RETURNING keeps no order; a batch reads oldest first.
		sortEvents(batch.Events)
		result = append(result, *batch)
	}
	return result, nil
}

// Done marks a batch handed to Krude. Its events keep their ids, so a late
// repeat of one is recognised, and lose their data: it was the app's, and
// has served its purpose.
func (store *Store) Done(ctx context.Context, batchID string) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.wakeup_events
		SET handled_at = now(), data = NULL, claimed_until = NULL
		WHERE batch_id = $1 AND handled_at IS NULL`, batchID)
	if err != nil {
		return fmt.Errorf("finish wake-up batch: %w", err)
	}
	return nil
}

// Release gives a batch's events back, to be claimed again once retryAfter
// has passed, so a member whose thread is busy does not hold up everyone
// else's. A failed wake-up counts against each event; one that has failed
// maxAttempts times is dropped rather than retried for ever, and says so.
func (store *Store) Release(ctx context.Context, batchID string, failed bool, maxAttempts int, retryAfter time.Duration) (dropped int, err error) {
	increment := 0
	if failed {
		increment = 1
	}
	err = pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE agents_primary.wakeup_events
			SET handled_at = now(), data = NULL, dropped = true, claimed_until = NULL, attempts = attempts + $2
			WHERE batch_id = $1 AND handled_at IS NULL AND attempts + $2 >= $3`, batchID, increment, maxAttempts)
		if err != nil {
			return fmt.Errorf("drop failing wake-up events: %w", err)
		}
		dropped = int(tag.RowsAffected())
		if _, err := tx.Exec(ctx, `
			UPDATE agents_primary.wakeup_events
			SET claimed_until = now() + make_interval(secs => $3), attempts = attempts + $2
			WHERE batch_id = $1 AND handled_at IS NULL`, batchID, increment, retryAfter.Seconds()); err != nil {
			return fmt.Errorf("release wake-up batch: %w", err)
		}
		return nil
	})
	return dropped, err
}

// Purge forgets events handled longer ago than age.
func (store *Store) Purge(ctx context.Context, age time.Duration) error {
	_, err := store.database.Pool.Exec(ctx, `
		DELETE FROM agents_primary.wakeup_events
		WHERE handled_at IS NOT NULL AND handled_at < now() - make_interval(secs => $1)`, age.Seconds())
	if err != nil {
		return fmt.Errorf("purge handled wake-ups: %w", err)
	}
	return nil
}

// Thread is the thread a member's wake-ups go to, or "" when there is none
// yet.
func (store *Store) Thread(ctx context.Context, organizationID, memberID string) (string, error) {
	var threadID string
	err := store.database.Pool.QueryRow(ctx, `
		SELECT thread_id FROM agents_primary.wakeup_threads
		WHERE organization_id = $1 AND member_id = $2`, organizationID, memberID).Scan(&threadID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read wake-up thread: %w", err)
	}
	return threadID, nil
}

// SetThread records the thread a member's wake-ups go to from now on.
func (store *Store) SetThread(ctx context.Context, organizationID, memberID, threadID string) error {
	_, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.wakeup_threads (organization_id, member_id, thread_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id, member_id) DO UPDATE SET thread_id = EXCLUDED.thread_id`,
		organizationID, memberID, threadID)
	if err != nil {
		return fmt.Errorf("record wake-up thread: %w", err)
	}
	return nil
}

// Forget removes what is kept for waking Krude in organizationID — only
// memberID's, when one is named — for a member who left or an organization
// that is gone.
func (store *Store) Forget(ctx context.Context, organizationID, memberID string) error {
	if organizationID == "" {
		return errors.New("forgetting wake-ups needs an organization")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			DELETE FROM agents_primary.wakeup_events
			WHERE organization_id = $1 AND ($2 = '' OR member_id = $2)`, organizationID, memberID); err != nil {
			return fmt.Errorf("forget wake-up events: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM agents_primary.wakeup_threads
			WHERE organization_id = $1 AND ($2 = '' OR member_id = $2)`, organizationID, memberID); err != nil {
			return fmt.Errorf("forget wake-up threads: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM agents_primary.wakeup_preferences
			WHERE organization_id = $1 AND ($2 = '' OR member_id = $2)`, organizationID, memberID); err != nil {
			return fmt.Errorf("forget wake-up preferences: %w", err)
		}
		return nil
	})
}

// Drop marks a batch's events handled without waking anyone, for events
// that must not be told to anybody, such as a departed member's.
func (store *Store) Drop(ctx context.Context, batchID string) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.wakeup_events
		SET handled_at = now(), data = NULL, dropped = true, claimed_until = NULL
		WHERE batch_id = $1 AND handled_at IS NULL`, batchID)
	if err != nil {
		return fmt.Errorf("drop wake-up batch: %w", err)
	}
	return nil
}

// Unclaim gives events back to wait for the next batch at once, such as
// those a wake-up had no room to tell.
func (store *Store) Unclaim(ctx context.Context, eventIDs []string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.wakeup_events
		SET batch_id = NULL, claimed_until = NULL
		WHERE id = ANY($1) AND handled_at IS NULL`, eventIDs)
	if err != nil {
		return fmt.Errorf("unclaim wake-up events: %w", err)
	}
	return nil
}

// DropStale drops events that have waited longer than age, such as behind a
// confirmation the member never answered, so a thread that stays busy does
// not hold its member's events for ever. It reports how many it dropped.
func (store *Store) DropStale(ctx context.Context, age time.Duration) (int, error) {
	tag, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.wakeup_events
		SET handled_at = now(), data = NULL, dropped = true, batch_id = NULL, claimed_until = NULL
		WHERE handled_at IS NULL AND recorded_at < now() - make_interval(secs => $1)
			AND (batch_id IS NULL OR claimed_until < now())`, age.Seconds())
	if err != nil {
		return 0, fmt.Errorf("drop stale wake-ups: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ReplaceThread records next as the thread a member's wake-ups go to, if the
// one recorded is still expected ("" for none), and answers whichever thread
// won: two wake-ups making a thread at once must settle on one.
func (store *Store) ReplaceThread(ctx context.Context, organizationID, memberID, expected, next string) (string, error) {
	var winner string
	var err error
	if expected == "" {
		err = store.database.Pool.QueryRow(ctx, `
			INSERT INTO agents_primary.wakeup_threads (organization_id, member_id, thread_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (organization_id, member_id) DO NOTHING
			RETURNING thread_id`, organizationID, memberID, next).Scan(&winner)
	} else {
		err = store.database.Pool.QueryRow(ctx, `
			UPDATE agents_primary.wakeup_threads SET thread_id = $4
			WHERE organization_id = $1 AND member_id = $2 AND thread_id = $3
			RETURNING thread_id`, organizationID, memberID, expected, next).Scan(&winner)
	}
	if err == nil {
		return winner, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("record wake-up thread: %w", err)
	}
	// Another wake-up recorded its own first; that one stands.
	current, err := store.Thread(ctx, organizationID, memberID)
	if err != nil {
		return "", err
	}
	if current == "" {
		return "", errors.New("record wake-up thread: it changed while being recorded; try again")
	}
	return current, nil
}

// AppUpdates uses the member's explicit choice when present and otherwise
// the organization's applied workspace default.
func (store *Store) AppUpdates(ctx context.Context, organizationID, memberID string) (bool, error) {
	var on bool
	err := store.database.Pool.QueryRow(ctx, `
		SELECT app_updates FROM agents_primary.wakeup_preferences
		WHERE organization_id = $1 AND member_id = $2`, organizationID, memberID).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.defaultAppUpdates(ctx, organizationID)
	}
	if err != nil {
		return false, fmt.Errorf("read app updates preference: %w", err)
	}
	return on, nil
}

// SetAppUpdates records whether a member wants Krude to tell them what their
// apps report. Turning it off drops what is already waiting to be told, so
// nothing from before wakes Krude later; a wake-up already running finishes.
func (store *Store) SetAppUpdates(ctx context.Context, organizationID, memberID string, on bool) error {
	if organizationID == "" || memberID == "" {
		return errors.New("a preference needs its organization and member")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO agents_primary.wakeup_preferences (organization_id, member_id, app_updates, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (organization_id, member_id) DO UPDATE SET
				app_updates = excluded.app_updates, updated_at = excluded.updated_at`,
			organizationID, memberID, on); err != nil {
			return fmt.Errorf("record app updates preference: %w", err)
		}
		if on {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE agents_primary.wakeup_events
			SET handled_at = now(), data = NULL, dropped = true, batch_id = NULL, claimed_until = NULL
			WHERE organization_id = $1 AND member_id = $2 AND handled_at IS NULL
				AND (batch_id IS NULL OR claimed_until < now())`, organizationID, memberID); err != nil {
			return fmt.Errorf("drop waiting app updates: %w", err)
		}
		return nil
	})
}

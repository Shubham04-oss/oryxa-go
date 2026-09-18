// Package postgres persists batching's inbox and transactional outbox.
package postgres

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510901

var ErrClaimLost = errors.New("batch claim is no longer owned")

type Store struct {
	database  *postgreskit.Database
	scheduler domain.AdaptiveScheduler
	now       func() time.Time
}

func New(database *postgreskit.Database, schedulers ...domain.AdaptiveScheduler) *Store {
	store := &Store{database: database, now: time.Now}
	if len(schedulers) > 0 {
		store.scheduler = schedulers[0]
	}
	return store
}

func (store *Store) Pool() *pgxpool.Pool { return store.database.Pool }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "batching", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Record keeps a classified trigger exactly once. EventBridge and SQS are
// both at-least-once, so the original Oryxa event id is the primary key. A
// trigger within a removal made after apps received it is refused, and
// nothing of it is kept: not the trigger, and not a traffic profile.
func (store *Store) Record(ctx context.Context, trigger domain.Trigger) (domain.Admission, error) {
	if trigger.ID == "" || trigger.OrganizationID == "" || trigger.MemberID == "" || trigger.ReceivedAt.IsZero() {
		return 0, errors.New("a trigger needs its event id, organization, member, and received time")
	}
	if err := trigger.Classification.Validate(); err != nil {
		return 0, err
	}
	if trigger.Classification.Lane == domain.LaneDigest && !store.scheduler.Valid() {
		return 0, errors.New("record digest trigger: adaptive scheduler is not configured")
	}
	var admission domain.Admission
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		var err error
		admission, err = store.record(ctx, tx, trigger)
		return err
	})
	if err != nil {
		return 0, err
	}
	return admission, nil
}

// Screen says what Record would do with a trigger, without recording it.
// The service asks before classifying, so neither a redelivery nor a trigger
// from something since removed costs a classification, or sends its payload
// to the classifier's provider again. Record still decides, under the
// organization's lock.
func (store *Store) Screen(ctx context.Context, trigger domain.Trigger) (domain.Admission, error) {
	if trigger.ID == "" || trigger.OrganizationID == "" || trigger.MemberID == "" || trigger.ReceivedAt.IsZero() {
		return 0, errors.New("a trigger needs its event id, organization, member, and received time")
	}
	return screen(ctx, store.database.Pool, trigger)
}

// record does Record's work in tx. It holds the organization's lock shared,
// so triggers are recorded side by side while Forget, which holds it
// exclusively, waits for those under way and they for it: a trigger is
// either committed before a removal, which then deletes it, or screened
// against the removal's tombstone after it.
func (store *Store) record(ctx context.Context, tx pgx.Tx, trigger domain.Trigger) (domain.Admission, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, organizationLock(trigger.OrganizationID)); err != nil {
		return 0, fmt.Errorf("lock organization: %w", err)
	}
	admission, err := screen(ctx, tx, trigger)
	if err != nil || admission != domain.Admitted {
		return admission, err
	}
	if trigger.Classification.Lane == domain.LaneImmediate {
		return store.recordImmediate(ctx, tx, trigger)
	}
	return store.recordDigest(ctx, tx, trigger)
}

// organizationLock is the advisory lock key that serialises recording an
// organization's triggers against forgetting them. A key is a number, so the
// organization's id is hashed; two organizations that share one only wait
// for each other.
func organizationLock(organizationID string) int64 {
	key := fnv.New64a()
	_, _ = key.Write([]byte("batching/organization/" + organizationID))
	return int64(key.Sum64())
}

// querier is the pool, or a transaction on it.
type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// screen says what Record would do with a trigger, from what is committed:
// nothing when its event is recorded already or it falls within a removal
// made after apps received it. A tombstone covers a trigger when every field
// it sets names the trigger.
func screen(ctx context.Context, database querier, trigger domain.Trigger) (domain.Admission, error) {
	var recorded, forgotten bool
	if err := database.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM batching_triggers WHERE id = $1),
			EXISTS (
				SELECT 1 FROM batching_forgotten
				WHERE organization_id = $2 AND member_id IN ('', $3)
				  AND connection_id IN ('', $4) AND trigger_id IN ('', $5)
				  AND forgotten_at >= $6)`,
		trigger.ID, trigger.OrganizationID, trigger.MemberID, trigger.ConnectionID, trigger.TriggerID,
		trigger.ReceivedAt).Scan(&recorded, &forgotten); err != nil {
		return 0, fmt.Errorf("screen trigger: %w", err)
	}
	switch {
	case recorded:
		return domain.AlreadyRecorded, nil
	case forgotten:
		return domain.Forgotten, nil
	default:
		return domain.Admitted, nil
	}
}

func (store *Store) recordImmediate(ctx context.Context, tx pgx.Tx, trigger domain.Trigger) (domain.Admission, error) {
	// Due at once: its deadlines are its receipt, counted as a digest's are.
	due := domain.CountFrom(trigger.ReceivedAt, store.now())
	var data any
	if len(trigger.Data) > 0 {
		data = trigger.Data
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO batching_triggers
			(id, trigger_id, connection_id, organization_id, member_id, toolkit, trigger,
			 data, data_omitted, received_at, lane, priority, classifier, decision_reason,
			 target_size, pressure, soft_due_at, hard_due_at, policy_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			1, $16, $15, $15, 'immediate.v1')
		ON CONFLICT (id) DO NOTHING`,
		trigger.ID, trigger.TriggerID, trigger.ConnectionID, trigger.OrganizationID, trigger.MemberID,
		trigger.Toolkit, trigger.Trigger, data, trigger.DataOmitted, trigger.ReceivedAt,
		trigger.Classification.Lane, trigger.Classification.Priority,
		trigger.Classification.Classifier, trigger.Classification.Reason, due,
		1+2*trigger.Classification.Priority)
	if err != nil {
		return 0, fmt.Errorf("record immediate trigger: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.AlreadyRecorded, nil
	}
	return domain.Admitted, nil
}

func (store *Store) recordDigest(ctx context.Context, tx pgx.Tx, trigger domain.Trigger) (domain.Admission, error) {
	at := store.now().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO batching_traffic_profiles
			(organization_id, member_id, fast_mass, slow_mass, observed_at, last_event_at, total_events)
		VALUES ($1, $2, 0, 0, $3, $3, 0)
		ON CONFLICT (organization_id, member_id) DO NOTHING`,
		trigger.OrganizationID, trigger.MemberID, at); err != nil {
		return 0, fmt.Errorf("ensure traffic profile: %w", err)
	}
	var profile domain.TrafficProfile
	if err := tx.QueryRow(ctx, `
		SELECT fast_mass, slow_mass, observed_at, last_event_at, total_events
		FROM batching_traffic_profiles
		WHERE organization_id = $1 AND member_id = $2
		FOR UPDATE`, trigger.OrganizationID, trigger.MemberID).Scan(
		&profile.FastMass, &profile.SlowMass, &profile.ObservedAt, &profile.LastEventAt, &profile.TotalEvents); err != nil {
		return 0, fmt.Errorf("lock traffic profile: %w", err)
	}
	// Screened again under the profile's lock: the same event delivered
	// twice at once waits here for the first, and sees it.
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM batching_triggers WHERE id = $1)`, trigger.ID).Scan(&exists); err != nil {
		return 0, fmt.Errorf("check trigger delivery: %w", err)
	}
	if exists {
		return domain.AlreadyRecorded, nil
	}
	profile, schedule, err := store.scheduler.Observe(profile, trigger.ReceivedAt, at,
		trigger.OrganizationID+"/"+trigger.MemberID, trigger.Classification.Priority)
	if err != nil {
		return 0, fmt.Errorf("schedule digest trigger: %w", err)
	}
	var data any
	if len(trigger.Data) > 0 {
		data = trigger.Data
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO batching_triggers
			(id, trigger_id, connection_id, organization_id, member_id, toolkit, trigger,
			 data, data_omitted, received_at, lane, priority, classifier, decision_reason,
			 target_size, pressure, soft_due_at, hard_due_at, policy_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19)`,
		trigger.ID, trigger.TriggerID, trigger.ConnectionID, trigger.OrganizationID, trigger.MemberID,
		trigger.Toolkit, trigger.Trigger, data, trigger.DataOmitted, trigger.ReceivedAt,
		trigger.Classification.Lane, trigger.Classification.Priority,
		trigger.Classification.Classifier, trigger.Classification.Reason,
		schedule.TargetSize, schedule.Pressure, schedule.SoftDueAt, schedule.HardDueAt, schedule.PolicyVersion); err != nil {
		return 0, fmt.Errorf("record digest trigger: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE batching_traffic_profiles
		SET fast_mass = $3, slow_mass = $4, observed_at = $5, last_event_at = $6,
			total_events = $7, updated_at = now()
		WHERE organization_id = $1 AND member_id = $2`,
		trigger.OrganizationID, trigger.MemberID, profile.FastMass, profile.SlowMass,
		profile.ObservedAt, profile.LastEventAt, profile.TotalEvents); err != nil {
		return 0, fmt.Errorf("update traffic profile: %w", err)
	}
	return domain.Admitted, nil
}

// ClaimDue claims immediate triggers one by one, followed by due digest
// groups. Different replicas claim different rows through SKIP LOCKED and a
// lease makes a crashed claim recoverable.
func (store *Store) ClaimDue(ctx context.Context, rule domain.Rule, lease time.Duration, limit int) ([]domain.Batch, error) {
	if rule.Size < 1 || rule.MaxWait <= 0 || rule.Largest < rule.Size || lease <= 0 || limit < 1 {
		return nil, errors.New("claiming batches needs a valid rule, lease, and limit")
	}
	// Reserve one slot for a due digest when the sweep has room. A sustained
	// stream of immediate work must not keep ordinary app activity waiting
	// forever. If no digest is due, the slot is backfilled below.
	immediateLimit := limit
	if limit > 1 {
		immediateLimit--
	}
	immediate, err := store.claimImmediate(ctx, lease, immediateLimit)
	if err != nil {
		return nil, err
	}
	remaining := limit - len(immediate)
	digests, err := store.claimDigests(ctx, rule, lease, remaining)
	if err != nil {
		return immediate, err
	}
	result := append(immediate, digests...)
	remaining = limit - len(result)
	if remaining == 0 || immediateLimit == limit {
		return result, nil
	}
	moreImmediate, err := store.claimImmediate(ctx, lease, remaining)
	if err != nil {
		return result, err
	}
	return append(result, moreImmediate...), nil
}

func (store *Store) claimImmediate(ctx context.Context, lease time.Duration, limit int) ([]domain.Batch, error) {
	rows, err := store.database.Pool.Query(ctx, `
		WITH picked AS (
			SELECT id,
				'bat_' || md5(id || ':' || clock_timestamp()::text || ':' || random()::text) AS next_batch_id
			FROM batching_triggers
			WHERE batched_at IS NULL AND lane = 'immediate'
			  AND (batch_id IS NULL OR claimed_until < now())
			ORDER BY priority DESC, recorded_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE batching_triggers claimed
		SET batch_id = picked.next_batch_id,
			claimed_until = now() + ($2 * interval '1 millisecond')
		FROM picked
		WHERE claimed.id = picked.id AND claimed.batched_at IS NULL
		RETURNING claimed.batch_id, claimed.id, claimed.trigger_id, claimed.connection_id,
			claimed.organization_id, claimed.member_id, claimed.toolkit, claimed.trigger,
			claimed.data, claimed.data_omitted, claimed.received_at, claimed.lane,
			claimed.priority, claimed.classifier, claimed.decision_reason,
			claimed.target_size, claimed.pressure, claimed.soft_due_at,
			claimed.hard_due_at, claimed.policy_version`, limit, lease.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim immediate triggers: %w", err)
	}
	return collectBatches(rows, store.now().UTC())
}

func (store *Store) claimDigests(ctx context.Context, rule domain.Rule, lease time.Duration, limit int) ([]domain.Batch, error) {
	rows, err := store.database.Pool.Query(ctx, `
		WITH due AS (
			SELECT organization_id, member_id,
				'bat_' || md5(organization_id || ':' || member_id || ':' || clock_timestamp()::text || ':' || random()::text) AS next_batch_id
			FROM batching_triggers
			WHERE batched_at IS NULL AND lane = 'digest'
			  AND (batch_id IS NULL OR claimed_until < now())
			GROUP BY organization_id, member_id
			HAVING count(*) >= min(target_size) OR sum(pressure) >= min(target_size)
				OR min(soft_due_at) <= now()
			ORDER BY min(soft_due_at), min(recorded_at)
			LIMIT $1
		),
		picked AS (
			SELECT id, next_batch_id FROM (
				SELECT waiting.id, due.next_batch_id,
					row_number() OVER (PARTITION BY waiting.organization_id, waiting.member_id
						ORDER BY waiting.received_at, waiting.id) AS position
				FROM batching_triggers waiting
				JOIN due ON due.organization_id = waiting.organization_id AND due.member_id = waiting.member_id
				WHERE waiting.batched_at IS NULL AND waiting.lane = 'digest'
				  AND (waiting.batch_id IS NULL OR waiting.claimed_until < now())
			) ranked
			WHERE position <= $2
		)
		UPDATE batching_triggers claimed
		SET batch_id = picked.next_batch_id,
			claimed_until = now() + ($3 * interval '1 millisecond')
		FROM picked
		WHERE claimed.id = picked.id AND claimed.batched_at IS NULL
		  AND (claimed.batch_id IS NULL OR claimed.claimed_until < now())
		RETURNING claimed.batch_id, claimed.id, claimed.trigger_id, claimed.connection_id,
			claimed.organization_id, claimed.member_id, claimed.toolkit, claimed.trigger,
			claimed.data, claimed.data_omitted, claimed.received_at, claimed.lane,
			claimed.priority, claimed.classifier, claimed.decision_reason,
			claimed.target_size, claimed.pressure, claimed.soft_due_at,
			claimed.hard_due_at, claimed.policy_version`,
		limit, rule.Largest, lease.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim digest triggers: %w", err)
	}
	return collectBatches(rows, store.now().UTC())
}

func collectBatches(rows pgx.Rows, now time.Time) ([]domain.Batch, error) {
	defer rows.Close()
	type claimed struct {
		batchID string
		trigger domain.Trigger
	}
	claimedRows, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (claimed, error) {
		var found claimed
		var data []byte
		err := row.Scan(&found.batchID, &found.trigger.ID, &found.trigger.TriggerID, &found.trigger.ConnectionID,
			&found.trigger.OrganizationID, &found.trigger.MemberID, &found.trigger.Toolkit, &found.trigger.Trigger,
			&data, &found.trigger.DataOmitted, &found.trigger.ReceivedAt, &found.trigger.Classification.Lane,
			&found.trigger.Classification.Priority, &found.trigger.Classification.Classifier,
			&found.trigger.Classification.Reason, &found.trigger.Schedule.TargetSize,
			&found.trigger.Schedule.Pressure, &found.trigger.Schedule.SoftDueAt,
			&found.trigger.Schedule.HardDueAt, &found.trigger.Schedule.PolicyVersion)
		found.trigger.Data = data
		return found, err
	})
	if err != nil {
		return nil, fmt.Errorf("read claimed triggers: %w", err)
	}

	byID := map[string]*domain.Batch{}
	var order []string
	for _, found := range claimedRows {
		batch, ok := byID[found.batchID]
		if !ok {
			batch = &domain.Batch{
				ID: found.batchID, OrganizationID: found.trigger.OrganizationID,
				MemberID: found.trigger.MemberID, Lane: found.trigger.Classification.Lane,
				TargetSize: found.trigger.Schedule.TargetSize, SoftDueAt: found.trigger.Schedule.SoftDueAt,
				HardDueAt: found.trigger.Schedule.HardDueAt, PolicyVersion: found.trigger.Schedule.PolicyVersion,
			}
			byID[found.batchID] = batch
			order = append(order, found.batchID)
		}
		batch.Triggers = append(batch.Triggers, found.trigger)
		batch.TargetSize = min(batch.TargetSize, found.trigger.Schedule.TargetSize)
		batch.Pressure += found.trigger.Schedule.Pressure
		batch.SoftDueAt = earlier(batch.SoftDueAt, found.trigger.Schedule.SoftDueAt)
		batch.HardDueAt = earlier(batch.HardDueAt, found.trigger.Schedule.HardDueAt)
		if batch.PolicyVersion != found.trigger.Schedule.PolicyVersion {
			batch.PolicyVersion = "mixed"
		}
	}
	result := make([]domain.Batch, 0, len(order))
	for _, id := range order {
		batch := byID[id]
		slices.SortFunc(batch.Triggers, func(a, b domain.Trigger) int {
			return cmp.Or(a.ReceivedAt.Compare(b.ReceivedAt), cmp.Compare(a.ID, b.ID))
		})
		batch.FlushReason = flushReason(*batch, now)
		result = append(result, *batch)
	}
	return result, nil
}

func earlier(first, second time.Time) time.Time {
	if first.IsZero() || second.Before(first) {
		return second
	}
	return first
}

func flushReason(batch domain.Batch, now time.Time) string {
	switch {
	case batch.Lane == domain.LaneImmediate:
		return "immediate"
	case !now.Before(batch.HardDueAt):
		return "hard_deadline"
	case len(batch.Triggers) >= batch.TargetSize:
		return "target_size"
	case batch.Pressure >= float64(batch.TargetSize):
		return "weighted_priority"
	case !now.Before(batch.SoftDueAt):
		return "priority_deadline"
	default:
		return "due"
	}
}

// Unclaim gives part of a claimed batch back immediately when the encoded
// event would exceed the batch payload budget.
func (store *Store) Unclaim(ctx context.Context, batchID string, eventIDs []string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE batching_triggers
		SET batch_id = NULL, claimed_until = NULL, soft_due_at = LEAST(soft_due_at, now())
		WHERE batch_id = $1 AND id = ANY($2) AND batched_at IS NULL`, batchID, eventIDs)
	if err != nil {
		return fmt.Errorf("unclaim batch overflow: %w", err)
	}
	return nil
}

// Complete atomically records the batch-ready event and settles every source
// trigger it carries. A stale batch id cannot settle a newer claim.
func (store *Store) Complete(ctx context.Context, batch domain.Batch, event domain.Event) error {
	expected := make([]string, 0, len(batch.Triggers))
	for _, trigger := range batch.Triggers {
		expected = append(expected, trigger.ID)
	}
	slices.Sort(expected)
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id FROM batching_triggers
			WHERE batch_id = $1 AND batched_at IS NULL
			FOR UPDATE`, batch.ID)
		if err != nil {
			return fmt.Errorf("lock claimed batch: %w", err)
		}
		owned, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read claimed batch: %w", err)
		}
		slices.Sort(owned)
		if !slices.Equal(owned, expected) {
			return ErrClaimLost
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO batching_outbox (id, detail_type, detail, occurred_at)
			VALUES ($1, $2, $3, $4)`, event.ID, event.DetailType, event.Data, event.OccurredAt); err != nil {
			return fmt.Errorf("record batch-ready event: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE batching_triggers
			SET batched_at = $2, claimed_until = NULL, data = NULL
			WHERE batch_id = $1 AND batched_at IS NULL`, batch.ID, event.OccurredAt); err != nil {
			return fmt.Errorf("settle batched triggers: %w", err)
		}
		return nil
	})
}

// Forget removes pending and retained triggers for a member, or an entire
// organization when memberID is empty, and remembers the removal, so a
// trigger apps received before it that the bus delivers afterwards is
// refused rather than starting a new group. A batch already on EventBridge
// cannot be recalled; agents still verify membership before starting a run.
//
// Forget holds the organization's lock exclusively: it waits for every
// trigger being recorded for the organization and deletes those too, and a
// trigger recorded after it is screened against its tombstone.
func (store *Store) Forget(ctx context.Context, organizationID, memberID string) error {
	if organizationID == "" {
		return errors.New("forgetting triggers needs an organization")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, organizationLock(organizationID)); err != nil {
			return fmt.Errorf("lock organization: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM batching_triggers
			WHERE organization_id = $1 AND ($2 = '' OR member_id = $2)`, organizationID, memberID); err != nil {
			return fmt.Errorf("forget triggers: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM batching_traffic_profiles
			WHERE organization_id = $1 AND ($2 = '' OR member_id = $2)`, organizationID, memberID); err != nil {
			return fmt.Errorf("forget traffic profile: %w", err)
		}
		// A batch completed just before the removal still waits in the
		// outbox with every payload in it. Not yet published, it is not yet
		// anyone's, so it goes with the rest; one already published was
		// blanked when it went.
		if _, err := tx.Exec(ctx, `
			DELETE FROM batching_outbox
			WHERE published_at IS NULL
			  AND detail->>'organization_id' = $1 AND ($2 = '' OR detail->>'member_id' = $2)`, organizationID, memberID); err != nil {
			return fmt.Errorf("forget unpublished batches: %w", err)
		}
		return remember(ctx, tx, organizationID, memberID, "", "", store.now().UTC())
	})
}

// Discard drops the triggers still waiting from a connection its member
// removed, or from one trigger when triggerID is set, and remembers the
// removal like Forget does, so one the bus delivers late is refused too.
// Waiting includes claimed and not yet completed: a claim that loses a
// trigger here can no longer complete, and the rest of its group is claimed
// again when its lease runs out. Completed batches are left alone; each is
// one event, carrying the member's other apps' triggers too, and may already
// be on the bus.
func (store *Store) Discard(ctx context.Context, organizationID, connectionID, triggerID string) error {
	if organizationID == "" || (connectionID == "" && triggerID == "") {
		return errors.New("discarding triggers needs an organization and a connection or trigger")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, organizationLock(organizationID)); err != nil {
			return fmt.Errorf("lock organization: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM batching_triggers
			WHERE organization_id = $1 AND batched_at IS NULL
			  AND ($2 = '' OR connection_id = $2) AND ($3 = '' OR trigger_id = $3)`,
			organizationID, connectionID, triggerID); err != nil {
			return fmt.Errorf("discard waiting triggers: %w", err)
		}
		return remember(ctx, tx, organizationID, "", connectionID, triggerID, store.now().UTC())
	})
}

// remember records a removal's tombstone, timed once the organization's lock
// is held: every trigger apps received before then is either deleted with
// the removal or refused by Record. Removing the same thing again moves the
// time forward, never back. A member added back later is not held up, since
// what their apps report then is received after it; one added back before a
// removal is delivered again loses what their apps reported in between,
// which errs towards not waking an agent for someone who may be gone.
func remember(ctx context.Context, tx pgx.Tx, organizationID, memberID, connectionID, triggerID string, at time.Time) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO batching_forgotten (organization_id, member_id, connection_id, trigger_id, forgotten_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (organization_id, member_id, connection_id, trigger_id)
		DO UPDATE SET forgotten_at = GREATEST(batching_forgotten.forgotten_at, EXCLUDED.forgotten_at)`,
		organizationID, memberID, connectionID, triggerID, at); err != nil {
		return fmt.Errorf("remember removal: %w", err)
	}
	return nil
}

// DeleteForgotten deletes the tombstones of removals made before before. By
// then the bus can no longer deliver anything apps received ahead of them.
func (store *Store) DeleteForgotten(ctx context.Context, before time.Time) (int, error) {
	tag, err := store.database.Pool.Exec(ctx, `DELETE FROM batching_forgotten WHERE forgotten_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("delete expired removals: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (store *Store) DeleteBatched(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := store.database.Pool.Exec(ctx, `
		DELETE FROM batching_triggers
		WHERE id IN (
			SELECT id FROM batching_triggers
			WHERE batched_at IS NOT NULL AND batched_at < $1
			ORDER BY batched_at
			LIMIT $2
		)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("delete retained triggers: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

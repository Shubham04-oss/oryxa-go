// Package interactiondelivery turns content-minimal interaction lifecycle
// events into durable jobs that wake the addressed persistent agents.
package interactiondelivery

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510704

const (
	KindTargetRequested    = "target_requested"
	KindTargetApproved     = "target_approved"
	KindRequesterRejected  = "requester_rejected"
	KindRequesterCompleted = "requester_completed"
)

// Delivery is one EventBridge lifecycle event waiting for its recipient.
// It intentionally contains no request, decision, or response content.
type Delivery struct {
	EventID          string
	RequestID        string
	OrganizationID   string
	RecipientUserID  string
	RecipientAgentID string
	Kind             string
	Status           string
	Revision         int64
	ChangedAt        time.Time
	Attempts         int
}

// OwnedThread identifies one persistent agent conversation used to deliver
// an interaction. It contains identity only, not interaction content.
type OwnedThread struct {
	RequestID      string
	OrganizationID string
	UserID         string
	AgentID        string
	ThreadID       string
}

// Store keeps interaction deliveries and their stable agent conversations.
type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agent-interaction-deliveries", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Record persists one delivery once. EventBridge and SQS both deliver at
// least once, so the publisher's event id is the idempotency key.
func (store *Store) Record(ctx context.Context, delivery Delivery) (bool, error) {
	if err := validate(delivery); err != nil {
		return false, err
	}
	tag, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.interaction_deliveries
			(event_id, request_id, organization_id, recipient_user_id, recipient_agent_id,
			 kind, interaction_status, revision, changed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (event_id) DO NOTHING`,
		delivery.EventID, delivery.RequestID, delivery.OrganizationID,
		delivery.RecipientUserID, delivery.RecipientAgentID, delivery.Kind,
		delivery.Status, delivery.Revision, delivery.ChangedAt)
	if err != nil {
		return false, fmt.Errorf("record interaction delivery: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Claim leases due deliveries. A later revision for the same interaction
// and recipient waits until earlier revisions settle, preserving lifecycle
// order even though EventBridge itself does not promise it.
func (store *Store) Claim(ctx context.Context, lease time.Duration, limit int) ([]Delivery, error) {
	if lease <= 0 || limit < 1 {
		return nil, errors.New("claiming interaction deliveries needs a positive lease and limit")
	}
	rows, err := store.database.Pool.Query(ctx, `
		WITH picked AS (
			SELECT candidate.event_id
			FROM agents_primary.interaction_deliveries candidate
			WHERE candidate.handled_at IS NULL
			  AND candidate.available_at <= now()
			  AND (candidate.claimed_until IS NULL OR candidate.claimed_until < now())
			  AND NOT EXISTS (
				SELECT 1 FROM agents_primary.interaction_deliveries earlier
				WHERE earlier.request_id = candidate.request_id
				  AND earlier.recipient_agent_id = candidate.recipient_agent_id
				  AND earlier.handled_at IS NULL
				  AND (earlier.revision < candidate.revision OR
				      (earlier.revision = candidate.revision AND
				       (earlier.recorded_at, earlier.event_id) < (candidate.recorded_at, candidate.event_id)))
			  )
			ORDER BY candidate.available_at, candidate.recorded_at, candidate.event_id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE agents_primary.interaction_deliveries claimed
		SET claimed_until = now() + make_interval(secs => $2)
		FROM picked
		WHERE claimed.event_id = picked.event_id
		RETURNING claimed.event_id, claimed.request_id, claimed.organization_id,
			claimed.recipient_user_id, claimed.recipient_agent_id, claimed.kind,
			claimed.interaction_status, claimed.revision, claimed.changed_at, claimed.attempts`,
		limit, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim interaction deliveries: %w", err)
	}
	claimed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Delivery, error) {
		var delivery Delivery
		err := row.Scan(&delivery.EventID, &delivery.RequestID, &delivery.OrganizationID,
			&delivery.RecipientUserID, &delivery.RecipientAgentID, &delivery.Kind,
			&delivery.Status, &delivery.Revision, &delivery.ChangedAt, &delivery.Attempts)
		return delivery, err
	})
	if err != nil {
		return nil, fmt.Errorf("read claimed interaction deliveries: %w", err)
	}
	return claimed, nil
}

// Done marks a delivery handled while retaining its event id for dedupe.
func (store *Store) Done(ctx context.Context, eventID string) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.interaction_deliveries
		SET handled_at=now(), claimed_until=NULL, last_error=''
		WHERE event_id=$1 AND handled_at IS NULL`, eventID)
	if err != nil {
		return fmt.Errorf("finish interaction delivery: %w", err)
	}
	return nil
}

// Drop marks a delivery handled without a run, for example when its request
// was removed with a departed member.
func (store *Store) Drop(ctx context.Context, eventID, reason string) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.interaction_deliveries
		SET handled_at=now(), claimed_until=NULL, dropped=true, last_error=$2
		WHERE event_id=$1 AND handled_at IS NULL`, eventID, boundedError(reason))
	if err != nil {
		return fmt.Errorf("drop interaction delivery: %w", err)
	}
	return nil
}

// Release retries a delivery after retryAfter. A failed attempt counts
// toward maxAttempts; a busy conversation does not. It reports whether the
// delivery was dropped because it failed too often.
func (store *Store) Release(ctx context.Context, eventID string, failed bool, maxAttempts int, retryAfter time.Duration, reason string) (bool, error) {
	if maxAttempts < 1 || retryAfter < 0 {
		return false, errors.New("releasing an interaction delivery needs positive attempts and a non-negative retry delay")
	}
	increment := 0
	if failed {
		increment = 1
	}
	var dropped bool
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE agents_primary.interaction_deliveries
			SET handled_at=now(), claimed_until=NULL, dropped=true,
				attempts=attempts+$2, last_error=$4
			WHERE event_id=$1 AND handled_at IS NULL AND attempts+$2 >= $3`,
			eventID, increment, maxAttempts, boundedError(reason))
		if err != nil {
			return fmt.Errorf("drop failing interaction delivery: %w", err)
		}
		dropped = tag.RowsAffected() == 1
		if dropped {
			return nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE agents_primary.interaction_deliveries
			SET claimed_until=NULL, available_at=now()+make_interval(secs => $3),
				attempts=attempts+$2, last_error=$4
			WHERE event_id=$1 AND handled_at IS NULL`,
			eventID, increment, retryAfter.Seconds(), boundedError(reason))
		if err != nil {
			return fmt.Errorf("release interaction delivery: %w", err)
		}
		return nil
	})
	return dropped, err
}

// Purge removes old dedupe records after their retention window.
func (store *Store) Purge(ctx context.Context, age time.Duration) error {
	_, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_deliveries
		WHERE handled_at IS NOT NULL AND handled_at < now()-make_interval(secs => $1)`, age.Seconds())
	if err != nil {
		return fmt.Errorf("purge interaction deliveries: %w", err)
	}
	return nil
}

// Thread returns the stable conversation used by one agent for one request.
func (store *Store) Thread(ctx context.Context, requestID, agentID string) (string, error) {
	var threadID string
	err := store.database.Pool.QueryRow(ctx, `SELECT thread_id
		FROM agents_primary.interaction_delivery_threads
		WHERE request_id=$1 AND agent_id=$2`, requestID, agentID).Scan(&threadID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read interaction delivery thread: %w", err)
	}
	return threadID, nil
}

// SetThread records a newly allocated conversation. Only one delivery for a
// request and recipient is claimable at once, so replacing is safe; the
// uniqueness constraint remains a final guard across replicas.
func (store *Store) SetThread(ctx context.Context, delivery Delivery, threadID string) error {
	_, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.interaction_delivery_threads
			(request_id, organization_id, user_id, agent_id, thread_id)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (request_id, agent_id) DO UPDATE SET thread_id=EXCLUDED.thread_id`,
		delivery.RequestID, delivery.OrganizationID, delivery.RecipientUserID, delivery.RecipientAgentID, threadID)
	if err != nil {
		return fmt.Errorf("record interaction delivery thread: %w", err)
	}
	return nil
}

// ClearThread forgets a deleted conversation only if it is still the one
// the failed start attempted to use.
func (store *Store) ClearThread(ctx context.Context, requestID, agentID, expected string) error {
	_, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_delivery_threads
		WHERE request_id=$1 AND agent_id=$2 AND thread_id=$3`, requestID, agentID, expected)
	if err != nil {
		return fmt.Errorf("clear interaction delivery thread: %w", err)
	}
	return nil
}

// AffectedThreads lists every delivery conversation whose interaction
// involves a departed member, including the other party's copy. It must run
// before the interaction ledger row is removed.
func (store *Store) AffectedThreads(ctx context.Context, organizationID, userID string) ([]OwnedThread, error) {
	if strings.TrimSpace(organizationID) == "" {
		return nil, errors.New("listing interaction delivery threads needs an organization")
	}
	rows, err := store.database.Pool.Query(ctx, `
		SELECT threads.request_id, threads.organization_id, threads.user_id, threads.agent_id, threads.thread_id
		FROM agents_primary.interaction_delivery_threads threads
		LEFT JOIN agents_primary.agent_interactions interaction ON interaction.id=threads.request_id
		WHERE threads.organization_id=$1 AND ($2='' OR threads.user_id=$2 OR
			interaction.requester_user_id=$2 OR interaction.target_user_id=$2)
		ORDER BY threads.request_id, threads.agent_id`, organizationID, userID)
	if err != nil {
		return nil, fmt.Errorf("list interaction delivery threads: %w", err)
	}
	result, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (OwnedThread, error) {
		var thread OwnedThread
		err := row.Scan(&thread.RequestID, &thread.OrganizationID, &thread.UserID, &thread.AgentID, &thread.ThreadID)
		return thread, err
	})
	if err != nil {
		return nil, fmt.Errorf("read interaction delivery threads: %w", err)
	}
	return result, nil
}

// Forget removes delivery state for a departed member or organization. When
// one member is named it also removes jobs and thread mappings for the other
// party to every interaction involving them.
func (store *Store) Forget(ctx context.Context, organizationID, userID string) error {
	if strings.TrimSpace(organizationID) == "" {
		return errors.New("forgetting interaction deliveries needs an organization")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.interaction_deliveries
			WHERE organization_id=$1 AND ($2='' OR recipient_user_id=$2 OR request_id IN (
				SELECT id FROM agents_primary.agent_interactions
				WHERE organization_id=$1 AND (requester_user_id=$2 OR target_user_id=$2)
			))`, organizationID, userID); err != nil {
			return fmt.Errorf("forget interaction deliveries: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.interaction_delivery_threads
			WHERE organization_id=$1 AND ($2='' OR user_id=$2 OR request_id IN (
				SELECT id FROM agents_primary.agent_interactions
				WHERE organization_id=$1 AND (requester_user_id=$2 OR target_user_id=$2)
			))`, organizationID, userID); err != nil {
			return fmt.Errorf("forget interaction delivery threads: %w", err)
		}
		return nil
	})
}

func validate(delivery Delivery) error {
	switch {
	case strings.TrimSpace(delivery.EventID) == "", strings.TrimSpace(delivery.RequestID) == "":
		return errors.New("an interaction delivery needs event and request ids")
	case strings.TrimSpace(delivery.OrganizationID) == "", strings.TrimSpace(delivery.RecipientUserID) == "", strings.TrimSpace(delivery.RecipientAgentID) == "":
		return errors.New("an interaction delivery needs organization, recipient member and agent ids")
	case delivery.Kind != KindTargetRequested && delivery.Kind != KindTargetApproved && delivery.Kind != KindRequesterRejected && delivery.Kind != KindRequesterCompleted:
		return fmt.Errorf("unknown interaction delivery kind %q", delivery.Kind)
	case strings.TrimSpace(delivery.Status) == "":
		return errors.New("an interaction delivery needs its interaction status")
	case delivery.Revision < 1:
		return errors.New("an interaction delivery needs a positive revision")
	case delivery.ChangedAt.IsZero():
		return errors.New("an interaction delivery needs when it changed")
	default:
		return nil
	}
}

func boundedError(reason string) string {
	runes := []rune(strings.TrimSpace(reason))
	if len(runes) > 1000 {
		runes = runes[:1000]
	}
	return string(runes)
}

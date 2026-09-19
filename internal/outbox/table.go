// Package outbox publishes the events a service wrote in the same transaction
// as the change they describe.
//
// Every service's outbox table has the same columns, so claiming, publishing,
// retrying and forgetting their events is one implementation, named by table.
// A claim carries a token as well as an expiry: a dispatcher whose claim
// expired while it was still publishing, so that another replica has taken
// the event over, cannot then mark a row it no longer owns. Published events
// are kept for a while to explain recent deliveries, then deleted, so a table
// does not grow by one row per change for ever.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is one row of an outbox table.
type Event struct {
	ID         string
	DetailType string
	Data       json.RawMessage
	OccurredAt time.Time
	Attempts   int
	// TraceContext is the W3C trace context of the transaction that wrote
	// this event, encoded by observability.CaptureTraceCarrier; "" when that
	// transaction carried no valid span. A publisher decodes it with
	// observability.DecodeTraceCarrier to link its producer span back to the
	// write.
	TraceContext string
}

// Table claims, marks and forgets the events in one outbox table. The table
// has the columns every service's outbox shares: id, detail_type, detail,
// occurred_at, attempts, available_at, locked_until, published_at, last_error,
// claim_token and trace_context.
type Table struct {
	pool   *pgxpool.Pool
	name   string
	redact []string
}

// Option adjusts a Table.
type Option func(*Table)

// RedactOnPublish blanks the detail of events of these types once they are
// published, for payloads that are needed only until delivery, such as a link
// carrying a single-use token.
func RedactOnPublish(detailTypes ...string) Option {
	return func(table *Table) { table.redact = append(table.redact, detailTypes...) }
}

var tableName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}(\.[a-z_][a-z0-9_]{0,62})?$`)

// NewTable names an outbox table, optionally schema-qualified. The name is
// written into SQL, so anything but a plain lowercase identifier is refused.
func NewTable(pool *pgxpool.Pool, name string, options ...Option) (*Table, error) {
	if pool == nil {
		return nil, errors.New("outbox table needs a pool")
	}
	if !tableName.MatchString(name) {
		return nil, fmt.Errorf("outbox table name %q is not a lowercase SQL identifier", name)
	}
	table := &Table{pool: pool, name: name}
	for _, option := range options {
		option(table)
	}
	return table, nil
}

// Name is the table's name, as given.
func (table *Table) Name() string { return table.name }

// Claim hands out up to limit events that are due and not claimed by anyone
// else, oldest first, and stamps them with token until ttl has passed.
func (table *Table) Claim(ctx context.Context, limit int, ttl time.Duration, token string) ([]Event, error) {
	rows, err := table.pool.Query(ctx, `
		WITH picked AS (
			SELECT id
			FROM `+table.name+`
			WHERE published_at IS NULL
			  AND available_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY occurred_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE `+table.name+` AS event
		SET locked_until = now() + ($2 * interval '1 millisecond'), claim_token = $3
		FROM picked
		WHERE event.id = picked.id
		RETURNING event.id, event.detail_type, event.detail, event.occurred_at, event.attempts, event.trace_context`,
		limit, ttl.Milliseconds(), token)
	if err != nil {
		return nil, fmt.Errorf("claim %s: %w", table.name, err)
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		var detail []byte
		var traceContext *string
		if err := rows.Scan(&event.ID, &event.DetailType, &detail, &event.OccurredAt, &event.Attempts, &traceContext); err != nil {
			return nil, fmt.Errorf("scan %s event: %w", table.name, err)
		}
		event.Data = json.RawMessage(detail)
		if traceContext != nil {
			event.TraceContext = *traceContext
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim %s: %w", table.name, err)
	}
	return events, nil
}

// MarkPublished records that an event reached the bus. A token that no longer
// owns the row, because its claim expired and another dispatcher took the
// event, changes nothing and reports false.
func (table *Table) MarkPublished(ctx context.Context, eventID, token string) (bool, error) {
	tag, err := table.pool.Exec(ctx, `
		UPDATE `+table.name+`
		SET published_at = now(), locked_until = NULL, claim_token = NULL, last_error = NULL,
		    detail = CASE WHEN detail_type = ANY($3) THEN '{}'::jsonb ELSE detail END
		WHERE id = $1 AND claim_token = $2`, eventID, token, table.redactedTypes())
	if err != nil {
		return false, fmt.Errorf("mark %s event published: %w", table.name, err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkFailed sends an event back to be retried after retryAfter. A token that
// no longer owns the row changes nothing and reports false.
func (table *Table) MarkFailed(ctx context.Context, eventID, token string, retryAfter time.Duration, message string) (bool, error) {
	tag, err := table.pool.Exec(ctx, `
		UPDATE `+table.name+`
		SET attempts = attempts + 1,
		    available_at = now() + ($3 * interval '1 millisecond'),
		    locked_until = NULL,
		    claim_token = NULL,
		    last_error = left($4, 1000)
		WHERE id = $1 AND claim_token = $2`, eventID, token, retryAfter.Milliseconds(), message)
	if err != nil {
		return false, fmt.Errorf("mark %s event failed: %w", table.name, err)
	}
	return tag.RowsAffected() > 0, nil
}

// Release gives back events claimed under token that were never attempted,
// without counting an attempt, so a dispatcher that stops mid-batch does not
// leave them waiting out its claim.
func (table *Table) Release(ctx context.Context, eventIDs []string, token string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	if _, err := table.pool.Exec(ctx, `
		UPDATE `+table.name+`
		SET locked_until = NULL, claim_token = NULL
		WHERE id = ANY($1) AND claim_token = $2`, eventIDs, token); err != nil {
		return fmt.Errorf("release %s events: %w", table.name, err)
	}
	return nil
}

// DeletePublished removes up to limit events published before the given
// time, and reports how many it removed.
func (table *Table) DeletePublished(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := table.pool.Exec(ctx, `
		DELETE FROM `+table.name+`
		WHERE id IN (
			SELECT id FROM `+table.name+`
			WHERE published_at IS NOT NULL AND published_at < $1
			LIMIT $2
		)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("delete published %s events: %w", table.name, err)
	}
	return int(tag.RowsAffected()), nil
}

func (table *Table) redactedTypes() []string {
	if table.redact == nil {
		return []string{}
	}
	return table.redact
}

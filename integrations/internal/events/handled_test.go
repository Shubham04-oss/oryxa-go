package events

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// The table needs a Postgres with the schema's migrations applied; see
// integrations/vault_integration_test.go. The rows the test makes are removed after.
func newHandled(t *testing.T) *Handled {
	t.Helper()
	url := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is not set")
	}
	database, err := postgreskit.Open(context.Background(), postgreskit.Config{URL: url, ApplicationName: "oryxa-integrations-events-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return NewHandled(database.Pool, nil)
}

// departures is the consumer's application over the real table, with
// handlers that count.
type departures struct {
	*Handled
	app *fakeApp
}

func (d departures) MemberRemoved(ctx context.Context, event authv1.MemberRemoved) error {
	return d.app.MemberRemoved(ctx, event)
}

func (d departures) OrganizationDeleted(ctx context.Context, event authv1.OrganizationDeleted) error {
	return d.app.OrganizationDeleted(ctx, event)
}

func TestHandledEventsAreRecordedOnceAndForgotten(t *testing.T) {
	handled := newHandled(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("evt_test_%d_", time.Now().UnixNano())
	t.Cleanup(func() {
		handled.pool.Exec(context.Background(), `DELETE FROM integrations.handled_events WHERE event_id LIKE $1`, prefix+"%")
	})

	app := &fakeApp{}
	harness()
	consumer, err := NewConsumer(departures{handled, app}, verifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	removed, deleted := prefix+"1", prefix+"2"
	for range 2 {
		if err := consumer.Handle(ctx, memberRemoved(removed)); err != nil {
			t.Fatal(err)
		}
		// A replay, signed afresh.
		if err := consumer.Handle(ctx, organizationDeleted(deleted)); err != nil {
			t.Fatal(err)
		}
	}
	if app.calls() != 2 {
		t.Fatalf("handlers ran %d times, want once per event", app.calls())
	}
	var detailType string
	if err := handled.pool.QueryRow(ctx, `SELECT detail_type FROM integrations.handled_events WHERE event_id = $1`, removed).Scan(&detailType); err != nil ||
		detailType != authv1.MemberRemovedDetailType {
		t.Fatalf("recorded %q, %v", detailType, err)
	}
	// Recording again changes nothing.
	if err := handled.MarkHandled(ctx, removed, "other"); err != nil {
		t.Fatal(err)
	}

	// Forgotten once old enough, and not before. The cutoff is long past, so
	// the sweep touches no other test's rows.
	cutoff := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := handled.Forget(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	if known, err := handled.AlreadyHandled(ctx, removed); err != nil || !known {
		t.Fatalf("a fresh event was forgotten: %v, %v", known, err)
	}
	if _, err := handled.pool.Exec(ctx, `UPDATE integrations.handled_events SET handled_at = $2 WHERE event_id = $1`,
		removed, cutoff.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if forgotten, err := handled.Forget(ctx, cutoff); err != nil || forgotten != 1 {
		t.Fatalf("Forget = %d, %v", forgotten, err)
	}
	if known, err := handled.AlreadyHandled(ctx, removed); err != nil || known {
		t.Fatalf("an old event is still known: %v, %v", known, err)
	}
	if known, err := handled.AlreadyHandled(ctx, deleted); err != nil || !known {
		t.Fatalf("a fresh event was forgotten: %v, %v", known, err)
	}
}

package events

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestOutboxLifecycle needs the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' \
//	  go test ./internal/events
func TestOutboxLifecycle(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agents-primary-test", Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	outbox := NewOutbox(database)
	if err := outbox.Ensure(ctx); err != nil {
		t.Fatal(err)
	}

	publishable, err := NewEvent("agents.session.created.v1", map[string]string{"session_id": "s_1"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	retryable, err := NewEvent("agents.run.completed.v1", map[string]string{"run_id": "run_1"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = database.SQLX.ExecContext(ctx, `DELETE FROM `+schema+`.agents_outbox WHERE id = ANY($1)`, []string{publishable.ID, retryable.ID})
	}()

	if err := outbox.RecordEvents(ctx, []Event{publishable, retryable}); err != nil {
		t.Fatal(err)
	}
	if err := outbox.RecordEvents(ctx, nil); err != nil {
		t.Fatalf("recording nothing: %v", err)
	}
	if err := outbox.RecordEvents(ctx, []Event{{ID: "", DetailType: "agents.bad.v1", OccurredAt: time.Now()}}); err == nil {
		t.Fatal("RecordEvents() error = nil for an event without an id")
	}

	claimed, err := outbox.ClaimOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	claimable := map[string]bool{}
	for _, event := range claimed {
		claimable[event.ID] = true
	}
	if !claimable[publishable.ID] || !claimable[retryable.ID] {
		t.Fatalf("claimed %v, want both events", claimable)
	}

	if err := outbox.MarkOutboxPublished(ctx, publishable.ID); err != nil {
		t.Fatal(err)
	}
	if err := outbox.MarkOutboxFailed(ctx, retryable.ID, time.Second, "bus is down"); err != nil {
		t.Fatal(err)
	}

	// The failed event is available again after its backoff, so claim until
	// only the published one is missing from the pending set.
	deadline := time.Now().Add(3 * time.Second)
	foundAgain := false
	for time.Now().Before(deadline) && !foundAgain {
		again, err := outbox.ClaimOutbox(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range again {
			if event.ID == publishable.ID {
				t.Fatal("a published event was claimed again")
			}
			if event.ID == retryable.ID {
				foundAgain = event.Attempts == 1
			}
		}
		if !foundAgain {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if !foundAgain {
		t.Fatal("the retried event was never claimed again")
	}

	var attempts int
	var lastError string
	if err := database.SQLX.QueryRowContext(ctx, `SELECT attempts, last_error FROM `+schema+`.agents_outbox WHERE id = $1`, retryable.ID).Scan(&attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastError != "bus is down" {
		t.Fatalf("attempts = %d, last_error = %q", attempts, lastError)
	}
}

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

	// Claiming, marking and retrying are internal/outbox's own concern now:
	// see its table_integration_test.go. This only has to prove both events
	// were actually written, and the bad one was not.
	var pending int
	if err := database.SQLX.QueryRowContext(ctx, `SELECT count(*) FROM `+schema+`.agents_outbox WHERE id = ANY($1) AND published_at IS NULL`,
		[]string{publishable.ID, retryable.ID}).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Fatalf("pending outbox events = %d, want 2", pending)
	}
	var total int
	if err := database.SQLX.QueryRowContext(ctx, `SELECT count(*) FROM `+schema+`.agents_outbox WHERE detail_type = $1`, "agents.bad.v1").Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("outbox events for the refused event = %d, want 0", total)
	}
}

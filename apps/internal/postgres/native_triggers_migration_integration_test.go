package postgres

import (
	"context"
	"testing"
)

// TestNativeTriggersApplyOverADatabaseAtSlackReply: production has 001–009
// applied and no record of 010, so 010 must apply on its own after 009, and a
// database that ran it already (the lane's old 009_native_triggers.sql name
// included) must take it again unchanged.
func TestNativeTriggersApplyOverADatabaseAtSlackReply(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	// Back to the schema as 009 left it: 010's columns and indexes gone, the
	// Composio index as 001 made it, and no record of 010.
	if _, err := store.pool.Exec(ctx, `
		DROP INDEX apps_triggers_own_idx;
		DROP INDEX apps_triggers_poll_due_idx;
		ALTER TABLE apps_triggers DROP COLUMN broker, DROP COLUMN own_key, DROP COLUMN cursor,
			DROP COLUMN next_poll_at, DROP COLUMN poll_claimed_until;
		DROP INDEX apps_triggers_instance_idx;
		CREATE UNIQUE INDEX apps_triggers_instance_idx ON apps_triggers (composio_trigger_id) WHERE status <> 'removed';
		DELETE FROM oryxa_migrations WHERE service = 'apps' AND name = '010_native_triggers.sql';
		INSERT INTO oryxa_migrations (service, name, checksum) VALUES ('apps', '009_native_triggers.sql', 'old-name')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}
	var columns, recorded int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'apps_triggers'
		AND column_name IN ('broker', 'own_key', 'cursor', 'next_poll_at', 'poll_claimed_until')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM oryxa_migrations
		WHERE service = 'apps' AND name = '010_native_triggers.sql'`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if columns != 5 || recorded != 1 {
		t.Fatalf("after migrating, %d of 5 columns and %d records of 010", columns, recorded)
	}
}

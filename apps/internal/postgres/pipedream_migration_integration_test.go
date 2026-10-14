package postgres

import (
	"context"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// TestPipedreamMigrationWidensTheBrokerAndCountsActionsByIt: 012 lets a
// connection be Pipedream's and nothing unknown, keeps one connection per
// Pipedream account, and records each action's broker, both for a new run
// and, when it applies again, for the runs from before it.
func TestPipedreamMigrationWidensTheBrokerAndCountsActionsByIt(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	composio, _ := testConnection(t, store, "org_1", "member_1")
	oryxa, _ := testConnection(t, store, "org_1", "member_2")
	pipedream, _ := testConnection(t, store, "org_1", "member_3")
	if _, err := store.pool.Exec(ctx, `UPDATE apps_connections SET broker = 'oryxa' WHERE id = $1`, oryxa.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE apps_connections SET broker = 'pipedream', pipedream_account_id = 'apn_1'
		WHERE id = $1`, pipedream.ID); err != nil {
		t.Fatalf("a Pipedream connection = %v", err)
	}
	// Running a Pipedream action names the account, so the store must read it.
	if read, err := store.Connection(ctx, "org_1", pipedream.ID); err != nil || read.PipedreamAccountID != "apn_1" {
		t.Fatalf("read back = %+v, %v", read, err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE apps_connections SET broker = 'nango' WHERE id = $1`, composio.ID); err == nil {
		t.Fatal("an unknown broker was stored")
	}
	if _, err := store.pool.Exec(ctx, `UPDATE apps_connections SET pipedream_account_id = 'apn_1' WHERE id = $1`, oryxa.ID); err == nil {
		t.Fatal("two connections share a Pipedream account")
	}

	brokerOf := func(actionID string) string {
		t.Helper()
		var broker string
		if err := store.pool.QueryRow(ctx, `SELECT broker FROM apps_actions WHERE id = $1`, actionID).Scan(&broker); err != nil {
			t.Fatal(err)
		}
		return broker
	}
	want := map[string]string{}
	for _, connection := range []domain.Connection{composio, oryxa, pipedream} {
		action := domain.Action{
			ID: "act_" + randomHex(8), OrganizationID: "org_1", MemberID: connection.MemberID, ConnectionID: connection.ID,
			Toolkit: "github", Tool: "GITHUB_LIST_ISSUES", ReadOnly: true, CreatedAt: testClock,
		}
		if _, _, err := store.StartAction(ctx, action); err != nil {
			t.Fatal(err)
		}
		want[action.ID] = map[string]string{composio.ID: "composio", oryxa.ID: "oryxa", pipedream.ID: "pipedream"}[connection.ID]
	}
	for id, broker := range want {
		if got := brokerOf(id); got != broker {
			t.Fatalf("a new run's broker = %q, want %q", got, broker)
		}
	}

	// The runs from before 012 all read as Composio's until it fills them;
	// applying it again fills them and changes nothing else.
	if _, err := store.pool.Exec(ctx, `
		UPDATE apps_actions SET broker = 'composio';
		DELETE FROM oryxa_migrations WHERE service = 'apps' AND name = '012_pipedream.sql'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}
	for id, broker := range want {
		if got := brokerOf(id); got != broker {
			t.Fatalf("a backfilled run's broker = %q, want %q", got, broker)
		}
	}

	// A trigger's Pipedream columns are there, and may be left NULL.
	var columns int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'apps_triggers'
		AND ((column_name = 'pipedream_trigger_id' AND data_type = 'text' AND is_nullable = 'YES')
		  OR (column_name = 'pipedream_signing_key' AND data_type = 'bytea' AND is_nullable = 'YES'))`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 2 {
		t.Fatalf("%d of apps_triggers' 2 Pipedream columns", columns)
	}
}

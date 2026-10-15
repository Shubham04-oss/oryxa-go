package integrations

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// The tables need a Postgres with the schema's migrations applied:
//
//	INTEGRATIONS_TEST_DATABASE_URL='postgres://oryxa:oryxa@localhost:5432/oryxa?sslmode=disable' go test ./integrations
func TestVaultTables(t *testing.T) {
	url := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: url, ApplicationName: "oryxa-integrations-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	// Twice: a second start finds nothing to do.
	for range 2 {
		if err := database.Migrate(ctx, Name, migrations, "migrations/*.sql", migrationsAdvisoryLock); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing the test writes outlives it.
	defer tx.Rollback(ctx)
	now := time.Now().UTC()
	exec := func(sql string, args ...any) error {
		t.Helper()
		_, err := tx.Exec(ctx, "SAVEPOINT s")
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, sql, args...)
		if err != nil {
			if _, rerr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT s"); rerr != nil {
				t.Fatal(rerr)
			}
		}
		return err
	}
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if err := exec(sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	const app = `INSERT INTO integrations.org_apps (id, org_id, app, kind, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'oauth_client', 'ok', $4, $4)`
	mustExec(app, "oa_1", "org_a", "github", now)
	mustExec(app, "oa_oryxa", nil, "github", now)
	if exec(app, "oa_2", "org_a", "github", now) == nil {
		t.Error("a second live app for the same org and app was accepted")
	}
	if exec(app, "oa_oryxa2", nil, "github", now) == nil {
		t.Error("a second Oryxa app for the same app was accepted")
	}
	mustExec(`UPDATE integrations.org_apps SET status = 'retired' WHERE id = 'oa_1'`)
	mustExec(app, "oa_3", "org_a", "github", now)

	const conn = `INSERT INTO integrations.connections (id, org_id, member_id, level, app, org_app_id, mode, status, created_at, updated_at)
		VALUES ($1, 'org_a', $2, $3, 'github', 'oa_3', 'byo', 'ok', $4, $4)`
	mustExec(conn, "c_1", "m_1", "personal", now)
	mustExec(conn, "c_org", nil, "org", now)
	if exec(conn, "c_bad", nil, "personal", now) == nil {
		t.Error("a personal connection without a member was accepted")
	}
	if exec(conn, "c_bad2", "m_1", "org", now) == nil {
		t.Error("an org connection with a member was accepted")
	}

	const cred = `INSERT INTO integrations.credentials (connection_id, kind, sealed) VALUES ($1, $2, '{"version":1}')`
	mustExec(cred, "c_1", "access")
	mustExec(cred, "c_1", "refresh")
	if exec(cred, "c_1", "access") == nil {
		t.Error("a second access credential for one connection was accepted")
	}
	if exec(cred, "nope", "access") == nil {
		t.Error("a credential for no connection was accepted")
	}
}

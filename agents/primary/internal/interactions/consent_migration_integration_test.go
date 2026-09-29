package interactions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// consentChecks are the checks 013 adds to the requests, and the one it
// replaces, all of which 014 validates.
var consentChecks = []string{
	"agent_interactions_origin_check", "agent_interactions_consent_check", "agent_interactions_consent_decision_check",
	"agent_interactions_decided_via_check", "agent_interactions_decided_surface_check", "agent_interactions_decided_by_check",
}

func validatedChecks(t *testing.T, ctx context.Context, tx interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) map[string]bool {
	t.Helper()
	rows, err := tx.Query(ctx, `SELECT conname, convalidated FROM pg_constraint
		WHERE conrelid='agents_primary.agent_interactions'::regclass AND conname = ANY($1)`, consentChecks)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	validated := map[string]bool{}
	for rows.Next() {
		var name string
		var ok bool
		if err := rows.Scan(&name, &ok); err != nil {
			t.Fatal(err)
		}
		validated[name] = ok
	}
	return validated
}

// TestTheConsentMigrationsAreSafeToApplyAgain: the runner applies a file again
// whenever its checksum changes, so 013 to 015 end up exactly as one
// application leaves them however many times they run: every check present and
// validated, the decider check the wider one, the tables and indexes there once.
func TestTheConsentMigrationsAreSafeToApplyAgain(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	for attempt := range 3 {
		if _, err := database.Pool.Exec(ctx, `UPDATE oryxa_migrations SET checksum='stale' WHERE service='agent-interactions'`); err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("applying the migrations again (attempt %d): %v", attempt+1, err)
		}
	}
	validated := validatedChecks(t, ctx, database.Pool)
	for _, name := range consentChecks {
		if !validated[name] {
			t.Errorf("check %s validated=%v, want present and validated", name, validated[name])
		}
	}
	var definition string
	if err := database.Pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname='agent_interactions_decided_by_check' AND conrelid='agents_primary.agent_interactions'::regclass`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"member", "agent", "system", "lease", "bypass"} {
		if !strings.Contains(definition, "'"+kind+"'") {
			t.Errorf("the decider check does not know %q: %s", kind, definition)
		}
	}

	rows, err := database.Pool.Query(ctx, `SELECT column_name, coalesce(column_default, ''), is_nullable FROM information_schema.columns
		WHERE table_schema='agents_primary' AND table_name='agent_interactions'
		  AND column_name IN ('ask_id', 'origin', 'consent', 'decided_via', 'decided_reason', 'secret_viewed_at') ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]string{}
	for rows.Next() {
		var name, fallback, nullable string
		if err := rows.Scan(&name, &fallback, &nullable); err != nil {
			t.Fatal(err)
		}
		columns[name] = fallback + " nullable=" + nullable
	}
	rows.Close()
	if columns["origin"] != "'agent'::text nullable=NO" || columns["ask_id"] != "''::text nullable=NO" || columns["consent"] != "''::text nullable=NO" ||
		columns["decided_via"] != "''::text nullable=NO" || columns["decided_reason"] != "''::text nullable=NO" || columns["secret_viewed_at"] != " nullable=YES" {
		t.Fatalf("columns = %v", columns)
	}
	for _, table := range []string{"interaction_asks", "interaction_leases"} {
		var found int
		if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='agents_primary' AND table_name=$1`, table).Scan(&found); err != nil || found != 1 {
			t.Errorf("table %s: %d found, %v", table, found, err)
		}
	}
	for _, index := range []string{"agent_interactions_ask_idx", "interaction_asks_trigger_idx", "interaction_asks_open_idx", "interaction_leases_live_idx"} {
		var found int
		if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname='agents_primary' AND indexname=$1`, index).Scan(&found); err != nil || found != 1 {
			t.Errorf("index %s: %d found, %v", index, found, err)
		}
	}
}

// scratchDatabase is a database of its own, made for one test and dropped after
// it, for a test that has to put the schema where a release met it before, which
// the database every other test shares must not be put through: a lease or a
// bypass cannot be stored while the older decider check is in place. It skips
// where the database's user may not make one.
func scratchDatabase(t *testing.T) *postgreskit.Database {
	t.Helper()
	shared, _ := openInteractionStore(t)
	ctx := context.Background()
	name := fmt.Sprintf("oryxa_interactions_upgrade_%d", time.Now().UnixNano())
	if _, err := shared.Pool.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Skipf("the test database's user cannot make a database of its own: %v", err)
	}
	t.Cleanup(func() {
		if _, err := shared.Pool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	scratch, err := url.Parse(os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("the test database's URL cannot be read")
	}
	scratch.Path = "/" + name
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: scratch.String(), ApplicationName: "oryxa-agent-interactions-upgrade-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database
}

// migrationsBefore013 is the interactions' migrations as a release before 013
// carried them: every file up to 012.
func migrationsBefore013(t *testing.T) fs.FS {
	t.Helper()
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	older := fstest.MapFS{}
	for _, name := range names {
		if path.Base(name) >= "013" {
			continue
		}
		contents, err := fs.ReadFile(migrations, name)
		if err != nil {
			t.Fatal(err)
		}
		older[name] = &fstest.MapFile{Data: contents}
	}
	return older
}

// TestADatabaseFromBefore013IsUpgradedWithItsRequestsIntact: the release that
// brings 013 meets a database at 012, whose decider check knows three kinds, with
// requests in it that check let through. Migrating adds the columns with their
// defaults, so those requests read as requests an agent made with no consent and
// no ask, widens the decider check under its name so a lease and a bypass can be
// stored, and validates every check over what is already there.
func TestADatabaseFromBefore013IsUpgradedWithItsRequestsIntact(t *testing.T) {
	database := scratchDatabase(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, "agent-interactions", migrationsBefore013(t), "migrations/*.sql", migrationsAdvisoryLock); err != nil {
		t.Fatalf("migrating to 012: %v", err)
	}
	var columns int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='agents_primary'
		AND table_name='agent_interactions' AND column_name IN ('ask_id', 'origin', 'consent')`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("a database at 012 has %d of 013's columns (%v)", columns, err)
	}
	// What the release before this one wrote: pending, decided by each kind of
	// decider it knew, and answered.
	insert := func(id, status, kind, userID, agentID string) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, `INSERT INTO agents_primary.agent_interactions
			(id, organization_id, requester_user_id, requester_agent_id, requester_agent_name, target_user_id, target_agent_id, target_agent_name,
			 purpose, task, status, idempotency_key, expires_at, decided_by_kind, decided_by_user_id, decided_by_agent_id)
			VALUES ($1, 'org_1', 'user_a', 'agt_a', 'Crew', 'user_b', 'agt_b', 'Carl', 'Launch', 'Review it.', $2, $1, now() + interval '1 day', $3, $4, $5)`,
			id, status, kind, userID, agentID); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("irq_pending", "pending", "", "", "")
	insert("irq_member", "approved", "member", "user_b", "")
	insert("irq_agent", "approved", "agent", "user_b", "agt_b")
	insert("irq_system", "rejected", "system", "", "")
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.agent_interactions SET decided_by_kind='lease', decided_by_user_id='user_b' WHERE id='irq_member'`); err == nil {
		t.Fatal("a database at 012 stored a lease decision")
	}

	if err := NewStore(database).Migrate(ctx); err != nil {
		t.Fatalf("migrating from 012: %v", err)
	}
	rows, err := database.Pool.Query(ctx, `SELECT id, origin, ask_id, consent, decided_via, decided_reason, secret_viewed_at IS NULL, decided_by_kind, status
		FROM agents_primary.agent_interactions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for rows.Next() {
		var id, origin, askID, consent, via, reason, kind, status string
		var unviewed bool
		if err := rows.Scan(&id, &origin, &askID, &consent, &via, &reason, &unviewed, &kind, &status); err != nil {
			t.Fatal(err)
		}
		if origin != "agent" || askID != "" || consent != "" || via != "" || reason != "" || !unviewed {
			t.Errorf("%s reads as origin %q ask %q consent %q via %q reason %q after the upgrade", id, origin, askID, consent, via, reason)
		}
		seen++
	}
	rows.Close()
	if seen != 4 {
		t.Fatalf("%d requests after the upgrade, want the four", seen)
	}
	validated := validatedChecks(t, ctx, database.Pool)
	for _, name := range consentChecks {
		if !validated[name] {
			t.Errorf("check %s validated=%v after migrating from before 013", name, validated[name])
		}
	}
	// Now a lease and a bypass can be stored, and the rows written before are as
	// they were.
	for name, set := range map[string]string{
		"a lease":  `status='approved', decided_by_kind='lease', decided_by_user_id='user_b', decided_by_agent_id='', decided_via='lease', consent='access'`,
		"a bypass": `status='approved', decided_by_kind='bypass', decided_by_user_id='', decided_by_agent_id='', decided_via='bypass', consent='access'`,
	} {
		if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.agent_interactions SET `+set+` WHERE id='irq_member'`); err != nil {
			t.Errorf("%s was refused after the upgrade: %v", name, err)
		}
	}
	// Migrating again changes nothing: every file is recorded as it is now.
	if err := NewStore(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		contents, err := fs.ReadFile(migrations, name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(contents)
		var recorded string
		if err := database.Pool.QueryRow(ctx, `SELECT checksum FROM oryxa_migrations WHERE service='agent-interactions' AND name=$1`, path.Base(name)).Scan(&recorded); err != nil ||
			recorded != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s is recorded as %q (%v), want this file's checksum", name, recorded, err)
		}
	}
}

// TestTheChecksHoldTheInvariantsOfTheConsentColumns: what the database refuses
// to store however the code is wrong, tried against a real row.
func TestTheChecksHoldTheInvariantsOfTheConsentColumns(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	request := storedRequest(organizationID, organizationID+"_checks", "user_a", "user_b", time.Now().UTC().Truncate(time.Microsecond))
	if _, err := store.Create(ctx, request, "checks"); err != nil {
		t.Fatal(err)
	}
	try := func(set string) error {
		_, err := database.Pool.Exec(ctx, `UPDATE agents_primary.agent_interactions SET `+set+` WHERE id=$1`, request.ID)
		return err
	}
	// Rows the code writes.
	for name, set := range map[string]string{
		"a member deciding in Slack":   `status='approved', decided_by_kind='member', decided_by_user_id='user_b', decided_by_agent_id='', decided_via='slack', decided_reason='', consent='summary'`,
		"a member deciding on the web": `status='approved', decided_by_kind='member', decided_by_user_id='user_b', decided_by_agent_id='', decided_via='web', decided_reason='', consent='access'`,
		"a lease":                      `status='approved', decided_by_kind='lease', decided_by_user_id='user_b', decided_by_agent_id='', decided_via='lease', decided_reason='ils_1', consent='access'`,
		"a bypass":                     `status='approved', decided_by_kind='bypass', decided_by_user_id='', decided_by_agent_id='', decided_via='bypass', decided_reason='outranks', consent='access'`,
		"an agent":                     `status='approved', decided_by_kind='agent', decided_by_user_id='user_b', decided_by_agent_id='agt_b', decided_via='', decided_reason='', consent=''`,
		"a closure":                    `status='rejected', decided_by_kind='system', decided_by_user_id='', decided_by_agent_id='', decided_via='', decided_reason='answered_elsewhere', consent=''`,
	} {
		if err := try(set); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
	// Rows nothing should write.
	for name, set := range map[string]string{
		"an origin nobody knows":                      `origin='email'`,
		"a consent nobody knows":                      `status='approved', consent='everything'`,
		"a consent on a pending request":              `status='pending', consent='summary'`,
		"a consent on a rejected request":             `status='rejected', consent='access'`,
		"a via nobody knows":                          `decided_via='carrier pigeon'`,
		"a lease decider with the web as its surface": `status='approved', decided_by_kind='lease', decided_by_user_id='user_b', decided_by_agent_id='', decided_via='web'`,
		"a lease decider with no surface":             `status='approved', decided_by_kind='lease', decided_by_user_id='user_b', decided_by_agent_id='', decided_via=''`,
		"a bypass decider with a member":              `status='approved', decided_by_kind='bypass', decided_by_user_id='user_b', decided_by_agent_id='', decided_via='bypass'`,
		"a bypass surface with a member decider":      `status='approved', decided_by_kind='member', decided_by_user_id='user_b', decided_by_agent_id='', decided_via='bypass'`,
		"a web decision by an agent":                  `status='approved', decided_by_kind='agent', decided_by_user_id='user_b', decided_by_agent_id='agt_b', decided_via='web'`,
		"a lease that names no owner":                 `status='approved', decided_by_kind='lease', decided_by_user_id='', decided_by_agent_id='', decided_via='lease'`,
		"a decider kind nobody knows":                 `status='approved', decided_by_kind='robot', decided_by_user_id='', decided_by_agent_id=''`,
	} {
		if err := try(set); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	var refused []string
	for _, statement := range []string{
		`INSERT INTO agents_primary.interaction_asks (id, organization_id, origin, asker_user_id) VALUES ('iak_x', 'org', 'slack', 'u')`,
		`INSERT INTO agents_primary.interaction_asks (id, organization_id, origin, team_id, channel_id, thread_ts, trigger_ts, asker_user_id, kind) VALUES ('iak_y', 'org', 'slack', 'T', 'C', '1', '1', 'u', 'poll')`,
		`INSERT INTO agents_primary.interaction_asks (id, organization_id, origin, team_id, channel_id, thread_ts, trigger_ts, asker_user_id, winner_request_id) VALUES ('iak_z', 'org', 'slack', 'T', 'C', '1', '1', 'u', 'irq_1')`,
		`INSERT INTO agents_primary.interaction_leases (id, organization_id, asker_user_id, owner_user_id, channel_id, thread_ts, expires_at) VALUES ('ils_x', 'org', 'u', 'u', 'C', '1', now() + interval '1 hour')`,
		`INSERT INTO agents_primary.interaction_leases (id, organization_id, asker_user_id, owner_user_id, channel_id, thread_ts, expires_at) VALUES ('ils_y', 'org', 'a', 'b', '', '1', now() + interval '1 hour')`,
		`INSERT INTO agents_primary.interaction_leases (id, organization_id, asker_user_id, owner_user_id, channel_id, thread_ts, expires_at) VALUES ('ils_z', 'org', 'a', 'b', 'C', '1', now() - interval '1 hour')`,
	} {
		if _, err := database.Pool.Exec(ctx, statement); err == nil {
			refused = append(refused, statement)
			_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_asks WHERE organization_id='org'`)
			_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_leases WHERE organization_id='org'`)
		}
	}
	if !slices.Equal(refused, nil) {
		t.Errorf("the database stored what it must not: %v", refused)
	}
}

package interactions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

// oldDeciderCheck is 005's check as it stood before 013: three kinds.
const oldDeciderCheck = `CHECK (
	(decided_by_kind = '' AND decided_by_user_id = '' AND decided_by_agent_id = '')
	OR (decided_by_kind = 'member' AND decided_by_user_id <> '' AND decided_by_agent_id = '')
	OR (decided_by_kind = 'agent' AND decided_by_user_id <> '' AND decided_by_agent_id <> '')
	OR (decided_by_kind = 'system' AND decided_by_user_id = '' AND decided_by_agent_id = '')
) NOT VALID`

// TestADatabaseFromBefore013EndsUpWithTheWiderDeciderCheck: the release that
// brings 013 meets a database whose decider check knows three kinds, and its
// requests written by that check. Migrating replaces the check, under its name,
// with the one that also knows the lease and the bypass, and validates it;
// before that a lease's decision could not be stored.
func TestADatabaseFromBefore013EndsUpWithTheWiderDeciderCheck(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `ALTER TABLE agents_primary.agent_interactions
			DROP CONSTRAINT agent_interactions_decided_by_check,
			ADD CONSTRAINT agent_interactions_decided_by_check `+oldDeciderCheck); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM oryxa_migrations WHERE service='agent-interactions' AND name >= '013'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	validated := validatedChecks(t, ctx, database.Pool)
	for _, name := range consentChecks {
		if !validated[name] {
			t.Errorf("check %s validated=%v after migrating from before 013", name, validated[name])
		}
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

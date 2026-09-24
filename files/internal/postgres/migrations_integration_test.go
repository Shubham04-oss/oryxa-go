package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// openMigratedSchema gives a test files' schema in a PostgreSQL schema of its
// own, dropped afterwards, with only the migrations pattern names applied, so
// it can put in place rows an older schema held before migrating the rest.
func openMigratedSchema(t *testing.T, pattern string) (*Store, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("FILES_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FILES_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schemaName := "files_migrations_" + hex.EncodeToString(suffix)
	schema := pgx.Identifier{schemaName}.Sanitize()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = admin.Close(cleanup)
	})
	scoped, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := scoped.Query()
	query.Set("search_path", schemaName)
	scoped.RawQuery = query.Encode()
	store, err := Open(ctx, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.database.Migrate(ctx, "files", migrations, pattern, migrationsAdvisoryLock); err != nil {
		t.Fatalf("migrate %s: %v", pattern, err)
	}
	return store, ctx
}

// Access labels existing billing attachments the first time it is applied,
// and applying it again — which postgreskit does whenever its text changes —
// fails on nothing and relabels nothing. The default stays: the build before
// it only uploads member-visible files, and a billing attachment it writes
// without an access is refused by the billing check.
func TestDocumentACLMigrationAppliesOnceInEffect(t *testing.T) {
	store, ctx := openMigratedSchema(t, "migrations/00[1-5]_*.sql")
	now := time.Now().UTC()
	exec := func(statement string, arguments ...any) error {
		t.Helper()
		_, err := store.pool.Exec(ctx, statement, arguments...)
		return err
	}
	insertObject := func(id, purpose, scope string, memberID any, access string) error {
		t.Helper()
		columns, values := "", ""
		arguments := []any{id, purpose, scope, memberID, now}
		if access != "" {
			columns, values = ", access", ", $6"
			arguments = append(arguments, access)
		}
		return exec(`INSERT INTO files_objects (id, organization_id, scope, member_id, filename, content_type, purpose,
			size_bytes, sha256, created_by, created_at, expires_at`+columns+`)
			VALUES ($1, 'org_1', $3, $4, 'invoice.pdf', 'application/pdf', $2, 1, repeat('a', 64), 'usr_1', $5, $5`+values+`)`, arguments...)
	}
	if err := exec(`INSERT INTO files_organizations (organization_id) VALUES ('org_1')`); err != nil {
		t.Fatal(err)
	}
	for _, object := range []struct{ id, purpose, scope string }{
		{"file_invoice", "billing.attachment", "organization"},
		{"file_notes", "upload", "organization"},
	} {
		if err := insertObject(object.id, object.purpose, object.scope, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("apply the rest: %v", err)
	}
	access := func(id string) string {
		t.Helper()
		var got string
		if err := store.pool.QueryRow(ctx, `SELECT access FROM files_objects WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if access("file_invoice") != "admins" || access("file_notes") != "members" {
		t.Fatalf("labels = %s and %s, want admins for the billing attachment and members otherwise", access("file_invoice"), access("file_notes"))
	}
	// An admin restricts the notes afterwards; applying again must not undo
	// that, nor fail on anything already there.
	if err := exec(`UPDATE files_objects SET access = 'admins' WHERE id = 'file_notes'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := store.pool.Exec(ctx, `UPDATE oryxa_migrations SET checksum = 'edited' WHERE service = 'files' AND name = '006_document_acl.sql'`)
		if err != nil || result.RowsAffected() != 1 {
			t.Fatalf("mark 006 edited = %v, %v", result.RowsAffected(), err)
		}
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("apply 006 again: %v", err)
		}
		if access("file_notes") != "admins" || access("file_invoice") != "admins" {
			t.Fatalf("applying again relabelled: notes %s, invoice %s", access("file_notes"), access("file_invoice"))
		}
	}
	for _, name := range []string{"files_objects_access_check", "files_objects_admin_access_scope", "files_objects_billing_access"} {
		var count int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'files_objects'::regclass AND conname = $1`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s: %d, %v; want exactly one", name, count, err)
		}
	}
	// The build before access writes no access: an upload is labelled
	// members, and a billing attachment is refused.
	if err := insertObject("file_old_upload", "upload", "organization", nil, ""); err != nil {
		t.Fatalf("an upload without an access: %v", err)
	}
	if got := access("file_old_upload"); got != "members" {
		t.Fatalf("an upload without an access was labelled %s, want members", got)
	}
	if err := insertObject("file_old_invoice", "billing.attachment", "organization", nil, ""); err == nil {
		t.Fatal("a billing attachment written without an access was labelled members")
	}
}

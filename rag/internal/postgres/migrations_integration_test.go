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

// openMigratedSchema gives a test rag's schema in a PostgreSQL schema of its
// own, dropped afterwards, with only the migrations pattern names applied, so
// it can put in place rows an older schema held before migrating the rest.
func openMigratedSchema(t *testing.T, pattern string) (*Store, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("RAG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RAG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schemaName := "rag_migrations_" + hex.EncodeToString(suffix)
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
	// public stays on the path for the pgvector types.
	query.Set("search_path", schemaName+",public")
	scoped.RawQuery = query.Encode()
	store, err := Open(ctx, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.database.Migrate(ctx, "rag", migrations, pattern, migrationsAdvisoryLock); err != nil {
		t.Fatalf("migrate %s: %v", pattern, err)
	}
	return store, ctx
}

// reapply has postgreskit apply these migrations again, as it does to one
// whose text changed since it was applied.
func reapply(t *testing.T, ctx context.Context, store *Store, service string, names ...string) {
	t.Helper()
	result, err := store.pool.Exec(ctx, `UPDATE oryxa_migrations SET checksum = 'edited' WHERE service = $1 AND name = ANY($2)`, service, names)
	if err != nil || result.RowsAffected() != int64(len(names)) {
		t.Fatalf("mark %v edited = %v, %v", names, result.RowsAffected(), err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("apply %v again: %v", names, err)
	}
}

// The migrations adding organization ownership, access labels, removal
// tombstones, provenance and sync markers label and requeue what was there
// the first time they are applied, and nothing the second or third time:
// postgreskit applies a migration again whenever its text changes, and a
// repeated backfill would relabel documents an admin has since reclassified
// and resubmit the whole knowledge base.
func TestDocumentAccessMigrationsApplyOnceInEffect(t *testing.T) {
	store, ctx := openMigratedSchema(t, "migrations/00[1-8]_*.sql")
	now := time.Now().UTC()
	exec := func(statement string, arguments ...any) {
		t.Helper()
		if _, err := store.pool.Exec(ctx, statement, arguments...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	// What the schema held before access existed.
	exec(`INSERT INTO rag_corpora (id, organization_id, slug, name, scope, embedding_provider, embedding_model,
		embedding_dimensions, embedding_metric, backend, chunk_strategy, chunk_max_tokens, chunk_overlap_tokens,
		metadata, created_by, created_at, updated_at) VALUES
		('cor_org', 'org_1', 'handbook', 'Handbook', 'organization', 'bedrock', 'amazon.titan-embed-text-v2:0',
		 1024, 'cosine', 'pgvector', 'structure', 512, 64, '{}', 'usr_1', $1, $1),
		('cor_kb', 'org_1', 'knowledge', 'Knowledge', 'organization', 'bedrock', 'amazon.titan-embed-text-v2:0',
		 1024, 'cosine', 'bedrock-kb', 'structure', 512, 64, '{"origin":"knowledge"}', 'usr_1', $1, $1)`, now)
	exec(`INSERT INTO rag_documents (id, corpus_id, organization_id, source_kind, source_ref, title, content_type,
		content_sha256, byte_size, body, metadata, status, created_by, created_at, updated_at, kb_synced_hash) VALUES
		('doc_file', 'cor_org', 'org_1', 'file', 'file_1', 'Payroll', 'text/plain', 'sha-file', 4, 'body', '{}', 'ready', 'usr_1', $1, $1, ''),
		('doc_page', 'cor_org', 'org_1', 'external', 'https://example.com/page', 'Page', 'text/plain', 'sha-page', 4, 'body', '{}', 'ready', 'usr_1', $1, $1, ''),
		('doc_gmail', 'cor_kb', 'org_1', 'external', 'https://mail.example.com/1', 'Invoice', 'text/plain', 'sha-gmail', 4, 'body',
		 '{"knowledge_source_id":"src_1","provider":"gmail"}', 'ready', 'usr_1', $1, $1, 'sha-gmail'),
		('doc_github', 'cor_kb', 'org_1', 'external', 'https://github.com/acme/repo/issues/1', 'Issue', 'text/plain', 'sha-github', 4, 'body',
		 '{"knowledge_source_id":"src_2","provider":"github"}', 'ready', 'usr_1', $1, $1, 'sha-github')`, now)

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("apply the rest: %v", err)
	}
	type row struct {
		Access, Status, SyncedHash string
		Kind                       *string
	}
	read := func(id string) row {
		t.Helper()
		var got row
		if err := store.pool.QueryRow(ctx, `SELECT access, status, kb_synced_hash, metadata->>'document_kind' FROM rag_documents WHERE id = $1`, id).
			Scan(&got.Access, &got.Status, &got.SyncedHash, &got.Kind); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return got
	}
	kind := func(r row) string {
		if r.Kind == nil {
			return ""
		}
		return *r.Kind
	}
	if got := read("doc_file"); got.Access != "admins" {
		t.Fatalf("an organization file document = %+v, want admins until reclassified", got)
	}
	if got := read("doc_page"); got.Access != "members" || got.Status != "ready" {
		t.Fatalf("an organization page = %+v, want members and untouched", got)
	}
	if got := read("doc_gmail"); got.Status != "pending" || got.SyncedHash != "" || kind(got) != "message" {
		t.Fatalf("a knowledge base gmail document = %+v, want requeued with its kind", got)
	}
	if got := read("doc_github"); got.Status != "pending" || got.SyncedHash != "" || kind(got) != "" {
		t.Fatalf("a knowledge base github document = %+v, want requeued", got)
	}

	// What happens afterwards: an admin reclassifies the file, and the
	// knowledge base documents are submitted under the current marker.
	exec(`UPDATE rag_documents SET access = 'members' WHERE id = 'doc_file'`)
	exec(`UPDATE rag_documents SET status = 'ready', kb_synced_hash = 'v2:synced' WHERE corpus_id = 'cor_kb'`)

	names := []string{"009_organization_ownership.sql", "010_document_acl.sql", "011_knowledge_removals.sql",
		"012_knowledge_provenance.sql", "013_kb_sync_marker.sql"}
	for range 2 {
		reapply(t, ctx, store, "rag", names...)
		if got := read("doc_file"); got.Access != "members" {
			t.Fatalf("applying again relabelled a reclassified document: %+v", got)
		}
		for _, id := range []string{"doc_gmail", "doc_github"} {
			if got := read(id); got.Status != "ready" || got.SyncedHash != "v2:synced" {
				t.Fatalf("applying again requeued %s: %+v", id, got)
			}
		}
	}
	for table, constraints := range map[string][]string{
		"rag_corpora":             {"rag_corpora_id_organization_unique"},
		"rag_documents":           {"rag_documents_id_corpus_organization_unique", "rag_documents_corpus_organization_fk", "rag_documents_access_check"},
		"rag_chunks":              {"rag_chunks_document_corpus_organization_fk"},
		"rag_knowledge_documents": {"rag_knowledge_documents_document_corpus_organization_fk"},
	} {
		for _, name := range constraints {
			var count int
			if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid = $1::regclass AND conname = $2`, table, name).Scan(&count); err != nil || count != 1 {
				t.Fatalf("%s on %s: %d, %v; want exactly one", name, table, count, err)
			}
		}
	}
	for _, index := range []string{"rag_knowledge_removals_removed_at_idx", "rag_documents_kb_unsynced_idx"} {
		var exists bool
		if err := store.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil || !exists {
			t.Fatalf("index %s = %v, %v", index, exists, err)
		}
	}
}

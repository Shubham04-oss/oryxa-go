package postgres

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The ownership keys and the Files source index are added the first time
// their migrations are applied, and applying them again — which postgreskit
// does whenever a migration's text changes — fails on nothing and leaves
// exactly one of each.
func TestOwnershipAndFilesSourceMigrationsApplyAgainCleanly(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for range 2 {
		names := []string{"012_organization_ownership.sql", "013_member_files_source_unique.sql"}
		result, err := store.pool.Exec(ctx, `UPDATE oryxa_migrations SET checksum = 'edited' WHERE service = 'knowledge' AND name = ANY($1)`, names)
		if err != nil || result.RowsAffected() != int64(len(names)) {
			t.Fatalf("mark %v edited = %v, %v", names, result.RowsAffected(), err)
		}
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("apply %v again: %v", names, err)
		}
	}
	for table, name := range map[string]string{
		"knowledge_documents": "knowledge_documents_source_organization_fk",
		"knowledge_sync_runs": "knowledge_sync_runs_source_organization_fk",
	} {
		var count int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid = $1::regclass AND conname = $2`, table, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s on %s: %d, %v; want exactly one", name, table, count, err)
		}
	}
	for index, predicate := range map[string]string{
		"knowledge_sources_live_target_unique":          "provider <> 'files'",
		"knowledge_files_sources_live_placement_unique": "provider = 'files'",
	} {
		var definition string
		if err := store.pool.QueryRow(ctx, `SELECT pg_get_indexdef(to_regclass($1))`, index).Scan(&definition); err != nil {
			t.Fatalf("index %s: %v", index, err)
		}
		if !strings.Contains(definition, predicate) {
			t.Fatalf("index %s = %s, want it to test %s", index, definition, predicate)
		}
	}
}

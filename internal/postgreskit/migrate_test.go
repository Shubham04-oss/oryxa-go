package postgreskit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// migrationDatabase opens a pool whose search_path leads with a schema the
// test creates and drops, so migrations land nowhere shared.
func migrationDatabase(t *testing.T) *Database {
	t.Helper()
	databaseURL := os.Getenv("POSTGRESKIT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("POSTGRESKIT_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, Config{URL: databaseURL, ApplicationName: "oryxa-postgreskit-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	schema := "migrate_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Pool.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	database, err := Open(ctx, Config{URL: databaseURL, ApplicationName: "oryxa-postgreskit-test", Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database
}

// counting is a migration that records every time it runs.
func counting(table string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(`
		CREATE TABLE IF NOT EXISTS ` + table + ` (ran_at timestamptz NOT NULL DEFAULT now());
		INSERT INTO ` + table + ` DEFAULT VALUES;`)}
}

func runs(t *testing.T, database *Database, table string) int {
	t.Helper()
	var count int
	if err := database.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestMigrateAppliesEachFileOnce(t *testing.T) {
	database := migrationDatabase(t)
	ctx := context.Background()
	files := fstest.MapFS{
		"migrations/001_first.sql":  counting("first_runs"),
		"migrations/002_second.sql": counting("second_runs"),
	}
	for range 3 {
		if err := database.Migrate(ctx, "test", files, "migrations/*.sql", 42); err != nil {
			t.Fatal(err)
		}
	}
	if runs(t, database, "first_runs") != 1 || runs(t, database, "second_runs") != 1 {
		t.Fatalf("runs = %d and %d, want each file applied once", runs(t, database, "first_runs"), runs(t, database, "second_runs"))
	}

	files["migrations/003_third.sql"] = counting("third_runs")
	if err := database.Migrate(ctx, "test", files, "migrations/*.sql", 42); err != nil {
		t.Fatal(err)
	}
	if runs(t, database, "third_runs") != 1 || runs(t, database, "first_runs") != 1 {
		t.Fatal("adding a file applied more than that file")
	}
}

func TestMigrateAppliesAFileAgainOnceItChanges(t *testing.T) {
	database := migrationDatabase(t)
	ctx := context.Background()
	files := fstest.MapFS{"migrations/001_first.sql": counting("first_runs")}
	if err := database.Migrate(ctx, "test", files, "migrations/*.sql", 42); err != nil {
		t.Fatal(err)
	}
	edited := counting("first_runs")
	edited.Data = append(edited.Data, []byte("\n-- edited in place\n")...)
	files["migrations/001_first.sql"] = edited
	for range 2 {
		if err := database.Migrate(ctx, "test", files, "migrations/*.sql", 42); err != nil {
			t.Fatal(err)
		}
	}
	if got := runs(t, database, "first_runs"); got != 2 {
		t.Fatalf("runs = %d, want the original once and the edit once", got)
	}
}

func TestMigrateKeepsWhatWentBeforeAFailingFile(t *testing.T) {
	database := migrationDatabase(t)
	ctx := context.Background()
	files := fstest.MapFS{
		"migrations/001_first.sql":  counting("first_runs"),
		"migrations/002_broken.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE broken (;`)},
	}
	err := database.Migrate(ctx, "test", files, "migrations/*.sql", 42)
	if err == nil || !strings.Contains(err.Error(), "002_broken.sql") {
		t.Fatalf("Migrate() error = %v, want it to name the failing file", err)
	}

	files["migrations/002_broken.sql"] = counting("second_runs")
	if err := database.Migrate(ctx, "test", files, "migrations/*.sql", 42); err != nil {
		t.Fatal(err)
	}
	if runs(t, database, "first_runs") != 1 || runs(t, database, "second_runs") != 1 {
		t.Fatal("the retry repeated the file that had already applied, or skipped the fixed one")
	}
}

func TestMigrateKeepsServicesApart(t *testing.T) {
	database := migrationDatabase(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, "billing", fstest.MapFS{"migrations/001.sql": counting("billing_runs")}, "migrations/*.sql", 42); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, "files", fstest.MapFS{"migrations/001.sql": counting("files_runs")}, "migrations/*.sql", 43); err != nil {
		t.Fatal(err)
	}
	if runs(t, database, "files_runs") != 1 {
		t.Fatal("a file named like another service's was taken for already applied")
	}
}

func TestMigrateTakesTurnsWhenStartedTogether(t *testing.T) {
	database := migrationDatabase(t)
	ctx := context.Background()
	files := fstest.MapFS{
		"migrations/001_first.sql":  counting("first_runs"),
		"migrations/002_second.sql": counting("second_runs"),
	}
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			if err := database.Migrate(ctx, "test", files, "migrations/*.sql", 42); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if runs(t, database, "first_runs") != 1 || runs(t, database, "second_runs") != 1 {
		t.Fatalf("runs = %d and %d, want each file applied once however many start together",
			runs(t, database, "first_runs"), runs(t, database, "second_runs"))
	}
}

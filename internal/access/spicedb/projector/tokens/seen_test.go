package tokens

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

func TestSeenSkipsOlderEventsAndPurges(t *testing.T) {
	url := os.Getenv("ACCESS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ACCESS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: url, ApplicationName: "oryxa-spicedb-seen-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	seen := NewSeen(database.Pool)
	pair := "team:seen_t1@member:" + time.Now().Format("150405.000000000")
	t.Cleanup(func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM access.spicedb_seen WHERE key = $1`, pair) })
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	ran := 0
	apply := func(context.Context) error { ran++; return nil }
	guard := func(at time.Time) bool {
		t.Helper()
		applied, err := seen.Guard(ctx, []string{pair}, []string{pair}, at, apply)
		if err != nil {
			t.Fatal(err)
		}
		return applied
	}

	if !guard(t0.Add(time.Minute)) {
		t.Fatal("the first event was skipped")
	}
	if guard(t0) {
		t.Fatal("an older event was applied after a newer one")
	}
	if !guard(t0.Add(time.Minute)) {
		t.Fatal("a duplicate of the newest event was skipped")
	}
	if ran != 2 {
		t.Fatalf("apply ran %d times, want 2", ran)
	}

	// A failed apply records nothing.
	if _, err := seen.Guard(ctx, []string{pair}, []string{pair}, t0.Add(time.Hour), func(context.Context) error { return context.Canceled }); err == nil {
		t.Fatal("a failing apply returned no error")
	}
	if !guard(t0.Add(2 * time.Minute)) {
		t.Fatal("a failed apply still marked its time as seen")
	}

	if _, err := seen.Purge(ctx, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !guard(t0) {
		t.Fatal("a purged key still refused an older event")
	}
}

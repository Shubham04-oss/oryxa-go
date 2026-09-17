package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testTable creates an outbox table with the shape every service's has, in a
// schema of its own that is dropped afterwards, so these tests are safe
// against a shared development database.
//
//	OUTBOX_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./internal/outbox
func testTable(t *testing.T, options ...Option) (*Table, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("OUTBOX_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("OUTBOX_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	schema := "outbox_test_" + hex.EncodeToString(suffix)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	name := schema + ".test_outbox"
	if _, err := pool.Exec(ctx, `
		CREATE TABLE `+name+` (
			id text PRIMARY KEY,
			detail_type text NOT NULL,
			detail jsonb NOT NULL,
			occurred_at timestamptz NOT NULL,
			attempts integer NOT NULL DEFAULT 0,
			available_at timestamptz NOT NULL DEFAULT now(),
			locked_until timestamptz,
			published_at timestamptz,
			last_error text,
			claim_token text
		)`); err != nil {
		t.Fatal(err)
	}
	table, err := NewTable(pool, name, options...)
	if err != nil {
		t.Fatal(err)
	}
	return table, pool
}

func insert(t *testing.T, pool *pgxpool.Pool, table *Table, id, detailType string, occurredAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO `+table.Name()+` (id, detail_type, detail, occurred_at) VALUES ($1, $2, '{"secret":"s3cr3t"}', $3)`,
		id, detailType, occurredAt); err != nil {
		t.Fatal(err)
	}
}

func ids(events []Event) []string {
	result := make([]string, 0, len(events))
	for _, event := range events {
		result = append(result, event.ID)
	}
	slices.Sort(result)
	return result
}

func TestTableClaimsOldestFirstAndSkipsLiveClaims(t *testing.T) {
	table, pool := testTable(t)
	ctx := context.Background()
	start := time.Now().Add(-time.Hour)
	for index, id := range []string{"evt_3", "evt_1", "evt_2"} {
		insert(t, pool, table, id, "test.v1", start.Add(time.Duration([]int{3, 1, 2}[index])*time.Minute))
	}

	first, err := table.Claim(ctx, 2, time.Minute, "token-a")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(first); !slices.Equal(got, []string{"evt_1", "evt_2"}) {
		t.Fatalf("first claim = %v, want the two oldest", got)
	}
	second, err := table.Claim(ctx, 10, time.Minute, "token-b")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(second); !slices.Equal(got, []string{"evt_3"}) {
		t.Fatalf("second claim = %v, want only what the first did not hold", got)
	}
}

func TestTableConcurrentClaimsNeverShareAnEvent(t *testing.T) {
	table, pool := testTable(t)
	ctx := context.Background()
	for index := range 60 {
		insert(t, pool, table, fmt.Sprintf("evt_%02d", index), "test.v1", time.Now().Add(-time.Duration(60-index)*time.Second))
	}
	var mu sync.Mutex
	seen := map[string]string{}
	var group sync.WaitGroup
	for claimer := range 4 {
		group.Go(func() {
			token := fmt.Sprintf("token-%d", claimer)
			for {
				claimed, err := table.Claim(ctx, 7, time.Minute, token)
				if err != nil {
					t.Error(err)
					return
				}
				if len(claimed) == 0 {
					return
				}
				mu.Lock()
				for _, event := range claimed {
					if owner, ok := seen[event.ID]; ok {
						t.Errorf("%s claimed by %s and %s", event.ID, owner, token)
					}
					seen[event.ID] = token
				}
				mu.Unlock()
			}
		})
	}
	group.Wait()
	if len(seen) != 60 {
		t.Fatalf("claimed %d events, want all 60", len(seen))
	}
}

func TestTableMarksOnlyUnderTheOwningToken(t *testing.T) {
	table, pool := testTable(t)
	ctx := context.Background()
	insert(t, pool, table, "evt_1", "test.v1", time.Now().Add(-time.Minute))
	if _, err := table.Claim(ctx, 1, time.Minute, "token-a"); err != nil {
		t.Fatal(err)
	}

	if marked, err := table.MarkPublished(ctx, "evt_1", "token-b"); err != nil || marked {
		t.Fatalf("MarkPublished with another token = %v, %v, want false", marked, err)
	}
	if marked, err := table.MarkFailed(ctx, "evt_1", "token-b", time.Second, "boom"); err != nil || marked {
		t.Fatalf("MarkFailed with another token = %v, %v, want false", marked, err)
	}
	if marked, err := table.MarkPublished(ctx, "evt_1", "token-a"); err != nil || !marked {
		t.Fatalf("MarkPublished with the claim's token = %v, %v, want true", marked, err)
	}
	var published bool
	var token *string
	if err := pool.QueryRow(ctx, `SELECT published_at IS NOT NULL, claim_token FROM `+table.Name()+` WHERE id = 'evt_1'`).Scan(&published, &token); err != nil {
		t.Fatal(err)
	}
	if !published || token != nil {
		t.Fatalf("published = %v, claim token = %v", published, token)
	}
}

func TestTableAnExpiredClaimIsTakenOverAndTheStaleOwnerCannotMark(t *testing.T) {
	table, pool := testTable(t)
	ctx := context.Background()
	insert(t, pool, table, "evt_1", "test.v1", time.Now().Add(-time.Minute))
	if _, err := table.Claim(ctx, 1, time.Millisecond, "stale"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	taken, err := table.Claim(ctx, 1, time.Minute, "fresh")
	if err != nil || len(taken) != 1 {
		t.Fatalf("claim after expiry = %v, %v, want the event", taken, err)
	}
	if marked, _ := table.MarkPublished(ctx, "evt_1", "stale"); marked {
		t.Fatal("the dispatcher whose claim expired marked a row another claim owns")
	}
	if marked, err := table.MarkPublished(ctx, "evt_1", "fresh"); err != nil || !marked {
		t.Fatalf("MarkPublished by the new owner = %v, %v", marked, err)
	}
}

func TestTableMarkFailedCountsAnAttemptAndWaits(t *testing.T) {
	table, pool := testTable(t)
	ctx := context.Background()
	insert(t, pool, table, "evt_1", "test.v1", time.Now().Add(-time.Minute))
	if _, err := table.Claim(ctx, 1, time.Minute, "token-a"); err != nil {
		t.Fatal(err)
	}
	if marked, err := table.MarkFailed(ctx, "evt_1", "token-a", time.Hour, strings.Repeat("x", 1500)); err != nil || !marked {
		t.Fatalf("MarkFailed = %v, %v", marked, err)
	}

	var attempts, errorLength int
	var waits bool
	if err := pool.QueryRow(ctx, `
		SELECT attempts, length(last_error), available_at > now() + interval '59 minutes'
		FROM `+table.Name()+` WHERE id = 'evt_1'`).Scan(&attempts, &errorLength, &waits); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || errorLength != 1000 || !waits {
		t.Fatalf("attempts = %d, error length = %d, waits an hour = %v", attempts, errorLength, waits)
	}
	if again, err := table.Claim(ctx, 1, time.Minute, "token-b"); err != nil || len(again) != 0 {
		t.Fatalf("claim before the retry is due = %v, %v, want nothing", again, err)
	}
}

func TestTableReleaseGivesEventsBackWithoutAnAttempt(t *testing.T) {
	table, pool := testTable(t)
	ctx := context.Background()
	insert(t, pool, table, "evt_1", "test.v1", time.Now().Add(-time.Minute))
	if _, err := table.Claim(ctx, 1, time.Minute, "token-a"); err != nil {
		t.Fatal(err)
	}
	if err := table.Release(ctx, []string{"evt_1"}, "token-b"); err != nil {
		t.Fatal(err)
	}
	if still, _ := table.Claim(ctx, 1, time.Minute, "token-c"); len(still) != 0 {
		t.Fatal("another token released a claim it did not own")
	}

	if err := table.Release(ctx, []string{"evt_1"}, "token-a"); err != nil {
		t.Fatal(err)
	}
	again, err := table.Claim(ctx, 1, time.Minute, "token-c")
	if err != nil || len(again) != 1 || again[0].Attempts != 0 {
		t.Fatalf("claim after release = %+v, %v, want the event with no attempt counted", again, err)
	}
}

func TestTableDeletesOnlyEventsPublishedBeforeTheCutoff(t *testing.T) {
	table, pool := testTable(t)
	ctx := context.Background()
	for _, id := range []string{"old_1", "old_2", "old_3", "recent", "pending"} {
		insert(t, pool, table, id, "test.v1", time.Now().Add(-10*24*time.Hour))
	}
	if _, err := pool.Exec(ctx, `UPDATE `+table.Name()+` SET published_at = now() - interval '8 days' WHERE id LIKE 'old_%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE `+table.Name()+` SET published_at = now() - interval '1 hour' WHERE id = 'recent'`); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, want := range []int{2, 1, 0} {
		deleted, err := table.DeletePublished(ctx, cutoff, 2)
		if err != nil || deleted != want {
			t.Fatalf("DeletePublished = %d, %v, want %d", deleted, err, want)
		}
	}
	var left []string
	rows, err := pool.Query(ctx, `SELECT id FROM `+table.Name()+` ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	if !slices.Equal(left, []string{"pending", "recent"}) {
		t.Fatalf("left = %v, want the unpublished and recently published events", left)
	}
}

func TestTableRedactsTheDetailOfListedTypesOnPublish(t *testing.T) {
	table, pool := testTable(t, RedactOnPublish("auth.member.invited.v1"))
	ctx := context.Background()
	insert(t, pool, table, "evt_invite", "auth.member.invited.v1", time.Now().Add(-time.Minute))
	insert(t, pool, table, "evt_other", "auth.organization.created.v1", time.Now().Add(-time.Minute))
	if _, err := table.Claim(ctx, 2, time.Minute, "token-a"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"evt_invite", "evt_other"} {
		if marked, err := table.MarkPublished(ctx, id, "token-a"); err != nil || !marked {
			t.Fatalf("MarkPublished(%s) = %v, %v", id, marked, err)
		}
	}
	details := map[string]string{}
	rows, err := pool.Query(ctx, `SELECT id, detail::text FROM `+table.Name())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, detail string
		_ = rows.Scan(&id, &detail)
		details[id] = detail
	}
	if details["evt_invite"] != "{}" || !strings.Contains(details["evt_other"], "s3cr3t") {
		t.Fatalf("details = %v, want only the invitation blanked", details)
	}
}

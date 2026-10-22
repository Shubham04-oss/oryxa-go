package jobs

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// The tests need a Postgres:
//
//	INTEGRATIONS_TEST_DATABASE_URL='postgres://oryxa:oryxa@localhost:5432/oryxa?sslmode=disable' go test ./integrations/internal/jobs

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func setup(t *testing.T) (*pgxpool.Pool, *Queue, *clock) {
	t.Helper()
	url := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: url, ApplicationName: "oryxa-integrations-jobs-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx, "integrations", os.DirFS("../.."), "migrations/*.sql", 7263511100); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DELETE FROM integrations.jobs`,
		`DELETE FROM integrations.stream_state WHERE connection_id LIKE 'jt_%'`,
		`DELETE FROM integrations.connections WHERE id LIKE 'jt_%'`,
		`DELETE FROM integrations.org_apps WHERE id = 'jt_app'`,
	} {
		if _, err := database.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	clk := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	return database.Pool, NewQueue(database.Pool, Config{Now: clk.Now, Jitter: func() float64 { return 1 }}), clk
}

func mustEnqueue(t *testing.T, q *Queue, j Job) string {
	t.Helper()
	id, added, err := q.Enqueue(context.Background(), j)
	if err != nil || !added {
		t.Fatalf("enqueue %+v: added=%v err=%v", j, added, err)
	}
	return id
}

func claim(t *testing.T, q *Queue, owner string) *Job {
	t.Helper()
	j, err := q.Claim(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestPriorityOrder(t *testing.T) {
	_, q, _ := setup(t)
	for _, kind := range []string{Reconcile, Slice, Renew, Sync, Refetch, Refresh} {
		mustEnqueue(t, q, Job{Org: "o1", Kind: kind, Connection: "jt_" + kind})
	}
	var got []string
	for j := claim(t, q, "w"); j != nil; j = claim(t, q, "w") {
		got = append(got, j.Kind)
	}
	want := []string{Refresh, Refetch, Sync, Renew, Slice, Reconcile}
	if len(got) != len(want) {
		t.Fatalf("claimed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("claimed %v, want %v", got, want)
		}
	}
}

func TestRunAfterIsHonoured(t *testing.T) {
	_, q, clk := setup(t)
	mustEnqueue(t, q, Job{Org: "o1", Kind: Sync, Connection: "jt_c", Stream: "s", RunAfter: clk.Now().Add(time.Minute)})
	if j := claim(t, q, "w"); j != nil {
		t.Fatalf("claimed a job before its run_after: %+v", j)
	}
	clk.Advance(time.Minute)
	if claim(t, q, "w") == nil {
		t.Fatal("job not claimed once due")
	}
}

func TestExclusivityPerConnectionStream(t *testing.T) {
	_, q, _ := setup(t)
	mustEnqueue(t, q, Job{Org: "o1", Kind: Sync, Connection: "jt_c", Stream: "issues"})
	mustEnqueue(t, q, Job{Org: "o1", Kind: Slice, Connection: "jt_c", Stream: "issues"})
	mustEnqueue(t, q, Job{Org: "o1", Kind: Slice, Connection: "jt_c", Stream: "prs"})
	first := claim(t, q, "w1")
	second := claim(t, q, "w2")
	if first == nil || second == nil || first.Stream == second.Stream {
		t.Fatalf("want one job per stream, got %+v and %+v", first, second)
	}
	if third := claim(t, q, "w3"); third != nil {
		t.Fatalf("a second job on a running (connection, stream) was claimed: %+v", third)
	}
	if err := q.Complete(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if again := claim(t, q, "w3"); again == nil || again.Stream != first.Stream {
		t.Fatalf("the waiting job was not released: %+v", again)
	}
}

func TestConcurrentClaimsNeverShareAStream(t *testing.T) {
	_, q, _ := setup(t)
	for range 6 {
		mustEnqueue(t, q, Job{Org: "o1", Kind: Slice, Connection: "jt_c", Stream: "issues"})
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := 0
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := q.Claim(context.Background(), string(rune('a'+i)))
			if err != nil {
				t.Error(err)
				return
			}
			if j != nil {
				mu.Lock()
				claimed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if claimed != 1 {
		t.Fatalf("%d jobs on one stream were running at once, want 1", claimed)
	}
}

func TestLeaseExpiryReclaim(t *testing.T) {
	_, q, clk := setup(t)
	mustEnqueue(t, q, Job{Org: "o1", Kind: Sync, Connection: "jt_c", Stream: "s"})
	dead := claim(t, q, "dead")
	if dead == nil || dead.Attempts != 1 {
		t.Fatalf("first claim: %+v", dead)
	}
	clk.Advance(Lease - time.Second)
	if j := claim(t, q, "other"); j != nil {
		t.Fatalf("job claimed while its lease was live: %+v", j)
	}
	// A heartbeat keeps it.
	if err := q.Heartbeat(context.Background(), dead); err != nil {
		t.Fatal(err)
	}
	clk.Advance(Lease - time.Second)
	if j := claim(t, q, "other"); j != nil {
		t.Fatalf("job claimed after a heartbeat: %+v", j)
	}
	clk.Advance(2 * time.Second)
	got := claim(t, q, "other")
	if got == nil || got.ID != dead.ID || got.Attempts != 2 || got.Owner != "other" {
		t.Fatalf("expired lease not reclaimed: %+v", got)
	}
	// The old owner has lost it, for every call.
	if err := q.Heartbeat(context.Background(), dead); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("heartbeat by the old owner: %v", err)
	}
	if err := q.Complete(context.Background(), dead); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("complete by the old owner: %v", err)
	}
	if _, err := q.Fail(context.Background(), dead, errors.New("x")); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("fail by the old owner: %v", err)
	}
}

func TestFairnessAcrossOrgs(t *testing.T) {
	_, q, clk := setup(t)
	for i := range 3 {
		mustEnqueue(t, q, Job{Org: "big", Kind: Sync, Connection: "jt_big", Stream: string(rune('a' + i))})
	}
	mustEnqueue(t, q, Job{Org: "small", Kind: Sync, Connection: "jt_small", Stream: "a"})
	mustEnqueue(t, q, Job{Org: "small", Kind: Sync, Connection: "jt_small", Stream: "b"})
	var orgs []string
	for j := claim(t, q, "w"); j != nil; j = claim(t, q, "w") {
		orgs = append(orgs, j.Org)
		clk.Advance(time.Second)
		if err := q.Complete(context.Background(), j); err != nil {
			t.Fatal(err)
		}
	}
	// Whichever org goes first, they then alternate until small runs out,
	// rather than big's backlog running to the end first.
	if len(orgs) != 5 {
		t.Fatalf("order %v", orgs)
	}
	if orgs[0] == orgs[1] || orgs[1] == orgs[2] || orgs[2] == orgs[3] {
		t.Fatalf("order %v: the orgs did not alternate", orgs)
	}
}

func TestFairnessDoesNotOutrankPriority(t *testing.T) {
	_, q, _ := setup(t)
	mustEnqueue(t, q, Job{Org: "a", Kind: Sync, Connection: "jt_a", Stream: "s"})
	j := claim(t, q, "w")
	if err := q.Complete(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, q, Job{Org: "b", Kind: Reconcile, Connection: "jt_b"})
	mustEnqueue(t, q, Job{Org: "a", Kind: Refresh, Connection: "jt_a"})
	if got := claim(t, q, "w"); got == nil || got.Kind != Refresh {
		t.Fatalf("a refresh from the org served most recently lost to a reconcile: %+v", got)
	}
}

func TestRetryBackoffAndCap(t *testing.T) {
	pool, q, clk := setup(t)
	ctx := context.Background()
	mustExec(t, pool, `INSERT INTO integrations.org_apps (id, org_id, app, kind, status, created_at, updated_at)
		VALUES ('jt_app', 'o1', 'github', 'oauth_client', 'ok', now(), now())`)
	mustExec(t, pool, `INSERT INTO integrations.connections (id, org_id, member_id, level, app, org_app_id, mode, status, created_at, updated_at)
		VALUES ('jt_c', 'o1', 'm1', 'personal', 'github', 'jt_app', 'byo', 'ok', now(), now())`)
	mustExec(t, pool, `INSERT INTO integrations.stream_state (connection_id, stream, next_run_at, status)
		VALUES ('jt_c', 'issues', now(), 'active')`)
	id := mustEnqueue(t, q, Job{Org: "o1", Kind: Sync, Connection: "jt_c", Stream: "issues"})

	wantWait := 5 * time.Second
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		j := claim(t, q, "w")
		if j == nil || j.ID != id || j.Attempts != attempt {
			t.Fatalf("attempt %d: claimed %+v", attempt, j)
		}
		retry, err := q.Fail(ctx, j, errors.New("boom"))
		if err != nil {
			t.Fatal(err)
		}
		if attempt == MaxAttempts {
			if retry {
				t.Fatal("job retried past the cap")
			}
			break
		}
		if !retry {
			t.Fatalf("attempt %d: not retried", attempt)
		}
		if claim(t, q, "w") != nil {
			t.Fatalf("attempt %d: job claimed before its backoff", attempt)
		}
		clk.Advance(wantWait - time.Millisecond)
		if claim(t, q, "w") != nil {
			t.Fatalf("attempt %d: job claimed %v in, backoff is %v", attempt, wantWait-time.Millisecond, wantWait)
		}
		clk.Advance(time.Millisecond)
		wantWait *= 2
	}
	var status, lastErr, streamStatus string
	if err := pool.QueryRow(ctx, `SELECT status, last_error FROM integrations.jobs WHERE id = $1`, id).Scan(&status, &lastErr); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || lastErr != "boom" {
		t.Errorf("job is %q with error %q", status, lastErr)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM integrations.stream_state WHERE connection_id = 'jt_c'`).Scan(&streamStatus); err != nil {
		t.Fatal(err)
	}
	if streamStatus != "degraded" {
		t.Errorf("stream is %q, want degraded", streamStatus)
	}
	clk.Advance(48 * time.Hour)
	if j := claim(t, q, "w"); j != nil {
		t.Fatalf("a failed job was claimed: %+v", j)
	}
}

func TestExpiredLeaseOnLastAttemptFails(t *testing.T) {
	pool, q, clk := setup(t)
	mustEnqueue(t, q, Job{Org: "o1", Kind: Refresh, Connection: "jt_c"})
	mustExec(t, pool, `UPDATE integrations.jobs SET attempts = $1`, MaxAttempts-1)
	if claim(t, q, "dies") == nil {
		t.Fatal("not claimed")
	}
	clk.Advance(2 * Lease)
	if j := claim(t, q, "w"); j != nil {
		t.Fatalf("a job out of attempts was handed back: %+v", j)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM integrations.jobs`).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("status %q, err %v", status, err)
	}
}

func TestDedupeKey(t *testing.T) {
	_, q, _ := setup(t)
	ctx := context.Background()
	mustEnqueue(t, q, Job{Org: "o1", Kind: Sync, Connection: "jt_c", Stream: "s", DedupeKey: "k"})
	if _, added, err := q.Enqueue(ctx, Job{Org: "o1", Kind: Sync, Connection: "jt_c", Stream: "s", DedupeKey: "k"}); err != nil || added {
		t.Fatalf("duplicate: added=%v err=%v", added, err)
	}
	j := claim(t, q, "w")
	if _, added, _ := q.Enqueue(ctx, Job{Org: "o1", Kind: Sync, Connection: "jt_c", Stream: "s", DedupeKey: "k"}); added {
		t.Fatal("a job was queued while one with its key was running")
	}
	if err := q.Complete(ctx, j); err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, q, Job{Org: "o1", Kind: Sync, Connection: "jt_c", Stream: "s", DedupeKey: "k"})
}

func TestEnqueueRefusesBadJobs(t *testing.T) {
	_, q, _ := setup(t)
	for name, j := range map[string]Job{
		"unknown kind":  {Org: "o", Kind: "nope", Connection: "c"},
		"no org":        {Kind: Sync, Connection: "c"},
		"no connection": {Org: "o", Kind: Sync},
		"bad payload":   {Org: "o", Kind: Sync, Connection: "c", Payload: []byte("{")},
	} {
		if _, _, err := q.Enqueue(context.Background(), j); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := q.Claim(context.Background(), ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("claim without owner: %v", err)
	}
}

func TestWorkerRunOne(t *testing.T) {
	_, q, _ := setup(t)
	mustEnqueue(t, q, Job{Org: "o1", Kind: Refresh, Connection: "jt_c1"})
	mustEnqueue(t, q, Job{Org: "o1", Kind: Revoke, Connection: "jt_c2"})
	mustEnqueue(t, q, Job{Org: "o1", Kind: Slice, Connection: "jt_c3", Stream: "s"})
	var seen []string
	w := NewWorker(q, "w1", map[string]Handler{
		Refresh: func(_ context.Context, j *Job) error { seen = append(seen, j.Kind); return nil },
		Revoke:  func(context.Context, *Job) error { panic("boom") },
	}, nil)
	for range 3 {
		if ran, err := w.RunOne(context.Background()); !ran || err != nil {
			t.Fatalf("ran=%v err=%v", ran, err)
		}
	}
	if ran, _ := w.RunOne(context.Background()); ran {
		t.Fatal("ran a job from an empty queue")
	}
	if len(seen) != 1 {
		t.Errorf("handlers saw %v", seen)
	}
	rows, err := q.pool.Query(context.Background(), `SELECT kind, status, attempts, last_error FROM integrations.jobs ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][3]any{}
	for rows.Next() {
		var kind, status, msg string
		var attempts int
		if err := rows.Scan(&kind, &status, &attempts, &msg); err != nil {
			t.Fatal(err)
		}
		got[kind] = [3]any{status, attempts, msg}
	}
	if g := got[Refresh]; g[0] != "done" {
		t.Errorf("refresh: %v", g)
	}
	if g := got[Revoke]; g[0] != "queued" || g[2] != "handler panicked: boom" {
		t.Errorf("revoke (panic): %v", g)
	}
	if g := got[Slice]; g[0] != "queued" || g[2] != "no handler for slice jobs" {
		t.Errorf("slice (no handler): %v", g)
	}
}

func TestParseSchedule(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"rate(1 minute)": time.Minute, "rate(15 minutes)": 15 * time.Minute, "rate(2 hours)": 2 * time.Hour, "rate(1 day)": 24 * time.Hour,
	} {
		if got, err := ParseSchedule(in); err != nil || got != want {
			t.Errorf("%q: %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "rate(0 minutes)", "rate(5 seconds)", "cron(* * * * *)", "rate(15 minutes) ", "rate(99999999999 days)"} {
		if _, err := ParseSchedule(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

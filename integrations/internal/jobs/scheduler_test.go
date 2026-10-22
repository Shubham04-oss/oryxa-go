package jobs

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func seedStream(t *testing.T, pool *pgxpool.Pool, conn, status, app, stream, partition string, due time.Time, streamStatus string) {
	t.Helper()
	mustExec(t, pool, `INSERT INTO integrations.org_apps (id, org_id, app, kind, status, created_at, updated_at)
		VALUES ('jt_app', 'o1', 'github', 'oauth_client', 'ok', now(), now()) ON CONFLICT DO NOTHING`)
	mustExec(t, pool, `INSERT INTO integrations.connections (id, org_id, member_id, level, app, org_app_id, mode, status, created_at, updated_at)
		VALUES ($1, 'o1', $2, 'personal', $3, 'jt_app', 'byo', $4, now(), now()) ON CONFLICT DO NOTHING`, conn, conn, app, status)
	mustExec(t, pool, `INSERT INTO integrations.stream_state (connection_id, stream, partition, next_run_at, status)
		VALUES ($1, $2, $3, $4, $5)`, conn, stream, partition, due, streamStatus)
}

func TestSchedulerEnqueuesDueStreamsOnce(t *testing.T) {
	pool, q, clk := setup(t)
	ctx := context.Background()
	now := clk.Now()
	seedStream(t, pool, "jt_due", "ok", "github", "issues", "repoA", now.Add(-time.Minute), "active")
	seedStream(t, pool, "jt_due", "ok", "github", "issues", "repoB", now.Add(-time.Minute), "active")
	seedStream(t, pool, "jt_later", "ok", "github", "issues", "", now.Add(time.Minute), "active")
	seedStream(t, pool, "jt_paused", "ok", "github", "issues", "", now.Add(-time.Minute), "paused")
	seedStream(t, pool, "jt_revoked", "revoked", "github", "issues", "", now.Add(-time.Minute), "active")
	seedStream(t, pool, "jt_reauth", "needs_reauth", "github", "issues", "", now.Add(-time.Minute), "active")
	seedStream(t, pool, "jt_unknown", "ok", "other", "issues", "", now.Add(-time.Minute), "active")

	s := NewScheduler(pool, SchedulerConfig{
		Now: clk.Now,
		Interval: func(app, stream string) (time.Duration, bool) {
			return 15 * time.Minute, app == "github" && stream == "issues"
		},
	})
	if n, err := s.Tick(ctx); err != nil || n != 2 {
		t.Fatalf("tick: %d, %v", n, err)
	}
	// A second tick finds nothing due: next_run_at moved on.
	if n, err := s.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("second tick: %d, %v", n, err)
	}
	var next time.Time
	if err := pool.QueryRow(ctx, `SELECT next_run_at FROM integrations.stream_state WHERE connection_id = 'jt_due' AND partition = 'repoA'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if !next.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("next_run_at %v, want %v", next, now.Add(15*time.Minute))
	}

	// The jobs carry org, connection, stream and partition. Both partitions
	// share a stream, so they run one at a time.
	partitions := map[string]bool{}
	for range 2 {
		j := claim(t, q, "w")
		if j == nil {
			t.Fatal("job missing")
		}
		if j.Kind != Sync || j.Org != "o1" || j.Connection != "jt_due" || j.Stream != "issues" {
			t.Fatalf("job %+v", j)
		}
		var p struct{ Partition string }
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			t.Fatal(err)
		}
		partitions[p.Partition] = true
		if err := q.Complete(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if !partitions["repoA"] || !partitions["repoB"] {
		t.Errorf("partitions %v", partitions)
	}

	// Due again, with a sync for each partition queued again by this tick and
	// then not twice by the next.
	clk.Advance(20 * time.Minute)
	if n, err := s.Tick(ctx); err != nil || n != 3 {
		t.Fatalf("tick after the interval: %d, %v", n, err)
	}
	clk.Advance(20 * time.Minute)
	if n, err := s.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("tick while syncs are still queued: %d, %v", n, err)
	}
}

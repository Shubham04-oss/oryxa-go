package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TickEvery is how often the scheduler looks for due streams (§10.2).
const TickEvery = 30 * time.Second

var scheduleRE = regexp.MustCompile(`^rate\(([1-9][0-9]*) (minutes?|hours?|days?)\)$`)

// ParseSchedule reads a manifest stream schedule such as "rate(15 minutes)".
func ParseSchedule(s string) (time.Duration, error) {
	m := scheduleRE.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("jobs: schedule %q must look like rate(15 minutes)", s)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > 1<<20 {
		return 0, fmt.Errorf("jobs: schedule %q is out of range", s)
	}
	unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour}[m[2][0]]
	return time.Duration(n) * unit, nil
}

// SchedulerConfig wires a Scheduler.
type SchedulerConfig struct {
	// Interval answers how often an app's stream runs. False means the app or
	// stream has no schedule (or no connector): the stream is left alone.
	Interval func(app, stream string) (time.Duration, bool)
	// Batch is the most streams one tick enqueues; zero means 200.
	Batch  int
	Logger *slog.Logger
	Now    func() time.Time
}

// Scheduler turns due stream_state rows into sync jobs.
type Scheduler struct {
	pool   *pgxpool.Pool
	config SchedulerConfig
}

// NewScheduler returns a Scheduler.
func NewScheduler(pool *pgxpool.Pool, config SchedulerConfig) *Scheduler {
	if config.Batch <= 0 {
		config.Batch = 200
	}
	if config.Logger == nil {
		config.Logger = slog.New(slog.DiscardHandler)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Scheduler{pool: pool, config: config}
}

// Tick enqueues a sync for every active stream whose next_run_at has passed
// and moves next_run_at one interval on, in one transaction, so a crash
// between the two neither loses nor doubles a run. Revoked connections are
// skipped. It returns how many jobs it added. Several tasks can tick at
// once: rows another tick holds are skipped, and the dedupe key keeps a
// stream with a sync still queued or running from being queued again.
func (s *Scheduler) Tick(ctx context.Context) (int, error) {
	now := s.config.Now().UTC().Truncate(time.Microsecond)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT s.connection_id, s.stream, s.partition, c.org_id, c.app
		FROM integrations.stream_state s
		JOIN integrations.connections c ON c.id = s.connection_id AND c.status NOT IN ('revoked', 'needs_reauth')
		WHERE s.status = 'active' AND s.next_run_at <= $1
		ORDER BY s.next_run_at
		LIMIT $2
		FOR UPDATE OF s SKIP LOCKED`, now, s.config.Batch)
	if err != nil {
		return 0, err
	}
	type due struct{ connection, stream, partition, org, app string }
	var dues []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.connection, &d.stream, &d.partition, &d.org, &d.app); err != nil {
			return 0, err
		}
		dues = append(dues, d)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	added := 0
	for _, d := range dues {
		interval, ok := s.config.Interval(d.app, d.stream)
		if !ok || interval <= 0 {
			continue
		}
		payload := fmt.Sprintf(`{"partition":%s}`, strconv.Quote(d.partition))
		_, isNew, err := enqueue(ctx, tx, Job{
			Org: d.org, Kind: Sync, Connection: d.connection, Stream: d.stream, Payload: []byte(payload),
			DedupeKey: "sync:" + d.connection + ":" + d.stream + ":" + d.partition,
		}, now)
		if err != nil {
			return 0, err
		}
		if isNew {
			added++
		}
		if _, err := tx.Exec(ctx, `UPDATE integrations.stream_state SET next_run_at = $4
			WHERE connection_id = $1 AND stream = $2 AND partition = $3`,
			d.connection, d.stream, d.partition, now.Add(interval)); err != nil {
			return 0, err
		}
	}
	return added, tx.Commit(ctx)
}

// Run ticks every TickEvery until ctx ends. A failed tick is logged and
// tried again at the next one.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(TickEvery)
	defer t.Stop()
	for {
		if n, err := s.Tick(ctx); err != nil {
			s.config.Logger.Error("integrations scheduler tick failed", "error", err)
		} else if n > 0 {
			s.config.Logger.Debug("integrations scheduler queued syncs", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

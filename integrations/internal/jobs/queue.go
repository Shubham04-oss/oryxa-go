// Package jobs is the integrations work queue and the scheduler that fills it
// (design §10.1 and §10.2). Jobs live in Postgres; workers claim with
// FOR UPDATE SKIP LOCKED under a lease, so a worker that dies hands its job
// back when the lease runs out.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// Lease is how long a claimed job is the worker's without a heartbeat.
	Lease = 60 * time.Second
	// HeartbeatEvery is how often a worker extends its lease.
	HeartbeatEvery = 20 * time.Second
	// MaxAttempts is how many times a job is claimed before it is failed.
	MaxAttempts = 8

	maxBackoff = 30 * time.Minute
	// claimTries bounds the retries when two workers race for the same
	// (connection, stream): the loser sees the winner on its next try.
	claimTries = 3
)

// Kinds, from the design's table. The priority order is refresh first and
// reconcile last (§10.1).
const (
	Refresh   = "refresh"
	Refetch   = "refetch"
	Sync      = "sync"
	Poll      = "poll"
	Renew     = "renew"
	Slice     = "slice"
	Reconcile = "reconcile"
	Revoke    = "revoke"
)

var priorities = map[string]int16{Refresh: 0, Revoke: 1, Refetch: 2, Sync: 3, Poll: 3, Renew: 4, Slice: 5, Reconcile: 6}

// Priority is a kind's rank; lower runs first. It is 0 and false for an
// unknown kind.
func Priority(kind string) (int, bool) {
	p, ok := priorities[kind]
	return int(p), ok
}

var (
	// ErrInvalid means a job is malformed; the error says which field.
	ErrInvalid = errors.New("jobs: invalid job")
	// ErrLeaseLost means the job is no longer this worker's: its lease ran out
	// and it was handed back or taken.
	ErrLeaseLost = errors.New("jobs: lease lost")
)

// Job is a unit of work. Org, Kind and Connection are required.
type Job struct {
	ID         string
	Org        string
	Kind       string
	Connection string
	// Stream is empty for work on the connection itself.
	Stream  string
	Payload json.RawMessage
	// DedupeKey, when set, keeps a second job with the same key from being
	// queued while one is queued or running.
	DedupeKey string
	// RunAfter is the earliest start; zero means now.
	RunAfter time.Time
	// Attempts is how many times it has been claimed, this claim included.
	Attempts int
	// Owner is the worker that holds it (set by Claim).
	Owner string
}

// Config tunes a Queue.
type Config struct {
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Jitter returns a factor in [0.5, 1.5) that spreads retries; nil is random.
	Jitter func() float64
}

// Queue is the jobs table.
type Queue struct {
	pool   *pgxpool.Pool
	config Config
}

// NewQueue returns a Queue on the integrations database.
func NewQueue(pool *pgxpool.Pool, config Config) *Queue {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Jitter == nil {
		config.Jitter = func() float64 { return 0.5 + mrand.Float64() }
	}
	return &Queue{pool: pool, config: config}
}

func (q *Queue) now() time.Time { return q.config.Now().UTC().Truncate(time.Microsecond) }

// Enqueue adds a job and returns its id. A job whose DedupeKey is held by a
// queued or running job is not added: it returns that the job was a
// duplicate, and no error.
func (q *Queue) Enqueue(ctx context.Context, j Job) (id string, added bool, err error) {
	return enqueue(ctx, q.pool, j, q.now())
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func enqueue(ctx context.Context, db execer, j Job, now time.Time) (string, bool, error) {
	priority, ok := Priority(j.Kind)
	switch {
	case !ok:
		return "", false, fmt.Errorf("%w: kind %q", ErrInvalid, j.Kind)
	case j.Org == "" || j.Connection == "":
		return "", false, fmt.Errorf("%w: org and connection are required", ErrInvalid)
	}
	payload := j.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	} else if !json.Valid(payload) {
		return "", false, fmt.Errorf("%w: payload is not JSON", ErrInvalid)
	}
	runAfter := j.RunAfter
	if runAfter.IsZero() {
		runAfter = now
	}
	id, err := newID()
	if err != nil {
		return "", false, err
	}
	tag, err := db.Exec(ctx, `INSERT INTO integrations.jobs
		(id, org_id, kind, connection_id, stream, payload, priority, dedupe_key, run_after, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'queued', $10, $10)
		ON CONFLICT (dedupe_key) WHERE dedupe_key <> '' AND status IN ('queued', 'running') DO NOTHING`,
		id, j.Org, j.Kind, j.Connection, j.Stream, payload, priority, j.DedupeKey, runAfter.UTC(), now)
	if err != nil {
		return "", false, err
	}
	return id, tag.RowsAffected() == 1, nil
}

// Claim leases the next job to owner, or returns nil when none is ready.
//
// Order: lowest priority number first; within one, the org served least
// recently first (so one org's backlog does not starve another), then the
// job that has waited longest. A job whose (connection, stream) has another
// running job is skipped. Jobs whose lease ran out are handed back first, or
// failed if they have used every attempt.
func (q *Queue) Claim(ctx context.Context, owner string) (*Job, error) {
	if owner == "" {
		return nil, fmt.Errorf("%w: owner is required", ErrInvalid)
	}
	var lastErr error
	for range claimTries {
		j, err := q.claimOnce(ctx, owner)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			lastErr = err
			continue
		}
		return j, err
	}
	return nil, lastErr
}

func (q *Queue) claimOnce(ctx context.Context, owner string) (*Job, error) {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	now := q.now()
	if err := reclaim(ctx, tx, now); err != nil {
		return nil, err
	}
	var j Job
	var payload []byte
	// ponytail: the org's last claim is a subquery per candidate; fine at the
	// queue depths of one task, to be replaced by a per-org cursor row if
	// the queue grows past thousands of ready jobs.
	err = tx.QueryRow(ctx, `
		WITH next AS (
			SELECT j.id FROM integrations.jobs j
			WHERE j.status = 'queued' AND j.run_after <= $1
			  AND NOT EXISTS (SELECT 1 FROM integrations.jobs r
			                  WHERE r.status = 'running' AND r.connection_id = j.connection_id AND r.stream = j.stream)
			ORDER BY j.priority,
			         (SELECT max(o.claimed_at) FROM integrations.jobs o WHERE o.org_id = j.org_id) NULLS FIRST,
			         j.run_after, j.id
			LIMIT 1
			FOR UPDATE OF j SKIP LOCKED)
		UPDATE integrations.jobs SET status = 'running', lease_owner = $2, lease_until = $3,
		       attempts = attempts + 1, claimed_at = $1, updated_at = $1
		FROM next WHERE integrations.jobs.id = next.id
		RETURNING integrations.jobs.id, org_id, kind, connection_id, stream, payload, dedupe_key, run_after, attempts`,
		now, owner, now.Add(Lease)).
		Scan(&j.ID, &j.Org, &j.Kind, &j.Connection, &j.Stream, &payload, &j.DedupeKey, &j.RunAfter, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	j.Payload, j.Owner = payload, owner
	return &j, tx.Commit(ctx)
}

// reclaim returns running jobs whose lease ran out to the queue, or fails
// them if they have no attempts left. Doing it at claim time means no
// separate reaper has to run.
func reclaim(ctx context.Context, tx pgx.Tx, now time.Time) error {
	rows, err := tx.Query(ctx, `
		UPDATE integrations.jobs SET
		    status = CASE WHEN attempts >= $2 THEN 'failed' ELSE 'queued' END,
		    last_error = 'lease expired', lease_owner = NULL, lease_until = NULL, updated_at = $1
		WHERE status = 'running' AND lease_until < $1
		RETURNING connection_id, stream, status`, now, MaxAttempts)
	if err != nil {
		return err
	}
	type key struct{ connection, stream string }
	var failed []key
	for rows.Next() {
		var k key
		var status string
		if err := rows.Scan(&k.connection, &k.stream, &status); err != nil {
			return err
		}
		if status == "failed" {
			failed = append(failed, k)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range failed {
		if err := degrade(ctx, tx, k.connection, k.stream, now); err != nil {
			return err
		}
	}
	return nil
}

// degrade marks what a failed job was working on: its stream, or the
// connection when the job had none.
func degrade(ctx context.Context, db execer, connection, stream string, now time.Time) error {
	if stream != "" {
		_, err := db.Exec(ctx, `UPDATE integrations.stream_state SET status = 'degraded'
			WHERE connection_id = $1 AND stream = $2 AND status = 'active'`, connection, stream)
		return err
	}
	_, err := db.Exec(ctx, `UPDATE integrations.connections SET status = 'degraded', updated_at = $2
		WHERE id = $1 AND status = 'ok'`, connection, now)
	return err
}

// Heartbeat extends the lease of a job the worker holds. ErrLeaseLost means
// it no longer does and should stop.
func (q *Queue) Heartbeat(ctx context.Context, j *Job) error {
	now := q.now()
	tag, err := q.pool.Exec(ctx, `UPDATE integrations.jobs SET lease_until = $3, updated_at = $4
		WHERE id = $1 AND lease_owner = $2 AND status = 'running' AND lease_until >= $4`,
		j.ID, j.Owner, now.Add(Lease), now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Complete marks a job the worker holds done.
func (q *Queue) Complete(ctx context.Context, j *Job) error {
	now := q.now()
	tag, err := q.pool.Exec(ctx, `UPDATE integrations.jobs SET status = 'done', lease_owner = NULL, lease_until = NULL, updated_at = $3
		WHERE id = $1 AND lease_owner = $2 AND status = 'running'`, j.ID, j.Owner, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Fail records a failed attempt. The job goes back to the queue after a
// backoff, or becomes failed (and degrades its stream or connection) when it
// has used MaxAttempts. It reports whether it will be tried again.
func (q *Queue) Fail(ctx context.Context, j *Job, cause error) (retry bool, err error) {
	now := q.now()
	msg := ""
	if cause != nil {
		msg = truncate(cause.Error(), 500)
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var attempts int
	err = tx.QueryRow(ctx, `SELECT attempts FROM integrations.jobs
		WHERE id = $1 AND lease_owner = $2 AND status = 'running' FOR UPDATE`, j.ID, j.Owner).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrLeaseLost
	}
	if err != nil {
		return false, err
	}
	retry = attempts < MaxAttempts
	if retry {
		_, err = tx.Exec(ctx, `UPDATE integrations.jobs SET status = 'queued', run_after = $2, last_error = $3,
			lease_owner = NULL, lease_until = NULL, updated_at = $4 WHERE id = $1`,
			j.ID, now.Add(q.backoff(attempts)), msg, now)
	} else {
		_, err = tx.Exec(ctx, `UPDATE integrations.jobs SET status = 'failed', last_error = $2,
			lease_owner = NULL, lease_until = NULL, updated_at = $3 WHERE id = $1`, j.ID, msg, now)
		if err == nil {
			err = degrade(ctx, tx, j.Connection, j.Stream, now)
		}
	}
	if err != nil {
		return false, err
	}
	return retry, tx.Commit(ctx)
}

// backoff is 5 s doubled per attempt already made, capped, spread by jitter.
func (q *Queue) backoff(attempts int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempts && d < maxBackoff; i++ {
		d *= 2
	}
	d = time.Duration(float64(d) * q.config.Jitter())
	return min(d, maxBackoff)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cutting mid-rune leaves invalid UTF-8, which Postgres refuses for text.
	return strings.ToValidUTF8(s[:n], "")
}

func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "job_" + hex.EncodeToString(b[:]), nil
}

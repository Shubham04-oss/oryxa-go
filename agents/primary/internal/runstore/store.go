// Package runstore keeps a record of every agent run in
// agents_primary.agent_runs: who started it, in which conversation, how it
// ended and what it cost. It is also what keeps a second run off a
// conversation while one is live, across every replica: a running run holds
// a lease on its conversation, renewed while it runs, and a run whose
// replica died stops holding it once the lease runs out.
//
// Finishing a run records its outcome and the event announcing it in one
// transaction, so the event is published exactly when the outcome is kept.
package runstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Replicas that start together must not install the schema and its indexes
// at the same time; IF NOT EXISTS alone does not serialize catalog changes.
// Other services use 7263510400 through 7263510600 for the same purpose.
const migrationsAdvisoryLock int64 = 7263510700

// oneLivePerSessionConstraint names the partial unique index Start relies on
// to keep a second run off a conversation. It is checked by name so that an
// ordinary duplicate primary key, which hits agent_runs_pkey instead, is
// never mistaken for ErrRunInProgress.
const oneLivePerSessionConstraint = "agent_runs_one_live_per_session"

// ErrRunInProgress reports that the conversation already has a live run.
var ErrRunInProgress = errors.New("another run is in progress in this conversation")

// ErrNotRunning reports that the run is not running any more: it finished,
// or it was never started.
var ErrNotRunning = errors.New("the run is not running")

// The states a run moves through. A run starts running and ends completed or
// failed; one whose lease ran out before it finished is abandoned by the next
// run to start in its conversation.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusAbandoned = "abandoned"
)

// Run is a run about to start.
type Run struct {
	ID             string
	SessionID      string
	UserID         string
	OrganizationID string
	// StartedBy is who started it, such as agentsv1.StartedByMember.
	StartedBy string
}

// Outcome is how a run ended.
type Outcome struct {
	// Status is StatusCompleted or StatusFailed.
	Status string
	// Error says why a failed run failed, in words fit to keep.
	Error string
	// Usage and CostMicros are what the run spent, across every model.
	Usage      agentsv1.Usage
	CostMicros int64
}

// Store is the runs table.
type Store struct {
	database *postgreskit.Database
}

// New returns the store over database's pool. Its tables are named with
// their schema, so the pool's search_path does not matter.
func New(database *postgreskit.Database) *Store {
	return &Store{database: database}
}

// Migrate installs the runs table. Running it twice is harmless.
func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agents", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Start records run as running, holding its conversation for lease. It
// answers ErrRunInProgress when another run holds the conversation.
func (store *Store) Start(ctx context.Context, run Run, lease time.Duration) error {
	if err := validateRun(run); err != nil {
		return err
	}
	if lease <= 0 {
		return errors.New("a run's lease must be a positive duration")
	}

	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		// A replica that dies mid-run stops renewing its lease; rather than
		// waiting on a separate sweeper, the next Start on the same
		// conversation is what notices the lapsed lease and abandons that row.
		if _, err := tx.Exec(ctx, `
			UPDATE agents_primary.agent_runs
			SET status = 'abandoned', finished_at = now(), lease_until = NULL,
			    error = 'the run stopped without finishing'
			WHERE session_id = $1 AND status = 'running' AND lease_until < now()`,
			run.SessionID); err != nil {
			return fmt.Errorf("abandon expired runs: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO agents_primary.agent_runs
				(id, session_id, user_id, organization_id, started_by, status, lease_until)
			VALUES ($1, $2, $3, $4, $5, 'running', now() + make_interval(secs => $6))`,
			run.ID, run.SessionID, run.UserID, run.OrganizationID, run.StartedBy, lease.Seconds()); err != nil {
			return fmt.Errorf("start run: %w", err)
		}
		return nil
	})
	if err == nil {
		return nil
	}
	if isUniqueViolation(err, oneLivePerSessionConstraint) {
		return ErrRunInProgress
	}
	return err
}

// Renew extends a running run's hold on its conversation to lease from now.
// It answers ErrNotRunning when the run is no longer running.
func (store *Store) Renew(ctx context.Context, runID string, lease time.Duration) error {
	tag, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.agent_runs
		SET lease_until = now() + make_interval(secs => $2)
		WHERE id = $1 AND status = 'running'`,
		runID, lease.Seconds())
	if err != nil {
		return fmt.Errorf("renew run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotRunning
	}
	return nil
}

// Finish records how the run ended and, in the same transaction, puts event
// in the agents outbox. It answers ErrNotRunning, and records nothing, when
// the run already finished.
func (store *Store) Finish(ctx context.Context, runID string, outcome Outcome, event events.Event) error {
	if outcome.Status != StatusCompleted && outcome.Status != StatusFailed {
		return fmt.Errorf("a run's outcome must be %q or %q", StatusCompleted, StatusFailed)
	}
	if event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
		return errors.New("finishing a run needs an event with an id, a detail type and when it occurred")
	}

	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE agents_primary.agent_runs
			SET status = $2, finished_at = now(), lease_until = NULL, error = NULLIF($3, ''),
			    input_tokens = $4, output_tokens = $5, cached_tokens = $6, total_tokens = $7,
			    cost_micros = $8
			WHERE id = $1 AND status IN ('running', 'abandoned')`,
			runID, outcome.Status, outcome.Error,
			outcome.Usage.InputTokens, outcome.Usage.OutputTokens, outcome.Usage.CachedTokens, outcome.Usage.TotalTokens,
			outcome.CostMicros)
		if err != nil {
			return fmt.Errorf("finish run: %w", err)
		}
		// Zero rows means the run already finished. Without this check, a
		// second Finish call — or one racing a lease expiry — would record
		// its outcome, and publish its event, a second time.
		if tag.RowsAffected() == 0 {
			return ErrNotRunning
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO agents_primary.agents_outbox (id, detail_type, detail, occurred_at)
			VALUES ($1, $2, $3, $4)`,
			event.ID, event.DetailType, event.Data, event.OccurredAt); err != nil {
			return fmt.Errorf("record run outcome event: %w", err)
		}
		return nil
	})
}

// validateRun reports the plain error Start returns when run is missing a
// field it needs. The lease is checked separately, since it is Start's own
// parameter rather than part of Run.
func validateRun(run Run) error {
	switch {
	case run.ID == "":
		return errors.New("a run needs an id")
	case run.SessionID == "":
		return errors.New("a run needs a session id")
	case run.UserID == "":
		return errors.New("a run needs a user id")
	case run.OrganizationID == "":
		return errors.New("a run needs an organization id")
	case run.StartedBy == "":
		return errors.New("a run needs who started it")
	default:
		return nil
	}
}

// isUniqueViolation reports whether err is a PostgreSQL unique violation on
// the named constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

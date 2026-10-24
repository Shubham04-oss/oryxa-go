package subagents

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock is this store's own, from the 72635107xx range the
// agents' other stores take theirs from.
const migrationsAdvisoryLock int64 = 7263510723

// The states a sub-agent moves through (docs/design/agent-runtime.md, 3).
const (
	StateRunning = "running"
	StateWaiting = "waiting_on_parent"
	StateDone    = "done"
	StateFailed  = "failed"
	StateStopped = "stopped"
	// StateExpired is never stored: it is what a row reads as once its
	// expires_at has passed and it is not running.
	StateExpired = "expired"
)

var (
	// ErrNotFound reports a sub-agent the thread does not have.
	ErrNotFound = errors.New("no such sub-agent in this conversation")
	// ErrStateChanged reports an update whose expected state no longer
	// held: another call moved the sub-agent first.
	ErrStateChanged = errors.New("the sub-agent's state changed first")
)

// Thread is the conversation a sub-agent belongs to, and whose it is: a
// sub-agent is only ever read or changed through the same four.
type Thread struct {
	OrganizationID string
	UserID         string
	App            string
	ID             string
}

func (thread Thread) valid() bool {
	return thread.OrganizationID != "" && thread.UserID != "" && thread.App != "" && thread.ID != ""
}

// Record is a sub-agent as the table keeps it, and all Krude ever reads of
// one (View is what it is shown).
type Record struct {
	ID          string
	Thread      Thread
	ParentRunID string
	Objective   string
	Output      string
	Tools       []string
	Files       []string
	State       string
	Question    string
	Progress    string
	Result      string
	// FullResult is a result too long for Result, kept until a run saves it
	// as an artifact of the thread (ResultArtifact).
	FullResult     string
	ResultArtifact string
	Error          string

	BudgetModelCalls int
	BudgetMinutes    int
	ModelCalls       int
	InputTokens      int64
	OutputTokens     int64
	CachedTokens     int64
	TotalTokens      int64

	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
}

// stateAt is the state the record reads as at now: expired once past its
// expiry, unless it is running.
func (record Record) stateAt(now time.Time) string {
	if record.State != StateRunning && !record.ExpiresAt.IsZero() && now.After(record.ExpiresAt) {
		return StateExpired
	}
	return record.State
}

// Store keeps the records. PostgresStore is the one the service runs on.
type Store interface {
	Create(ctx context.Context, record Record) error
	Get(ctx context.Context, thread Thread, id string) (Record, error)
	List(ctx context.Context, thread Thread) ([]Record, error)
	// Update writes every field but the brief's, the thread and the
	// creation time, when the stored state is one of from; ErrStateChanged
	// otherwise.
	Update(ctx context.Context, record Record, from ...string) error
	// SetProgress writes a running sub-agent's progress note and the model
	// calls it has made, and pushes its expiry to expiresAt; it does nothing
	// to one that is not running.
	SetProgress(ctx context.Context, id, progress string, modelCalls int, expiresAt time.Time) error
	// ListMember lists a member's records in an organization, or every
	// member's when userID is empty, for forgetting them.
	ListMember(ctx context.Context, organizationID, userID string) ([]Record, error)
	Delete(ctx context.Context, ids ...string) error
}

// PostgresStore is agents_primary.subagents.
type PostgresStore struct {
	database *postgreskit.Database
}

// NewPostgresStore returns the store over database's pool. Its table is
// named with its schema, so the pool's search_path does not matter.
func NewPostgresStore(database *postgreskit.Database) *PostgresStore {
	return &PostgresStore{database: database}
}

// Migrate installs the table. Running it twice is harmless.
func (store *PostgresStore) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agents-subagents", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

const columns = `id, organization_id, user_id, thread_app, thread_id, parent_run_id, objective, output, tools, files,
	state, question, progress, result, full_result, result_artifact, error,
	budget_model_calls, budget_minutes, model_calls, input_tokens, output_tokens, cached_tokens, total_tokens,
	created_at, updated_at, expires_at`

func (store *PostgresStore) Create(ctx context.Context, record Record) error {
	_, err := store.database.Pool.Exec(ctx, `INSERT INTO agents_primary.subagents (`+columns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`,
		record.ID, record.Thread.OrganizationID, record.Thread.UserID, record.Thread.App, record.Thread.ID, record.ParentRunID,
		record.Objective, record.Output, nonNil(record.Tools), nonNil(record.Files),
		record.State, record.Question, record.Progress, record.Result, record.FullResult, record.ResultArtifact, record.Error,
		record.BudgetModelCalls, record.BudgetMinutes, record.ModelCalls, record.InputTokens, record.OutputTokens, record.CachedTokens, record.TotalTokens,
		record.CreatedAt, record.UpdatedAt, record.ExpiresAt)
	if err != nil {
		return fmt.Errorf("record a sub-agent: %w", err)
	}
	return nil
}

func (store *PostgresStore) Get(ctx context.Context, thread Thread, id string) (Record, error) {
	records, err := store.query(ctx, `SELECT `+columns+` FROM agents_primary.subagents
		WHERE id=$1 AND organization_id=$2 AND user_id=$3 AND thread_app=$4 AND thread_id=$5`,
		id, thread.OrganizationID, thread.UserID, thread.App, thread.ID)
	if err != nil {
		return Record{}, err
	}
	if len(records) == 0 {
		return Record{}, ErrNotFound
	}
	return records[0], nil
}

func (store *PostgresStore) List(ctx context.Context, thread Thread) ([]Record, error) {
	return store.query(ctx, `SELECT `+columns+` FROM agents_primary.subagents
		WHERE organization_id=$1 AND user_id=$2 AND thread_app=$3 AND thread_id=$4
		ORDER BY created_at, id`,
		thread.OrganizationID, thread.UserID, thread.App, thread.ID)
}

func (store *PostgresStore) ListMember(ctx context.Context, organizationID, userID string) ([]Record, error) {
	if userID == "" {
		return store.query(ctx, `SELECT `+columns+` FROM agents_primary.subagents WHERE organization_id=$1 ORDER BY created_at, id`, organizationID)
	}
	return store.query(ctx, `SELECT `+columns+` FROM agents_primary.subagents
		WHERE organization_id=$1 AND user_id=$2 ORDER BY created_at, id`, organizationID, userID)
}

func (store *PostgresStore) Update(ctx context.Context, record Record, from ...string) error {
	tag, err := store.database.Pool.Exec(ctx, `UPDATE agents_primary.subagents SET
		parent_run_id=$2, state=$3, question=$4, progress=$5, result=$6, full_result=$7, result_artifact=$8, error=$9,
		model_calls=$10, input_tokens=$11, output_tokens=$12, cached_tokens=$13, total_tokens=$14,
		updated_at=$15, expires_at=$16
		WHERE id=$1 AND state = ANY($17)`,
		record.ID, record.ParentRunID, record.State, record.Question, record.Progress, record.Result, record.FullResult,
		record.ResultArtifact, record.Error, record.ModelCalls, record.InputTokens, record.OutputTokens, record.CachedTokens,
		record.TotalTokens, record.UpdatedAt, record.ExpiresAt, nonNil(from))
	if err != nil {
		return fmt.Errorf("update a sub-agent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStateChanged
	}
	return nil
}

func (store *PostgresStore) SetProgress(ctx context.Context, id, progress string, modelCalls int, expiresAt time.Time) error {
	if _, err := store.database.Pool.Exec(ctx, `UPDATE agents_primary.subagents
		SET progress=$2, model_calls=$3, updated_at=now(), expires_at=$4
		WHERE id=$1 AND state='running'`, id, progress, modelCalls, expiresAt); err != nil {
		return fmt.Errorf("note a sub-agent's progress: %w", err)
	}
	return nil
}

func (store *PostgresStore) Delete(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.subagents WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("delete sub-agents: %w", err)
	}
	return nil
}

func (store *PostgresStore) query(ctx context.Context, sql string, args ...any) ([]Record, error) {
	rows, err := store.database.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("read sub-agents: %w", err)
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Record, error) {
		var record Record
		err := row.Scan(&record.ID, &record.Thread.OrganizationID, &record.Thread.UserID, &record.Thread.App, &record.Thread.ID,
			&record.ParentRunID, &record.Objective, &record.Output, &record.Tools, &record.Files,
			&record.State, &record.Question, &record.Progress, &record.Result, &record.FullResult, &record.ResultArtifact, &record.Error,
			&record.BudgetModelCalls, &record.BudgetMinutes, &record.ModelCalls, &record.InputTokens, &record.OutputTokens,
			&record.CachedTokens, &record.TotalTokens, &record.CreatedAt, &record.UpdatedAt, &record.ExpiresAt)
		return record, err
	})
	if err != nil {
		return nil, fmt.Errorf("read sub-agents: %w", err)
	}
	return records, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return slices.Clone(values)
}

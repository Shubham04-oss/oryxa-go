package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

const runColumns = `id, organization_id, source_id, kind, status, documents_seen, documents_published,
	documents_removed, documents_excluded, error, retryable, started_at, finished_at`

func (store *Store) StartRun(ctx context.Context, run domain.SyncRun) error {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO knowledge_sync_runs (id, organization_id, source_id, kind, status, started_at)
		VALUES ($1, $2, $3, $4, 'running', $5)`,
		run.ID, run.OrganizationID, run.SourceID, run.Kind, run.StartedAt); err != nil {
		return mapWriteError("start run", err)
	}
	return nil
}

// FinishRun records how a pass ended, with its counts. The event that says so
// is written in the same transaction, as everything on the bus is.
func (store *Store) FinishRun(ctx context.Context, run domain.SyncRun, event *domain.Event) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE knowledge_sync_runs
			SET status = $2, documents_seen = $3, documents_published = $4, documents_removed = $5,
			    documents_excluded = $9, error = left($6, 200), retryable = $7, finished_at = $8
			WHERE id = $1`,
			run.ID, string(run.Status), run.DocumentsSeen, run.DocumentsPublished, run.DocumentsRemoved,
			run.Error, run.Retryable, run.FinishedAt, run.DocumentsExcluded); err != nil {
			return mapWriteError("finish run", err)
		}
		if event != nil {
			return insertEvents(ctx, tx, *event)
		}
		return nil
	})
}

func (store *Store) Runs(ctx context.Context, organizationID, sourceID string, limit int) ([]domain.SyncRun, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT `+runColumns+`
		FROM knowledge_sync_runs
		WHERE organization_id = $1 AND source_id = $2
		ORDER BY started_at DESC, id
		LIMIT $3`, organizationID, sourceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
	runs := []domain.SyncRun{}
	for rows.Next() {
		var run domain.SyncRun
		var status string
		if err := rows.Scan(&run.ID, &run.OrganizationID, &run.SourceID, &run.Kind, &status,
			&run.DocumentsSeen, &run.DocumentsPublished, &run.DocumentsRemoved, &run.DocumentsExcluded, &run.Error,
			&run.Retryable, &run.StartedAt, &run.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		run.Status = domain.RunStatus(status)
		run.StartedAt = run.StartedAt.UTC()
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	return runs, nil
}

// DeleteRunsBefore forgets old runs. What a source holds is in its documents;
// the runs only explain how it got there.
func (store *Store) DeleteRunsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := store.pool.Exec(ctx, `DELETE FROM knowledge_sync_runs WHERE started_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("forget old runs: %w", err)
	}
	return tag.RowsAffected(), nil
}

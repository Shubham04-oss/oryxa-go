package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

const knowledgeDocumentColumns = `
	knowledge_document_id, organization_id, corpus_id, document_id,
	content_hash, extracted_at, updated_at`

type knowledgeDocumentRow struct {
	KnowledgeDocumentID string    `db:"knowledge_document_id"`
	OrganizationID      string    `db:"organization_id"`
	CorpusID            string    `db:"corpus_id"`
	DocumentID          string    `db:"document_id"`
	ContentHash         string    `db:"content_hash"`
	ExtractedAt         time.Time `db:"extracted_at"`
	UpdatedAt           time.Time `db:"updated_at"`
}

func (row knowledgeDocumentRow) mapping() domain.KnowledgeDocumentMapping {
	return domain.KnowledgeDocumentMapping{
		KnowledgeDocumentID: row.KnowledgeDocumentID,
		OrganizationID:      row.OrganizationID,
		CorpusID:            row.CorpusID,
		DocumentID:          row.DocumentID,
		ContentHash:         row.ContentHash,
		ExtractedAt:         row.ExtractedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}

// KnowledgeDocumentMapping reads what a knowledge document became here, so a
// later publish or removal can find it again by knowledge's own id.
func (store *Store) KnowledgeDocumentMapping(ctx context.Context, organizationID, knowledgeDocumentID string) (domain.KnowledgeDocumentMapping, error) {
	var row knowledgeDocumentRow
	err := store.db.GetContext(ctx, &row, `
		SELECT `+knowledgeDocumentColumns+`
		FROM rag_knowledge_documents
		WHERE organization_id = $1 AND knowledge_document_id = $2`, organizationID, knowledgeDocumentID)
	if isNoRows(err) {
		return domain.KnowledgeDocumentMapping{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.KnowledgeDocumentMapping{}, fmt.Errorf("read knowledge document mapping: %w", err)
	}
	return row.mapping(), nil
}

// UpsertKnowledgeDocumentMapping records, or updates, what a knowledge
// document became.
//
// This is a plain upsert rather than something written through inTx with an
// outbox event: the table is the bridge's own bookkeeping, nothing outside
// rag ever reads it, and the rag document it points at already carried its
// own event when UpsertDocument wrote it.
func (store *Store) UpsertKnowledgeDocumentMapping(ctx context.Context, mapping domain.KnowledgeDocumentMapping) error {
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO rag_knowledge_documents (
			knowledge_document_id, organization_id, corpus_id, document_id,
			content_hash, extracted_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (knowledge_document_id) DO UPDATE
		SET corpus_id = EXCLUDED.corpus_id, document_id = EXCLUDED.document_id,
		    content_hash = EXCLUDED.content_hash, extracted_at = EXCLUDED.extracted_at,
		    updated_at = EXCLUDED.updated_at`,
		mapping.KnowledgeDocumentID, mapping.OrganizationID, mapping.CorpusID, mapping.DocumentID,
		mapping.ContentHash, mapping.ExtractedAt, mapping.UpdatedAt); err != nil {
		return mapWriteError("upsert knowledge document mapping", err)
	}
	return nil
}

// DeleteKnowledgeDocumentMapping forgets a knowledge document.
//
// Deleting one already gone is reported as domain.ErrNotFound, exactly like
// DeleteDocument and DeleteCorpus do: a removal event that arrives twice, or
// one for a document whose corpus was wiped and took the mapping with it by
// cascade, both want the caller to treat it as already done rather than fail.
func (store *Store) DeleteKnowledgeDocumentMapping(ctx context.Context, organizationID, knowledgeDocumentID string) error {
	result, err := store.db.ExecContext(ctx, `
		DELETE FROM rag_knowledge_documents WHERE organization_id = $1 AND knowledge_document_id = $2`,
		organizationID, knowledgeDocumentID)
	if err != nil {
		return fmt.Errorf("delete knowledge document mapping: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete knowledge document mapping: %w", err)
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// knowledgeRemovalRetention is how long a knowledge document's removal is
// remembered. A tombstone exists to refuse a publish that was sent before
// the removal and is delivered after it, and nothing on the bus is delivered
// that late: SQS keeps a message at most fourteen days, however often it is
// redriven from a dead-letter queue. Past this, a tombstone can refuse
// nothing, and it is let go.
const knowledgeRemovalRetention = 30 * 24 * time.Hour

// knowledgeRemovalPruneBatch bounds how many expired tombstones one recorded
// removal lets go, so a backlog is worked off a batch at a time rather than
// in one long statement.
const knowledgeRemovalPruneBatch = 500

// KnowledgeDocumentRemovedAt reads when a knowledge document was last
// removed, so a publish sent before that removal can be refused.
func (store *Store) KnowledgeDocumentRemovedAt(ctx context.Context, organizationID, knowledgeDocumentID string) (time.Time, error) {
	var removedAt time.Time
	err := store.db.GetContext(ctx, &removedAt, `
		SELECT removed_at FROM rag_knowledge_removals
		WHERE organization_id = $1 AND knowledge_document_id = $2`, organizationID, knowledgeDocumentID)
	if isNoRows(err) {
		return time.Time{}, domain.ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read knowledge document removal: %w", err)
	}
	return removedAt, nil
}

// RecordKnowledgeDocumentRemoval remembers that a knowledge document was
// removed, keeping the latest removal when one is already remembered.
//
// It also lets go of tombstones past knowledgeRemovalRetention, whichever
// organization they belong to. Removals are the only thing that creates
// tombstones, so recording one is when old ones are cleared — including an
// organization's that was deleted and will record no more, whose documents
// knowledge retracts, one removal each, after rag has already wiped its
// corpora.
func (store *Store) RecordKnowledgeDocumentRemoval(ctx context.Context, organizationID, knowledgeDocumentID string, removedAt time.Time) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO rag_knowledge_removals (organization_id, knowledge_document_id, removed_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (organization_id, knowledge_document_id) DO UPDATE
			SET removed_at = GREATEST(rag_knowledge_removals.removed_at, EXCLUDED.removed_at)`,
			organizationID, knowledgeDocumentID, removedAt); err != nil {
			return fmt.Errorf("record knowledge document removal: %w", err)
		}
		// SKIP LOCKED: a tombstone another replica is writing or clearing
		// right now is left to it, rather than waited on.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM rag_knowledge_removals
			WHERE ctid IN (
				SELECT ctid FROM rag_knowledge_removals
				WHERE removed_at < $1
				ORDER BY removed_at
				LIMIT $2
				FOR UPDATE SKIP LOCKED)`,
			time.Now().Add(-knowledgeRemovalRetention), knowledgeRemovalPruneBatch); err != nil {
			return fmt.Errorf("clear expired knowledge document removals: %w", err)
		}
		return nil
	})
}

// WithKnowledgeDocumentLock serializes publishes and removals of the same
// Knowledge document across rag replicas. The lock is transaction-scoped and
// released on commit or rollback; the work itself uses normal store calls.
func (store *Store) WithKnowledgeDocumentLock(ctx context.Context, organizationID, knowledgeDocumentID string, run func() error) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		key := organizationID + "/" + knowledgeDocumentID
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return fmt.Errorf("lock knowledge document: %w", err)
		}
		return run()
	})
}

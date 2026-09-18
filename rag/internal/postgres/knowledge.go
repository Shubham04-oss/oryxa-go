package postgres

import (
	"context"
	"fmt"
	"time"

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

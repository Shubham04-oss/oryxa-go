package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

const documentColumns = `
	id, corpus_id, organization_id, source_kind, source_ref, title, content_type,
	content_sha256, byte_size, metadata, status, failure_reason, chunk_count,
	attempts, created_by, created_at, updated_at, ingested_at`

// documentColumnsQualified is the same list for a statement that also has
// another relation in scope, where a bare "id" is ambiguous.
const documentColumnsQualified = `
	document.id, document.corpus_id, document.organization_id, document.source_kind,
	document.source_ref, document.title, document.content_type, document.content_sha256,
	document.byte_size, document.metadata, document.status, document.failure_reason,
	document.chunk_count, document.attempts, document.created_by, document.created_at,
	document.updated_at, document.ingested_at`

type documentRow struct {
	ID             string     `db:"id"`
	CorpusID       string     `db:"corpus_id"`
	OrganizationID string     `db:"organization_id"`
	SourceKind     string     `db:"source_kind"`
	SourceRef      string     `db:"source_ref"`
	Title          string     `db:"title"`
	ContentType    string     `db:"content_type"`
	ContentSHA256  string     `db:"content_sha256"`
	ByteSize       int64      `db:"byte_size"`
	Metadata       []byte     `db:"metadata"`
	Status         string     `db:"status"`
	FailureReason  string     `db:"failure_reason"`
	ChunkCount     int        `db:"chunk_count"`
	Attempts       int        `db:"attempts"`
	CreatedBy      string     `db:"created_by"`
	CreatedAt      time.Time  `db:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at"`
	IngestedAt     *time.Time `db:"ingested_at"`
}

func (row documentRow) document() (domain.Document, error) {
	metadata, err := decodeMetadata(row.Metadata)
	if err != nil {
		return domain.Document{}, err
	}
	return domain.Document{
		ID:             row.ID,
		CorpusID:       row.CorpusID,
		OrganizationID: row.OrganizationID,
		Source:         domain.Source{Kind: domain.SourceKind(row.SourceKind), Ref: row.SourceRef},
		Title:          row.Title,
		ContentType:    row.ContentType,
		ContentSHA256:  row.ContentSHA256,
		ByteSize:       row.ByteSize,
		Metadata:       metadata,
		Status:         domain.IngestStatus(row.Status),
		FailureReason:  row.FailureReason,
		ChunkCount:     row.ChunkCount,
		Attempts:       row.Attempts,
		CreatedBy:      row.CreatedBy,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		IngestedAt:     row.IngestedAt,
	}, nil
}

// Upsert is what happened when a document was offered.
type Upsert int

const (
	// UpsertCreated means the document is new and needs ingesting.
	UpsertCreated Upsert = iota
	// UpsertChanged means the document was already there with different
	// content, which has been replaced and needs ingesting again.
	UpsertChanged
	// UpsertUnchanged means the same bytes were offered again. Nothing was
	// written and nothing needs embedding. This is what lets an upstream
	// pipeline re-deliver everything it has without paying to re-embed it.
	UpsertUnchanged
)

// UpsertDocument records a document, or recognises one already held.
//
// The body is stored alongside the metadata because a citation addresses a
// byte range of it. Recording the row and the event together means a document
// cannot exist without the ingest work having been announced.
func (store *Store) UpsertDocument(ctx context.Context, document domain.Document, body string, event domain.Event) (domain.Document, Upsert, error) {
	metadata, err := encodeMetadata(document.Metadata)
	if err != nil {
		return domain.Document{}, 0, err
	}
	var (
		stored  domain.Document
		outcome Upsert
	)
	err = store.inTx(ctx, func(tx *sqlx.Tx) error {
		existing, found, err := findDocument(ctx, tx, document.CorpusID, document.Source, document.ContentSHA256)
		if err != nil {
			return err
		}
		switch {
		case found && existing.ContentSHA256 == document.ContentSHA256 && existing.Status == domain.StatusReady:
			stored, outcome = existing, UpsertUnchanged
			return nil
		case found:
			// Same source, new content — or a previous attempt that never
			// finished. Either way it goes back to pending and is re-cut.
			var row documentRow
			if err := tx.GetContext(ctx, &row, `
				UPDATE rag_documents
				SET title = $2, content_type = $3, content_sha256 = $4, byte_size = $5,
				    body = $6, metadata = $7, status = 'pending', failure_reason = '',
				    attempts = 0, locked_until = NULL, updated_at = $8, ingested_at = NULL
				WHERE id = $1
				RETURNING `+documentColumns,
				existing.ID, document.Title, document.ContentType, document.ContentSHA256,
				document.ByteSize, body, metadata, document.UpdatedAt); err != nil {
				return mapWriteError("update document", err)
			}
			stored, err = row.document()
			if err != nil {
				return err
			}
			outcome = UpsertChanged
		default:
			var row documentRow
			if err := tx.GetContext(ctx, &row, `
				INSERT INTO rag_documents (
					id, corpus_id, organization_id, source_kind, source_ref, title,
					content_type, content_sha256, byte_size, body, metadata, status,
					created_by, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending', $12, $13, $13)
				RETURNING `+documentColumns,
				document.ID, document.CorpusID, document.OrganizationID,
				document.Source.Kind, document.Source.Ref, document.Title,
				document.ContentType, document.ContentSHA256, document.ByteSize, body, metadata,
				document.CreatedBy, document.CreatedAt); err != nil {
				return mapWriteError("insert document", err)
			}
			stored, err = row.document()
			if err != nil {
				return err
			}
			outcome = UpsertCreated
		}
		if outcome == UpsertUnchanged {
			return nil
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
	if err != nil {
		return domain.Document{}, 0, err
	}
	return stored, outcome, nil
}

// findDocument looks for the document a source already has. Inline text has no
// reference to be found by, so its content is its identity.
func findDocument(ctx context.Context, ext execer, corpusID string, source domain.Source, contentSHA256 string) (domain.Document, bool, error) {
	var row documentRow
	var err error
	if source.Ref != "" {
		err = ext.GetContext(ctx, &row, `
			SELECT `+documentColumns+`
			FROM rag_documents
			WHERE corpus_id = $1 AND source_kind = $2 AND source_ref = $3
			FOR UPDATE`, corpusID, source.Kind, source.Ref)
	} else {
		err = ext.GetContext(ctx, &row, `
			SELECT `+documentColumns+`
			FROM rag_documents
			WHERE corpus_id = $1 AND source_kind = 'inline' AND content_sha256 = $2
			FOR UPDATE`, corpusID, contentSHA256)
	}
	if isNoRows(err) {
		return domain.Document{}, false, nil
	}
	if err != nil {
		return domain.Document{}, false, fmt.Errorf("look up document: %w", err)
	}
	document, err := row.document()
	return document, err == nil, err
}

func (store *Store) Document(ctx context.Context, organizationID, documentID string) (domain.Document, error) {
	var row documentRow
	err := store.db.GetContext(ctx, &row, `
		SELECT `+documentColumns+`
		FROM rag_documents
		WHERE organization_id = $1 AND id = $2`, organizationID, documentID)
	if isNoRows(err) {
		return domain.Document{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Document{}, fmt.Errorf("read document: %w", err)
	}
	return row.document()
}

// DocumentBody returns the text a document was chunked from, which is what a
// citation's byte range addresses.
func (store *Store) DocumentBody(ctx context.Context, documentID string) (string, error) {
	var body string
	err := store.db.GetContext(ctx, &body, `SELECT body FROM rag_documents WHERE id = $1`, documentID)
	if isNoRows(err) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read document body: %w", err)
	}
	return body, nil
}

// Documents lists a corpus's documents, newest first.
func (store *Store) Documents(ctx context.Context, organizationID, corpusID string, status domain.IngestStatus, limit int) ([]domain.Document, error) {
	statement := `SELECT ` + documentColumns + ` FROM rag_documents WHERE organization_id = $1 AND corpus_id = $2`
	arguments := []any{organizationID, corpusID}
	if status != "" {
		arguments = append(arguments, string(status))
		statement += fmt.Sprintf(` AND status = $%d`, len(arguments))
	}
	arguments = append(arguments, limit)
	statement += fmt.Sprintf(` ORDER BY created_at DESC, id LIMIT $%d`, len(arguments))

	var rows []documentRow
	if err := store.db.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	documents := make([]domain.Document, 0, len(rows))
	for _, row := range rows {
		document, err := row.document()
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	return documents, nil
}

// DeleteDocument removes a document and, by cascade, its chunks and any
// pgvector rows. It returns the chunk ids first so the caller can take the
// vectors out of S3 Vectors, which has no cascade of its own.
func (store *Store) DeleteDocument(ctx context.Context, organizationID, documentID string, event domain.Event) ([]string, error) {
	var chunkIDs []string
	err := store.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.SelectContext(ctx, &chunkIDs, `
			SELECT id FROM rag_chunks WHERE document_id = $1 ORDER BY ordinal`, documentID); err != nil {
			return fmt.Errorf("list chunks to delete: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			DELETE FROM rag_documents WHERE organization_id = $1 AND id = $2`, organizationID, documentID)
		if err != nil {
			return fmt.Errorf("delete document: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete document: %w", err)
		}
		if affected == 0 {
			return domain.ErrNotFound
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
	if err != nil {
		return nil, err
	}
	return chunkIDs, nil
}

// ClaimDocuments takes the next documents waiting to be ingested and holds
// them, so two workers never embed the same document twice. The lease expires,
// so a worker that dies does not strand its work.
func (store *Store) ClaimDocuments(ctx context.Context, limit int, lease time.Duration) ([]domain.Document, error) {
	var rows []documentRow
	err := store.db.SelectContext(ctx, &rows, `
		WITH picked AS (
			SELECT id
			FROM rag_documents
			WHERE status IN ('pending', 'ingesting')
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE rag_documents AS document
		SET status = 'ingesting',
		    locked_until = now() + ($2 * interval '1 millisecond'),
		    attempts = document.attempts + 1,
		    updated_at = now()
		FROM picked
		WHERE document.id = picked.id
		RETURNING `+documentColumnsQualified, limit, lease.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim documents: %w", err)
	}
	documents := make([]domain.Document, 0, len(rows))
	for _, row := range rows {
		document, err := row.document()
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	return documents, nil
}

// MarkDocumentReady records a finished ingest and its event together.
func (store *Store) MarkDocumentReady(ctx context.Context, documentID string, chunkCount int, at time.Time, event domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE rag_documents
			SET status = 'ready', chunk_count = $2, failure_reason = '',
			    locked_until = NULL, ingested_at = $3, updated_at = $3
			WHERE id = $1`, documentID, chunkCount, at); err != nil {
			return fmt.Errorf("mark document ready: %w", err)
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

// MarkDocumentFailed keeps the failure with its reason rather than deleting
// the row, so it can be seen and retried instead of quietly disappearing.
func (store *Store) MarkDocumentFailed(ctx context.Context, documentID, reason string, at time.Time, event domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE rag_documents
			SET status = 'failed', failure_reason = left($2, 1000),
			    locked_until = NULL, updated_at = $3
			WHERE id = $1`, documentID, reason, at); err != nil {
			return fmt.Errorf("mark document failed: %w", err)
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

// ReleaseDocument puts a document back for another attempt without counting it
// as a failure, for a worker shutting down mid-ingest.
func (store *Store) ReleaseDocument(ctx context.Context, documentID string) error {
	if _, err := store.db.ExecContext(ctx, `
		UPDATE rag_documents
		SET status = 'pending', locked_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'ingesting'`, documentID); err != nil {
		return fmt.Errorf("release document: %w", err)
	}
	return nil
}

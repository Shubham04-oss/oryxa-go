package postgres

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

const documentColumns = `
	id, corpus_id, organization_id, source_kind, source_ref, title, content_type,
	content_sha256, byte_size, metadata, status, failure_reason, chunk_count,
	attempts, created_by, created_at, updated_at, ingested_at,
	original_bucket, original_key, kb_synced_hash, kb_submitted_at`

// documentColumnsQualified is the same list for a statement that also has
// another relation in scope, where a bare "id" is ambiguous.
const documentColumnsQualified = `
	document.id, document.corpus_id, document.organization_id, document.source_kind,
	document.source_ref, document.title, document.content_type, document.content_sha256,
	document.byte_size, document.metadata, document.status, document.failure_reason,
	document.chunk_count, document.attempts, document.created_by, document.created_at,
	document.updated_at, document.ingested_at,
	document.original_bucket, document.original_key, document.kb_synced_hash,
	document.kb_submitted_at`

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
	OriginalBucket string     `db:"original_bucket"`
	OriginalKey    string     `db:"original_key"`
	KBSyncedHash   string     `db:"kb_synced_hash"`
	KBSubmittedAt  *time.Time `db:"kb_submitted_at"`
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
		OriginalBucket: row.OriginalBucket,
		OriginalKey:    row.OriginalKey,
		KBSyncedHash:   row.KBSyncedHash,
		KBSubmittedAt:  row.KBSubmittedAt,
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
			// original_bucket and original_key are written again too: an
			// original object is content addressed, so a changed document
			// with one is a new object at a new key.
			var row documentRow
			if err := tx.GetContext(ctx, &row, `
				UPDATE rag_documents
				SET title = $2, content_type = $3, content_sha256 = $4, byte_size = $5,
				    body = $6, metadata = $7, status = 'pending', failure_reason = '',
				    attempts = 0, locked_until = NULL, updated_at = $8, ingested_at = NULL,
				    original_bucket = $9, original_key = $10
				WHERE id = $1
				RETURNING `+documentColumns,
				existing.ID, document.Title, document.ContentType, document.ContentSHA256,
				document.ByteSize, body, metadata, document.UpdatedAt,
				document.OriginalBucket, document.OriginalKey); err != nil {
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
					created_by, created_at, updated_at, original_bucket, original_key)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending', $12, $13, $13, $14, $15)
				RETURNING `+documentColumns,
				document.ID, document.CorpusID, document.OrganizationID,
				document.Source.Kind, document.Source.Ref, document.Title,
				document.ContentType, document.ContentSHA256, document.ByteSize, body, metadata,
				document.CreatedBy, document.CreatedAt,
				document.OriginalBucket, document.OriginalKey); err != nil {
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

// documentCursor is where a page of documents left off. It is opaque to
// everything outside this file: a caller only ever passes back a NextCursor
// this store handed it. The pair inside is what lets a keyset scan resume
// after exactly the last row a page returned, in the same order the listing
// promises — newest first, ties (there are none: id is unique) broken by id —
// rather than by offset, which would skip or repeat rows when a document is
// added between two pages.
type documentCursor struct {
	CreatedAt time.Time
	ID        string
}

// encodeDocumentCursor renders where a page ended, opaque and URL-safe.
func encodeDocumentCursor(createdAt time.Time, id string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeDocumentCursor reverses encodeDocumentCursor. A cursor that was not
// just handed back unmodified — edited, truncated, or carried over from a
// different listing — is refused rather than guessed at.
func decodeDocumentCursor(cursor string) (documentCursor, error) {
	invalid := fmt.Errorf("cursor is not valid: %w", domain.ErrInvalid)
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return documentCursor{}, invalid
	}
	createdAtField, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return documentCursor{}, invalid
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdAtField)
	if err != nil {
		return documentCursor{}, invalid
	}
	return documentCursor{CreatedAt: createdAt, ID: id}, nil
}

// Documents lists a corpus's documents, newest first. With no cursor it
// returns the first page. Passing back the cursor a page returned continues
// the listing from exactly where that page ended; the returned cursor is
// empty once there is nothing more to read, which is what tells a caller to
// stop asking.
func (store *Store) Documents(ctx context.Context, organizationID, corpusID string, status domain.IngestStatus, cursor string, limit int) ([]domain.Document, string, error) {
	statement := `SELECT ` + documentColumns + ` FROM rag_documents WHERE organization_id = $1 AND corpus_id = $2`
	arguments := []any{organizationID, corpusID}
	if status != "" {
		arguments = append(arguments, string(status))
		statement += fmt.Sprintf(` AND status = $%d`, len(arguments))
	}
	if cursor != "" {
		after, err := decodeDocumentCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		arguments = append(arguments, after.CreatedAt, after.ID)
		createdAtIndex, idIndex := len(arguments)-1, len(arguments)
		// Matches ORDER BY created_at DESC, id below: strictly older, or the
		// same instant and a greater id, is exactly what comes after the
		// cursor's row in that order.
		statement += fmt.Sprintf(` AND (created_at < $%d OR (created_at = $%d AND id > $%d))`, createdAtIndex, createdAtIndex, idIndex)
	}
	// One row more than the page asks for is the cheap way to know whether
	// another page follows, without a second round trip to count it.
	arguments = append(arguments, limit+1)
	statement += fmt.Sprintf(` ORDER BY created_at DESC, id LIMIT $%d`, len(arguments))

	var rows []documentRow
	if err := store.db.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return nil, "", fmt.Errorf("list documents: %w", err)
	}
	var nextCursor string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		nextCursor = encodeDocumentCursor(last.CreatedAt, last.ID)
	}
	documents := make([]domain.Document, 0, len(rows))
	for _, row := range rows {
		document, err := row.document()
		if err != nil {
			return nil, "", err
		}
		documents = append(documents, document)
	}
	return documents, nextCursor, nil
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

// MarkKnowledgeBaseSubmitted records that a document's current content has
// been handed to a Bedrock knowledge base, without otherwise disturbing its
// status or its lease: the document stays 'ingesting' until the ingest
// worker's next claim polls the knowledge base for the outcome, exactly as
// the lease that claim already holds intends. Recording this separately from
// MarkDocumentReady is what lets a claim tell "never submitted, or the
// content changed since it last was" from "already submitted, only the
// outcome is still unknown" without resubmitting on every poll.
//
// The time goes with the hash, so the worker can tell how long a submission
// has been outstanding. An empty hash forgets both: nothing is outstanding.
func (store *Store) MarkKnowledgeBaseSubmitted(ctx context.Context, documentID, contentSHA256 string, at time.Time) error {
	if _, err := store.db.ExecContext(ctx, `
		UPDATE rag_documents
		SET kb_synced_hash = $2,
		    kb_submitted_at = CASE WHEN $2 = '' THEN NULL ELSE $3::timestamptz END,
		    updated_at = $3
		WHERE id = $1`,
		documentID, contentSHA256, at); err != nil {
		return fmt.Errorf("mark document submitted to the knowledge base: %w", err)
	}
	return nil
}

// RecheckDocumentAt leaves a document ingesting but claimable again at at:
// how a bedrock-kb document waits on the knowledge base, which indexes in
// seconds, rather than holding a whole lease. The claim that polled it did
// not count as an attempt, so polling cannot use a document's attempts up.
func (store *Store) RecheckDocumentAt(ctx context.Context, documentID string, at time.Time) error {
	if _, err := store.db.ExecContext(ctx, `
		UPDATE rag_documents
		SET locked_until = $2, attempts = GREATEST(attempts - 1, 0), updated_at = now()
		WHERE id = $1 AND status = 'ingesting'`, documentID, at); err != nil {
		return fmt.Errorf("recheck document: %w", err)
	}
	return nil
}

// RetryDocumentAt puts a document whose attempt failed back in the queue,
// claimable again at at. The failed attempt still counts toward giving up;
// only the next one waits.
func (store *Store) RetryDocumentAt(ctx context.Context, documentID string, at time.Time) error {
	if _, err := store.db.ExecContext(ctx, `
		UPDATE rag_documents
		SET status = 'pending', locked_until = $2, updated_at = now()
		WHERE id = $1 AND status = 'ingesting'`, documentID, at); err != nil {
		return fmt.Errorf("retry document: %w", err)
	}
	return nil
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

// DocumentIDsForCorpus lists every document a corpus holds, whatever its
// status. It exists for dropping a bedrock-kb corpus's documents from the
// knowledge base: there is no per-corpus index there to drop the way S3
// Vectors has one, so removing a corpus means naming every document it owned
// instead of the whole thing at once.
func (store *Store) DocumentIDsForCorpus(ctx context.Context, corpusID string) ([]string, error) {
	var ids []string
	if err := store.db.SelectContext(ctx, &ids, `SELECT id FROM rag_documents WHERE corpus_id = $1`, corpusID); err != nil {
		return nil, fmt.Errorf("list document ids for corpus: %w", err)
	}
	return ids, nil
}

// DocumentsBySource finds every document indexed from one source, across all
// of an organization's corpora.
//
// Scanning each corpus's newest documents instead would quietly miss the ones
// in a corpus holding more than a page of them, and a deleted file would go on
// answering questions out of the index.
func (store *Store) DocumentsBySource(ctx context.Context, organizationID string, kind domain.SourceKind, ref string) ([]domain.Document, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, nil
	}
	var rows []documentRow
	if err := store.db.SelectContext(ctx, &rows, `
		SELECT `+documentColumns+`
		FROM rag_documents
		WHERE organization_id = $1 AND source_kind = $2 AND source_ref = $3
		ORDER BY created_at, id`, organizationID, string(kind), ref); err != nil {
		return nil, fmt.Errorf("list documents by source: %w", err)
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

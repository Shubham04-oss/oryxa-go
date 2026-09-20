package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

const documentColumns = `id, organization_id, source_id, external_id, kind, title, url, mime,
	content_hash, byte_size, original_bucket, original_key, original_size, metadata,
	source_updated_at, extracted_at, last_seen_at, removed_at, excluded_reason, judged_policy, judgment`

// UpsertDocument writes what a run read, and the event saying so only when the
// content actually changed. It answers whether anything was published, so a
// pass over an unchanged source publishes nothing.
func (store *Store) UpsertDocument(ctx context.Context, document domain.Document, event func(domain.Document) (domain.Event, error)) (bool, error) {
	metadata, err := encodeMap(document.Metadata)
	if err != nil {
		return false, err
	}
	originalBucket, originalKey, originalSize := originalColumns(document.Original)
	published := false
	err = store.inTx(ctx, func(tx pgx.Tx) error {
		var existingID, existingHash string
		var removedAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT id, content_hash, removed_at
			FROM knowledge_documents
			WHERE source_id = $1 AND external_id = $2`, document.SourceID, document.ExternalID).
			Scan(&existingID, &existingHash, &removedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read document: %w", err)
		}
		// A document that came back counts as changed, whatever its hash, so a
		// consumer that forgot it is told to hold it again.
		changed := existingID == "" || existingHash != document.ContentHash || removedAt != nil
		if existingID != "" {
			document.ID = existingID
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO knowledge_documents (
				id, organization_id, source_id, external_id, kind, title, url, mime,
				content_hash, body, byte_size, original_bucket, original_key, original_size,
				metadata, source_updated_at, extracted_at, last_seen_at, excluded_reason, judged_policy, judgment
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $17, '', $18, $19)
			ON CONFLICT (source_id, external_id) DO UPDATE SET
				kind = excluded.kind,
				title = excluded.title,
				url = excluded.url,
				mime = excluded.mime,
				content_hash = excluded.content_hash,
				body = excluded.body,
				byte_size = excluded.byte_size,
				original_bucket = excluded.original_bucket,
				original_key = excluded.original_key,
				original_size = excluded.original_size,
				metadata = excluded.metadata,
				source_updated_at = excluded.source_updated_at,
				extracted_at = excluded.extracted_at,
				last_seen_at = excluded.last_seen_at,
				removed_at = NULL,
				excluded_reason = '',
				judged_policy = excluded.judged_policy,
				judgment = excluded.judgment`,
			document.ID, document.OrganizationID, document.SourceID, document.ExternalID, document.Kind,
			document.Title, document.URL, document.MIME, document.ContentHash, document.Body,
			document.ByteSize, originalBucket, originalKey, originalSize, metadata,
			document.SourceUpdatedAt, document.ExtractedAt, document.JudgedPolicy, judgmentColumn(document.Judgment)); err != nil {
			return mapWriteError("upsert document", err)
		}
		if !changed || event == nil {
			return nil
		}
		recorded, err := event(document)
		if err != nil {
			return err
		}
		if err := insertEvents(ctx, tx, recorded); err != nil {
			return err
		}
		published = true
		return nil
	})
	return published, err
}

// DocumentByExternalID reads one document as its source last wrote it,
// whatever its removed_at: a caller deciding what a document's own bytes
// should be keyed and compared against needs to see a document even if a
// previous pass retracted it, the same way UpsertDocument's own internal
// lookup does.
func (store *Store) DocumentByExternalID(ctx context.Context, sourceID, externalID string) (domain.Document, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT `+documentColumns+`
		FROM knowledge_documents
		WHERE source_id = $1 AND external_id = $2`, sourceID, externalID)
	if err != nil {
		return domain.Document{}, fmt.Errorf("read document: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.Document{}, fmt.Errorf("read document: %w", err)
		}
		return domain.Document{}, domain.ErrNotFound
	}
	return scanDocument(rows, false)
}

// RetractDocument forgets one document immediately, named by the provider's
// own id for it, rather than waiting for a full pass to notice it is gone —
// Drive's changes feed and Gmail's history both say so directly. Answers the
// document retracted and whether there was a live one to.
func (store *Store) RetractDocument(ctx context.Context, sourceID, externalID string, now time.Time, event func(domain.Document) (domain.Event, error)) (domain.Document, bool, error) {
	var document domain.Document
	found := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		documents, err := queryDocuments(ctx, tx, `
			UPDATE knowledge_documents
			SET removed_at = $3, body = ''
			WHERE source_id = $1 AND external_id = $2 AND removed_at IS NULL
			RETURNING `+documentColumns, sourceID, externalID, now)
		if err != nil {
			return err
		}
		if len(documents) == 0 {
			return nil
		}
		document = documents[0]
		found = true
		if event == nil {
			return nil
		}
		recorded, err := event(document)
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, recorded)
	})
	return document, found, err
}

// originalColumns splits a domain.Original into the three nullable columns it
// is stored as. Nil answers three nulls, which is every document that is not
// a PDF or an Office file kept in the configured bucket.
func originalColumns(original *domain.Original) (bucket, key *string, size *int64) {
	if original == nil {
		return nil, nil, nil
	}
	return &original.Bucket, &original.Key, &original.Size
}

// ExcludeDocument records a document judged not worth indexing. Its row is
// written, text and judgment included, so the same content is not judged
// again and a member who later chooses to index its kind can have it without
// reading it from the source again; but without its original, and removed
// with document.Excluded as the reason. One that was
// live until now is retracted: event's removal is written in the same
// transaction, and ExcludeDocument answers true so the caller can delete the
// original it held once this commits.
func (store *Store) ExcludeDocument(ctx context.Context, document domain.Document, now time.Time, event func(domain.Document) (domain.Event, error)) (bool, error) {
	metadata, err := encodeMap(document.Metadata)
	if err != nil {
		return false, err
	}
	retracted := false
	err = store.inTx(ctx, func(tx pgx.Tx) error {
		var existingID string
		var removedAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT id, removed_at
			FROM knowledge_documents
			WHERE source_id = $1 AND external_id = $2
			FOR UPDATE`, document.SourceID, document.ExternalID).
			Scan(&existingID, &removedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read document: %w", err)
		}
		live := existingID != "" && removedAt == nil
		if existingID != "" {
			document.ID = existingID
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO knowledge_documents (
				id, organization_id, source_id, external_id, kind, title, url, mime,
				content_hash, body, byte_size, metadata, source_updated_at, extracted_at,
				last_seen_at, removed_at, excluded_reason, judged_policy, judgment
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $17, $10, $11, $12, $13, $14, $14, $15, $16, $18)
			ON CONFLICT (source_id, external_id) DO UPDATE SET
				kind = excluded.kind,
				title = excluded.title,
				url = excluded.url,
				mime = excluded.mime,
				content_hash = excluded.content_hash,
				body = excluded.body,
				byte_size = excluded.byte_size,
				original_bucket = NULL,
				original_key = NULL,
				original_size = NULL,
				metadata = excluded.metadata,
				source_updated_at = excluded.source_updated_at,
				extracted_at = excluded.extracted_at,
				last_seen_at = excluded.last_seen_at,
				removed_at = coalesce(knowledge_documents.removed_at, excluded.removed_at),
				excluded_reason = excluded.excluded_reason,
				judged_policy = excluded.judged_policy,
				judgment = excluded.judgment`,
			document.ID, document.OrganizationID, document.SourceID, document.ExternalID, document.Kind,
			document.Title, document.URL, document.MIME, document.ContentHash, document.ByteSize, metadata,
			document.SourceUpdatedAt, document.ExtractedAt, now, document.Excluded, document.JudgedPolicy,
			document.Body, judgmentColumn(document.Judgment)); err != nil {
			return mapWriteError("exclude document", err)
		}
		if !live || event == nil {
			return nil
		}
		recorded, err := event(document)
		if err != nil {
			return err
		}
		if err := insertEvents(ctx, tx, recorded); err != nil {
			return err
		}
		retracted = true
		return nil
	})
	return retracted, err
}

// TouchDocument records that a run saw a document without re-reading it, so an
// incremental pass does not retract what it simply did not revisit.
func (store *Store) TouchDocument(ctx context.Context, sourceID, externalID string, seenAt time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE knowledge_documents
		SET last_seen_at = $3
		WHERE source_id = $1 AND external_id = $2`, sourceID, externalID, seenAt); err != nil {
		return fmt.Errorf("touch document: %w", err)
	}
	return nil
}

// RetractUnseen forgets the documents a full pass did not find, writing one
// event for each. Only a run that read the whole source may call it: an
// incremental pass has not looked at everything.
func (store *Store) RetractUnseen(ctx context.Context, sourceID string, before time.Time, now time.Time, event func(domain.Document) (domain.Event, error)) (int, error) {
	retracted := 0
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		documents, err := queryDocuments(ctx, tx, `
			UPDATE knowledge_documents
			SET removed_at = $3, body = ''
			WHERE source_id = $1 AND removed_at IS NULL AND last_seen_at < $2
			RETURNING `+documentColumns, sourceID, before, now)
		if err != nil {
			return err
		}
		for _, document := range documents {
			if event == nil {
				continue
			}
			recorded, err := event(document)
			if err != nil {
				return err
			}
			if err := insertEvents(ctx, tx, recorded); err != nil {
				return err
			}
		}
		retracted = len(documents)
		return nil
	})
	return retracted, err
}

// retractDocuments forgets everything a source held, for a source that is
// itself going away. The caller writes the events, inside the same
// transaction that removed the source.
func retractDocuments(ctx context.Context, tx pgx.Tx, sourceID string, now time.Time) ([]domain.Document, error) {
	return queryDocuments(ctx, tx, `
		UPDATE knowledge_documents
		SET removed_at = $2, body = ''
		WHERE source_id = $1 AND removed_at IS NULL
		RETURNING `+documentColumns, sourceID, now)
}

// Document reads one document with its body, for the caller fetching what an
// event was too small to carry.
func (store *Store) Document(ctx context.Context, organizationID, documentID string) (domain.Document, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT `+documentColumns+`, body
		FROM knowledge_documents
		WHERE organization_id = $1 AND id = $2`, organizationID, documentID)
	if err != nil {
		return domain.Document{}, fmt.Errorf("read document: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.Document{}, fmt.Errorf("read document: %w", err)
		}
		return domain.Document{}, domain.ErrNotFound
	}
	document, err := scanDocument(rows, true)
	if err != nil {
		return domain.Document{}, err
	}
	return document, nil
}

// Cursor is where the source's last run stopped. A source that has never run
// answers a zero cursor, which reads as "start at the beginning".
func (store *Store) Cursor(ctx context.Context, sourceID string) (domain.Cursor, error) {
	cursor := domain.Cursor{SourceID: sourceID}
	err := store.pool.QueryRow(ctx, `
		SELECT value, watermark, token, walk_started_at, updated_at
		FROM knowledge_cursors
		WHERE source_id = $1`, sourceID).
		Scan(&cursor.Value, &cursor.Watermark, &cursor.Token, &cursor.WalkStartedAt, &cursor.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return cursor, nil
	}
	if err != nil {
		return domain.Cursor{}, fmt.Errorf("read cursor: %w", err)
	}
	cursor.UpdatedAt = cursor.UpdatedAt.UTC()
	return cursor, nil
}

// SaveCursor records where a run has reached. It is written per page, so a run
// that dies resumes rather than reading the whole source again. It writes
// exactly the cursor it is given, walk_started_at included: the caller
// carries that forward itself while a walk is under way and leaves it unset
// once one ends, so there is nothing here for a previous value to survive.
func (store *Store) SaveCursor(ctx context.Context, cursor domain.Cursor) error {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO knowledge_cursors (source_id, value, watermark, token, walk_started_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (source_id) DO UPDATE SET
			value = excluded.value,
			watermark = excluded.watermark,
			token = excluded.token,
			walk_started_at = excluded.walk_started_at,
			updated_at = excluded.updated_at`,
		cursor.SourceID, cursor.Value, cursor.Watermark, cursor.Token, cursor.WalkStartedAt, cursor.UpdatedAt); err != nil {
		return mapWriteError("save cursor", err)
	}
	return nil
}

// ClearCursor starts the next run from the beginning, for a source whose
// incremental position no longer means anything.
func (store *Store) ClearCursor(ctx context.Context, sourceID string) error {
	if _, err := store.pool.Exec(ctx, `DELETE FROM knowledge_cursors WHERE source_id = $1`, sourceID); err != nil {
		return fmt.Errorf("clear cursor: %w", err)
	}
	return nil
}

func queryDocuments(ctx context.Context, db querier, sql string, args ...any) ([]domain.Document, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("query documents", err)
	}
	defer rows.Close()
	documents := []domain.Document{}
	for rows.Next() {
		document, err := scanDocument(rows, false)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query documents: %w", err)
	}
	return documents, nil
}

func scanDocument(rows pgx.Rows, withBody bool) (domain.Document, error) {
	var document domain.Document
	var metadata []byte
	var originalBucket, originalKey *string
	var originalSize *int64
	targets := []any{&document.ID, &document.OrganizationID, &document.SourceID, &document.ExternalID,
		&document.Kind, &document.Title, &document.URL, &document.MIME, &document.ContentHash,
		&document.ByteSize, &originalBucket, &originalKey, &originalSize, &metadata, &document.SourceUpdatedAt,
		&document.ExtractedAt, &document.LastSeenAt, &document.RemovedAt, &document.Excluded, &document.JudgedPolicy,
		&document.Judgment}
	if withBody {
		targets = append(targets, &document.Body)
	}
	if err := rows.Scan(targets...); err != nil {
		return domain.Document{}, fmt.Errorf("scan document: %w", err)
	}
	decoded, err := decodeMap(metadata)
	if err != nil {
		return domain.Document{}, err
	}
	document.Metadata = decoded
	document.ExtractedAt = document.ExtractedAt.UTC()
	document.LastSeenAt = document.LastSeenAt.UTC()
	if originalBucket != nil && originalKey != nil {
		size := int64(0)
		if originalSize != nil {
			size = *originalSize
		}
		document.Original = &domain.Original{Bucket: *originalBucket, Key: *originalKey, Size: size, SHA256: document.ContentHash}
	}
	return document, nil
}

// judgmentColumn is a judgment as the jsonb column takes it: NULL for none.
func judgmentColumn(judgment []byte) any {
	if len(judgment) == 0 {
		return nil
	}
	return string(judgment)
}

// DocumentsToRedecide pages through a source's documents that a judge
// decided, live or excluded, with their text and judgments, after the one
// named after.
func (store *Store) DocumentsToRedecide(ctx context.Context, sourceID, after string, limit int) ([]domain.Document, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT `+documentColumns+`, body
		FROM knowledge_documents
		WHERE source_id = $1 AND id > $2 AND judgment IS NOT NULL
		  AND (removed_at IS NULL OR excluded_reason <> '')
		ORDER BY id
		LIMIT $3`, sourceID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read documents to decide again: %w", err)
	}
	defer rows.Close()
	documents := []domain.Document{}
	for rows.Next() {
		document, err := scanDocument(rows, true)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read documents to decide again: %w", err)
	}
	return documents, nil
}

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
	content_hash, byte_size, metadata, source_updated_at, extracted_at, last_seen_at, removed_at`

// UpsertDocument writes what a run read, and the event saying so only when the
// content actually changed. It answers whether anything was published, so a
// pass over an unchanged source publishes nothing.
func (store *Store) UpsertDocument(ctx context.Context, document domain.Document, event func(domain.Document) (domain.Event, error)) (bool, error) {
	metadata, err := encodeMap(document.Metadata)
	if err != nil {
		return false, err
	}
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
				content_hash, body, byte_size, metadata, source_updated_at, extracted_at, last_seen_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)
			ON CONFLICT (source_id, external_id) DO UPDATE SET
				kind = excluded.kind,
				title = excluded.title,
				url = excluded.url,
				mime = excluded.mime,
				content_hash = excluded.content_hash,
				body = excluded.body,
				byte_size = excluded.byte_size,
				metadata = excluded.metadata,
				source_updated_at = excluded.source_updated_at,
				extracted_at = excluded.extracted_at,
				last_seen_at = excluded.last_seen_at,
				removed_at = NULL`,
			document.ID, document.OrganizationID, document.SourceID, document.ExternalID, document.Kind,
			document.Title, document.URL, document.MIME, document.ContentHash, document.Body,
			document.ByteSize, metadata, document.SourceUpdatedAt, document.ExtractedAt); err != nil {
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
		SELECT value, watermark, walk_started_at, updated_at
		FROM knowledge_cursors
		WHERE source_id = $1`, sourceID).Scan(&cursor.Value, &cursor.Watermark, &cursor.WalkStartedAt, &cursor.UpdatedAt)
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
		INSERT INTO knowledge_cursors (source_id, value, watermark, walk_started_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (source_id) DO UPDATE SET
			value = excluded.value,
			watermark = excluded.watermark,
			walk_started_at = excluded.walk_started_at,
			updated_at = excluded.updated_at`,
		cursor.SourceID, cursor.Value, cursor.Watermark, cursor.WalkStartedAt, cursor.UpdatedAt); err != nil {
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
	targets := []any{&document.ID, &document.OrganizationID, &document.SourceID, &document.ExternalID,
		&document.Kind, &document.Title, &document.URL, &document.MIME, &document.ContentHash,
		&document.ByteSize, &metadata, &document.SourceUpdatedAt, &document.ExtractedAt,
		&document.LastSeenAt, &document.RemovedAt}
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
	return document, nil
}

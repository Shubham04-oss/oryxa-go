package postgres

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

const chunkColumns = `
	id, document_id, corpus_id, organization_id, ordinal, body, token_count,
	heading_path, start_byte, end_byte, sha256, metadata`

type chunkRow struct {
	ID             string   `db:"id"`
	DocumentID     string   `db:"document_id"`
	CorpusID       string   `db:"corpus_id"`
	OrganizationID string   `db:"organization_id"`
	Ordinal        int      `db:"ordinal"`
	Body           string   `db:"body"`
	TokenCount     int      `db:"token_count"`
	HeadingPath    []string `db:"heading_path"`
	StartByte      int      `db:"start_byte"`
	EndByte        int      `db:"end_byte"`
	SHA256         string   `db:"sha256"`
	Metadata       []byte   `db:"metadata"`
}

func (row chunkRow) chunk() (domain.Chunk, error) {
	metadata, err := decodeMetadata(row.Metadata)
	if err != nil {
		return domain.Chunk{}, err
	}
	return domain.Chunk{
		ID:             row.ID,
		DocumentID:     row.DocumentID,
		CorpusID:       row.CorpusID,
		OrganizationID: row.OrganizationID,
		Ordinal:        row.Ordinal,
		Text:           row.Body,
		TokenCount:     row.TokenCount,
		HeadingPath:    row.HeadingPath,
		StartByte:      row.StartByte,
		EndByte:        row.EndByte,
		SHA256:         row.SHA256,
		Metadata:       metadata,
	}, nil
}

// SyncChunks replaces a document's chunks with a freshly cut set, keeping the
// ones whose text did not change.
//
// This is the difference between a one-word edit costing one embedding call
// and costing as many as the document has chunks. A chunk is matched by the
// hash of its text, so a paragraph that only moved keeps its vector even
// though its position changed.
//
// The caller supplies chunks without ids; this assigns them, reusing the id of
// a matching chunk so its vector row stays attached.
//
// A chunk whose text is unchanged but whose metadata is not — the document
// was reclassified — keeps its id too, but is returned as Fresh: the vector
// written for it still carries the old metadata, which a filtered search
// reads. Its row is marked unembedded until the caller writes that vector
// again, so an attempt that stops first leaves it to be embedded afresh
// rather than looking settled. Keeping the id is what keeps its vector
// searchable meanwhile, and what lets a vector store that can hand the vector
// back rewrite it without embedding the text again.
func (store *Store) SyncChunks(ctx context.Context, document domain.Document, chunks []domain.Chunk, at time.Time) (domain.ChunkSync, error) {
	var result domain.ChunkSync
	err := store.inTx(ctx, func(tx *sqlx.Tx) error {
		var placement struct {
			CorpusID       string `db:"corpus_id"`
			OrganizationID string `db:"organization_id"`
		}
		if err := tx.GetContext(ctx, &placement, `
			SELECT corpus_id, organization_id FROM rag_documents WHERE id = $1 FOR KEY SHARE`, document.ID); err != nil {
			if isNoRows(err) {
				return domain.ErrConflict
			}
			return fmt.Errorf("read chunk's document: %w", err)
		}
		if placement.CorpusID != document.CorpusID || placement.OrganizationID != document.OrganizationID {
			return domain.ErrConflict
		}
		var existing []struct {
			ID         string     `db:"id"`
			SHA256     string     `db:"sha256"`
			Metadata   []byte     `db:"metadata"`
			EmbeddedAt *time.Time `db:"embedded_at"`
		}
		if err := tx.SelectContext(ctx, &existing, `
			SELECT id, sha256, metadata, embedded_at FROM rag_chunks WHERE document_id = $1 ORDER BY ordinal
			FOR UPDATE`, document.ID); err != nil {
			return fmt.Errorf("read existing chunks: %w", err)
		}
		// Only a chunk whose vector was actually written can be reused.
		// Matching on the text hash alone is not enough: a first attempt
		// writes every chunk row and may then fail while embedding them, and
		// on the retry those rows would look reusable and the document would
		// be marked searchable holding no vectors at all.
		//
		// Chunks with identical text are rare but possible — a repeated
		// boilerplate paragraph — so each hash holds a queue of ids rather
		// than one, and each is claimed at most once.
		type reusableChunk struct {
			id       string
			metadata domain.Metadata
		}
		available := make(map[string][]reusableChunk, len(existing))
		for _, row := range existing {
			if row.EmbeddedAt == nil {
				continue
			}
			metadata, err := decodeMetadata(row.Metadata)
			if err != nil {
				return err
			}
			available[row.SHA256] = append(available[row.SHA256], reusableChunk{id: row.ID, metadata: metadata})
		}

		result = domain.ChunkSync{}
		claimed := make(map[string]bool, len(existing))
		// take claims an embedded chunk with this text for chunk: one whose
		// metadata is the same when sameMetadata, any otherwise.
		take := func(chunk *domain.Chunk, sameMetadata bool) bool {
			choices := available[chunk.SHA256]
			for i, choice := range choices {
				if sameMetadata && !maps.Equal(choice.metadata, chunk.Metadata) {
					continue
				}
				chunk.ID = choice.id
				available[chunk.SHA256] = append(choices[:i], choices[i+1:]...)
				claimed[chunk.ID] = true
				return true
			}
			return false
		}
		// Exact matches are settled for every chunk before any metadata-only
		// match, so a chunk is never relabelled when another with the same
		// text could have kept its vector as it is.
		reused := make([]bool, len(chunks))
		for index := range chunks {
			reused[index] = take(&chunks[index], true)
		}
		var relabelled []string
		for index := range chunks {
			chunk := &chunks[index]
			switch {
			case reused[index]:
				result.Reused = append(result.Reused, *chunk)
				continue
			case take(chunk, false):
				relabelled = append(relabelled, chunk.ID)
			case chunk.ID == "":
				id, err := domain.NewID(domain.ChunkIDPrefix)
				if err != nil {
					return err
				}
				chunk.ID = id
			}
			result.Fresh = append(result.Fresh, *chunk)
		}
		for _, row := range existing {
			if !claimed[row.ID] {
				result.Removed = append(result.Removed, row.ID)
			}
		}

		if len(result.Removed) > 0 {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM rag_chunks WHERE document_id = $1 AND id = ANY($2)`,
				document.ID, pgTextArray(result.Removed)); err != nil {
				return fmt.Errorf("delete stale chunks: %w", err)
			}
		}
		if len(relabelled) > 0 {
			if _, err := tx.ExecContext(ctx, `
				UPDATE rag_chunks SET embedded_at = NULL WHERE document_id = $1 AND id = ANY($2)`,
				document.ID, pgTextArray(relabelled)); err != nil {
				return fmt.Errorf("mark relabelled chunks unembedded: %w", err)
			}
		}
		// Ordinals are unique per document and a re-cut moves chunks between
		// them, so one row vacating an ordinal that another takes is a normal
		// step rather than a conflict. Deferring the constraint asks the
		// question at commit, when the rewrite is whole.
		if _, err := tx.ExecContext(ctx, `SET CONSTRAINTS rag_chunks_ordinal_unique DEFERRED`); err != nil {
			return fmt.Errorf("defer the chunk ordinal constraint: %w", err)
		}
		for _, chunk := range chunks {
			metadata, err := encodeMetadata(chunk.Metadata)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO rag_chunks (
					id, document_id, corpus_id, organization_id, ordinal, body, token_count,
					heading_path, heading_trail, start_byte, end_byte, sha256, metadata, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
				ON CONFLICT (id) DO UPDATE SET
					ordinal = EXCLUDED.ordinal,
					body = EXCLUDED.body,
					token_count = EXCLUDED.token_count,
					heading_path = EXCLUDED.heading_path,
					heading_trail = EXCLUDED.heading_trail,
					start_byte = EXCLUDED.start_byte,
					end_byte = EXCLUDED.end_byte,
					metadata = EXCLUDED.metadata`,
				chunk.ID, document.ID, document.CorpusID, document.OrganizationID, chunk.Ordinal,
				chunk.Text, chunk.TokenCount, pgTextArray(chunk.HeadingPath),
				strings.Join(chunk.HeadingPath, " > "),
				chunk.StartByte, chunk.EndByte, chunk.SHA256, metadata, at); err != nil {
				return mapWriteError("upsert chunk", err)
			}
		}
		return nil
	})
	if err != nil {
		return domain.ChunkSync{}, err
	}
	return result, nil
}

// Chunks reads chunks by id, scoped to the corpus, and returns them in the
// order the ids were given. Retrieval has already ranked them, and re-sorting
// here would throw that away.
func (store *Store) Chunks(ctx context.Context, organizationID, corpusID string, ids []string) ([]domain.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []chunkRow
	if err := store.db.SelectContext(ctx, &rows, `
		SELECT `+chunkColumns+`
		FROM rag_chunks
		WHERE organization_id = $1 AND corpus_id = $2 AND id = ANY($3)`,
		organizationID, corpusID, pgTextArray(ids)); err != nil {
		return nil, fmt.Errorf("read chunks: %w", err)
	}
	byID := make(map[string]domain.Chunk, len(rows))
	for _, row := range rows {
		chunk, err := row.chunk()
		if err != nil {
			return nil, err
		}
		byID[chunk.ID] = chunk
	}
	chunks := make([]domain.Chunk, 0, len(ids))
	for _, id := range ids {
		if chunk, found := byID[id]; found {
			chunks = append(chunks, chunk)
		}
	}
	return chunks, nil
}

// DocumentRefs reads the little about each document that travels with a match,
// so a citation can be rendered without a second round trip per result.
func (store *Store) DocumentRefs(ctx context.Context, organizationID, corpusID string, ids []string, includeAdminOnly bool) (map[string]domain.Ref, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []struct {
		ID         string `db:"id"`
		Title      string `db:"title"`
		SourceKind string `db:"source_kind"`
		SourceRef  string `db:"source_ref"`
		Metadata   []byte `db:"metadata"`
	}
	if err := store.db.SelectContext(ctx, &rows, `
		SELECT id, title, source_kind, source_ref, metadata
		FROM rag_documents
		WHERE organization_id = $1 AND corpus_id = $2 AND id = ANY($3)
		  AND ($4 OR access = 'members')`,
		organizationID, corpusID, pgTextArray(ids), includeAdminOnly); err != nil {
		return nil, fmt.Errorf("read document references: %w", err)
	}
	refs := make(map[string]domain.Ref, len(rows))
	for _, row := range rows {
		metadata, err := decodeMetadata(row.Metadata)
		if err != nil {
			return nil, err
		}
		refs[row.ID] = domain.Ref{
			ID:       row.ID,
			Title:    row.Title,
			Source:   domain.Source{Kind: domain.SourceKind(row.SourceKind), Ref: row.SourceRef},
			Metadata: metadata,
		}
	}
	return refs, nil
}

// LexicalSearch is the other half of hybrid retrieval.
//
// It exists because vectors are bad at exactly what full-text search is good
// at: an error code, an account number, a surname, a flag spelled exactly one
// way. A dense retriever will happily return a passage about something
// adjacent; this one returns the passage that contains the string.
//
// websearch_to_tsquery is used rather than plainto_tsquery because it accepts
// what people actually type — quoted phrases, and a leading minus to exclude —
// and because it never errors on punctuation the way to_tsquery does.
func (store *Store) LexicalSearch(ctx context.Context, corpus domain.Corpus, text string, limit int, filter domain.Filter, includeAdminOnly bool) ([]domain.ScoredChunk, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	arguments := []any{corpus.OrganizationID, corpus.ID, text, includeAdminOnly}
	// A chunk matches on any of the question's words, and ranks by how many it
	// holds and how close together. A question worded the way a person asks
	// it rarely has every one of its words in one passage, and requiring them
	// all — as a search box does — left the lexical half finding nothing for
	// most questions. plainto_tsquery quotes each lexeme, so turning its ANDs
	// into ORs cannot change what the words are.
	statement := `
		WITH asked AS (
			SELECT replace(plainto_tsquery('english', $3)::text, ' & ', ' | ')::tsquery AS query
		)
		SELECT id, ts_rank_cd(search_vector, asked.query) AS score
		FROM rag_chunks, asked
		WHERE organization_id = $1 AND corpus_id = $2 AND search_vector @@ asked.query
		  AND ($4 OR EXISTS (SELECT 1 FROM rag_documents AS acl_document
		                    WHERE acl_document.id = rag_chunks.document_id AND acl_document.access = 'members'))`
	if len(filter.DocumentIDs) > 0 {
		arguments = append(arguments, pgTextArray(filter.DocumentIDs))
		statement += fmt.Sprintf(` AND document_id = ANY($%d)`, len(arguments))
	}
	for _, condition := range filter.Conditions {
		arguments = append(arguments, condition.Key)
		key := len(arguments)
		switch condition.Operator {
		case domain.OperatorEqual:
			arguments = append(arguments, condition.Values[0])
			statement += fmt.Sprintf(` AND metadata->>$%d = $%d`, key, len(arguments))
		case domain.OperatorNotEqual:
			arguments = append(arguments, condition.Values[0])
			statement += fmt.Sprintf(` AND metadata->>$%d IS DISTINCT FROM $%d`, key, len(arguments))
		case domain.OperatorIn:
			arguments = append(arguments, pgTextArray(condition.Values))
			statement += fmt.Sprintf(` AND metadata->>$%d = ANY($%d)`, key, len(arguments))
		case domain.OperatorNotIn:
			arguments = append(arguments, pgTextArray(condition.Values))
			statement += fmt.Sprintf(` AND (metadata->>$%d IS NULL OR NOT (metadata->>$%d = ANY($%d)))`, key, key, len(arguments))
		default:
			return nil, fmt.Errorf("filter operator %q is not allowed: %w", condition.Operator, domain.ErrInvalid)
		}
	}
	arguments = append(arguments, limit)
	statement += fmt.Sprintf(` ORDER BY score DESC, id LIMIT $%d`, len(arguments))

	var rows []struct {
		ID    string  `db:"id"`
		Score float64 `db:"score"`
	}
	if err := store.db.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}
	hits := make([]domain.ScoredChunk, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, domain.ScoredChunk{ChunkID: row.ID, Score: row.Score})
	}
	return hits, nil
}

// pgTextArray renders a Go slice as a PostgreSQL text array literal.
//
// The sqlx path runs over database/sql, where a []string is not a value a
// driver accepts. Rendering it here keeps every value a bound parameter — the
// array is one parameter, not a list of them spliced into the statement.
func pgTextArray(values []string) string {
	if len(values) == 0 {
		return "{}"
	}
	var builder strings.Builder
	builder.WriteByte('{')
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteByte('"')
		builder.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value))
		builder.WriteByte('"')
	}
	builder.WriteByte('}')
	return builder.String()
}

// MarkChunksEmbedded records that these chunks' vectors are in the store.
//
// It runs after the vectors are written, never before, because it is what a
// later re-cut trusts when it decides a chunk need not be paid for again.
func (store *Store) MarkChunksEmbedded(ctx context.Context, chunkIDs []string, at time.Time) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE rag_chunks SET embedded_at = $2 WHERE id = ANY($1)`,
		pgTextArray(chunkIDs), at); err != nil {
		return fmt.Errorf("mark chunks embedded: %w", err)
	}
	return nil
}

// ChunkIDs lists a document's chunks, so a caller can take their vectors out
// of a store that has no cascade of its own before the rows go.
func (store *Store) ChunkIDs(ctx context.Context, organizationID, documentID string) ([]string, error) {
	var ids []string
	if err := store.db.SelectContext(ctx, &ids, `
		SELECT id FROM rag_chunks
		WHERE organization_id = $1 AND document_id = $2
		ORDER BY ordinal`, organizationID, documentID); err != nil {
		return nil, fmt.Errorf("list chunk ids: %w", err)
	}
	return ids, nil
}

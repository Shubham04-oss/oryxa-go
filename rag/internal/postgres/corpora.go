package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

const corpusColumns = `
	id, organization_id, slug, name, scope,
	COALESCE(team_id, '') AS team_id, COALESCE(member_id, '') AS member_id,
	embedding_provider, embedding_model, embedding_dimensions, embedding_metric,
	backend, chunk_strategy, chunk_max_tokens, chunk_overlap_tokens,
	metadata, created_by, created_at, updated_at`

type corpusRow struct {
	ID                  string    `db:"id"`
	OrganizationID      string    `db:"organization_id"`
	Slug                string    `db:"slug"`
	Name                string    `db:"name"`
	Scope               string    `db:"scope"`
	TeamID              string    `db:"team_id"`
	MemberID            string    `db:"member_id"`
	EmbeddingProvider   string    `db:"embedding_provider"`
	EmbeddingModel      string    `db:"embedding_model"`
	EmbeddingDimensions int       `db:"embedding_dimensions"`
	EmbeddingMetric     string    `db:"embedding_metric"`
	Backend             string    `db:"backend"`
	ChunkStrategy       string    `db:"chunk_strategy"`
	ChunkMaxTokens      int       `db:"chunk_max_tokens"`
	ChunkOverlapTokens  int       `db:"chunk_overlap_tokens"`
	Metadata            []byte    `db:"metadata"`
	CreatedBy           string    `db:"created_by"`
	CreatedAt           time.Time `db:"created_at"`
	UpdatedAt           time.Time `db:"updated_at"`
}

func (row corpusRow) corpus() (domain.Corpus, error) {
	metadata, err := decodeMetadata(row.Metadata)
	if err != nil {
		return domain.Corpus{}, err
	}
	return domain.Corpus{
		ID:             row.ID,
		OrganizationID: row.OrganizationID,
		Slug:           row.Slug,
		Name:           row.Name,
		Scope:          domain.Scope(row.Scope),
		TeamID:         row.TeamID,
		MemberID:       row.MemberID,
		Spec: domain.EmbeddingSpec{
			Provider:   domain.Provider(row.EmbeddingProvider),
			Model:      row.EmbeddingModel,
			Dimensions: row.EmbeddingDimensions,
			Metric:     domain.Metric(row.EmbeddingMetric),
		},
		Backend: domain.Backend(row.Backend),
		Chunking: domain.ChunkPolicy{
			Strategy:      domain.Strategy(row.ChunkStrategy),
			MaxTokens:     row.ChunkMaxTokens,
			OverlapTokens: row.ChunkOverlapTokens,
		},
		Metadata:  metadata,
		CreatedBy: row.CreatedBy,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// CreateCorpus records a corpus and the event announcing it in one
// transaction, so nothing can exist without having been announced.
func (store *Store) CreateCorpus(ctx context.Context, corpus domain.Corpus, event domain.Event) error {
	metadata, err := encodeMetadata(corpus.Metadata)
	if err != nil {
		return err
	}
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO rag_corpora (
				id, organization_id, slug, name, scope, team_id, member_id,
				embedding_provider, embedding_model, embedding_dimensions, embedding_metric,
				backend, chunk_strategy, chunk_max_tokens, chunk_overlap_tokens,
				metadata, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''),
				$8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $18)`,
			corpus.ID, corpus.OrganizationID, corpus.Slug, corpus.Name, corpus.Scope,
			corpus.TeamID, corpus.MemberID,
			corpus.Spec.Provider, corpus.Spec.Model, corpus.Spec.Dimensions, corpus.Spec.Metric,
			corpus.Backend, corpus.Chunking.Strategy, corpus.Chunking.MaxTokens, corpus.Chunking.OverlapTokens,
			metadata, corpus.CreatedBy, corpus.CreatedAt); err != nil {
			return mapWriteError("insert corpus", err)
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

// Corpus reads one corpus. The organization is part of the lookup rather than
// checked afterwards, so a corpus in another organization reads as missing.
func (store *Store) Corpus(ctx context.Context, organizationID, corpusID string) (domain.Corpus, error) {
	var row corpusRow
	err := store.db.GetContext(ctx, &row, `
		SELECT `+corpusColumns+`
		FROM rag_corpora
		WHERE organization_id = $1 AND id = $2`, organizationID, corpusID)
	if isNoRows(err) {
		return domain.Corpus{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Corpus{}, fmt.Errorf("read corpus: %w", err)
	}
	return row.corpus()
}

// CorpusBySlug lets a caller address a corpus by the name it chose.
func (store *Store) CorpusBySlug(ctx context.Context, organizationID, slug string) (domain.Corpus, error) {
	var row corpusRow
	err := store.db.GetContext(ctx, &row, `
		SELECT `+corpusColumns+`
		FROM rag_corpora
		WHERE organization_id = $1 AND lower(slug) = lower($2)`, organizationID, slug)
	if isNoRows(err) {
		return domain.Corpus{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Corpus{}, fmt.Errorf("read corpus by slug: %w", err)
	}
	return row.corpus()
}

// CorpusQuery narrows a listing to what the caller may see. It is built from
// the caller's membership, never from request parameters alone.
type CorpusQuery struct {
	Scope    domain.Scope
	TeamID   string
	MemberID string
	Admin    bool
}

// listedCorpusRow is a corpus as a listing reads it: with its counts, taken in
// the same statement.
type listedCorpusRow struct {
	corpusRow
	DocumentCount int64 `db:"document_count"`
	ChunkCount    int64 `db:"chunk_count"`
}

// Corpora lists what the caller can see, each with its document and chunk
// counts. A listing without them would read as a list of empty corpora, and a
// caller choosing which to search skips an empty one. With no scope named,
// that is their own member corpora plus the organization's, which mirrors how
// files lists.
func (store *Store) Corpora(ctx context.Context, organizationID string, query CorpusQuery) ([]domain.Corpus, error) {
	statement := `SELECT ` + corpusColumns + `,
		(SELECT count(*) FROM rag_documents AS d WHERE d.corpus_id = rag_corpora.id AND ($2 OR d.access = 'members')) AS document_count,
		(SELECT count(*) FROM rag_chunks AS c JOIN rag_documents AS d ON d.id = c.document_id
		 WHERE c.corpus_id = rag_corpora.id AND ($2 OR d.access = 'members')) AS chunk_count
		FROM rag_corpora WHERE organization_id = $1`
	arguments := []any{organizationID, query.Admin}
	switch query.Scope {
	case domain.ScopeTeam:
		arguments = append(arguments, query.TeamID)
		statement += fmt.Sprintf(` AND scope = 'team' AND team_id = $%d`, len(arguments))
	case domain.ScopeOrganization:
		statement += ` AND scope = 'organization'`
	case domain.ScopeMember:
		if query.Admin && query.MemberID == "" {
			statement += ` AND scope = 'member'`
		} else {
			arguments = append(arguments, query.MemberID)
			statement += fmt.Sprintf(` AND scope = 'member' AND member_id = $%d`, len(arguments))
		}
	default:
		arguments = append(arguments, query.MemberID)
		statement += fmt.Sprintf(` AND (scope = 'organization' OR (scope = 'member' AND member_id = $%d))`, len(arguments))
	}
	statement += ` ORDER BY created_at DESC, id`

	var rows []listedCorpusRow
	if err := store.db.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return nil, fmt.Errorf("list corpora: %w", err)
	}
	corpora := make([]domain.Corpus, 0, len(rows))
	for _, row := range rows {
		corpus, err := row.corpus()
		if err != nil {
			return nil, err
		}
		corpus.DocumentCount, corpus.ChunkCount = row.DocumentCount, row.ChunkCount
		corpora = append(corpora, corpus)
	}
	return corpora, nil
}

// CorpusCounts reads the document and chunk totals for one corpus, counted as
// Corpora counts each corpus it lists.
func (store *Store) CorpusCounts(ctx context.Context, corpusID string, includeAdminOnly bool) (documents, chunks int64, err error) {
	row := struct {
		Documents int64 `db:"documents"`
		Chunks    int64 `db:"chunks"`
	}{}
	if err := store.db.GetContext(ctx, &row, `
		SELECT
			(SELECT count(*) FROM rag_documents WHERE corpus_id = $1 AND ($2 OR access = 'members')) AS documents,
			(SELECT count(*) FROM rag_chunks AS c JOIN rag_documents AS d ON d.id = c.document_id
			 WHERE c.corpus_id = $1 AND ($2 OR d.access = 'members')) AS chunks`, corpusID, includeAdminOnly); err != nil {
		return 0, 0, fmt.Errorf("count corpus contents: %w", err)
	}
	return row.Documents, row.Chunks, nil
}

// DeleteCorpus removes a corpus and everything under it. The documents and
// chunks go with it by cascade, and so do the pgvector rows; an S3 Vectors
// index is dropped separately by the caller, which is why the event is
// written here in the same transaction.
func (store *Store) DeleteCorpus(ctx context.Context, organizationID, corpusID string, event domain.Event) error {
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM rag_corpora WHERE organization_id = $1 AND id = $2`, organizationID, corpusID)
		if err != nil {
			return fmt.Errorf("delete corpus: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete corpus: %w", err)
		}
		if affected == 0 {
			return domain.ErrNotFound
		}
		return insertEvents(ctx, tx, []domain.Event{event})
	})
}

// UpsertOrganization records what this service knows of an organization, from
// the auth events it consumes.
func (store *Store) UpsertOrganization(ctx context.Context, organizationID, name string, at time.Time) error {
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO rag_organizations (organization_id, name, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id) DO UPDATE
		SET name = EXCLUDED.name, updated_at = EXCLUDED.updated_at
		WHERE rag_organizations.updated_at <= EXCLUDED.updated_at`,
		organizationID, name, at); err != nil {
		return fmt.Errorf("upsert organization: %w", err)
	}
	return nil
}

// CorporaForWipe lists every corpus under an organization, or under one member
// or team of it, so their vectors can be removed from whichever backend holds
// them before the rows go.
func (store *Store) CorporaForWipe(ctx context.Context, organizationID string, scope domain.Scope, ownerID string) ([]domain.Corpus, error) {
	statement := `SELECT ` + corpusColumns + ` FROM rag_corpora WHERE organization_id = $1`
	arguments := []any{organizationID}
	switch scope {
	case domain.ScopeMember:
		arguments = append(arguments, ownerID)
		statement += fmt.Sprintf(` AND scope = 'member' AND member_id = $%d`, len(arguments))
	case domain.ScopeTeam:
		arguments = append(arguments, ownerID)
		statement += fmt.Sprintf(` AND scope = 'team' AND team_id = $%d`, len(arguments))
	}
	var rows []corpusRow
	if err := store.db.SelectContext(ctx, &rows, statement, arguments...); err != nil {
		return nil, fmt.Errorf("list corpora to wipe: %w", err)
	}
	corpora := make([]domain.Corpus, 0, len(rows))
	for _, row := range rows {
		corpus, err := row.corpus()
		if err != nil {
			return nil, err
		}
		corpora = append(corpora, corpus)
	}
	return corpora, nil
}

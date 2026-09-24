package vectorstore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// Widths are the vector widths that have a table of their own.
//
// pgvector needs a fixed width on the column to build an index on it, so one
// table per width is what makes an indexed search possible at all. These cover
// every model in the catalog, plus the sizes the common open-source encoders
// emit. A width not listed here still works — see otherTable — it is simply
// not indexed.
var Widths = []int{256, 384, 512, 768, 1024, 1536, 3072}

// The two ceilings pgvector puts on an HNSW index, which is what decides the
// column type per width. Both are measured against pgvector 0.8, not inferred:
// vector stops at 2000, halfvec at 4000. So 3072, which is what
// gemini-embedding-001 emits, can only be indexed as halfvec — that is half
// the storage, and for the unit-length vectors these models return the
// precision difference does not show up in ranking. A width above 4000 cannot
// be indexed at all and belongs in otherTable.
const (
	maxIndexedVectorWidth = 2000
	maxIndexedHalfWidth   = 4000
)

// otherTable holds widths with no table of their own. Its column has no
// declared width, so it cannot be indexed and searches it exactly. That is
// slow on a large corpus and correct on any, which is the right way round for
// a fallback.
const otherTable = "rag_vectors_other"

// Table is where a corpus of this width keeps its vectors.
func Table(dimensions int) string {
	if slices.Contains(Widths, dimensions) {
		return "rag_vectors_" + strconv.Itoa(dimensions)
	}
	return otherTable
}

// Indexed reports whether searches of this width use an index. The ingest path
// logs it once per corpus, so nobody has to work out from a slow query that
// their model's width has no table.
func Indexed(dimensions int) bool { return slices.Contains(Widths, dimensions) }

// HalfPrecision reports whether a width is stored as halfvec.
func HalfPrecision(dimensions int) bool {
	return dimensions > maxIndexedVectorWidth && dimensions <= maxIndexedHalfWidth &&
		slices.Contains(Widths, dimensions)
}

// Pgvector keeps vectors in the shared PostgreSQL.
type Pgvector struct {
	pool *pgxpool.Pool
	// iterativeScan turns on pgvector 0.8's iterative index scan. It matters
	// for exactly the case this service always has: a search that also
	// filters, whether by the caller's metadata filter or by a member's
	// access. Without it the index returns its nearest ef_search rows and the
	// filter is applied afterwards, so a narrow filter can return far fewer
	// rows than asked for, or none, from a corpus that plainly contains them.
	iterativeScan bool
}

// NewPgvector builds the store. The extension version decides whether
// iterative scan is available; asking for it on an older pgvector aborts the
// transaction, so it is settled once here rather than per query.
func NewPgvector(pool *pgxpool.Pool, extensionVersion string) *Pgvector {
	return &Pgvector{pool: pool, iterativeScan: atLeastVersion(extensionVersion, 0, 8)}
}

func (store *Pgvector) Backend() domain.Backend { return domain.BackendPgvector }

func (store *Pgvector) Upsert(ctx context.Context, corpus domain.Corpus, vectors []domain.Vector) error {
	if len(vectors) == 0 {
		return nil
	}
	if err := validate(corpus, vectors); err != nil {
		return err
	}
	statement := fmt.Sprintf(`
		INSERT INTO %s (chunk_id, corpus_id, organization_id, document_id, embedding, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (chunk_id) DO UPDATE SET
			corpus_id = EXCLUDED.corpus_id,
			organization_id = EXCLUDED.organization_id,
			document_id = EXCLUDED.document_id,
			embedding = EXCLUDED.embedding,
			metadata = EXCLUDED.metadata`, Table(corpus.Spec.Dimensions))

	batch := &pgx.Batch{}
	for _, vector := range vectors {
		metadata, err := json.Marshal(vector.Metadata)
		if err != nil {
			return fmt.Errorf("encode vector metadata: %w", err)
		}
		batch.Queue(statement, vector.ChunkID, corpus.ID, corpus.OrganizationID, vector.DocumentID,
			embeddingValue(corpus.Spec.Dimensions, vector.Values), metadata)
	}
	results := store.pool.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()
	for range vectors {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("upsert vectors: %w", err)
		}
	}
	return results.Close()
}

func (store *Pgvector) Search(ctx context.Context, corpus domain.Corpus, search Search) ([]Hit, error) {
	if len(search.Vector) != corpus.Spec.Dimensions {
		return nil, fmt.Errorf("the query vector is %d wide but corpus %s is %d: %w",
			len(search.Vector), corpus.ID, corpus.Spec.Dimensions, domain.ErrConflict)
	}
	if search.TopK < 1 {
		return nil, fmt.Errorf("top_k must be at least 1: %w", domain.ErrInvalid)
	}
	operator, err := distanceOperator(corpus.Spec.Metric)
	if err != nil {
		return nil, err
	}
	arguments := []any{embeddingValue(corpus.Spec.Dimensions, search.Vector), corpus.ID, corpus.OrganizationID}
	where := []string{"corpus_id = $2", "organization_id = $3"}
	if !search.IncludeAdminOnly {
		where = append(where, `EXISTS (SELECT 1 FROM rag_chunks AS acl_chunk
			JOIN rag_documents AS acl_document ON acl_document.id = acl_chunk.document_id
			WHERE acl_chunk.id = chunk_id AND acl_document.access = 'members')`)
	}
	where, arguments, err = appendFilter(where, arguments, search.Filter)
	if err != nil {
		return nil, err
	}
	arguments = append(arguments, search.TopK)
	statement := fmt.Sprintf(`
		SELECT chunk_id, embedding %s $1 AS distance
		FROM %s
		WHERE %s
		ORDER BY embedding %s $1
		LIMIT $%d`, operator, Table(corpus.Spec.Dimensions), strings.Join(where, " AND "), operator, len(arguments))

	// The scan settings are transaction-local, so one expensive query cannot
	// leave the pooled connection tuned that way for the next caller.
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin vector search: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if Indexed(corpus.Spec.Dimensions) {
		if err := store.tune(ctx, transaction, search); err != nil {
			return nil, err
		}
	}
	rows, err := transaction.Query(ctx, statement, arguments...)
	if err != nil {
		return nil, fmt.Errorf("search vectors: %w", err)
	}
	defer rows.Close()

	hits := make([]Hit, 0, search.TopK)
	for rows.Next() {
		var (
			chunkID  string
			distance float64
		)
		if err := rows.Scan(&chunkID, &distance); err != nil {
			return nil, fmt.Errorf("scan vector match: %w", err)
		}
		hits = append(hits, Hit{
			ChunkID:  chunkID,
			Distance: distance,
			Score:    corpus.Spec.Metric.Similarity(distance),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read vector matches: %w", err)
	}
	return hits, nil
}

// tune widens the index search for this transaction.
//
// ef_search is how many candidates HNSW keeps while descending; the default of
// 40 is below what a top-k of any size with a filter needs. Iterative scan
// then lets the index keep going when the filter rejects what it found, which
// is what stops a filtered search from silently coming back short. A member's
// search is always filtered, by access: the admin-only chunks nearest a
// question would otherwise use up every candidate the index hands back.
func (store *Pgvector) tune(ctx context.Context, transaction pgx.Tx, search Search) error {
	efSearch := min(max(search.TopK*4, 40), 1000)
	if _, err := transaction.Exec(ctx, "SET LOCAL hnsw.ef_search = "+strconv.Itoa(efSearch)); err != nil {
		return fmt.Errorf("set hnsw.ef_search: %w", err)
	}
	if store.iterativeScan && (!search.Filter.Empty() || !search.IncludeAdminOnly) {
		if _, err := transaction.Exec(ctx, "SET LOCAL hnsw.iterative_scan = 'relaxed_order'"); err != nil {
			return fmt.Errorf("set hnsw.iterative_scan: %w", err)
		}
	}
	return nil
}

func (store *Pgvector) Delete(ctx context.Context, corpus domain.Corpus, chunkIDs []string) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	statement := fmt.Sprintf(`
		DELETE FROM %s
		WHERE corpus_id = $1 AND organization_id = $2 AND chunk_id = ANY($3)`, Table(corpus.Spec.Dimensions))
	if _, err := store.pool.Exec(ctx, statement, corpus.ID, corpus.OrganizationID, chunkIDs); err != nil {
		return fmt.Errorf("delete vectors: %w", err)
	}
	return nil
}

func (store *Pgvector) DropCorpus(ctx context.Context, corpus domain.Corpus) error {
	statement := fmt.Sprintf(`DELETE FROM %s WHERE corpus_id = $1 AND organization_id = $2`, Table(corpus.Spec.Dimensions))
	if _, err := store.pool.Exec(ctx, statement, corpus.ID, corpus.OrganizationID); err != nil {
		return fmt.Errorf("drop corpus vectors: %w", err)
	}
	return nil
}

// embeddingValue picks the wire type for the column the width maps to. The
// codecs for both are registered on every connection by postgreskit, so this
// travels in binary rather than as a very long string.
func embeddingValue(dimensions int, values []float32) any {
	if HalfPrecision(dimensions) {
		return pgvector.NewHalfVector(values)
	}
	return pgvector.NewVector(values)
}

func distanceOperator(metric domain.Metric) (string, error) {
	switch metric {
	case domain.MetricCosine:
		return "<=>", nil
	case domain.MetricL2:
		return "<->", nil
	case domain.MetricInnerProduct:
		return "<#>", nil
	default:
		return "", fmt.Errorf("metric %q has no pgvector operator: %w", metric, domain.ErrInvalid)
	}
}

// appendFilter turns a domain filter into SQL. Every value is a bound
// parameter: a metadata filter is caller input, and the only safe way to put
// caller input into a statement is not to.
func appendFilter(where []string, arguments []any, filter domain.Filter) ([]string, []any, error) {
	if err := filter.Validate(); err != nil {
		return nil, nil, err
	}
	if len(filter.DocumentIDs) > 0 {
		arguments = append(arguments, filter.DocumentIDs)
		where = append(where, fmt.Sprintf("document_id = ANY($%d)", len(arguments)))
	}
	for _, condition := range filter.Conditions {
		// The key is checked against the metadata key pattern by Validate, and
		// is passed as a parameter to the -> operator regardless.
		arguments = append(arguments, condition.Key)
		keyPlaceholder := len(arguments)
		switch condition.Operator {
		case domain.OperatorEqual:
			arguments = append(arguments, condition.Values[0])
			where = append(where, fmt.Sprintf("metadata->>$%d = $%d", keyPlaceholder, len(arguments)))
		case domain.OperatorNotEqual:
			arguments = append(arguments, condition.Values[0])
			where = append(where, fmt.Sprintf("metadata->>$%d IS DISTINCT FROM $%d", keyPlaceholder, len(arguments)))
		case domain.OperatorIn:
			arguments = append(arguments, condition.Values)
			where = append(where, fmt.Sprintf("metadata->>$%d = ANY($%d)", keyPlaceholder, len(arguments)))
		case domain.OperatorNotIn:
			arguments = append(arguments, condition.Values)
			where = append(where, fmt.Sprintf("(metadata->>$%d IS NULL OR NOT (metadata->>$%d = ANY($%d)))",
				keyPlaceholder, keyPlaceholder, len(arguments)))
		default:
			return nil, nil, fmt.Errorf("filter operator %q is not allowed: %w", condition.Operator, domain.ErrInvalid)
		}
	}
	return where, arguments, nil
}

// atLeastVersion compares a PostgreSQL extension version such as "0.8.6"
// against a floor. An unparseable version reads as older, so a feature is
// only used where it is certainly present.
func atLeastVersion(version string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimSpace(version), ".", 3)
	if len(parts) < 2 {
		return false
	}
	gotMajor, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	gotMinor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}

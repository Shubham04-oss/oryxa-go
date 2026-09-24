package vectorstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	s3vectorsdoc "github.com/aws/aws-sdk-go-v2/service/s3vectors/document"
	s3vectorstypes "github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// S3VectorsAPI is the part of Amazon S3 Vectors this package uses.
type S3VectorsAPI interface {
	PutVectors(context.Context, *s3vectors.PutVectorsInput, ...func(*s3vectors.Options)) (*s3vectors.PutVectorsOutput, error)
	QueryVectors(context.Context, *s3vectors.QueryVectorsInput, ...func(*s3vectors.Options)) (*s3vectors.QueryVectorsOutput, error)
	DeleteVectors(context.Context, *s3vectors.DeleteVectorsInput, ...func(*s3vectors.Options)) (*s3vectors.DeleteVectorsOutput, error)
	CreateIndex(context.Context, *s3vectors.CreateIndexInput, ...func(*s3vectors.Options)) (*s3vectors.CreateIndexOutput, error)
	DeleteIndex(context.Context, *s3vectors.DeleteIndexInput, ...func(*s3vectors.Options)) (*s3vectors.DeleteIndexOutput, error)
	GetIndex(context.Context, *s3vectors.GetIndexInput, ...func(*s3vectors.Options)) (*s3vectors.GetIndexOutput, error)
}

// putBatchSize is how many vectors go in one PutVectors call. S3 Vectors
// rejects a request that exceeds its capacity rather than trimming it, and the
// ceiling moves with the size of the metadata each vector carries, so this
// stays well inside it.
const putBatchSize = 200

// queryLimit is the most results one QueryVectors call returns. Rag asks a
// vector store for more candidates than it will keep — up to 400 — so that
// fusion and reranking have something to choose between; S3 Vectors refuses
// a request for more than this rather than returning fewer, so a search
// asking for over 25 results would have failed outright. Asking for this
// many instead leaves fusion and reranking fewer candidates, never an error.
const queryLimit = 100

// nonFilterableKeys are the provenance fields kept with a vector but never
// filtered on. S3 Vectors budgets filterable metadata far more tightly than
// the rest, so a heading trail or a title — which are for showing a citation,
// not for narrowing a search — is declared non-filterable and stops competing
// for that budget.
var nonFilterableKeys = []string{"heading_path", "title", "source_ref", "source_kind", "chunk_id"}

// S3Vectors keeps vectors in Amazon S3 Vectors, one index per corpus.
//
// An index per corpus, rather than one index with a corpus_id filter, is what
// makes the boundaries real: deleting a corpus is deleting an index, and one
// organization's vectors are never in the same index as another's.
type S3Vectors struct {
	client S3VectorsAPI
	bucket string
}

func NewS3Vectors(client S3VectorsAPI, bucket string) (*S3Vectors, error) {
	if client == nil {
		return nil, errors.New("an S3 Vectors client is required")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, errors.New("an S3 Vectors bucket name is required")
	}
	return &S3Vectors{client: client, bucket: bucket}, nil
}

func (store *S3Vectors) Backend() domain.Backend { return domain.BackendS3Vectors }

// IndexName is the S3 Vectors index a corpus lives in.
//
// It is derived rather than stored, and it is a hash rather than the corpus id
// itself: index names allow only lowercase letters, digits and hyphens, while
// a corpus id is base64url and case-significant. Lower-casing it would risk
// two corpora sharing an index, which is the one mistake here that silently
// mixes two organizations' documents together.
func IndexName(corpus domain.Corpus) string {
	sum := sha256.Sum256([]byte(corpus.OrganizationID + "/" + corpus.ID))
	return "oryxa-rag-" + hex.EncodeToString(sum[:16])
}

// EnsureIndex creates the corpus's index if it is not already there. It is
// idempotent, so the ingest path can call it without first asking.
func (store *S3Vectors) EnsureIndex(ctx context.Context, corpus domain.Corpus) error {
	metric, err := s3Metric(corpus.Spec.Metric)
	if err != nil {
		return err
	}
	name := IndexName(corpus)
	_, err = store.client.GetIndex(ctx, &s3vectors.GetIndexInput{
		VectorBucketName: aws.String(store.bucket),
		IndexName:        aws.String(name),
	})
	if err == nil {
		return nil
	}
	var missing *s3vectorstypes.NotFoundException
	if !errors.As(err, &missing) {
		return fmt.Errorf("look up the S3 Vectors index for corpus %s: %w", corpus.ID, err)
	}
	_, err = store.client.CreateIndex(ctx, &s3vectors.CreateIndexInput{
		VectorBucketName: aws.String(store.bucket),
		IndexName:        aws.String(name),
		DataType:         s3vectorstypes.DataTypeFloat32,
		Dimension:        aws.Int32(int32(corpus.Spec.Dimensions)),
		DistanceMetric:   metric,
		MetadataConfiguration: &s3vectorstypes.MetadataConfiguration{
			NonFilterableMetadataKeys: nonFilterableKeys,
		},
	})
	if err != nil {
		// Two ingests for a new corpus can race here; whichever loses finds
		// the index already made, which is the outcome it wanted anyway.
		var exists *s3vectorstypes.ConflictException
		if errors.As(err, &exists) {
			return nil
		}
		return fmt.Errorf("create the S3 Vectors index for corpus %s: %w", corpus.ID, err)
	}
	return nil
}

func (store *S3Vectors) Upsert(ctx context.Context, corpus domain.Corpus, vectors []domain.Vector) error {
	if len(vectors) == 0 {
		return nil
	}
	if err := validate(corpus, vectors); err != nil {
		return err
	}
	if err := store.EnsureIndex(ctx, corpus); err != nil {
		return err
	}
	name := IndexName(corpus)
	for start := 0; start < len(vectors); start += putBatchSize {
		batch := vectors[start:min(start+putBatchSize, len(vectors))]
		inputs := make([]s3vectorstypes.PutInputVector, 0, len(batch))
		for _, vector := range batch {
			metadata := vector.Metadata.Clone()
			if metadata == nil {
				metadata = domain.Metadata{}
			}
			// document_id is what a filtered search and a document delete
			// both narrow on, so it is written whether or not provenance
			// happened to include it.
			metadata["document_id"] = vector.DocumentID
			inputs = append(inputs, s3vectorstypes.PutInputVector{
				Key:      aws.String(vector.ChunkID),
				Data:     &s3vectorstypes.VectorDataMemberFloat32{Value: vector.Values},
				Metadata: s3vectorsdoc.NewLazyDocument(map[string]any(stringMap(metadata))),
			})
		}
		if _, err := store.client.PutVectors(ctx, &s3vectors.PutVectorsInput{
			VectorBucketName: aws.String(store.bucket),
			IndexName:        aws.String(name),
			Vectors:          inputs,
		}); err != nil {
			return fmt.Errorf("put %d vectors into corpus %s: %w", len(inputs), corpus.ID, err)
		}
	}
	return nil
}

func (store *S3Vectors) Search(ctx context.Context, corpus domain.Corpus, search Search) ([]Hit, error) {
	if len(search.Vector) != corpus.Spec.Dimensions {
		return nil, fmt.Errorf("the query vector is %d wide but corpus %s is %d: %w",
			len(search.Vector), corpus.ID, corpus.Spec.Dimensions, domain.ErrConflict)
	}
	if search.TopK < 1 {
		return nil, fmt.Errorf("top_k must be at least 1: %w", domain.ErrInvalid)
	}
	filter, err := s3Filter(search.Filter)
	if err != nil {
		return nil, err
	}
	topK := min(search.TopK, queryLimit)
	if !search.IncludeAdminOnly {
		// A vector here carries no access of its own: a member's admin-only
		// matches are dropped only once PostgreSQL is asked about them, after
		// this returns (see retrieval's hydrate). Asking for as many as one
		// query returns keeps the admin-only chunks nearest a question from
		// using up everything a member would otherwise have got.
		topK = queryLimit
	}
	input := &s3vectors.QueryVectorsInput{
		VectorBucketName: aws.String(store.bucket),
		IndexName:        aws.String(IndexName(corpus)),
		QueryVector:      &s3vectorstypes.VectorDataMemberFloat32{Value: search.Vector},
		TopK:             aws.Int32(int32(topK)),
		ReturnDistance:   true,
	}
	if filter != nil {
		input.Filter = s3vectorsdoc.NewLazyDocument(filter)
	}
	output, err := store.client.QueryVectors(ctx, input)
	if err != nil {
		// A corpus that has never had a document has no index yet. That is an
		// empty result, not a failure.
		var missing *s3vectorstypes.NotFoundException
		if errors.As(err, &missing) {
			return nil, nil
		}
		return nil, fmt.Errorf("query corpus %s: %w", corpus.ID, err)
	}
	hits := make([]Hit, 0, len(output.Vectors))
	for _, match := range output.Vectors {
		if match.Key == nil {
			continue
		}
		var distance float64
		if match.Distance != nil {
			distance = float64(*match.Distance)
		}
		hits = append(hits, Hit{
			ChunkID:  *match.Key,
			Distance: distance,
			Score:    corpus.Spec.Metric.Similarity(distance),
		})
	}
	return hits, nil
}

func (store *S3Vectors) Delete(ctx context.Context, corpus domain.Corpus, chunkIDs []string) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	name := IndexName(corpus)
	for start := 0; start < len(chunkIDs); start += putBatchSize {
		keys := chunkIDs[start:min(start+putBatchSize, len(chunkIDs))]
		if _, err := store.client.DeleteVectors(ctx, &s3vectors.DeleteVectorsInput{
			VectorBucketName: aws.String(store.bucket),
			IndexName:        aws.String(name),
			Keys:             keys,
		}); err != nil {
			var missing *s3vectorstypes.NotFoundException
			if errors.As(err, &missing) {
				return nil
			}
			return fmt.Errorf("delete %d vectors from corpus %s: %w", len(keys), corpus.ID, err)
		}
	}
	return nil
}

// DropCorpus deletes the whole index, which is why there is one per corpus:
// otherwise this would be a scan of every key looking for the ones to remove.
func (store *S3Vectors) DropCorpus(ctx context.Context, corpus domain.Corpus) error {
	_, err := store.client.DeleteIndex(ctx, &s3vectors.DeleteIndexInput{
		VectorBucketName: aws.String(store.bucket),
		IndexName:        aws.String(IndexName(corpus)),
	})
	if err != nil {
		var missing *s3vectorstypes.NotFoundException
		if errors.As(err, &missing) {
			return nil
		}
		return fmt.Errorf("delete the S3 Vectors index for corpus %s: %w", corpus.ID, err)
	}
	return nil
}

func s3Metric(metric domain.Metric) (s3vectorstypes.DistanceMetric, error) {
	switch metric {
	case domain.MetricCosine:
		return s3vectorstypes.DistanceMetricCosine, nil
	case domain.MetricL2:
		return s3vectorstypes.DistanceMetricEuclidean, nil
	default:
		return "", domain.BackendS3Vectors.Supports(metric)
	}
}

// s3Filter renders a domain filter as an S3 Vectors metadata filter. Nil means
// no filter, which the caller must leave unset rather than send as an empty
// object.
func s3Filter(filter domain.Filter) (map[string]any, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	if filter.Empty() {
		return nil, nil
	}
	var clauses []any
	if len(filter.DocumentIDs) > 0 {
		clauses = append(clauses, map[string]any{"document_id": map[string]any{"$in": filter.DocumentIDs}})
	}
	for _, condition := range filter.Conditions {
		var test map[string]any
		switch condition.Operator {
		case domain.OperatorEqual:
			test = map[string]any{"$eq": condition.Values[0]}
		case domain.OperatorNotEqual:
			test = map[string]any{"$ne": condition.Values[0]}
		case domain.OperatorIn:
			test = map[string]any{"$in": condition.Values}
		case domain.OperatorNotIn:
			test = map[string]any{"$nin": condition.Values}
		default:
			return nil, fmt.Errorf("filter operator %q is not allowed: %w", condition.Operator, domain.ErrInvalid)
		}
		clauses = append(clauses, map[string]any{condition.Key: test})
	}
	if len(clauses) == 1 {
		return clauses[0].(map[string]any), nil
	}
	return map[string]any{"$and": clauses}, nil
}

// stringMap widens metadata for the document encoder, which takes any.
func stringMap(metadata domain.Metadata) map[string]any {
	widened := make(map[string]any, len(metadata))
	for key, value := range metadata {
		widened[key] = value
	}
	return widened
}

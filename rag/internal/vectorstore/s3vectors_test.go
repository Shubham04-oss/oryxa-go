package vectorstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	s3vectorstypes "github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// fakeS3Vectors records what it was asked to do and answers plausibly.
type fakeS3Vectors struct {
	puts     []*s3vectors.PutVectorsInput
	queries  []*s3vectors.QueryVectorsInput
	deletes  []*s3vectors.DeleteVectorsInput
	created  []*s3vectors.CreateIndexInput
	dropped  []*s3vectors.DeleteIndexInput
	indexes  map[string]bool
	queryOut []s3vectorstypes.QueryOutputVector
}

func newFake() *fakeS3Vectors { return &fakeS3Vectors{indexes: map[string]bool{}} }

func (fake *fakeS3Vectors) GetIndex(_ context.Context, input *s3vectors.GetIndexInput, _ ...func(*s3vectors.Options)) (*s3vectors.GetIndexOutput, error) {
	if fake.indexes[aws.ToString(input.IndexName)] {
		return &s3vectors.GetIndexOutput{}, nil
	}
	return nil, &s3vectorstypes.NotFoundException{Message: aws.String("no such index")}
}

func (fake *fakeS3Vectors) CreateIndex(_ context.Context, input *s3vectors.CreateIndexInput, _ ...func(*s3vectors.Options)) (*s3vectors.CreateIndexOutput, error) {
	fake.created = append(fake.created, input)
	fake.indexes[aws.ToString(input.IndexName)] = true
	return &s3vectors.CreateIndexOutput{}, nil
}

func (fake *fakeS3Vectors) PutVectors(_ context.Context, input *s3vectors.PutVectorsInput, _ ...func(*s3vectors.Options)) (*s3vectors.PutVectorsOutput, error) {
	fake.puts = append(fake.puts, input)
	return &s3vectors.PutVectorsOutput{}, nil
}

func (fake *fakeS3Vectors) QueryVectors(_ context.Context, input *s3vectors.QueryVectorsInput, _ ...func(*s3vectors.Options)) (*s3vectors.QueryVectorsOutput, error) {
	fake.queries = append(fake.queries, input)
	if !fake.indexes[aws.ToString(input.IndexName)] {
		return nil, &s3vectorstypes.NotFoundException{Message: aws.String("no such index")}
	}
	return &s3vectors.QueryVectorsOutput{Vectors: fake.queryOut}, nil
}

func (fake *fakeS3Vectors) DeleteVectors(_ context.Context, input *s3vectors.DeleteVectorsInput, _ ...func(*s3vectors.Options)) (*s3vectors.DeleteVectorsOutput, error) {
	fake.deletes = append(fake.deletes, input)
	return &s3vectors.DeleteVectorsOutput{}, nil
}

func (fake *fakeS3Vectors) DeleteIndex(_ context.Context, input *s3vectors.DeleteIndexInput, _ ...func(*s3vectors.Options)) (*s3vectors.DeleteIndexOutput, error) {
	fake.dropped = append(fake.dropped, input)
	delete(fake.indexes, aws.ToString(input.IndexName))
	return &s3vectors.DeleteIndexOutput{}, nil
}

func s3Corpus(metric domain.Metric) domain.Corpus {
	return domain.Corpus{
		ID: "cor_abc", OrganizationID: "org_1", Backend: domain.BackendS3Vectors,
		Spec: domain.EmbeddingSpec{
			Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0",
			Dimensions: 4, Metric: metric,
		},
	}
}

func s3Vector(id string) domain.Vector {
	return domain.Vector{
		ChunkID: id, DocumentID: "doc_1",
		Values:   []float32{1, 0, 0, 0},
		Metadata: domain.Metadata{"team": "platform"},
	}
}

func TestS3VectorsCreatesTheIndexOnFirstWrite(t *testing.T) {
	t.Parallel()
	fake := newFake()
	store, err := NewS3Vectors(fake, "oryxa-vectors")
	if err != nil {
		t.Fatalf("NewS3Vectors: %v", err)
	}
	corpus := s3Corpus(domain.MetricCosine)
	if err := store.Upsert(context.Background(), corpus, []domain.Vector{s3Vector("chk_1")}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(fake.created) != 1 {
		t.Fatalf("created %d indexes, want 1", len(fake.created))
	}
	created := fake.created[0]
	if aws.ToInt32(created.Dimension) != 4 {
		t.Fatalf("index dimension = %d, want 4", aws.ToInt32(created.Dimension))
	}
	if created.DistanceMetric != s3vectorstypes.DistanceMetricCosine {
		t.Fatalf("metric = %q", created.DistanceMetric)
	}
	if created.MetadataConfiguration == nil || len(created.MetadataConfiguration.NonFilterableMetadataKeys) == 0 {
		t.Fatal("the index was created without declaring its non-filterable keys")
	}
	// A second write must not try to create it again.
	if err := store.Upsert(context.Background(), corpus, []domain.Vector{s3Vector("chk_2")}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(fake.created) != 1 {
		t.Fatalf("created %d indexes after two writes, want 1", len(fake.created))
	}
}

func TestS3VectorsIndexNameIsDeterministicAndLegal(t *testing.T) {
	t.Parallel()
	corpus := s3Corpus(domain.MetricCosine)
	name := IndexName(corpus)
	if name != IndexName(corpus) {
		t.Fatal("IndexName is not deterministic")
	}
	if length := len(name); length < 3 || length > 63 {
		t.Fatalf("index name is %d characters, outside the 3 to 63 allowed", length)
	}
	for _, symbol := range name {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789-", symbol) {
			t.Fatalf("index name %q contains %q, which index names do not allow", name, symbol)
		}
	}
	// Two corpora whose ids differ only in case must not share an index. This
	// is the mistake that would silently mix two organizations' documents.
	upper := corpus
	upper.ID = strings.ToUpper(corpus.ID)
	if IndexName(upper) == name {
		t.Fatal("corpora differing only in case collided onto one index")
	}
	other := corpus
	other.OrganizationID = "org_2"
	if IndexName(other) == name {
		t.Fatal("the same corpus id in two organizations collided onto one index")
	}
}

func TestS3VectorsBatchesLargeWrites(t *testing.T) {
	t.Parallel()
	fake := newFake()
	store, _ := NewS3Vectors(fake, "oryxa-vectors")
	corpus := s3Corpus(domain.MetricCosine)

	vectors := make([]domain.Vector, 0, 450)
	for index := range 450 {
		vectors = append(vectors, s3Vector("chk_"+string(rune('a'+index%26))+string(rune('a'+index/26))))
	}
	if err := store.Upsert(context.Background(), corpus, vectors); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if len(fake.puts) != 3 {
		t.Fatalf("made %d PutVectors calls for 450 vectors, want 3", len(fake.puts))
	}
	total := 0
	for _, put := range fake.puts {
		if len(put.Vectors) > putBatchSize {
			t.Fatalf("a batch carried %d vectors, over the %d limit", len(put.Vectors), putBatchSize)
		}
		total += len(put.Vectors)
	}
	if total != 450 {
		t.Fatalf("wrote %d vectors in total, want 450", total)
	}
}

func TestS3VectorsWritesDocumentIDIntoMetadata(t *testing.T) {
	t.Parallel()
	fake := newFake()
	store, _ := NewS3Vectors(fake, "oryxa-vectors")
	if err := store.Upsert(context.Background(), s3Corpus(domain.MetricCosine), []domain.Vector{s3Vector("chk_1")}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	encoded, err := fake.puts[0].Vectors[0].Metadata.MarshalSmithyDocument()
	if err != nil {
		t.Fatalf("render metadata: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		t.Fatalf("read metadata back: %v", err)
	}
	// Filtering and deleting by document both need it, so it is written
	// whether or not the caller's provenance happened to include it.
	if metadata["document_id"] != any("doc_1") {
		t.Fatalf("metadata = %v, want document_id", metadata)
	}
	if metadata["team"] != any("platform") {
		t.Fatalf("caller metadata was lost: %v", metadata)
	}
}

func TestS3VectorsFilterRendering(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		filter domain.Filter
		want   string
	}{{
		name:   "nothing",
		filter: domain.Filter{},
		want:   "null",
	}, {
		name:   "one condition needs no conjunction",
		filter: domain.Filter{Conditions: []domain.Condition{{Key: "team", Operator: domain.OperatorEqual, Values: []string{"platform"}}}},
		want:   `{"team":{"$eq":"platform"}}`,
	}, {
		name:   "documents",
		filter: domain.Filter{DocumentIDs: []string{"doc_1", "doc_2"}},
		want:   `{"document_id":{"$in":["doc_1","doc_2"]}}`,
	}, {
		name: "several are joined",
		filter: domain.Filter{
			DocumentIDs: []string{"doc_1"},
			Conditions:  []domain.Condition{{Key: "team", Operator: domain.OperatorNotIn, Values: []string{"people"}}},
		},
		want: `{"$and":[{"document_id":{"$in":["doc_1"]}},{"team":{"$nin":["people"]}}]}`,
	}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rendered, err := s3Filter(testCase.filter)
			if err != nil {
				t.Fatalf("s3Filter: %v", err)
			}
			encoded, err := json.Marshal(rendered)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(encoded) != testCase.want {
				t.Fatalf("filter = %s, want %s", encoded, testCase.want)
			}
		})
	}
}

func TestS3VectorsSearchReadsDistances(t *testing.T) {
	t.Parallel()
	fake := newFake()
	corpus := s3Corpus(domain.MetricCosine)
	fake.indexes[IndexName(corpus)] = true
	fake.queryOut = []s3vectorstypes.QueryOutputVector{
		{Key: aws.String("chk_1"), Distance: aws.Float32(0.1)},
		{Key: aws.String("chk_2"), Distance: aws.Float32(0.4)},
	}
	store, _ := NewS3Vectors(fake, "oryxa-vectors")
	hits, err := store.Search(context.Background(), corpus, Search{Vector: []float32{1, 0, 0, 0}, TopK: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].ChunkID != "chk_1" || hits[0].Score <= hits[1].Score {
		t.Fatalf("hits = %+v", hits)
	}
	if !fake.queries[0].ReturnDistance {
		t.Fatal("the query did not ask for distances, so nothing could be ranked")
	}
}

func TestS3VectorsEmptyCorpusIsNotAnError(t *testing.T) {
	t.Parallel()
	fake := newFake()
	store, _ := NewS3Vectors(fake, "oryxa-vectors")
	// A corpus with no documents yet has no index. That is an empty result,
	// not a failure the caller has to special-case.
	hits, err := store.Search(context.Background(), s3Corpus(domain.MetricCosine), Search{Vector: []float32{1, 0, 0, 0}, TopK: 5})
	if err != nil {
		t.Fatalf("Search on an unwritten corpus: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits from an unwritten corpus", len(hits))
	}
	if err := store.DropCorpus(context.Background(), s3Corpus(domain.MetricCosine)); err != nil {
		t.Fatalf("DropCorpus on an unwritten corpus: %v", err)
	}
}

func TestS3VectorsRefusesInnerProduct(t *testing.T) {
	t.Parallel()
	fake := newFake()
	store, _ := NewS3Vectors(fake, "oryxa-vectors")
	err := store.Upsert(context.Background(), s3Corpus(domain.MetricInnerProduct), []domain.Vector{s3Vector("chk_1")})
	if err == nil {
		t.Fatal("an inner-product corpus was written to S3 Vectors, which cannot index it")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if len(fake.created) != 0 {
		t.Fatal("an index that could never be queried was created")
	}
}

func TestS3VectorsDropsTheWholeIndex(t *testing.T) {
	t.Parallel()
	fake := newFake()
	store, _ := NewS3Vectors(fake, "oryxa-vectors")
	corpus := s3Corpus(domain.MetricCosine)
	if err := store.Upsert(context.Background(), corpus, []domain.Vector{s3Vector("chk_1")}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.DropCorpus(context.Background(), corpus); err != nil {
		t.Fatalf("DropCorpus: %v", err)
	}
	// One index per corpus is what makes this a delete rather than a scan of
	// every key looking for the ones to remove.
	if len(fake.dropped) != 1 || aws.ToString(fake.dropped[0].IndexName) != IndexName(corpus) {
		t.Fatalf("dropped = %+v", fake.dropped)
	}
}

func TestRouterSendsACorpusToItsOwnStore(t *testing.T) {
	t.Parallel()
	s3, _ := NewS3Vectors(newFake(), "oryxa-vectors")
	router := NewRouter(s3)

	store, err := router.For(s3Corpus(domain.MetricCosine))
	if err != nil || store.Backend() != domain.BackendS3Vectors {
		t.Fatalf("For = (%v, %v)", store, err)
	}
	// A corpus on a backend this deployment was not configured for is an
	// error with a name in it, never an empty result.
	pgCorpus := s3Corpus(domain.MetricCosine)
	pgCorpus.Backend = domain.BackendPgvector
	if _, err := router.For(pgCorpus); err == nil {
		t.Fatal("a pgvector corpus was routed with no pgvector store configured")
	} else if !strings.Contains(err.Error(), "pgvector") {
		t.Fatalf("error = %v, want it to name the backend", err)
	}
	if backends := router.Backends(); len(backends) != 1 || backends[0] != domain.BackendS3Vectors {
		t.Fatalf("Backends = %v", backends)
	}
}

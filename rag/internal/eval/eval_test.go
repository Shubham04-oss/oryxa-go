package eval

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"golang.org/x/oauth2/google"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/chunking"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/ingest"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

// question is one entry of testdata/questions.json. An answerable question
// names the document and the heading its answer sits under; an unanswerable
// one names neither.
type question struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Question string `json:"question"`
	Document string `json:"document"`
	Section  string `json:"section"`
	Answer   string `json:"answer"`
}

// span is a byte range of a stored document.
type span struct{ start, end int }

// countingTransport counts the calls that reach the embedding provider, which
// is what a batch size or a cache is meant to save.
type countingTransport struct {
	base  http.RoundTripper
	calls atomic.Int64
}

func (transport *countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return transport.base.RoundTrip(request)
}

// modeResult is how one retrieval mode did over the answerable questions.
type modeResult struct {
	Mode          string             `json:"mode"`
	Questions     int                `json:"questions"`
	DocumentHit   map[string]float64 `json:"document_hit"`
	SectionHit    map[string]float64 `json:"section_hit"`
	DocumentMRR   float64            `json:"document_mrr"`
	SectionMRR    float64            `json:"section_mrr"`
	LatencyP50    float64            `json:"latency_p50_ms"`
	LatencyP95    float64            `json:"latency_p95_ms"`
	ProviderCalls int64              `json:"provider_calls"`
	// RetrievedTokens is the mean estimated tokens the top matches carry: what
	// a model reading them pays, which bigger chunks raise as they raise hits.
	RetrievedTokens float64 `json:"retrieved_tokens_mean"`
	// Misses are the questions whose section no retrieved chunk touched.
	Misses []string `json:"misses"`
	// Empty counts answerable questions that came back with nothing, and
	// TurnedAway unanswerable ones that did: with a score floor, the first is
	// a wrong refusal and the second a right one.
	Empty      int `json:"answerable_empty"`
	TurnedAway int `json:"unanswerable_turned_away"`
	// TopScores are the best score for each answerable question, and for each
	// unanswerable one: how far apart they are says whether a threshold can
	// tell "found it" from "nothing here".
	AnswerableTop   []float64 `json:"answerable_top_scores"`
	UnanswerableTop []float64 `json:"unanswerable_top_scores"`
}

type report struct {
	Embedding       string             `json:"embedding"`
	Backend         domain.Backend     `json:"backend"`
	Policy          domain.ChunkPolicy `json:"policy"`
	Documents       int                `json:"documents"`
	Chunks          int                `json:"chunks"`
	ChunkTokensMean float64            `json:"chunk_tokens_mean"`
	ChunkBytesMean  float64            `json:"chunk_bytes_mean"`
	EmbeddingTokens int                `json:"embedding_tokens"`
	IngestCalls     int64              `json:"ingest_provider_calls"`
	IngestSeconds   float64            `json:"ingest_seconds"`
	Modes           []modeResult       `json:"modes"`
}

const topK = 8

func TestRetrievalQuality(t *testing.T) {
	databaseURL, project := os.Getenv("RAG_EVAL_DATABASE_URL"), os.Getenv("GOOGLE_CLOUD_PROJECT")
	onBedrock := os.Getenv("RAG_EVAL_EMBEDDING") == "bedrock"
	if databaseURL == "" || (!onBedrock && project == "") {
		t.Skip("RAG_EVAL_DATABASE_URL, and GOOGLE_CLOUD_PROJECT for Vertex embeddings, are not set")
	}
	location := os.Getenv("GOOGLE_CLOUD_LOCATION")
	if location == "" {
		location = "global"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var pgvectorVersion string
	if err := store.Pool().QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&pgvectorVersion); err != nil {
		t.Fatalf("pgvector version: %v", err)
	}
	// AWS is reached only for what asks for it: Bedrock embeddings or
	// reranking, or an S3 Vectors bucket. AWS_REGION says where.
	bucket := os.Getenv("RAG_EVAL_VECTOR_BUCKET")
	rerankARN := os.Getenv("RAG_EVAL_RERANK_ARN")
	var (
		awsConfig aws.Config
		awsLoaded bool
	)
	loadAWS := func() {
		if awsLoaded {
			return
		}
		loaded, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			t.Fatalf("AWS configuration: %v", err)
		}
		awsConfig, awsLoaded = loaded, true
	}

	stores := []vectorstore.Store{vectorstore.NewPgvector(store.Pool(), pgvectorVersion)}
	if bucket != "" {
		loadAWS()
		s3Store, err := vectorstore.NewS3Vectors(s3vectors.NewFromConfig(awsConfig), bucket)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, s3Store)
	}
	vectors := vectorstore.NewRouter(stores...)
	backend := domain.BackendPgvector
	if os.Getenv("RAG_EVAL_BACKEND") == string(domain.BackendS3Vectors) {
		if bucket == "" {
			t.Fatal("RAG_EVAL_BACKEND=s3vectors needs RAG_EVAL_VECTOR_BUCKET")
		}
		backend = domain.BackendS3Vectors
	}

	// counter counts the calls that reach the embedding provider, whichever
	// it is.
	counter := &countingTransport{base: http.DefaultTransport}
	config := embedding.Config{Options: embedding.Options{Logger: logger}}
	spec := domain.EmbeddingSpec{Provider: domain.ProviderVertex, Model: "text-embedding-005", Dimensions: 768, Metric: domain.MetricCosine}
	if onBedrock {
		loadAWS()
		config.Bedrock = bedrockruntime.NewFromConfig(awsConfig, func(options *bedrockruntime.Options) {
			options.HTTPClient = &http.Client{Transport: counter, Timeout: 60 * time.Second}
		})
		spec = domain.EmbeddingSpec{Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0", Dimensions: 1024, Metric: domain.MetricCosine}
	} else {
		httpClient, err := google.DefaultClient(ctx, embedding.VertexScope)
		if err != nil {
			t.Fatalf("application default credentials: %v", err)
		}
		counter.base = httpClient.Transport
		httpClient.Transport = counter
		httpClient.Timeout = 60 * time.Second
		config.Vertex = embedding.VertexConfig{Project: project, Location: location, HTTPClient: httpClient}
	}
	embedders := embedding.NewRegistry(config)
	policy := domain.DefaultChunkPolicy(spec)
	if value, err := strconv.Atoi(os.Getenv("RAG_EVAL_MAX_TOKENS")); err == nil && value > 0 {
		policy.MaxTokens = value
		policy.OverlapTokens = int(float64(value) * domain.DefaultOverlapRatio)
	}
	if value, err := strconv.Atoi(os.Getenv("RAG_EVAL_OVERLAP_TOKENS")); err == nil && value >= 0 {
		policy.OverlapTokens = value
	}

	organizationID := "org-eval-" + time.Now().UTC().Format("20060102150405")
	defer cleanup(t, store, organizationID)
	corpus := createCorpus(t, ctx, store, organizationID, spec, backend, policy)
	// The rows cascade when the organization goes; an S3 Vectors index does
	// not, so it is dropped first.
	defer func() {
		if vectorStore, err := vectors.For(corpus); err == nil {
			if err := vectorStore.DropCorpus(context.Background(), corpus); err != nil {
				t.Errorf("drop the corpus's vectors: %v", err)
			}
		}
	}()

	// Index the corpus the way the service does: store the normalized text,
	// then run the ingest pipeline over each document.
	paths, _ := filepath.Glob("testdata/corpus/*.md")
	sort.Strings(paths)
	pipeline := ingest.New(store, vectors, embedders, nil, ingest.Options{}, logger)
	documentIDs := map[string]string{}
	bodies := map[string]string{}
	result := report{Embedding: spec.Model, Backend: backend, Policy: policy, Documents: len(paths)}
	var chunkTokens, chunkBytes int
	callsBefore := counter.calls.Load()
	started := time.Now()
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(path), ".md")
		body := chunking.Normalize(string(raw), "text/markdown")
		stored := storeDocument(t, ctx, store, corpus, name, body)
		indexed, err := pipeline.Index(ctx, stored)
		if err != nil {
			t.Fatalf("index %s: %v", name, err)
		}
		documentIDs[name], bodies[name] = stored.ID, body
		result.Chunks += len(indexed.Sync.Fresh) + len(indexed.Sync.Reused)
		result.EmbeddingTokens += indexed.Usage.EmbeddingTokens
		for _, chunk := range indexed.Sync.Fresh {
			chunkTokens += chunking.EstimateTokens(chunk.Text)
			chunkBytes += len(chunk.Text)
		}
	}
	result.IngestSeconds = time.Since(started).Seconds()
	result.IngestCalls = counter.calls.Load() - callsBefore
	result.ChunkTokensMean = float64(chunkTokens) / float64(max(result.Chunks, 1))
	result.ChunkBytesMean = float64(chunkBytes) / float64(max(result.Chunks, 1))

	questions := loadQuestions(t)
	var reranker retrieval.Reranker
	switch {
	case os.Getenv("RAG_EVAL_RERANK_MODEL") != "":
		reranker, err = retrieval.NewVertexReranker(ctx, retrieval.VertexRerankConfig{Project: project, Model: os.Getenv("RAG_EVAL_RERANK_MODEL")})
	case rerankARN != "":
		loadAWS()
		reranker, err = retrieval.NewBedrockReranker(bedrockagentruntime.NewFromConfig(awsConfig), rerankARN)
	}
	if err != nil {
		t.Fatalf("reranker: %v", err)
	}
	retriever := retrieval.New(store, vectors, embedders, reranker, nil, logger)
	for _, mode := range []domain.SearchMode{domain.ModeHybrid, domain.ModeDense, domain.ModeLexical} {
		result.Modes = append(result.Modes, measure(t, ctx, retriever, counter, corpus, mode, false, questions, documentIDs, bodies))
	}
	if reranker != nil {
		result.Modes = append(result.Modes, measure(t, ctx, retriever, counter, corpus, domain.ModeHybrid, true, questions, documentIDs, bodies))
	}

	t.Logf("%s on %s, policy %+v: %d documents, %d chunks, %.0f estimated tokens and %.0f bytes a chunk",
		result.Embedding, result.Backend, policy, result.Documents, result.Chunks, result.ChunkTokensMean, result.ChunkBytesMean)
	t.Logf("ingest: %d embedding tokens, %d provider calls, %.1fs", result.EmbeddingTokens, result.IngestCalls, result.IngestSeconds)
	for _, mode := range result.Modes {
		t.Logf("%-7s section hit@1 %.2f @3 %.2f @8 %.2f MRR %.3f | document hit@1 %.2f @8 %.2f MRR %.3f | %.0f tokens retrieved | p50 %.0fms p95 %.0fms, %d calls",
			mode.Mode, mode.SectionHit["1"], mode.SectionHit["3"], mode.SectionHit["8"], mode.SectionMRR,
			mode.DocumentHit["1"], mode.DocumentHit["8"], mode.DocumentMRR, mode.RetrievedTokens, mode.LatencyP50, mode.LatencyP95, mode.ProviderCalls)
		t.Logf("        misses: %s; answerable came back empty %d, unanswerable turned away %d", strings.Join(mode.Misses, ", "), mode.Empty, mode.TurnedAway)
		t.Logf("        top score answerable median %.3f, unanswerable median %.3f max %.3f",
			median(mode.AnswerableTop), median(mode.UnanswerableTop), maximum(mode.UnanswerableTop))
	}
	if out := os.Getenv("RAG_EVAL_OUT"); out != "" {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(out, encoded, 0o644); err != nil {
			t.Fatalf("write %s: %v", out, err)
		}
	}
}

// measure asks every question in one mode. A question counts as found at rank
// r when the r-th match comes from the right document and its byte range
// overlaps the section the answer sits in, which credits a chunk for holding
// the answer however the document was cut.
func measure(t *testing.T, ctx context.Context, retriever *retrieval.Retriever, counter *countingTransport, corpus domain.Corpus,
	mode domain.SearchMode, rerank bool, questions []question, documentIDs, bodies map[string]string) modeResult {
	t.Helper()
	label := string(mode)
	if rerank {
		label += "+rerank"
	}
	result := modeResult{Mode: label, DocumentHit: map[string]float64{}, SectionHit: map[string]float64{}}
	var latencies []float64
	callsBefore := counter.calls.Load()
	for _, entry := range questions {
		started := time.Now()
		query := domain.Query{Text: entry.Question, TopK: topK, Mode: mode, Rerank: rerank}
		if rerank {
			// A floor on relevance means something only on a reranker's scores.
			query.MinScore, _ = strconv.ParseFloat(os.Getenv("RAG_EVAL_MIN_SCORE"), 64)
		}
		found, err := retriever.Search(ctx, corpus, query)
		if err != nil {
			t.Fatalf("%s %s: %v", label, entry.ID, err)
		}
		if rerank && !found.Reranked {
			t.Fatalf("%s %s: the reranker did not run", label, entry.ID)
		}
		latencies = append(latencies, float64(time.Since(started).Milliseconds()))
		for _, match := range found.Matches {
			result.RetrievedTokens += float64(chunking.EstimateTokens(match.Chunk.Text))
		}
		top := 0.0
		if len(found.Matches) > 0 {
			top = found.Matches[0].Score
		}
		if entry.Kind == "unanswerable" {
			result.UnanswerableTop = append(result.UnanswerableTop, top)
			if len(found.Matches) == 0 {
				result.TurnedAway++
			}
			continue
		}
		if len(found.Matches) == 0 {
			result.Empty++
		}
		result.AnswerableTop = append(result.AnswerableTop, top)
		result.Questions++
		documentID := documentIDs[entry.Document]
		sections := sectionSpans(bodies[entry.Document], entry.Section)
		if documentID == "" || len(sections) == 0 {
			t.Fatalf("%s names %s / %q, which the corpus does not have", entry.ID, entry.Document, entry.Section)
		}
		documentRank, sectionRank := 0, 0
		for index, match := range found.Matches {
			if match.Chunk.DocumentID != documentID {
				continue
			}
			if documentRank == 0 {
				documentRank = index + 1
			}
			if sectionRank == 0 && overlaps(match.Chunk.StartByte, match.Chunk.EndByte, sections) {
				sectionRank = index + 1
			}
		}
		score(result.DocumentHit, &result.DocumentMRR, documentRank)
		score(result.SectionHit, &result.SectionMRR, sectionRank)
		if sectionRank == 0 {
			result.Misses = append(result.Misses, entry.ID)
		}
	}
	for _, cut := range []string{"1", "3", "8"} {
		result.DocumentHit[cut] /= float64(result.Questions)
		result.SectionHit[cut] /= float64(result.Questions)
	}
	result.DocumentMRR /= float64(result.Questions)
	result.RetrievedTokens /= float64(len(questions))
	result.SectionMRR /= float64(result.Questions)
	sort.Float64s(latencies)
	result.LatencyP50 = latencies[len(latencies)/2]
	result.LatencyP95 = latencies[len(latencies)*95/100]
	result.ProviderCalls = counter.calls.Load() - callsBefore
	return result
}

func score(hits map[string]float64, reciprocal *float64, rank int) {
	if rank == 0 {
		return
	}
	*reciprocal += 1 / float64(rank)
	for _, cut := range []int{1, 3, 8} {
		if rank <= cut {
			hits[strconv.Itoa(cut)]++
		}
	}
}

// sectionSpans finds every section headed by title: from the heading line to
// the next heading of the same or a higher level, outside code fences.
func sectionSpans(body, title string) []span {
	type heading struct{ level, start, end int }
	var headings []heading
	var titles []string
	fence := false
	for lineStart := 0; lineStart < len(body); {
		lineEnd := strings.IndexByte(body[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(body)
		} else {
			lineEnd += lineStart + 1
		}
		line := strings.TrimSpace(body[lineStart:lineEnd])
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fence = !fence
		} else if !fence && strings.HasPrefix(line, "#") {
			level := len(line) - len(strings.TrimLeft(line, "#"))
			text := strings.TrimSpace(strings.TrimRight(line[level:], "#"))
			if level <= 6 && text != "" && (len(line) == level || line[level] == ' ') {
				headings = append(headings, heading{level: level, start: lineStart})
				titles = append(titles, text)
			}
		}
		lineStart = lineEnd
	}
	var spans []span
	for index, current := range headings {
		if titles[index] != title {
			continue
		}
		end := len(body)
		for _, next := range headings[index+1:] {
			if next.level <= current.level {
				end = next.start
				break
			}
		}
		spans = append(spans, span{start: current.start, end: end})
	}
	return spans
}

func overlaps(start, end int, spans []span) bool {
	return slices.ContainsFunc(spans, func(section span) bool { return start < section.end && end > section.start })
}

func createCorpus(t *testing.T, ctx context.Context, store *postgres.Store, organizationID string, spec domain.EmbeddingSpec, backend domain.Backend, policy domain.ChunkPolicy) domain.Corpus {
	t.Helper()
	id, err := domain.NewID(domain.CorpusIDPrefix)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	corpus := domain.Corpus{
		ID: id, OrganizationID: organizationID, Slug: "eval", Name: "Retrieval evaluation",
		Scope: domain.ScopeOrganization, Spec: spec, Backend: backend, Chunking: policy,
		CreatedBy: "usr_eval", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateCorpus(ctx, corpus, event(t, "rag.corpus.created.v1", organizationID)); err != nil {
		t.Fatalf("create corpus: %v", err)
	}
	return corpus
}

func storeDocument(t *testing.T, ctx context.Context, store *postgres.Store, corpus domain.Corpus, name, body string) domain.Document {
	t.Helper()
	id, err := domain.NewID(domain.DocumentIDPrefix)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	stored, _, err := store.UpsertDocument(ctx, domain.Document{
		ID: id, CorpusID: corpus.ID, OrganizationID: corpus.OrganizationID,
		Source: domain.Source{Kind: domain.SourceInline}, Title: title(name, body), ContentType: "text/markdown",
		ContentSHA256: domain.Hash(body), ByteSize: int64(len(body)), CreatedBy: "usr_eval", CreatedAt: now, UpdatedAt: now,
	}, body, event(t, "rag.document.received.v1", corpus.OrganizationID))
	if err != nil {
		t.Fatalf("store %s: %v", name, err)
	}
	// Hold the document as the ingest worker's claim would, so a rag service
	// sharing this database does not index it at the same time as this run.
	if _, err := store.Pool().Exec(ctx, `UPDATE rag_documents SET status = 'ingesting',
		locked_until = now() + interval '1 hour' WHERE id = $1`, stored.ID); err != nil {
		t.Fatalf("hold %s: %v", name, err)
	}
	return stored
}

// title is a document's first top-level heading, as an upstream pipeline
// would name it, or its file name.
func title(name, body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(line[2:])
		}
	}
	return name
}

// event is a placeholder for what the service would announce. It carries the
// run's organization, as every event ingest writes does, so cleanup finds it.
func event(t *testing.T, detailType, organizationID string) domain.Event {
	t.Helper()
	id, err := domain.NewID(domain.EventIDPrefix)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"organization_id": organizationID})
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: time.Now().UTC(), Data: data}
}

func loadQuestions(t *testing.T) []question {
	t.Helper()
	raw, err := os.ReadFile("testdata/questions.json")
	if err != nil {
		t.Fatal(err)
	}
	var questions []question
	if err := json.Unmarshal(raw, &questions); err != nil {
		t.Fatal(err)
	}
	return questions
}

// cleanup deletes the run's organization: its corpora take their documents,
// chunks and vectors with them.
func cleanup(t *testing.T, store *postgres.Store, organizationID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, statement := range []string{
		`DELETE FROM rag_corpora WHERE organization_id = $1`,
		`DELETE FROM rag_organizations WHERE organization_id = $1`,
		`DELETE FROM rag_outbox WHERE published_at IS NULL AND detail->>'organization_id' = $1`,
	} {
		if _, err := store.Pool().Exec(ctx, statement, organizationID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2]
}

func maximum(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return slices.Max(values)
}

package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/application"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
)

// fakeService records what the handler passed down and answers with whatever
// a test set up.
type fakeService struct {
	corpus      domain.Corpus
	corpora     []domain.Corpus
	document    domain.Document
	documents   []domain.Document
	results     retrieval.Results
	answered    application.Answered
	err         error
	createInput application.CreateCorpusInput
	addInput    application.AddDocumentInput
	listFilter  application.ListFilter
	query       domain.Query
	credentials authclient.Credentials
}

func (fake *fakeService) CreateCorpus(_ context.Context, credentials authclient.Credentials, _ string, input application.CreateCorpusInput) (domain.Corpus, error) {
	fake.credentials, fake.createInput = credentials, input
	return fake.corpus, fake.err
}

func (fake *fakeService) Corpora(_ context.Context, _ authclient.Credentials, _ string, filter application.ListFilter) ([]domain.Corpus, error) {
	fake.listFilter = filter
	return fake.corpora, fake.err
}

func (fake *fakeService) Corpus(context.Context, authclient.Credentials, string, string) (domain.Corpus, error) {
	return fake.corpus, fake.err
}

func (fake *fakeService) DeleteCorpus(context.Context, authclient.Credentials, string, string) error {
	return fake.err
}

func (fake *fakeService) AddDocument(_ context.Context, _ authclient.Credentials, _, _ string, input application.AddDocumentInput) (domain.Document, error) {
	fake.addInput = input
	return fake.document, fake.err
}

func (fake *fakeService) Documents(context.Context, authclient.Credentials, string, string, domain.IngestStatus, int) ([]domain.Document, error) {
	return fake.documents, fake.err
}

func (fake *fakeService) Document(context.Context, authclient.Credentials, string, string, string) (domain.Document, error) {
	return fake.document, fake.err
}

func (fake *fakeService) DeleteDocument(context.Context, authclient.Credentials, string, string, string) error {
	return fake.err
}

func (fake *fakeService) Search(_ context.Context, _ authclient.Credentials, _, _ string, query domain.Query) (retrieval.Results, error) {
	fake.query = query
	return fake.results, fake.err
}

func (fake *fakeService) Answer(_ context.Context, _ authclient.Credentials, _, _ string, query domain.Query) (application.Answered, error) {
	fake.query = query
	return fake.answered, fake.err
}

func newServer(fake *fakeService) *Server {
	return New(fake, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"},
		Capabilities:   Capabilities{Answering: true},
	}, nil)
}

func do(t *testing.T, server *Server, method, path, body string, decorate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Authorization", "Bearer token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, apply := range decorate {
		apply(request)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func TestCreateCorpusPassesTheChoicesDown(t *testing.T) {
	t.Parallel()
	fake := &fakeService{corpus: domain.Corpus{ID: "cor_1", Slug: "handbook"}}
	recorder := do(t, newServer(fake), http.MethodPost, "/v1/organizations/org_1/corpora", `{
		"name":"Handbook","slug":"handbook","scope":"organization",
		"embedding_provider":"vertex","embedding_model":"gemini-embedding-001",
		"embedding_dimensions":1536,"embedding_metric":"cosine","backend":"s3vectors",
		"chunking":{"strategy":"structure","max_tokens":512,"overlap_tokens":64},
		"metadata":{"team":"platform"}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	input := fake.createInput
	if input.Provider != domain.ProviderVertex || input.Model != "gemini-embedding-001" || input.Dimensions != 1536 {
		t.Fatalf("embedding choices = %+v", input)
	}
	if input.Backend != domain.BackendS3Vectors || input.Scope != domain.ScopeOrganization {
		t.Fatalf("placement = %+v", input)
	}
	if input.Chunking.MaxTokens != 512 || input.Chunking.OverlapTokens != 64 {
		t.Fatalf("chunking = %+v", input.Chunking)
	}
	if input.Metadata["team"] != "platform" {
		t.Fatalf("metadata = %v", input.Metadata)
	}
}

func TestUnknownFieldIsNamed(t *testing.T) {
	t.Parallel()
	// Naming the field is the difference between fixing a typo in a minute
	// and guessing at it for an afternoon.
	recorder := do(t, newServer(&fakeService{}), http.MethodPost, "/v1/organizations/org_1/corpora",
		`{"name":"Handbook","embedding_modle":"typo"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "embedding_modle") {
		t.Fatalf("the response did not name the unknown field: %s", recorder.Body)
	}
}

func TestAddDocumentIsAcceptedNotCreated(t *testing.T) {
	t.Parallel()
	fake := &fakeService{document: domain.Document{ID: "doc_1", Status: domain.StatusPending}}
	recorder := do(t, newServer(fake), http.MethodPost, "/v1/organizations/org_1/corpora/cor_1/documents",
		`{"title":"Runbook","content_type":"text/markdown","text":"# Rotation\n\nEvery ninety days.\n"}`)
	// Accepted, because it is queued and not yet searchable. Returning 201
	// would say it is there when it is not.
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.addInput.Source.Kind != domain.SourceInline {
		t.Fatalf("source = %+v, want inline by default", fake.addInput.Source)
	}
	var document domain.Document
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if document.Status != domain.StatusPending {
		t.Fatalf("status = %q", document.Status)
	}
}

func TestAddDocumentFromAFile(t *testing.T) {
	t.Parallel()
	fake := &fakeService{document: domain.Document{ID: "doc_1"}}
	recorder := do(t, newServer(fake), http.MethodPost, "/v1/organizations/org_1/corpora/cor_1/documents",
		`{"source":{"kind":"file","ref":"file_abc"}}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.addInput.Source.Kind != domain.SourceFile || fake.addInput.Source.Ref != "file_abc" {
		t.Fatalf("source = %+v", fake.addInput.Source)
	}
}

func TestSearchPassesEveryKnobDown(t *testing.T) {
	t.Parallel()
	fake := &fakeService{results: retrieval.Results{Matches: []domain.Match{{Score: 0.9}}, Reranked: true}}
	recorder := do(t, newServer(fake), http.MethodPost, "/v1/organizations/org_1/corpora/cor_1/search", `{
		"query":"how often do credentials rotate?","top_k":5,"mode":"hybrid",
		"min_score":0.3,"diversity":0.7,"rerank":true,"over_fetch":6,
		"filter":{"document_ids":["doc_1"],"conditions":[{"key":"team","operator":"in","values":["platform","people"]}]}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	query := fake.query
	if query.Text != "how often do credentials rotate?" || query.TopK != 5 || query.Mode != domain.ModeHybrid {
		t.Fatalf("query = %+v", query)
	}
	if query.MinScore != 0.3 || query.Diversity != 0.7 || !query.Rerank || query.OverFetch != 6 {
		t.Fatalf("knobs = %+v", query)
	}
	if len(query.Filter.DocumentIDs) != 1 || len(query.Filter.Conditions) != 1 {
		t.Fatalf("filter = %+v", query.Filter)
	}
	if query.Filter.Conditions[0].Operator != domain.OperatorIn || len(query.Filter.Conditions[0].Values) != 2 {
		t.Fatalf("condition = %+v", query.Filter.Conditions[0])
	}
	var body struct {
		Matches  []domain.Match `json:"matches"`
		Reranked bool           `json:"reranked"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Matches) != 1 || !body.Reranked {
		t.Fatalf("body = %+v", body)
	}
}

func TestSearchWithNoMatchesReturnsAnEmptyList(t *testing.T) {
	t.Parallel()
	// A null here makes every caller handle two shapes for the same thing.
	recorder := do(t, newServer(&fakeService{}), http.MethodPost,
		"/v1/organizations/org_1/corpora/cor_1/search", `{"query":"anything"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"matches":[]`) {
		t.Fatalf("body = %s", recorder.Body)
	}
}

func TestAnswerReturnsTheAnswerAndWhatItWasBuiltFrom(t *testing.T) {
	t.Parallel()
	fake := &fakeService{answered: application.Answered{
		Answer: domain.Answer{
			Text:      "Every ninety days [1].",
			Citations: []domain.Citation{{Index: 1, ChunkID: "chk_a", DocumentID: "doc_a", StartByte: 10, EndByte: 40}},
			Grounded:  true, Model: "test-model",
		},
		Matches: []domain.Match{{Chunk: domain.Chunk{ID: "chk_a"}, Score: 0.9}},
	}}
	recorder := do(t, newServer(fake), http.MethodPost,
		"/v1/organizations/org_1/corpora/cor_1/answer", `{"query":"how often?"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	var body application.Answered
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Answer.Grounded || len(body.Answer.Citations) != 1 {
		t.Fatalf("answer = %+v", body.Answer)
	}
	// The passages come back with the answer, so a reader can check it
	// against the sources rather than take it on trust.
	if len(body.Matches) != 1 {
		t.Fatalf("matches = %+v", body.Matches)
	}
	if body.Answer.Citations[0].EndByte != 40 {
		t.Fatalf("the citation lost its byte range: %+v", body.Answer.Citations[0])
	}
}

func TestErrorsMapToStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"invalid", fmt.Errorf("top_k is too large: %w", domain.ErrInvalid), http.StatusBadRequest},
		{"unauthorized", domain.ErrUnauthorized, http.StatusUnauthorized},
		{"forbidden", domain.ErrForbidden, http.StatusForbidden},
		{"not found", domain.ErrNotFound, http.StatusNotFound},
		{"conflict", fmt.Errorf("that slug is taken: %w", domain.ErrConflict), http.StatusConflict},
		{"anything else", fmt.Errorf("pq: relation \"rag_chunks\" does not exist"), http.StatusInternalServerError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			recorder := do(t, newServer(&fakeService{err: testCase.err}), http.MethodGet,
				"/v1/organizations/org_1/corpora/cor_1", "")
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}

func TestAnUnexpectedErrorIsNotShownToTheCaller(t *testing.T) {
	t.Parallel()
	// An unexpected error can quote a provider, a statement, or an
	// organization's own text. It belongs in the log.
	fake := &fakeService{err: fmt.Errorf("embedding failed for chunk containing: salary review notes")}
	recorder := do(t, newServer(fake), http.MethodGet, "/v1/organizations/org_1/corpora/cor_1", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "salary review") {
		t.Fatalf("the response leaked the underlying error: %s", recorder.Body)
	}
}

func TestSentinelSuffixIsNotShownToTheCaller(t *testing.T) {
	t.Parallel()
	fake := &fakeService{err: fmt.Errorf("top_k must be between 1 and 100: %w", domain.ErrInvalid)}
	recorder := do(t, newServer(fake), http.MethodGet, "/v1/organizations/org_1/corpora/cor_1", "")
	body := recorder.Body.String()
	if !strings.Contains(body, "top_k must be between 1 and 100") {
		t.Fatalf("the reason was lost: %s", body)
	}
	if strings.Contains(body, "invalid input") {
		t.Fatalf("the sentinel leaked into the message: %s", body)
	}
}

func TestCredentialsAreRequired(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org_1/corpora", nil)
	recorder := httptest.NewRecorder()
	newServer(&fakeService{}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestCookieMutationsNeedAnAllowedOrigin(t *testing.T) {
	t.Parallel()
	// A cookie travels on a cross-site request by itself; a bearer token does
	// not, so only the cookie path needs the check.
	withCookie := func(origin string) func(*http.Request) {
		return func(request *http.Request) {
			request.Header.Del("Authorization")
			request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: "session"})
			if origin != "" {
				request.Header.Set("Origin", origin)
			}
		}
	}
	server := newServer(&fakeService{document: domain.Document{ID: "doc_1"}})

	blocked := do(t, server, http.MethodPost, "/v1/organizations/org_1/corpora/cor_1/documents",
		`{"text":"hello"}`, withCookie("https://evil.test"))
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("a cross-origin cookie write got %d, want 403", blocked.Code)
	}
	allowed := do(t, server, http.MethodPost, "/v1/organizations/org_1/corpora/cor_1/documents",
		`{"text":"hello"}`, withCookie("https://app.oryxa.test"))
	if allowed.Code != http.StatusAccepted {
		t.Fatalf("an allowed origin got %d: %s", allowed.Code, allowed.Body)
	}
	// A bearer token is not sent by a browser on its own, so no Origin check.
	token := do(t, server, http.MethodPost, "/v1/organizations/org_1/corpora/cor_1/documents",
		`{"text":"hello"}`, func(request *http.Request) { request.Header.Set("Origin", "https://evil.test") })
	if token.Code != http.StatusAccepted {
		t.Fatalf("a bearer-token write got %d", token.Code)
	}
}

func TestResponsesAreNotCached(t *testing.T) {
	t.Parallel()
	// Search results and answers are an organization's own text; nothing in
	// between should keep a copy.
	recorder := do(t, newServer(&fakeService{}), http.MethodGet, "/v1/organizations/org_1/corpora", "")
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestHealthzNeedsNoCredentials(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	newServer(&fakeService{}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestCapabilitiesSayWhatThisDeploymentCanServe(t *testing.T) {
	t.Parallel()
	// An operator reading this learns which providers and backends are
	// configured, instead of trying one and reading the error.
	server := New(&fakeService{}, Config{Capabilities: Capabilities{
		Providers: []domain.Provider{domain.ProviderBedrock},
		Backends:  []domain.Backend{domain.BackendPgvector},
		Models:    domain.Catalog(),
		Answering: false,
	}}, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var capabilities Capabilities
	if err := json.Unmarshal(recorder.Body.Bytes(), &capabilities); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(capabilities.Providers) != 1 || capabilities.Answering {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if len(capabilities.Models) == 0 {
		t.Fatal("the model catalog was not advertised")
	}
}

func TestListCorporaDefaultsToTheCallersOwnPlusTheOrganizations(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	if code := do(t, newServer(fake), http.MethodGet, "/v1/organizations/org_1/corpora", "").Code; code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if fake.listFilter.Scope != "" {
		t.Fatalf("scope = %q, want empty so the service applies the default", fake.listFilter.Scope)
	}
	if code := do(t, newServer(fake), http.MethodGet, "/v1/organizations/org_1/corpora?scope=team&team_id=tm_1", "").Code; code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if fake.listFilter.Scope != domain.ScopeTeam || fake.listFilter.TeamID != "tm_1" {
		t.Fatalf("filter = %+v", fake.listFilter)
	}
}

func TestDeleteReturnsNoContent(t *testing.T) {
	t.Parallel()
	server := newServer(&fakeService{})
	for _, path := range []string{
		"/v1/organizations/org_1/corpora/cor_1",
		"/v1/organizations/org_1/corpora/cor_1/documents/doc_1",
	} {
		if code := do(t, server, http.MethodDelete, path, "").Code; code != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d", path, code)
		}
	}
}

func TestNonJSONBodyIsRefused(t *testing.T) {
	t.Parallel()
	recorder := do(t, newServer(&fakeService{}), http.MethodPost, "/v1/organizations/org_1/corpora",
		`name=Handbook`, func(request *http.Request) {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		})
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d", recorder.Code)
	}
}

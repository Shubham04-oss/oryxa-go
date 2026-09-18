package retrieval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVertexRerankerRequestAndResponse(t *testing.T) {
	t.Parallel()
	var got vertexRankRequest
	var path, project string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path, project = request.URL.Path, request.Header.Get("X-Goog-User-Project")
		_ = json.NewDecoder(request.Body).Decode(&got)
		_, _ = writer.Write([]byte(`{"records":[{"id":"2","score":0.91},{"id":"0","score":0.40}]}`))
	}))
	defer server.Close()
	reranker, err := NewVertexReranker(context.Background(), VertexRerankConfig{
		Project: "oryxa-prod", HTTPClient: server.Client(), Endpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("NewVertexReranker: %v", err)
	}
	scored, err := reranker.Rerank(context.Background(), "how often do credentials rotate?",
		[]string{"Rotation > Schedule\n\nEvery ninety days.", "Holidays accrue monthly.", "Credentials rotate every ninety days."}, 2)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if path != "/v1/projects/oryxa-prod/locations/global/rankingConfigs/default_ranking_config:rank" {
		t.Fatalf("path = %q", path)
	}
	// User credentials carry no project, and the API refuses a call that
	// does not name the one it bills.
	if project != "oryxa-prod" {
		t.Fatalf("X-Goog-User-Project = %q", project)
	}
	if got.Model != DefaultVertexRankingModel || got.TopN != 2 || !got.IgnoreRecordDetails || len(got.Records) != 3 {
		t.Fatalf("request = %+v", got)
	}
	if got.Records[1].ID != "1" || got.Records[1].Content != "Holidays accrue monthly." {
		t.Fatalf("records are not the passages by position: %+v", got.Records)
	}
	if len(scored) != 2 || scored[0] != (Scored{Index: 2, Score: 0.91}) || scored[1] != (Scored{Index: 0, Score: 0.40}) {
		t.Fatalf("scored = %+v", scored)
	}
}

func TestVertexRerankerRefusesWhatItCannotTrust(t *testing.T) {
	t.Parallel()
	reply := `{"records":[{"id":"7","score":0.5}]}`
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(reply))
	}))
	defer server.Close()
	reranker, err := NewVertexReranker(context.Background(), VertexRerankConfig{
		Project: "p", HTTPClient: server.Client(), Endpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("NewVertexReranker: %v", err)
	}
	passages := []string{"a", "b"}
	if _, err := reranker.Rerank(context.Background(), "q", passages, 2); err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("a record that was never sent = %v, want an error", err)
	}

	// A refusal can quote the question or a passage, so its body stays out
	// of the error, which reaches logs.
	status, reply = http.StatusForbidden, `{"error":{"message":"the document said something private"}}`
	if _, err := reranker.Rerank(context.Background(), "q", passages, 2); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("a refused call = %v, want an error without its body", err)
	}

	if _, err := reranker.Rerank(context.Background(), "q", make([]string, vertexRankLimit+1), 5); err == nil {
		t.Fatal("more passages than one call accepts were sent")
	}
	if _, err := NewVertexReranker(context.Background(), VertexRerankConfig{}); err == nil {
		t.Fatal("a reranker with no project was built")
	}
}

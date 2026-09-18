package ragclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCorporaAndSearchCallRagAsTheMember(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request.Method+" "+request.URL.EscapedPath()+" "+request.Header.Get("Authorization"))
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/corpora"):
			_, _ = writer.Write([]byte(`{"corpora":[{"id":"c1","name":"Mine","slug":"mine","scope":"member","document_count":3}]}`))
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/search"):
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["query"] != "launch" || body["top_k"] != float64(5) || body["rerank"] != true {
				t.Errorf("search body = %v (%v)", body, err)
			}
			_, _ = writer.Write([]byte(`{"matches":[{"chunk":{"text":"Launch is on the 3rd.","heading_path":["Plan"]},"document":{"id":"d1","title":"Launch plan","source":{"kind":"external","ref":"https://notion.so/x"}},"score":0.8}],"reranked":false}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := New(server.URL+"/", nil)

	corpora, err := client.Corpora(context.Background(), "org 1", "member-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(corpora) != 1 || corpora[0].ID != "c1" || corpora[0].DocumentCount != 3 {
		t.Fatalf("corpora = %+v", corpora)
	}
	matches, err := client.Search(context.Background(), "org 1", "c/1", "member-token", "launch", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Document.Title != "Launch plan" || matches[0].Document.Source.Ref != "https://notion.so/x" ||
		matches[0].Chunk.HeadingPath[0] != "Plan" || matches[0].Score != 0.8 {
		t.Fatalf("matches = %+v", matches)
	}
	want := []string{
		"GET /v1/organizations/org%201/corpora Bearer member-token",
		"POST /v1/organizations/org%201/corpora/c%2F1/search Bearer member-token",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rag saw:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

func TestRagsRefusalsBecomeWordsAModelCanRepeat(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
		words  string
	}{
		{http.StatusUnauthorized, `{"error":"authentication failed"}`, ErrDenied, ""},
		{http.StatusForbidden, `{"error":"forbidden"}`, ErrDenied, ""},
		{http.StatusNotFound, `{"error":"resource not found"}`, ErrNotFound, ""},
		{http.StatusTooManyRequests, `{}`, ErrUnavailable, ""},
		{http.StatusBadGateway, `{}`, ErrUnavailable, ""},
		{http.StatusBadRequest, `{"error":"top_k must be at most 100"}`, nil, "top_k must be at most 100"},
	}
	for _, testCase := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(testCase.status)
			_, _ = writer.Write([]byte(testCase.body))
		}))
		_, err := New(server.URL, nil).Search(context.Background(), "org_1", "c1", "t", "q", 5)
		server.Close()
		if testCase.want != nil && !errors.Is(err, testCase.want) {
			t.Errorf("status %d: error = %v, want %v", testCase.status, err, testCase.want)
		}
		if testCase.words != "" && (err == nil || !strings.Contains(err.Error(), testCase.words)) {
			t.Errorf("status %d: error = %v, want rag's own words", testCase.status, err)
		}
	}
	if _, err := New("http://127.0.0.1:1", nil).Corpora(context.Background(), "org_1", "t"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("rag down: error = %v, want ErrUnavailable", err)
	}
}

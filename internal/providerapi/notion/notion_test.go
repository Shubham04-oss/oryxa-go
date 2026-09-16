package notion_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/notion"
)

// example returns the response example Notion's description gives for an
// operation.
func example(t *testing.T, path, method, status, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var description struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]struct {
					Examples map[string]struct {
						Value json.RawMessage `json:"value"`
					} `json:"examples"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &description); err != nil {
		t.Fatal(err)
	}
	value := description.Paths[path][method].Responses[status].Content["application/json"].Examples[name].Value
	if len(value) == 0 {
		t.Fatalf("no %s example for %s %s %s", name, method, path, status)
	}
	return value
}

func TestClientCallsNotionAsDescribed(t *testing.T) {
	type sent struct {
		method, path, authorization, version string
		body                                 []byte
	}
	var mu sync.Mutex
	var requests []sent
	self, comment := example(t, "/v1/users/me", "get", "200", "Result"), example(t, "/v1/comments", "post", "200", "Result")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		requests = append(requests, sent{request.Method, request.URL.Path, request.Header.Get("Authorization"), request.Header.Get("Notion-Version"), body})
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /v1/users/me":
			_, _ = writer.Write(self)
		case "POST /v1/comments":
			_, _ = writer.Write(comment)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	bearer := func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer ntn_example")
		return nil
	}
	client, err := notion.NewClientWithResponses(server.URL, notion.WithHTTPClient(server.Client()),
		notion.WithRequestEditorFn(notion.Versioned), notion.WithRequestEditorFn(bearer))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	me, err := client.GetSelfWithResponse(ctx, nil)
	if err != nil || me.JSON200 == nil || me.JSON200.Bot == nil || me.JSON200.Bot.Owner == nil || me.JSON200.Bot.Owner.User == nil ||
		me.JSON200.Bot.Owner.User.Person == nil || *me.JSON200.Bot.Owner.User.Person.Email != "christine@makenotion.com" {
		t.Fatalf("GetSelf() = %+v %v", me, err)
	}
	// A call that names its own version keeps it.
	newer := notion.NotionVersion("2026-03-11")
	if _, err := client.GetSelfWithResponse(ctx, &notion.GetSelfParams{NotionVersion: &newer}); err != nil {
		t.Fatal(err)
	}

	created, err := client.CreateACommentWithResponse(ctx, notion.CreateACommentJSONRequestBody{
		Parent:   notion.CreateACommentJSONBody_Parent{PageID: "5c6a2821-6bb1-4a7e-b6e1-c50111515c3d"},
		RichText: []notion.CreateACommentJSONBody_RichText{{Text: notion.CreateACommentJSONBody_RichText_Text{Content: "Hello world"}}},
	})
	// The description gives this response an example but no schema, so its
	// body is read as sent.
	if err != nil || created.StatusCode() != http.StatusOK || !strings.Contains(string(created.Body), `"b52b8ed6-e029-4707-a671-832549c09de3"`) {
		t.Fatalf("CreateAComment() = %d %s %v", created.StatusCode(), created.Body, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("Notion was sent %d requests, want 3", len(requests))
	}
	for index, want := range []string{notion.APIVersion, "2026-03-11", notion.APIVersion} {
		if requests[index].version != want || requests[index].authorization != "Bearer ntn_example" {
			t.Errorf("request %d = %s %s with Notion-Version %q, Authorization %q", index, requests[index].method, requests[index].path, requests[index].version, requests[index].authorization)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(requests[2].body, &body); err != nil ||
		!strings.Contains(string(requests[2].body), `"page_id":"5c6a2821-6bb1-4a7e-b6e1-c50111515c3d"`) ||
		!strings.Contains(string(requests[2].body), `"content":"Hello world"`) {
		t.Errorf("CreateAComment sent %s", requests[2].body)
	}
}

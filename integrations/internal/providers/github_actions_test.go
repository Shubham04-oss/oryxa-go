package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

var githubActionCredential = domain.Credential{AccessToken: "tok123", Prefix: "Bearer "}

// runGithubAction marshals input and runs the named action against server,
// the way a caller with a connection to it would.
func runGithubAction(t *testing.T, server *httptest.Server, name string, input map[string]any) (ActionResult, error) {
	t.Helper()
	provider := githubProvider(server.URL)
	action, ok := provider.Action(name)
	if !ok {
		t.Fatalf("provider has no action %q", name)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return action.Run(context.Background(), server.Client(), ActionRequest{Credential: githubActionCredential, Input: raw, Now: fixedNow})
}

// githubFakeRequest is what reached githubFakeServer, for a happy-path test to
// assert against.
type githubFakeRequest struct {
	Method string
	Path   string
	Auth   string
	Query  url.Values
	Body   map[string]any
}

// githubFakeServer answers one request with status and body, both as JSON,
// and reports what it received.
func githubFakeServer(t *testing.T, status int, body string) (*httptest.Server, *githubFakeRequest) {
	t.Helper()
	got := &githubFakeRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		got.Method, got.Path, got.Auth, got.Query = request.Method, request.URL.Path, request.Header.Get("Authorization"), request.URL.Query()
		if request.ContentLength != 0 {
			if err := json.NewDecoder(request.Body).Decode(&got.Body); err != nil {
				t.Errorf("decode request body: %v", err)
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}))
	return server, got
}

func TestGithubGetIssueAction(t *testing.T) {
	server, got := githubFakeServer(t, http.StatusOK, `{"id":111,"number":42,"title":"Bug found","state":"open","body":"It crashes.",`+
		`"html_url":"https://github.com/acme/api/issues/42","comments":3,"user":{"login":"octocat"},`+
		`"labels":[{"name":"bug"},{"name":"P1"}]}`)
	defer server.Close()

	result, err := runGithubAction(t, server, "get_issue", map[string]any{"repository": "acme/api", "number": 42})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Method != http.MethodGet || got.Path != "/repos/acme/api/issues/42" {
		t.Fatalf("request = %s %s, want GET /repos/acme/api/issues/42", got.Method, got.Path)
	}
	if got.Auth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", got.Auth, "Bearer tok123")
	}
	wantResource := integrationsv1.Resource{Type: "issue", ID: "111", Name: "Bug found", URL: "https://github.com/acme/api/issues/42"}
	if result.Resource != wantResource {
		t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
	}
	var gotResult githubIssueDetail
	if err := json.Unmarshal(result.Result, &gotResult); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	wantResult := githubIssueDetail{
		Number: 42, Title: "Bug found", State: "open", Body: "It crashes.",
		URL: "https://github.com/acme/api/issues/42", Author: "octocat", Labels: []string{"bug", "P1"}, Comments: 3,
	}
	if !reflect.DeepEqual(gotResult, wantResult) {
		t.Fatalf("Result = %+v, want %+v", gotResult, wantResult)
	}
}

func TestGithubListIssuesAction(t *testing.T) {
	server, got := githubFakeServer(t, http.StatusOK, `[`+
		`{"id":1,"number":10,"title":"First","state":"open","html_url":"https://github.com/acme/api/issues/10","user":{"login":"alice"}},`+
		`{"id":2,"number":11,"title":"Second","state":"open","html_url":"https://github.com/acme/api/issues/11","user":{"login":"bob"}}`+
		`]`)
	defer server.Close()

	result, err := runGithubAction(t, server, "list_issues", map[string]any{"repository": "acme/api", "labels": "bug,P1"})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Method != http.MethodGet || got.Path != "/repos/acme/api/issues" {
		t.Fatalf("request = %s %s, want GET /repos/acme/api/issues", got.Method, got.Path)
	}
	if got.Auth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", got.Auth, "Bearer tok123")
	}
	if got.Query.Get("state") != "open" || got.Query.Get("per_page") != "20" || got.Query.Get("labels") != "bug,P1" {
		t.Fatalf("query = %v, want state=open per_page=20 labels=bug,P1", got.Query)
	}
	if result.Resource != (integrationsv1.Resource{}) {
		t.Fatalf("Resource = %+v, want the zero value", result.Resource)
	}
	var gotResult []githubIssueSummary
	if err := json.Unmarshal(result.Result, &gotResult); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	wantResult := []githubIssueSummary{
		{Number: 10, Title: "First", State: "open", URL: "https://github.com/acme/api/issues/10", Author: "alice"},
		{Number: 11, Title: "Second", State: "open", URL: "https://github.com/acme/api/issues/11", Author: "bob"},
	}
	if !reflect.DeepEqual(gotResult, wantResult) {
		t.Fatalf("Result = %+v, want %+v", gotResult, wantResult)
	}
}

func TestGithubListIssuesActionDefaults(t *testing.T) {
	server, got := githubFakeServer(t, http.StatusOK, `[]`)
	defer server.Close()

	if _, err := runGithubAction(t, server, "list_issues", map[string]any{"repository": "acme/api"}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Query.Get("state") != "open" || got.Query.Get("per_page") != "20" {
		t.Fatalf("query = %v, want default state=open per_page=20", got.Query)
	}
	if _, ok := got.Query["labels"]; ok {
		t.Fatalf("query = %v, want no labels parameter when none is given", got.Query)
	}
}

func TestGithubCreateIssueAction(t *testing.T) {
	server, got := githubFakeServer(t, http.StatusCreated,
		`{"id":123,"number":7,"title":"New bug","html_url":"https://github.com/acme/api/issues/7"}`)
	defer server.Close()

	result, err := runGithubAction(t, server, "create_issue", map[string]any{
		"repository": "acme/api", "title": "New bug", "body": "Steps to reproduce.",
		"labels": []string{"bug"}, "assignees": []string{"octocat"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Method != http.MethodPost || got.Path != "/repos/acme/api/issues" {
		t.Fatalf("request = %s %s, want POST /repos/acme/api/issues", got.Method, got.Path)
	}
	if got.Auth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", got.Auth, "Bearer tok123")
	}
	if got.Body["title"] != "New bug" || got.Body["body"] != "Steps to reproduce." {
		t.Fatalf("request body = %v, want title \"New bug\", body \"Steps to reproduce.\"", got.Body)
	}
	if labels, _ := got.Body["labels"].([]any); len(labels) != 1 || labels[0] != "bug" {
		t.Fatalf("request body labels = %v, want [bug]", got.Body["labels"])
	}
	if assignees, _ := got.Body["assignees"].([]any); len(assignees) != 1 || assignees[0] != "octocat" {
		t.Fatalf("request body assignees = %v, want [octocat]", got.Body["assignees"])
	}
	wantResource := integrationsv1.Resource{Type: "issue", ID: "123", Name: "New bug", URL: "https://github.com/acme/api/issues/7"}
	if result.Resource != wantResource {
		t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
	}
	var gotResult githubIssueCreated
	if err := json.Unmarshal(result.Result, &gotResult); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	wantResult := githubIssueCreated{Number: 7, URL: "https://github.com/acme/api/issues/7", Title: "New bug"}
	if gotResult != wantResult {
		t.Fatalf("Result = %+v, want %+v", gotResult, wantResult)
	}
}

func TestGithubCommentOnIssueAction(t *testing.T) {
	server, got := githubFakeServer(t, http.StatusCreated,
		`{"id":555,"html_url":"https://github.com/acme/api/issues/7#issuecomment-555"}`)
	defer server.Close()

	result, err := runGithubAction(t, server, "comment_on_issue", map[string]any{"repository": "acme/api", "number": 7, "body": "Thanks!"})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Method != http.MethodPost || got.Path != "/repos/acme/api/issues/7/comments" {
		t.Fatalf("request = %s %s, want POST /repos/acme/api/issues/7/comments", got.Method, got.Path)
	}
	if got.Auth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", got.Auth, "Bearer tok123")
	}
	if got.Body["body"] != "Thanks!" {
		t.Fatalf("request body = %v, want body \"Thanks!\"", got.Body)
	}
	wantResource := integrationsv1.Resource{Type: "comment", ID: "555", URL: "https://github.com/acme/api/issues/7#issuecomment-555"}
	if result.Resource != wantResource {
		t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
	}
	var gotResult githubCommentCreated
	if err := json.Unmarshal(result.Result, &gotResult); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	wantResult := githubCommentCreated{ID: "555", URL: "https://github.com/acme/api/issues/7#issuecomment-555"}
	if gotResult != wantResult {
		t.Fatalf("Result = %+v, want %+v", gotResult, wantResult)
	}
}

func TestGithubCloseIssueAction(t *testing.T) {
	server, got := githubFakeServer(t, http.StatusOK,
		`{"id":123,"number":7,"title":"New bug","state":"closed","html_url":"https://github.com/acme/api/issues/7"}`)
	defer server.Close()

	result, err := runGithubAction(t, server, "close_issue", map[string]any{"repository": "acme/api", "number": 7, "reason": "completed"})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Method != http.MethodPatch || got.Path != "/repos/acme/api/issues/7" {
		t.Fatalf("request = %s %s, want PATCH /repos/acme/api/issues/7", got.Method, got.Path)
	}
	if got.Auth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", got.Auth, "Bearer tok123")
	}
	if got.Body["state"] != "closed" || got.Body["state_reason"] != "completed" {
		t.Fatalf("request body = %v, want state \"closed\", state_reason \"completed\"", got.Body)
	}
	wantResource := integrationsv1.Resource{Type: "issue", ID: "123", Name: "New bug", URL: "https://github.com/acme/api/issues/7"}
	if result.Resource != wantResource {
		t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
	}
	var gotResult githubIssueClosed
	if err := json.Unmarshal(result.Result, &gotResult); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	wantResult := githubIssueClosed{Number: 7, State: "closed", URL: "https://github.com/acme/api/issues/7"}
	if gotResult != wantResult {
		t.Fatalf("Result = %+v, want %+v", gotResult, wantResult)
	}
}

func TestGithubCloseIssueActionWithoutReason(t *testing.T) {
	server, got := githubFakeServer(t, http.StatusOK,
		`{"id":123,"number":7,"title":"New bug","state":"closed","html_url":"https://github.com/acme/api/issues/7"}`)
	defer server.Close()

	if _, err := runGithubAction(t, server, "close_issue", map[string]any{"repository": "acme/api", "number": 7}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if _, ok := got.Body["state_reason"]; ok {
		t.Fatalf("request body = %v, want no state_reason when none is given", got.Body)
	}
	if got.Body["state"] != "closed" {
		t.Fatalf("request body state = %v, want \"closed\"", got.Body["state"])
	}
}

func TestGithubActionsInputRefusals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Errorf("unexpected request for invalid input: %s %s", request.Method, request.URL.Path)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	provider := githubProvider(server.URL)

	tests := []struct {
		name   string
		action string
		input  map[string]any
	}{
		{"get_issue missing repository", "get_issue", map[string]any{"number": 1}},
		{"get_issue missing number", "get_issue", map[string]any{"repository": "acme/api"}},
		{"get_issue unknown field", "get_issue", map[string]any{"repository": "acme/api", "number": 1, "extra": "nope"}},
		{"get_issue malformed repository", "get_issue", map[string]any{"repository": "not-a-repository", "number": 1}},

		{"list_issues missing repository", "list_issues", map[string]any{}},
		{"list_issues unknown field", "list_issues", map[string]any{"repository": "acme/api", "extra": "nope"}},
		{"list_issues malformed repository", "list_issues", map[string]any{"repository": "not-a-repository"}},
		{"list_issues limit too high", "list_issues", map[string]any{"repository": "acme/api", "limit": 51}},
		{"list_issues limit negative", "list_issues", map[string]any{"repository": "acme/api", "limit": -1}},
		{"list_issues unknown state", "list_issues", map[string]any{"repository": "acme/api", "state": "archived"}},

		{"create_issue missing repository", "create_issue", map[string]any{"title": "Bug"}},
		{"create_issue missing title", "create_issue", map[string]any{"repository": "acme/api"}},
		{"create_issue unknown field", "create_issue", map[string]any{"repository": "acme/api", "title": "Bug", "extra": "nope"}},
		{"create_issue malformed repository", "create_issue", map[string]any{"repository": "not-a-repository", "title": "Bug"}},

		{"comment_on_issue missing repository", "comment_on_issue", map[string]any{"number": 1, "body": "hi"}},
		{"comment_on_issue missing number", "comment_on_issue", map[string]any{"repository": "acme/api", "body": "hi"}},
		{"comment_on_issue missing body", "comment_on_issue", map[string]any{"repository": "acme/api", "number": 1}},
		{"comment_on_issue unknown field", "comment_on_issue", map[string]any{"repository": "acme/api", "number": 1, "body": "hi", "extra": "nope"}},
		{"comment_on_issue malformed repository", "comment_on_issue", map[string]any{"repository": "not-a-repository", "number": 1, "body": "hi"}},

		{"close_issue missing repository", "close_issue", map[string]any{"number": 1}},
		{"close_issue missing number", "close_issue", map[string]any{"repository": "acme/api"}},
		{"close_issue unknown field", "close_issue", map[string]any{"repository": "acme/api", "number": 1, "extra": "nope"}},
		{"close_issue malformed repository", "close_issue", map[string]any{"repository": "not-a-repository", "number": 1}},
		{"close_issue unknown reason", "close_issue", map[string]any{"repository": "acme/api", "number": 1, "reason": "duplicate"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			action, ok := provider.Action(tc.action)
			if !ok {
				t.Fatalf("provider has no action %q", tc.action)
			}
			raw, err := json.Marshal(tc.input)
			if err != nil {
				t.Fatalf("marshal input: %v", err)
			}
			_, err = action.Run(context.Background(), server.Client(), ActionRequest{
				Credential: githubActionCredential, Input: raw, Now: fixedNow,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
			}
		})
	}
}

func TestGithubActionsStatusMapping(t *testing.T) {
	actions := []struct {
		name  string
		input map[string]any
	}{
		{"get_issue", map[string]any{"repository": "acme/api", "number": 1}},
		{"list_issues", map[string]any{"repository": "acme/api"}},
		{"create_issue", map[string]any{"repository": "acme/api", "title": "Bug"}},
		{"comment_on_issue", map[string]any{"repository": "acme/api", "number": 1, "body": "hi"}},
		{"close_issue", map[string]any{"repository": "acme/api", "number": 1}},
	}
	statuses := []struct {
		name        string
		status      int
		rateLimited bool
		want        error
	}{
		{"unauthorized", http.StatusUnauthorized, false, domain.ErrCredentialRejected},
		{"not found", http.StatusNotFound, false, domain.ErrNotFound},
		{"rate limited", http.StatusForbidden, true, domain.ErrProviderUnavailable},
		{"plain forbidden", http.StatusForbidden, false, domain.ErrForbidden},
		{"unprocessable", http.StatusUnprocessableEntity, false, domain.ErrInvalid},
	}
	for _, action := range actions {
		for _, tc := range statuses {
			t.Run(action.name+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					if tc.rateLimited {
						writer.Header().Set("X-RateLimit-Remaining", "0")
					}
					writer.Header().Set("Content-Type", "application/json")
					writer.WriteHeader(tc.status)
					_, _ = io.WriteString(writer, `{}`)
				}))
				defer server.Close()
				_, err := runGithubAction(t, server, action.name, action.input)
				if !errors.Is(err, tc.want) {
					t.Fatalf("Run() error = %v, want %v", err, tc.want)
				}
			})
		}
	}
}

func TestGithubActionsCatalog(t *testing.T) {
	actions := githubActions("https://api.github.test")
	want := []struct {
		name   string
		writes bool
	}{
		{"get_issue", false},
		{"list_issues", false},
		{"create_issue", true},
		{"comment_on_issue", true},
		{"close_issue", true},
	}
	if len(actions) != len(want) {
		t.Fatalf("githubActions() has %d actions, want %d", len(actions), len(want))
	}
	for index, action := range actions {
		if action.Name != want[index].name || action.Writes != want[index].writes {
			t.Errorf("actions[%d] = %q, writes %v; want %q, writes %v", index, action.Name, action.Writes, want[index].name, want[index].writes)
		}
		if action.Summary == "" {
			t.Errorf("actions[%d] %q has no Summary", index, action.Name)
		}
		if action.Run == nil {
			t.Errorf("actions[%d] %q has no Run", index, action.Name)
		}
	}
}

func TestGithubActionSchemas(t *testing.T) {
	for _, action := range githubActions("https://api.github.test") {
		t.Run(action.Name, func(t *testing.T) {
			var schema map[string]any
			if err := json.Unmarshal(action.Input, &schema); err != nil {
				t.Fatalf("Input is not valid JSON: %v", err)
			}
			if schema["type"] != "object" {
				t.Errorf("type = %v, want %q", schema["type"], "object")
			}
			additionalProperties, ok := schema["additionalProperties"].(bool)
			if !ok || additionalProperties {
				t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
			}
			properties, ok := schema["properties"].(map[string]any)
			if !ok || len(properties) == 0 {
				t.Fatalf("properties = %v, want a non-empty object", schema["properties"])
			}
			for name, raw := range properties {
				property, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("property %q is not an object", name)
				}
				if _, ok := property["type"]; !ok {
					t.Errorf("property %q has no type", name)
				}
				if description, ok := property["description"].(string); !ok || description == "" {
					t.Errorf("property %q has no description", name)
				}
			}
		})
	}
}

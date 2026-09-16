package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// linearActionCredential is an API key credential as connections places it:
// bare, with no prefix, on the Authorization header.
var linearActionCredential = domain.Credential{AccessToken: "lin_api_key_xyz"}

// runLinearAction runs a named action from a provider built against server,
// the way a caller with a Linear connection would.
func runLinearAction(t *testing.T, server *httptest.Server, name string, input map[string]any) (ActionResult, error) {
	t.Helper()
	provider := linearProvider(server.URL + "/graphql")
	action, ok := provider.Action(name)
	if !ok {
		t.Fatalf("provider has no action %q", name)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return action.Run(context.Background(), server.Client(), ActionRequest{Credential: linearActionCredential, Input: raw, Now: fixedNow})
}

// linearGraphQLRequest is one GraphQL call captured by linearFakeServer.
type linearGraphQLRequest struct {
	Auth      string
	Query     string
	Variables map[string]any
}

// linearFakeServer answers successive requests with bodies, in order, and
// reports what each one sent, the way an action that calls linearGraphQL more
// than once (a lookup, then the operation itself) can be driven and checked.
func linearFakeServer(t *testing.T, bodies ...string) (*httptest.Server, *[]linearGraphQLRequest) {
	t.Helper()
	var got []linearGraphQLRequest
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		got = append(got, linearGraphQLRequest{Auth: request.Header.Get("Authorization"), Query: body.Query, Variables: body.Variables})
		writer.Header().Set("Content-Type", "application/json")
		if calls >= len(bodies) {
			t.Errorf("unexpected request %d: %s", calls+1, body.Query)
			_, _ = io.WriteString(writer, `{"errors":[{"message":"unexpected request"}]}`)
			return
		}
		_, _ = io.WriteString(writer, bodies[calls])
		calls++
	}))
	return server, &got
}

// linearNoCallServer fails the test if Linear is ever called, for asserting
// an input refusal never reaches the network.
func linearNoCallServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("unexpected request to Linear")
	}))
}

func TestLinearGetIssueAction(t *testing.T) {
	t.Run("happy path takes either a UUID or an identifier straight to issue(id:)", func(t *testing.T) {
		server, got := linearFakeServer(t, `{"data":{"issue":{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d","identifier":"ENG-42",`+
			`"title":"Bug found","description":"It crashes.","state":{"name":"In Progress"},"priority":2,`+
			`"url":"https://linear.app/acme/issue/ENG-42","assignee":{"name":"Ada Lovelace"}}}}`)
		defer server.Close()

		result, err := runLinearAction(t, server, "get_issue", map[string]any{"issue": "ENG-42"})
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if len(*got) != 1 {
			t.Fatalf("requests = %d, want 1 (get_issue needs no separate lookup)", len(*got))
		}
		call := (*got)[0]
		if call.Auth != "lin_api_key_xyz" {
			t.Fatalf("Authorization = %q, want the bare key %q", call.Auth, "lin_api_key_xyz")
		}
		if !strings.Contains(call.Query, "issue(id:") {
			t.Fatalf("query = %q, want it to call issue(id:)", call.Query)
		}
		if call.Variables["id"] != "ENG-42" {
			t.Fatalf("variables = %v, want id ENG-42", call.Variables)
		}
		wantResource := integrationsv1.Resource{
			Type: "issue", ID: "9cfb482a-81e3-4154-b5b9-2c805e70a02d", Name: "Bug found", URL: "https://linear.app/acme/issue/ENG-42",
		}
		if result.Resource != wantResource {
			t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
		}
		var gotResult linearIssueDetail
		if err := json.Unmarshal(result.Result, &gotResult); err != nil {
			t.Fatalf("unmarshal Result: %v", err)
		}
		wantResult := linearIssueDetail{
			ID: "9cfb482a-81e3-4154-b5b9-2c805e70a02d", Identifier: "ENG-42", Title: "Bug found", Description: "It crashes.",
			State: "In Progress", Priority: 2, URL: "https://linear.app/acme/issue/ENG-42", Assignee: "Ada Lovelace",
		}
		if gotResult != wantResult {
			t.Fatalf("Result = %+v, want %+v", gotResult, wantResult)
		}
	})

	t.Run("no assignee and no description decode as empty, not a panic", func(t *testing.T) {
		server, _ := linearFakeServer(t, `{"data":{"issue":{"id":"i1","identifier":"ENG-1","title":"Bug","description":null,`+
			`"state":{"name":"Todo"},"priority":0,"url":"https://linear.app/acme/issue/ENG-1","assignee":null}}}`)
		defer server.Close()

		result, err := runLinearAction(t, server, "get_issue", map[string]any{"issue": "ENG-1"})
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		var gotResult linearIssueDetail
		if err := json.Unmarshal(result.Result, &gotResult); err != nil {
			t.Fatalf("unmarshal Result: %v", err)
		}
		if gotResult.Description != "" || gotResult.Assignee != "" {
			t.Fatalf("Result = %+v, want empty description and assignee", gotResult)
		}
	})

	t.Run("an issue Linear cannot find is not found", func(t *testing.T) {
		server, _ := linearFakeServer(t, `{"data":{"issue":null}}`)
		defer server.Close()
		_, err := runLinearAction(t, server, "get_issue", map[string]any{"issue": "ENG-999"})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrNotFound)
		}
	})

	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{"missing issue", map[string]any{}},
		{"unknown field", map[string]any{"issue": "ENG-42", "extra": "nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := linearNoCallServer(t)
			defer server.Close()
			_, err := runLinearAction(t, server, "get_issue", tc.input)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
			}
		})
	}
}

func TestLinearListIssuesAction(t *testing.T) {
	t.Run("resolves a team key then lists its issues", func(t *testing.T) {
		server, got := linearFakeServer(t,
			`{"data":{"teams":{"nodes":[{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d"}]}}}`,
			`{"data":{"issues":{"nodes":[`+
				`{"id":"i1","identifier":"ENG-1","title":"First","state":{"name":"Todo"},"url":"https://linear.app/acme/issue/ENG-1"},`+
				`{"id":"i2","identifier":"ENG-2","title":"Second","state":{"name":"Todo"},"url":"https://linear.app/acme/issue/ENG-2"}`+
				`]}}}`,
		)
		defer server.Close()

		result, err := runLinearAction(t, server, "list_issues", map[string]any{"team": "ENG", "state": "unstarted", "limit": 10})
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if len(*got) != 2 {
			t.Fatalf("requests = %d, want 2 (team lookup then issues)", len(*got))
		}
		if (*got)[0].Variables["key"] != "ENG" {
			t.Fatalf("team lookup variables = %v, want key ENG", (*got)[0].Variables)
		}
		if !strings.Contains((*got)[1].Query, "issues(filter:") {
			t.Fatalf("query = %q, want it to call issues(filter:)", (*got)[1].Query)
		}
		filter, _ := (*got)[1].Variables["filter"].(map[string]any)
		team, _ := filter["team"].(map[string]any)
		teamID, _ := team["id"].(map[string]any)
		if teamID["eq"] != "9cfb482a-81e3-4154-b5b9-2c805e70a02d" {
			t.Fatalf("filter.team.id.eq = %v, want the resolved team ID", teamID["eq"])
		}
		state, _ := filter["state"].(map[string]any)
		stateType, _ := state["type"].(map[string]any)
		if stateType["eq"] != "unstarted" {
			t.Fatalf("filter.state.type.eq = %v, want unstarted", stateType["eq"])
		}
		if (*got)[1].Variables["first"] != float64(10) {
			t.Fatalf("first = %v, want 10", (*got)[1].Variables["first"])
		}
		var summaries []linearIssueSummary
		if err := json.Unmarshal(result.Result, &summaries); err != nil {
			t.Fatalf("unmarshal Result: %v", err)
		}
		want := []linearIssueSummary{
			{ID: "i1", Identifier: "ENG-1", Title: "First", State: "Todo", URL: "https://linear.app/acme/issue/ENG-1"},
			{ID: "i2", Identifier: "ENG-2", Title: "Second", State: "Todo", URL: "https://linear.app/acme/issue/ENG-2"},
		}
		if !reflect.DeepEqual(summaries, want) {
			t.Fatalf("Result = %+v, want %+v", summaries, want)
		}
		if result.Resource != (integrationsv1.Resource{}) {
			t.Fatalf("Resource = %+v, want the zero value for a read", result.Resource)
		}
	})

	t.Run("a team ID needs no lookup, limit defaults, and no state means no filter", func(t *testing.T) {
		server, got := linearFakeServer(t, `{"data":{"issues":{"nodes":[]}}}`)
		defer server.Close()

		result, err := runLinearAction(t, server, "list_issues", map[string]any{"team": "9cfb482a-81e3-4154-b5b9-2c805e70a02d"})
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if len(*got) != 1 {
			t.Fatalf("requests = %d, want 1 (no team lookup for a UUID)", len(*got))
		}
		if (*got)[0].Variables["first"] != float64(20) {
			t.Fatalf("first = %v, want default 20", (*got)[0].Variables["first"])
		}
		filter, _ := (*got)[0].Variables["filter"].(map[string]any)
		if _, has := filter["state"]; has {
			t.Fatalf("filter = %v, want no state key when none is given", filter)
		}
		if string(result.Result) != "[]" {
			t.Fatalf("Result = %s, want [] for no issues", result.Result)
		}
	})

	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{"missing team", map[string]any{}},
		{"unknown state", map[string]any{"team": "ENG", "state": "archived"}},
		{"limit too high", map[string]any{"team": "ENG", "limit": 51}},
		{"limit negative", map[string]any{"team": "ENG", "limit": -1}},
		{"unknown field", map[string]any{"team": "ENG", "extra": "nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := linearNoCallServer(t)
			defer server.Close()
			_, err := runLinearAction(t, server, "list_issues", tc.input)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
			}
		})
	}
}

func TestLinearCreateIssueAction(t *testing.T) {
	t.Run("resolves a team key and sends description and priority", func(t *testing.T) {
		server, got := linearFakeServer(t,
			`{"data":{"teams":{"nodes":[{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d"}]}}}`,
			`{"data":{"issueCreate":{"success":true,"issue":{"id":"i9","identifier":"ENG-9","title":"New bug","url":"https://linear.app/acme/issue/ENG-9"}}}}`,
		)
		defer server.Close()

		result, err := runLinearAction(t, server, "create_issue", map[string]any{
			"team": "ENG", "title": "New bug", "description": "Steps to reproduce.", "priority": 1,
		})
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if len(*got) != 2 {
			t.Fatalf("requests = %d, want 2 (team lookup then issueCreate)", len(*got))
		}
		if !strings.Contains((*got)[1].Query, "issueCreate") {
			t.Fatalf("query = %q, want it to call issueCreate", (*got)[1].Query)
		}
		variables, _ := (*got)[1].Variables["input"].(map[string]any)
		if variables["teamId"] != "9cfb482a-81e3-4154-b5b9-2c805e70a02d" || variables["title"] != "New bug" ||
			variables["description"] != "Steps to reproduce." || variables["priority"] != float64(1) {
			t.Fatalf("input = %v, want teamId/title/description/priority", variables)
		}
		wantResource := integrationsv1.Resource{Type: "issue", ID: "i9", Name: "New bug", URL: "https://linear.app/acme/issue/ENG-9"}
		if result.Resource != wantResource {
			t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
		}
		var gotResult linearIssueCreated
		if err := json.Unmarshal(result.Result, &gotResult); err != nil {
			t.Fatalf("unmarshal Result: %v", err)
		}
		wantResult := linearIssueCreated{ID: "i9", Identifier: "ENG-9", Title: "New bug", URL: "https://linear.app/acme/issue/ENG-9"}
		if gotResult != wantResult {
			t.Fatalf("Result = %+v, want %+v", gotResult, wantResult)
		}
	})

	t.Run("omits description and priority when neither is given", func(t *testing.T) {
		server, got := linearFakeServer(t,
			`{"data":{"issueCreate":{"success":true,"issue":{"id":"i1","identifier":"ENG-1","title":"Bug","url":"https://linear.app/acme/issue/ENG-1"}}}}`)
		defer server.Close()

		if _, err := runLinearAction(t, server, "create_issue", map[string]any{"team": "9cfb482a-81e3-4154-b5b9-2c805e70a02d", "title": "Bug"}); err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		variables, _ := (*got)[0].Variables["input"].(map[string]any)
		if _, has := variables["description"]; has {
			t.Fatalf("input = %v, want no description when none is given", variables)
		}
		if _, has := variables["priority"]; has {
			t.Fatalf("input = %v, want no priority when none is given", variables)
		}
	})

	t.Run("priority zero is sent, not treated as absent", func(t *testing.T) {
		server, got := linearFakeServer(t,
			`{"data":{"issueCreate":{"success":true,"issue":{"id":"i1","identifier":"ENG-1","title":"Bug","url":"https://linear.app/acme/issue/ENG-1"}}}}`)
		defer server.Close()

		if _, err := runLinearAction(t, server, "create_issue", map[string]any{
			"team": "9cfb482a-81e3-4154-b5b9-2c805e70a02d", "title": "Bug", "priority": 0,
		}); err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		variables, _ := (*got)[0].Variables["input"].(map[string]any)
		if value, has := variables["priority"]; !has || value != float64(0) {
			t.Fatalf("input priority = %v, present %v; want 0, present", value, has)
		}
	})

	t.Run("success false is an error, not a silent success", func(t *testing.T) {
		server, _ := linearFakeServer(t, `{"data":{"issueCreate":{"success":false}}}`)
		defer server.Close()
		_, err := runLinearAction(t, server, "create_issue", map[string]any{"team": "9cfb482a-81e3-4154-b5b9-2c805e70a02d", "title": "Bug"})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{"missing team", map[string]any{"title": "Bug"}},
		{"missing title", map[string]any{"team": "ENG"}},
		{"priority too high", map[string]any{"team": "ENG", "title": "Bug", "priority": 5}},
		{"priority negative", map[string]any{"team": "ENG", "title": "Bug", "priority": -1}},
		{"unknown field", map[string]any{"team": "ENG", "title": "Bug", "extra": "nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := linearNoCallServer(t)
			defer server.Close()
			_, err := runLinearAction(t, server, "create_issue", tc.input)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
			}
		})
	}
}

func TestLinearCommentOnIssueAction(t *testing.T) {
	t.Run("resolves an identifier to a UUID before commentCreate", func(t *testing.T) {
		server, got := linearFakeServer(t,
			`{"data":{"issue":{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d"}}}`,
			`{"data":{"commentCreate":{"success":true,"comment":{"id":"c1","url":"https://linear.app/acme/issue/ENG-42#comment-c1"}}}}`,
		)
		defer server.Close()

		result, err := runLinearAction(t, server, "comment_on_issue", map[string]any{"issue": "ENG-42", "body": "Thanks!"})
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if len(*got) != 2 {
			t.Fatalf("requests = %d, want 2 (issue lookup then commentCreate)", len(*got))
		}
		if !strings.Contains((*got)[0].Query, "issue(id:") {
			t.Fatalf("first query = %q, want it to resolve the identifier via issue(id:)", (*got)[0].Query)
		}
		if (*got)[0].Variables["id"] != "ENG-42" {
			t.Fatalf("issue lookup variables = %v, want id ENG-42", (*got)[0].Variables)
		}
		if !strings.Contains((*got)[1].Query, "commentCreate") {
			t.Fatalf("second query = %q, want it to call commentCreate", (*got)[1].Query)
		}
		variables, _ := (*got)[1].Variables["input"].(map[string]any)
		if variables["issueId"] != "9cfb482a-81e3-4154-b5b9-2c805e70a02d" || variables["body"] != "Thanks!" {
			t.Fatalf("input = %v, want issueId 9cfb482a-81e3-4154-b5b9-2c805e70a02d, body Thanks!", variables)
		}
		wantResource := integrationsv1.Resource{Type: "comment", ID: "c1", URL: "https://linear.app/acme/issue/ENG-42#comment-c1"}
		if result.Resource != wantResource {
			t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
		}
		var gotResult linearCommentCreated
		if err := json.Unmarshal(result.Result, &gotResult); err != nil {
			t.Fatalf("unmarshal Result: %v", err)
		}
		wantResult := linearCommentCreated{ID: "c1", URL: "https://linear.app/acme/issue/ENG-42#comment-c1"}
		if gotResult != wantResult {
			t.Fatalf("Result = %+v, want %+v", gotResult, wantResult)
		}
	})

	t.Run("a UUID needs no lookup", func(t *testing.T) {
		server, got := linearFakeServer(t,
			`{"data":{"commentCreate":{"success":true,"comment":{"id":"c2","url":"https://linear.app/acme/issue/ENG-1#comment-c2"}}}}`)
		defer server.Close()

		if _, err := runLinearAction(t, server, "comment_on_issue", map[string]any{
			"issue": "9cfb482a-81e3-4154-b5b9-2c805e70a02d", "body": "hi",
		}); err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if len(*got) != 1 {
			t.Fatalf("requests = %d, want 1 (no lookup for a UUID)", len(*got))
		}
		variables, _ := (*got)[0].Variables["input"].(map[string]any)
		if variables["issueId"] != "9cfb482a-81e3-4154-b5b9-2c805e70a02d" {
			t.Fatalf("issueId = %v, want the given UUID unchanged", variables["issueId"])
		}
	})

	t.Run("an identifier Linear does not know is not found, and nothing else is called", func(t *testing.T) {
		server, got := linearFakeServer(t, `{"data":{"issue":null}}`)
		defer server.Close()
		_, err := runLinearAction(t, server, "comment_on_issue", map[string]any{"issue": "ENG-999", "body": "hi"})
		if !errors.Is(err, domain.ErrNotFound) || len(*got) != 1 {
			t.Fatalf("Run() error = %v after %d requests, want domain.ErrNotFound after the lookup alone", err, len(*got))
		}
	})

	t.Run("success false is an error, not a silent success", func(t *testing.T) {
		server, _ := linearFakeServer(t, `{"data":{"commentCreate":{"success":false}}}`)
		defer server.Close()
		_, err := runLinearAction(t, server, "comment_on_issue", map[string]any{"issue": "9cfb482a-81e3-4154-b5b9-2c805e70a02d", "body": "hi"})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{"missing issue", map[string]any{"body": "hi"}},
		{"missing body", map[string]any{"issue": "ENG-42"}},
		{"unknown field", map[string]any{"issue": "ENG-42", "body": "hi", "extra": "nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := linearNoCallServer(t)
			defer server.Close()
			_, err := runLinearAction(t, server, "comment_on_issue", tc.input)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
			}
		})
	}
}

func TestLinearCloseIssueAction(t *testing.T) {
	t.Run("looks up the team's completed state, then moves the issue into it", func(t *testing.T) {
		server, got := linearFakeServer(t,
			`{"data":{"issue":{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d","identifier":"ENG-42","title":"Bug found",`+
				`"url":"https://linear.app/acme/issue/ENG-42","team":{"states":{"nodes":[{"id":"state-done","name":"Done"}]}}}}}`,
			`{"data":{"issueUpdate":{"success":true,"issue":{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d","identifier":"ENG-42",`+
				`"title":"Bug found","state":{"name":"Done"},"url":"https://linear.app/acme/issue/ENG-42"}}}}`,
		)
		defer server.Close()

		result, err := runLinearAction(t, server, "close_issue", map[string]any{"issue": "ENG-42"})
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if len(*got) != 2 {
			t.Fatalf("requests = %d, want 2 (state lookup then issueUpdate)", len(*got))
		}
		if !strings.Contains((*got)[0].Query, `states(filter: { type: { eq: "completed" } }`) {
			t.Fatalf("query = %q, want it to look up the team's completed state", (*got)[0].Query)
		}
		if (*got)[0].Variables["id"] != "ENG-42" {
			t.Fatalf("lookup variables = %v, want id ENG-42", (*got)[0].Variables)
		}
		if !strings.Contains((*got)[1].Query, "issueUpdate") {
			t.Fatalf("query = %q, want it to call issueUpdate", (*got)[1].Query)
		}
		if (*got)[1].Variables["id"] != "9cfb482a-81e3-4154-b5b9-2c805e70a02d" {
			t.Fatalf("issueUpdate id = %v, want the issue's UUID, resolved from the lookup", (*got)[1].Variables["id"])
		}
		variables, _ := (*got)[1].Variables["input"].(map[string]any)
		if variables["stateId"] != "state-done" {
			t.Fatalf("input.stateId = %v, want state-done", variables["stateId"])
		}
		wantResource := integrationsv1.Resource{
			Type: "issue", ID: "9cfb482a-81e3-4154-b5b9-2c805e70a02d", Name: "Bug found", URL: "https://linear.app/acme/issue/ENG-42",
		}
		if result.Resource != wantResource {
			t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
		}
		var gotResult linearIssueClosed
		if err := json.Unmarshal(result.Result, &gotResult); err != nil {
			t.Fatalf("unmarshal Result: %v", err)
		}
		wantResult := linearIssueClosed{
			ID: "9cfb482a-81e3-4154-b5b9-2c805e70a02d", Identifier: "ENG-42", State: "Done", URL: "https://linear.app/acme/issue/ENG-42",
		}
		if gotResult != wantResult {
			t.Fatalf("Result = %+v, want %+v", gotResult, wantResult)
		}
	})

	t.Run("a team with no completed state is domain.ErrInvalid, and issueUpdate is never called", func(t *testing.T) {
		server, got := linearFakeServer(t,
			`{"data":{"issue":{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d","identifier":"ENG-42","title":"Bug found",`+
				`"url":"https://linear.app/acme/issue/ENG-42","team":{"states":{"nodes":[]}}}}}`,
		)
		defer server.Close()
		_, err := runLinearAction(t, server, "close_issue", map[string]any{"issue": "ENG-42"})
		if !errors.Is(err, domain.ErrInvalid) || len(*got) != 1 {
			t.Fatalf("Run() error = %v after %d requests, want domain.ErrInvalid after the lookup alone", err, len(*got))
		}
	})

	t.Run("an unknown issue is not found", func(t *testing.T) {
		server, _ := linearFakeServer(t, `{"data":{"issue":null}}`)
		defer server.Close()
		_, err := runLinearAction(t, server, "close_issue", map[string]any{"issue": "ENG-999"})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrNotFound)
		}
	})

	t.Run("success false is an error, not a silent success", func(t *testing.T) {
		server, _ := linearFakeServer(t,
			`{"data":{"issue":{"id":"9cfb482a-81e3-4154-b5b9-2c805e70a02d","identifier":"ENG-42","title":"Bug found",`+
				`"url":"https://linear.app/acme/issue/ENG-42","team":{"states":{"nodes":[{"id":"state-done","name":"Done"}]}}}}}`,
			`{"data":{"issueUpdate":{"success":false}}}`,
		)
		defer server.Close()
		_, err := runLinearAction(t, server, "close_issue", map[string]any{"issue": "ENG-42"})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{"missing issue", map[string]any{}},
		{"unknown field", map[string]any{"issue": "ENG-42", "extra": "nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := linearNoCallServer(t)
			defer server.Close()
			_, err := runLinearAction(t, server, "close_issue", tc.input)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
			}
		})
	}
}

// TestLinearActionsErrorMapping checks that Linear's GraphQL error extensions
// map to the same domain errors through an action's Run as they do through
// Register and Unregister: linearGraphQL is shared, but this task's contract
// asks for the mapping to be checked through an action too.
func TestLinearActionsErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{"authentication error", `{"errors":[{"message":"Not authenticated","extensions":{"type":"authentication error"}}]}`, domain.ErrCredentialRejected},
		{"forbidden", `{"errors":[{"message":"Not allowed","extensions":{"type":"forbidden"}}]}`, domain.ErrForbidden},
		{"ratelimit", `{"errors":[{"message":"Too many requests","extensions":{"code":"RATELIMIT_EXCEEDED"}}]}`, domain.ErrProviderUnavailable},
		{"not found", `{"errors":[{"message":"Entity not found: Issue"}]}`, domain.ErrNotFound},
		{"anything else", `{"errors":[{"message":"Bad input value","extensions":{"type":"SomeOtherType"}}]}`, domain.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := linearFakeServer(t, tc.body)
			defer server.Close()
			_, err := runLinearAction(t, server, "get_issue", map[string]any{"issue": "ENG-42"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run() error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestLinearActionsCatalog checks linearActions' own contract: the five
// actions, in order, with the Writes the task spec calls for, wired into
// linearProvider so a caller building the provider gets them too.
func TestLinearActionsCatalog(t *testing.T) {
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
	actions := linearActions("https://api.linear.test/graphql")
	if len(actions) != len(want) {
		t.Fatalf("linearActions() has %d actions, want %d", len(actions), len(want))
	}
	for index, tc := range want {
		action := actions[index]
		if action.Name != tc.name || action.Writes != tc.writes {
			t.Errorf("actions[%d] = %q, writes %v; want %q, writes %v", index, action.Name, action.Writes, tc.name, tc.writes)
		}
		if action.Summary == "" || strings.Contains(action.Summary, "\n") {
			t.Errorf("actions[%d] %q needs a one-line Summary", index, action.Name)
		}
		if action.Run == nil {
			t.Errorf("actions[%d] %q has no Run", index, action.Name)
		}
		if !ActionNamePattern.MatchString(action.Name) {
			t.Errorf("action name %q does not match ActionNamePattern", action.Name)
		}
	}
	provider := linearProvider("https://api.linear.test/graphql")
	if len(provider.Actions) != len(want) {
		t.Fatalf("linearProvider().Actions = %d, want %d", len(provider.Actions), len(want))
	}
}

// TestLinearActionSchemas checks the shape every action's Input must have to
// be usable as a tool definition: a JSON object schema that refuses unknown
// properties, where every declared property tells a model both its type and
// what it is for.
func TestLinearActionSchemas(t *testing.T) {
	for _, action := range linearActions("https://api.linear.test/graphql") {
		t.Run(action.Name, func(t *testing.T) {
			var schema struct {
				Type                 string                    `json:"type"`
				Properties           map[string]map[string]any `json:"properties"`
				Required             []string                  `json:"required"`
				AdditionalProperties *bool                     `json:"additionalProperties"`
			}
			if err := json.Unmarshal(action.Input, &schema); err != nil {
				t.Fatalf("Input is not valid JSON: %v (%s)", err, action.Input)
			}
			if schema.Type != "object" {
				t.Errorf("type = %q, want object", schema.Type)
			}
			if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
				t.Errorf("additionalProperties = %v, want false", schema.AdditionalProperties)
			}
			if len(schema.Properties) == 0 {
				t.Errorf("properties is empty")
			}
			for name, property := range schema.Properties {
				propType, _ := property["type"].(string)
				if propType == "" {
					t.Errorf("property %s has no type", name)
				}
				description, _ := property["description"].(string)
				if description == "" {
					t.Errorf("property %s has no description", name)
				}
			}
			for _, name := range schema.Required {
				if _, ok := schema.Properties[name]; !ok {
					t.Errorf("required field %q is not a declared property", name)
				}
			}
		})
	}
}

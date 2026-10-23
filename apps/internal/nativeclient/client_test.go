package nativeclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fixedSigner struct {
	audience, purpose string
}

func (signer *fixedSigner) Token(_ context.Context, audience, purpose string) (string, error) {
	signer.audience, signer.purpose = audience, purpose
	return "service-token", nil
}

func TestConnectSendsTheSignedRequestAndAnswersTheURL(t *testing.T) {
	t.Parallel()
	var got ConnectRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/connect" || r.Header.Get("Authorization") != "Bearer service-token" {
			t.Errorf("request = %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"url":"https://github.com/login/oauth/authorize?state=x"}`))
	}))
	defer server.Close()
	signer := &fixedSigner{}
	client, err := New(Config{BaseURL: server.URL + "/", Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	want := ConnectRequest{Org: "org_1", Member: "usr_1", App: "github", Level: "personal", ReturnTo: "https://app.example/done"}
	url, err := client.Connect(context.Background(), want)
	if err != nil || url != "https://github.com/login/oauth/authorize?state=x" {
		t.Fatalf("Connect() = %q, %v", url, err)
	}
	if got != want || signer.audience != Audience || signer.purpose != ConnectPurpose {
		t.Fatalf("sent %+v as %q/%q", got, signer.audience, signer.purpose)
	}
}

func TestConnectMapsTheServicesRefusals(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		status int
		code   string
		want   error
	}{
		"not set up":  {http.StatusConflict, "not_set_up", ErrNotSetUp},
		"broken app":  {http.StatusConflict, "app_broken", ErrBroken},
		"unknown app": {http.StatusNotFound, "not_found", ErrNotFound},
		"invalid":     {http.StatusBadRequest, "invalid_request", ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(`{"error":{"code":"` + testCase.code + `","message":"words"}}`))
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, Signer: &fixedSigner{}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Connect(context.Background(), ConnectRequest{}); !errors.Is(err, testCase.want) {
				t.Fatalf("Connect() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestConnectDoesNotFollowARedirectWithItsToken(t *testing.T) {
	t.Parallel()
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the redirect target was called")
	}))
	defer elsewhere.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Signer: &fixedSigner{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Connect(context.Background(), ConnectRequest{}); err == nil {
		t.Fatal("Connect() followed a redirect")
	}
}

func TestNewRefusesAURLAServiceTokenShouldNotTravelOn(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "http://integrations.internal", "ftp://localhost", "https://user:pw@host", "https://host?x=1", "/relative"} {
		if _, err := New(Config{BaseURL: raw, Signer: &fixedSigner{}}); err == nil {
			t.Errorf("New(%q) accepted the URL", raw)
		}
	}
	if _, err := New(Config{BaseURL: "https://integrations.oryxa.in", Signer: &fixedSigner{}}); err != nil {
		t.Errorf("https refused: %v", err)
	}
	if _, err := New(Config{BaseURL: "https://integrations.oryxa.in"}); err == nil || !strings.Contains(err.Error(), "signer") {
		t.Errorf("no signer: %v", err)
	}
}

func TestCallToolSendsTheRequestAndAnswersOutputOrTheAppsError(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		answer    string
		output    string
		toolError *ToolError
	}{
		"output":     {`{"output":{"n":1}}`, `{"n":1}`, nil},
		"tool error": {`{"tool_error":{"code":"rate_limited","message":"slow"}}`, "", &ToolError{Code: "rate_limited", Message: "slow"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got CallRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/tools/call" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewDecoder(r.Body).Decode(&got)
				_, _ = w.Write([]byte(testCase.answer))
			}))
			defer server.Close()
			signer := &fixedSigner{}
			client, err := New(Config{BaseURL: server.URL, Signer: signer})
			if err != nil {
				t.Fatal(err)
			}
			want := CallRequest{
				Org: "org_1", Member: "usr_1", Agent: "member", Role: "member", Connection: "icn_1",
				Tool: "github.list_issues", Input: json.RawMessage(`{"a":1}`), Confirmed: true,
			}
			result, err := client.CallTool(context.Background(), want)
			if err != nil || string(result.Output) != testCase.output || (result.ToolError == nil) != (testCase.toolError == nil) ||
				(testCase.toolError != nil && *result.ToolError != *testCase.toolError) {
				t.Fatalf("CallTool() = %+v, %v", result, err)
			}
			if string(got.Input) != `{"a":1}` || got.Tool != want.Tool || got.Connection != want.Connection || !got.Confirmed || signer.purpose != ToolsPurpose {
				t.Fatalf("sent %+v as purpose %q", got, signer.purpose)
			}
		})
	}
}

func TestCallToolMapsTheServicesRefusals(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		status int
		code   string
		want   error
	}{
		"not found":        {http.StatusNotFound, "not_found", ErrNotFound},
		"confirmation":     {http.StatusConflict, "confirmation_required", ErrConfirmation},
		"needs reauth":     {http.StatusConflict, "needs_reauth", ErrNeedsReauth},
		"unavailable":      {http.StatusConflict, "connection_unavailable", ErrUnavailable},
		"output too large": {http.StatusBadGateway, "output_too_large", ErrOutputTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(`{"error":{"code":"` + testCase.code + `","message":"words"}}`))
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, Signer: &fixedSigner{}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.CallTool(context.Background(), CallRequest{}); !errors.Is(err, testCase.want) {
				t.Fatalf("CallTool() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestListConnectionsAsksForTheOrgsOrAMembersConnections(t *testing.T) {
	t.Parallel()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer service-token" {
			t.Errorf("request = %s %q", r.Method, r.Header.Get("Authorization"))
		}
		paths = append(paths, r.URL.RequestURI())
		_, _ = w.Write([]byte(`{"connections":[{"connection_id":"icn_1","organization_id":"org_1","member_id":"usr_1",` +
			`"level":"personal","app":"github","mode":"oryxa","status":"ok","updated_at":"2026-10-01T10:00:00Z"}]}`))
	}))
	defer server.Close()
	signer := &fixedSigner{}
	client, err := New(Config{BaseURL: server.URL, Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := client.ListConnections(context.Background(), "org_1", "")
	if err != nil || len(listed) != 1 || listed[0].ID != "icn_1" || listed[0].MemberID != "usr_1" || listed[0].UpdatedAt.IsZero() {
		t.Fatalf("ListConnections() = %+v, %v", listed, err)
	}
	if signer.audience != Audience || signer.purpose != ConnectionsPurpose {
		t.Fatalf("signed as %q/%q", signer.audience, signer.purpose)
	}
	if _, err := client.ListConnections(context.Background(), "org_1", "usr 1"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/connections?org=org_1" || paths[1] != "/v1/connections?member=usr+1&org=org_1" {
		t.Fatalf("paths = %q", paths)
	}
}

// An answer that does not say which connections there are must never read as
// "none": reconciling would retire every row.
func TestListConnectionsRefusesAnAnswerWithoutTheList(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		status int
		body   string
	}{
		"no field":      {http.StatusOK, `{}`},
		"null":          {http.StatusOK, `{"connections":null}`},
		"service fails": {http.StatusInternalServerError, `{"error":{"code":"internal"}}`},
		"not JSON":      {http.StatusOK, `<html>`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, Signer: &fixedSigner{}})
			if err != nil {
				t.Fatal(err)
			}
			if listed, err := client.ListConnections(context.Background(), "org_1", ""); err == nil {
				t.Fatalf("ListConnections() = %+v, want an error", listed)
			}
		})
	}
}

func TestListToolsAsksForAMembersToolsAndAnswersThem(t *testing.T) {
	t.Parallel()
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer service-token" {
			t.Errorf("request = %s %q", r.Method, r.Header.Get("Authorization"))
		}
		path = r.URL.RequestURI()
		_, _ = w.Write([]byte(`{"tools":[{"app":"github","name":"github.list_issues","description":"Lists issues",` +
			`"effect":"read","input_schema":{"type":"object"}}]}`))
	}))
	defer server.Close()
	signer := &fixedSigner{}
	client, err := New(Config{BaseURL: server.URL, Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(context.Background(), "org_1", "usr_1", "github")
	if err != nil || len(tools) != 1 {
		t.Fatalf("ListTools() = %+v, %v", tools, err)
	}
	want := Tool{App: "github", Name: "github.list_issues", Description: "Lists issues", Effect: "read"}
	got := tools[0]
	if got.App != want.App || got.Name != want.Name || got.Description != want.Description || got.Effect != want.Effect ||
		string(got.InputSchema) != `{"type":"object"}` {
		t.Fatalf("tool = %+v", got)
	}
	if path != "/v1/tools?app=github&member=usr_1&org=org_1" || signer.purpose != ToolsPurpose {
		t.Fatalf("asked %q as %q", path, signer.purpose)
	}
	if _, err := client.ListTools(context.Background(), "org_1", "usr_1", ""); err != nil || path != "/v1/tools?member=usr_1&org=org_1" {
		t.Fatalf("without an app: %q, %v", path, err)
	}
}

func TestListToolsMapsTheServicesRefusals(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_request"}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Signer: &fixedSigner{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListTools(context.Background(), "org_1", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ListTools() error = %v, want invalid", err)
	}
}

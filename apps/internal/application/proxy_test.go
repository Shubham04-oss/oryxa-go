package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

func TestProxyReadsTheAppAsTheCallersConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.composio.proxyResult = composio.ProxyResponse{
		Status: 200, Headers: map[string]string{"Link": "<next>"}, Data: json.RawMessage(`{"tree":[]}`),
	}

	result, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{
		Toolkit: "github", Endpoint: "/repos/oryxa/oryxa-go/git/trees/main?recursive=1",
		Headers: map[string]string{"Accept": "application/vnd.github+json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 200 || string(result.Data) != `{"tree":[]}` || result.Headers["Link"] != "<next>" {
		t.Fatalf("result = %+v", result)
	}
	if len(h.composio.proxied) != 1 {
		t.Fatalf("proxied = %d", len(h.composio.proxied))
	}
	request := h.composio.proxied[0]
	// The account is the caller's own, and the endpoint keeps its query.
	if request.ConnectedAccountID != "ca_1" || request.Method != http.MethodGet {
		t.Fatalf("request = %+v", request)
	}
	if request.Endpoint != "/repos/oryxa/oryxa-go/git/trees/main?recursive=1" {
		t.Fatalf("endpoint = %q", request.Endpoint)
	}
	if len(request.Parameters) != 1 || request.Parameters[0].Type != "header" ||
		request.Parameters[0].Name != "Accept" {
		t.Fatalf("parameters = %+v", request.Parameters)
	}
}

func TestProxyRefusesAnythingThatIsNotARead(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		toolkit  string
		method   string
		endpoint string
		want     error
	}{
		"a POST to an app with no read POST": {
			toolkit: "github", method: http.MethodPost, endpoint: "/repos/oryxa/oryxa-go/issues", want: domain.ErrNotReadOnly,
		},
		"a PATCH": {
			toolkit: "github", method: http.MethodPatch, endpoint: "/repos/oryxa/oryxa-go/issues/1", want: domain.ErrNotReadOnly,
		},
		"a DELETE": {
			toolkit: "github", method: http.MethodDelete, endpoint: "/repos/oryxa/oryxa-go", want: domain.ErrNotReadOnly,
		},
		"a PUT": {
			toolkit: "notion", method: http.MethodPut, endpoint: "/v1/pages", want: domain.ErrNotReadOnly,
		},
		"a POST that is not the app's read endpoint": {
			toolkit: "notion", method: http.MethodPost, endpoint: "/v1/pages", want: domain.ErrNotReadOnly,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.activeConnection("apc_1", "user-1", testCase.toolkit, "ca_1")

			_, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{
				Toolkit: testCase.toolkit, Method: testCase.method, Endpoint: testCase.endpoint,
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			if len(h.composio.proxied) != 0 {
				t.Fatal("the call reached Composio anyway")
			}
		})
	}
}

func TestProxyAllowsTheReadEndpointsThatArePosts(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		toolkit  string
		endpoint string
	}{
		"Notion's search":           {toolkit: "notion", endpoint: "/v1/search"},
		"a Notion database query":   {toolkit: "notion", endpoint: "/v1/databases/2f1c4ba9d0e84f4d9a6b3c8e5d7a1b20/query"},
		"a Slack channel history":   {toolkit: "slack", endpoint: "/api/conversations.history"},
		"a Notion query with query": {toolkit: "notion", endpoint: "/v1/search?page_size=100"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.activeConnection("apc_1", "user-1", testCase.toolkit, "ca_1")

			if _, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{
				Toolkit: testCase.toolkit, Method: http.MethodPost, Endpoint: testCase.endpoint,
				Body: json.RawMessage(`{"page_size":100}`),
			}); err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(h.composio.proxied) != 1 {
				t.Fatalf("proxied = %d", len(h.composio.proxied))
			}
		})
	}
}

func TestProxySaysAWriteIsAWriteEvenWithNothingConnected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// The request is wrong whichever way the connection went, and a caller
	// debugging a refusal is better told that than sent looking for a
	// connection it would not have been allowed to use anyway.
	_, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{
		Toolkit: "github", Method: http.MethodPost, Endpoint: "/repos/oryxa/oryxa-go/issues",
	})
	if !errors.Is(err, domain.ErrNotReadOnly) {
		t.Fatalf("error = %v, want not read-only", err)
	}
	// Named by connection alone, the app is not known until it is read, so a
	// missing connection is still what a caller hears first.
	if _, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{
		ConnectionID: "apc_nope", Method: http.MethodPost, Endpoint: "/repos/oryxa/oryxa-go/issues",
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	// A caller naming another app than its connection's does not get around
	// the check: the connection's own app decides.
	h.activeConnection("apc_1", "user-1", "notion", "ca_1")
	if _, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{
		ConnectionID: "apc_1", Toolkit: "notion", Method: http.MethodPost, Endpoint: "/v1/pages",
	}); !errors.Is(err, domain.ErrNotReadOnly) {
		t.Fatalf("error = %v, want not read-only", err)
	}
	if len(h.composio.proxied) != 0 {
		t.Fatal("a refused call reached Composio")
	}
}

func TestProxyRefusesAnEndpointThatIsNotTheAppsOwn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.activeConnection("apc_1", "user-1", "github", "ca_1")

	for name, endpoint := range map[string]string{
		"another host":         "https://evil.example/steal",
		"a protocol-less host": "//evil.example/steal",
		"a relative path":      "repos/oryxa",
		"climbing out":         "/repos/../../admin",
		"encoded climbing":     "/repos/%2e%2e/%2e%2e/admin",
		"nothing":              "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{
				Toolkit: "github", Endpoint: endpoint,
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error = %v, want invalid", err)
			}
		})
	}
	if len(h.composio.proxied) != 0 {
		t.Fatal("a refused endpoint reached Composio")
	}
}

func TestProxyRefusesHeadersThatAreNotTheCallersToSet(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.activeConnection("apc_1", "user-1", "github", "ca_1")

	for _, header := range []string{"Authorization", "authorization", "Cookie", "Host", "X-API-Key"} {
		_, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{
			Toolkit: "github", Endpoint: "/user", Headers: map[string]string{header: "someone else's"},
		})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s = %v, want invalid", header, err)
		}
	}
	if len(h.composio.proxied) != 0 {
		t.Fatal("a forged header reached Composio")
	}
}

func TestProxyUsesOnlyTheCallersOwnConnections(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	theirs := h.activeConnection("apc_2", "user-2", "github", "ca_2")

	for name, input := range map[string]ProxyInput{
		"another member's connection":     {ConnectionID: theirs.ID, Endpoint: "/user"},
		"an app they have not connected":  {Toolkit: "github", Endpoint: "/user"},
		"neither a connection nor an app": {Endpoint: "/user"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.Proxy(context.Background(), memberToken, "org_1", input); err == nil {
				t.Fatal("it read anyway")
			}
		})
	}
	if len(h.composio.proxied) != 0 {
		t.Fatal("a refused call reached Composio")
	}
}

func TestProxySaysWhatComposioSaid(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.composio.proxyErr = composio.ErrRateLimited

	_, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{Toolkit: "github", Endpoint: "/user"})
	if !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("error = %v, want rate limited", err)
	}
}

func TestProxyCarriesABinaryAnswerByURL(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.composio.proxyResult = composio.ProxyResponse{
		Status: 200, Binary: &composio.BinaryData{URL: "https://files.composio.dev/abc", ContentType: "application/pdf"},
	}

	result, err := h.service.Proxy(context.Background(), memberToken, "org_1", ProxyInput{
		Toolkit: "github", Endpoint: "/repos/oryxa/oryxa-go/contents/handbook.pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A file is not read into this process: the caller fetches it from where
	// Composio put it.
	if result.BinaryURL != "https://files.composio.dev/abc" || result.ContentType != "application/pdf" {
		t.Fatalf("result = %+v", result)
	}
}

func TestBlankIDsKeepsTheRouteAndHidesTheIdentifiers(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"/v1/databases/2f1c4ba9d0e84f4d9a6b3c8e5d7a1b20/query":          "/v1/databases/{}/query",
		"/v1/blocks/2f1c4ba9-d0e8-4f4d-9a6b-3c8e5d7a1b20/children/list": "/v1/blocks/{}/children/list",
		"/repos/oryxa/oryxa-go/issues/42":                               "/repos/oryxa/oryxa-go/issues/{}",
		"/v1/search":                                                    "/v1/search",
		"/api/conversations.history/":                                   "/api/conversations.history",
	} {
		if got := blankIDs(path); got != want {
			t.Errorf("blankIDs(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestProxyRejectsSlackMutatingGETRPCsEvenForAgents(t *testing.T) {
	for _, caller := range []authclient.Credentials{memberToken, agentToken} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			for _, endpoint := range []string{"/api/auth.revoke", "/api/conversations.inviteShared?channel=C123&user_ids=U123", "/api/chat.delete?channel=C123&ts=123", "/api/new.method"} {
				h := newHarness(t)
				h.activeConnection("apc_1", "user-1", "slack", "ca_1")
				_, err := h.service.Proxy(context.Background(), caller, "org_1", ProxyInput{Toolkit: "slack", Method: method, Endpoint: endpoint})
				if !errors.Is(err, domain.ErrNotReadOnly) || len(h.composio.proxied) != 0 {
					t.Errorf("%s %s reached provider or was accepted: %v", method, endpoint, err)
				}
			}
		}
	}
}

func TestProxyRetainsExplicitSlackReads(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		for _, endpoint := range []string{"/api/conversations.history?channel=C123", "/api/users.conversations", "/api/search.messages?query=test"} {
			h := newHarness(t)
			h.activeConnection("apc_1", "user-1", "slack", "ca_1")
			_, err := h.service.Proxy(context.Background(), agentToken, "org_1", ProxyInput{Toolkit: "slack", Method: method, Endpoint: endpoint})
			if err != nil || len(h.composio.proxied) != 1 {
				t.Errorf("%s %s = %v requests=%d", method, endpoint, err, len(h.composio.proxied))
			}
		}
	}
}

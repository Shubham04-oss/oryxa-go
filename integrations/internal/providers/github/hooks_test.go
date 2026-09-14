package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateRepositoryHookUsesGitHubContract(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/repos/oryxa/api/hooks" ||
			request.Header.Get("authorization") != "Bearer token" || request.Header.Get("x-github-api-version") != githubAPIVersion {
			t.Fatalf("request = %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("authorization"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || !strings.Contains(string(body), `"url":"https://connect.example.com/v1/webhooks/opaque"`) ||
			!strings.Contains(string(body), `"content_type":"json"`) || !strings.Contains(string(body), `"events":["issues"]`) ||
			!strings.Contains(string(body), `"secret":"abcdefghijklmnopqrstuvwxyz012345"`) {
			t.Fatalf("body/error = %s / %v", body, err)
		}
		writer.Header().Set("content-type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"id":12345,"url":"https://api.github.com/repos/oryxa/api/hooks/12345"}`))
	}))
	defer server.Close()
	hook, err := (Client{HTTPClient: server.Client(), BaseURL: server.URL}).Create(context.Background(), "token", HookRequest{
		Scope: HookScopeRepository, Owner: "oryxa", Repo: "api", Events: []string{EventIssues},
		URL: "https://connect.example.com/v1/webhooks/opaque", Secret: webhookSecret,
	})
	if err != nil || hook.ID != "12345" || hook.URL != "https://api.github.com/repos/oryxa/api/hooks/12345" {
		t.Fatalf("hook/error = %+v / %v", hook, err)
	}
}

func TestCreateOrganizationHookAndRemoveFollowGitHubContract(t *testing.T) {
	t.Parallel()
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.Method+" "+request.URL.Path)
		if request.Header.Get("authorization") != "Bearer token" {
			t.Fatal("missing GitHub bearer token")
		}
		switch request.Method {
		case http.MethodPost:
			if request.URL.Path != "/orgs/oryxa/hooks" {
				t.Fatalf("org path = %s", request.URL.Path)
			}
			writer.Header().Set("content-type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"id":99,"url":"https://api.github.com/orgs/oryxa/hooks/99"}`))
		case http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL}
	hook, err := client.Create(context.Background(), "token", HookRequest{
		Scope: HookScopeOrganization, Owner: "oryxa", Events: []string{EventPullRequest},
		URL: "https://connect.example.com/v1/webhooks/opaque", Secret: webhookSecret,
	})
	if err != nil || hook.ID != "99" {
		t.Fatalf("org hook/error = %+v / %v", hook, err)
	}
	if err := client.Remove(context.Background(), "token", HookState{ID: "99", Scope: HookScopeOrganization, Owner: "oryxa"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "POST /orgs/oryxa/hooks,DELETE /orgs/oryxa/hooks/99" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestHookRejectsUnsafeInputsAndClassifiesAuthorization(t *testing.T) {
	t.Parallel()
	client := Client{}
	if _, err := client.Create(context.Background(), "token", HookRequest{Scope: "user", Owner: "oryxa", Events: []string{EventIssues}, URL: "http://insecure", Secret: "short"}); !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("error = %v", err)
	}
	if err := client.Remove(context.Background(), "token", HookState{ID: "1?redirect=other", Scope: HookScopeRepository, Owner: "oryxa", Repo: "api"}); !errors.Is(err, ErrInvalidHook) {
		t.Fatalf("unsafe hook ID error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"message":"access-token-value"}`))
	}))
	defer server.Close()
	err := (Client{HTTPClient: server.Client(), BaseURL: server.URL}).Remove(context.Background(), "access-token-value", HookState{
		ID: "1", Scope: HookScopeRepository, Owner: "oryxa", Repo: "api",
	})
	var rejection RejectionError
	if !errors.As(err, &rejection) || !IsAuthorizationRejection(err) || rejection.StatusCode != http.StatusUnauthorized || strings.Contains(err.Error(), "access-token-value") {
		t.Fatalf("rejection = %#v", err)
	}
}

func TestHookStateUsesExistingTriggerStoreProtocolWithoutSecret(t *testing.T) {
	t.Parallel()
	state := HookState{ID: "12345", URL: "https://api.github.com/repos/oryxa/api/hooks/12345", Scope: HookScopeRepository, Owner: "oryxa", Repo: "api"}
	mutations := state.StoreMutations()
	if len(mutations) != 5 {
		t.Fatalf("mutations = %+v", mutations)
	}
	store := map[string]any{}
	for _, mutation := range mutations {
		if strings.Contains(mutation.Key, "secret") {
			t.Fatalf("secret leaked into store: %+v", mutation)
		}
		store[mutation.Key] = mutation.Value
	}
	loaded, err := HookStateFromStore(store)
	if err != nil || loaded.ID != state.ID || loaded.Owner != state.Owner || loaded.Repo != state.Repo {
		t.Fatalf("loaded/error = %+v / %v", loaded, err)
	}
}

func TestNewHookSecretIsRandomURLSafe(t *testing.T) {
	t.Parallel()
	secret, err := NewHookSecret(strings.NewReader(strings.Repeat("a", 32)))
	if err != nil || len(secret) < 32 || strings.ContainsAny(secret, "+/=") {
		t.Fatalf("secret/error = %q / %v", secret, err)
	}
}

func TestDefaultHTTPClientHasBoundedTimeout(t *testing.T) {
	t.Parallel()
	client := (Client{}).client()
	if client == http.DefaultClient || client.Timeout != defaultTimeout || client.Timeout <= 0 {
		t.Fatalf("default client = %#v", client)
	}
}

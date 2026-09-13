package microsoftgraph

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateSubscriptionUsesGraphContract(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/subscriptions" || request.Header.Get("authorization") != "Bearer token" {
			t.Fatalf("request = %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("authorization"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || !strings.Contains(string(body), `"notificationUrl":"https://connect.example.com/v1/webhooks/opaque"`) ||
			!strings.Contains(string(body), `"clientState":"abcdefghijklmnopqrstuvwxyz012345"`) {
			t.Fatalf("body/error = %s / %v", body, err)
		}
		writer.Header().Set("content-type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"id":"sub-1","resource":"/me/messages","changeType":"created","expirationDateTime":"2026-09-14T12:00:00Z"}`))
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now }}
	subscription, err := client.Create(context.Background(), "token", SubscriptionRequest{
		ChangeType: "created", Resource: "/me/messages", NotificationURL: "https://connect.example.com/v1/webhooks/opaque",
		ClientState: "abcdefghijklmnopqrstuvwxyz012345", ExpirationDateTime: now.Add(24 * time.Hour),
	})
	if err != nil || subscription.ID != "sub-1" || subscription.ExpirationDateTime.IsZero() {
		t.Fatalf("subscription/error = %+v / %v", subscription, err)
	}
}

func TestRenewAndRemoveFollowGraphContract(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.Method+" "+request.URL.Path)
		if request.Header.Get("authorization") != "Bearer token" {
			t.Fatal("missing Graph bearer token")
		}
		switch request.Method {
		case http.MethodPatch:
			writer.Header().Set("content-type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"sub-1","expirationDateTime":"2026-09-14T12:00:00Z"}`))
		case http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now }}
	if _, err := client.Renew(context.Background(), "token", "sub-1", now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := client.Remove(context.Background(), "token", "sub-1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "PATCH /subscriptions/sub-1,DELETE /subscriptions/sub-1" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSubscriptionRejectsUnsafeInputsAndProviderErrors(t *testing.T) {
	t.Parallel()
	client := Client{Now: func() time.Time { return time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC) }}
	if _, err := client.Create(context.Background(), "token", SubscriptionRequest{ChangeType: "created", Resource: "https://wrong", NotificationURL: "http://not-secure", ClientState: "short"}); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("error = %v", err)
	}
	if err := client.Remove(context.Background(), "token", "sub-1?redirect=other"); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("unsafe subscription ID error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusConflict)
		_, _ = writer.Write([]byte(`{"error":{"code":"SubscriptionIdAlreadyExists"}}`))
	}))
	defer server.Close()
	client.HTTPClient, client.BaseURL = server.Client(), server.URL
	_, err := client.Create(context.Background(), "token", SubscriptionRequest{ChangeType: "created", Resource: "/me/messages", NotificationURL: "https://connect.example.com/hook", ClientState: "abcdefghijklmnopqrstuvwxyz012345", ExpirationDateTime: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)})
	if !errors.Is(err, ErrGraphRejected) || !strings.Contains(err.Error(), "SubscriptionIdAlreadyExists") {
		t.Fatalf("error = %v", err)
	}
}

func TestGraphRejectionClassifiesAuthorizationWithoutResponseBody(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"code":"InvalidAuthenticationToken","message":"access-token-value"}}`))
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL}
	err := client.Remove(context.Background(), "access-token-value", "sub-1")
	var rejection RejectionError
	if !errors.As(err, &rejection) || !IsAuthorizationRejection(err) || rejection.StatusCode != http.StatusUnauthorized || strings.Contains(err.Error(), "access-token-value") {
		t.Fatalf("rejection = %#v", err)
	}
}

func TestGraphRejectionSanitizesUntrustedProviderCode(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"code":"access-token-value leaked"}}`))
	}))
	defer server.Close()
	err := (Client{HTTPClient: server.Client(), BaseURL: server.URL}).Remove(context.Background(), "token", "sub-1")
	if err == nil || !strings.Contains(err.Error(), "request_failed") || strings.Contains(err.Error(), "access-token-value") {
		t.Fatalf("unsafe rejection = %v", err)
	}
}

func TestNewClientStateIsRandomURLSafeSecret(t *testing.T) {
	t.Parallel()
	state, err := NewClientState(strings.NewReader(strings.Repeat("a", 32)))
	if err != nil || len(state) < 32 || strings.ContainsAny(state, "+/=") {
		t.Fatalf("state/error = %q / %v", state, err)
	}
}

func TestDefaultHTTPClientHasBoundedTimeout(t *testing.T) {
	t.Parallel()
	client := (Client{}).client()
	if client == http.DefaultClient || client.Timeout != defaultTimeout || client.Timeout <= 0 {
		t.Fatalf("default client = %#v", client)
	}
}

func TestSubscriptionStateUsesExistingTriggerStoreProtocolWithoutSecret(t *testing.T) {
	t.Parallel()
	state := Subscription{ID: "sub-1", ExpirationDateTime: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}.State()
	mutations := state.StoreMutations()
	if len(mutations) != 2 || strings.Contains(mutations[0].Key+mutations[1].Key, "client") {
		t.Fatalf("mutations = %+v", mutations)
	}
	store := map[string]any{mutations[0].Key: mutations[0].Value, mutations[1].Key: mutations[1].Value}
	loaded, err := SubscriptionStateFromStore(store)
	if err != nil || loaded.ID != state.ID || !loaded.ExpiresAt.Equal(state.ExpiresAt) {
		t.Fatalf("loaded/error = %+v / %v", loaded, err)
	}
}

package knowledgeclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSourcesUsesMemberTokenAndFiltersOnServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/v1/organizations/org%201/sources" ||
			r.URL.Query().Get("provider") != "github" || r.URL.Query().Get("connection_id") != "conn/1" ||
			r.URL.Query().Has("scope") || r.Header.Get("Authorization") != "Bearer member-token" {
			t.Errorf("unexpected request: %s %s, auth=%q", r.Method, r.URL.String(), r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"sources":[{"id":"s1","provider":"github","connection_id":"conn/1","target":{"repository":"org/repo"},"status":"active","last_synced_at":"2026-09-20T10:00:00Z"}]}`))
	}))
	defer server.Close()
	sources, err := New(server.URL+"/", nil).Sources(context.Background(), "org 1", "member-token", "github", "conn/1")
	if err != nil || len(sources) != 1 || sources[0].Target["repository"] != "org/repo" || sources[0].LastSyncedAt == nil {
		t.Fatalf("Sources() = %+v, %v", sources, err)
	}
}

func TestSourcesDoesNotLeakErrorBodies(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusForbidden, ErrDenied},
		{http.StatusInternalServerError, ErrUnavailable},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`private diagnostic`))
		}))
		_, err := New(server.URL, nil).Sources(context.Background(), "org", "token", "", "")
		server.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: %v, want %v", tc.status, err, tc.want)
		}
	}
}

func TestSyncSourceOnlySchedulesExistingSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/organizations/org%201/sources/source%2F1/sync" ||
			r.Header.Get("Authorization") != "Bearer member-token" {
			t.Errorf("unexpected sync: %s %s auth=%q", r.Method, r.URL.String(), r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":"source/1","status":"active","sync_at":"2026-09-21T10:00:00Z"}`))
	}))
	defer server.Close()
	result, err := New(server.URL, nil).SyncSource(context.Background(), "org 1", "source/1", "member-token")
	if err != nil || result.ID != "source/1" || result.SyncAt == nil || result.LastSyncedAt != nil {
		t.Fatalf("SyncSource() = %+v, %v", result, err)
	}
}

func TestSyncSourceDistinguishesReconnectFromOtherConflict(t *testing.T) {
	for _, tc := range []struct {
		body string
		want error
	}{
		{`{"error":{"code":"needs_reconnect"}}`, ErrNeedsReconnect},
		{`{"error":{"code":"conflict"}}`, ErrConflict},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := New(server.URL, nil).SyncSource(context.Background(), "org", "source", "token")
		server.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("body %s: %v, want %v", tc.body, err, tc.want)
		}
	}
}

package filesclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

func TestObjectsCarriesTheMembersOwnToken(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request
		_, _ = writer.Write([]byte(`{"files":[]}`))
	}))
	defer server.Close()

	if _, err := New(server.URL).Objects(context.Background(), "org_1", "member-token", Query{}); err != nil {
		t.Fatalf("Objects() error = %v", err)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer member-token" {
		t.Fatalf("Authorization = %q, want the member's own bearer token", got)
	}
	if !strings.Contains(seen.URL.Path, "/v1/organizations/org_1/files") {
		t.Fatalf("path = %q, want the organization's files", seen.URL.Path)
	}
}

func TestObjectsNarrowsToATeamWhenOneIsNamed(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request
		_, _ = writer.Write([]byte(`{"files":[]}`))
	}))
	defer server.Close()

	if _, err := New(server.URL).Objects(context.Background(), "org_1", "token", Query{
		Scope: "team", TeamID: "team_1",
	}); err != nil {
		t.Fatalf("Objects() error = %v", err)
	}
	if got := seen.URL.Query().Get("team_id"); got != "team_1" {
		t.Fatalf("team_id = %q, want team_1: files never includes a team's files unless one is named", got)
	}
}

func TestObjectsReadsWhatFilesAnswered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"files":[{
			"id":"file_1","organization_id":"org_1","scope":"organization","filename":"notes.md",
			"content_type":"text/markdown","purpose":"upload","size_bytes":12,
			"sha256":"abc","created_by":"user-1","created_at":"2026-09-17T12:00:00Z",
			"expires_at":"2026-10-17T12:00:00Z"}]}`))
	}))
	defer server.Close()

	objects, err := New(server.URL).Objects(context.Background(), "org_1", "token", Query{})
	if err != nil {
		t.Fatalf("Objects() error = %v", err)
	}
	if len(objects) != 1 {
		t.Fatalf("Objects() = %d objects, want 1", len(objects))
	}
	if objects[0].ID != "file_1" || objects[0].ContentType != "text/markdown" || objects[0].SHA256 != "abc" {
		t.Fatalf("Objects() = %+v", objects[0])
	}
}

func TestContentReadsTheBytesAndTheTypeFilesServedThemAs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/markdown")
		_, _ = writer.Write([]byte("# notes"))
	}))
	defer server.Close()

	body, contentType, err := New(server.URL).Content(context.Background(), "org_1", "file_1", "token")
	if err != nil {
		t.Fatalf("Content() error = %v", err)
	}
	if string(body) != "# notes" {
		t.Fatalf("Content() body = %q, want %q", body, "# notes")
	}
	if contentType != "text/markdown" {
		t.Fatalf("Content() type = %q, want text/markdown", contentType)
	}
}

func TestContentReportsAFileThatQuietlyExpiredAsMissing(t *testing.T) {
	// Files publishes no event when retention lapses; it simply stops
	// answering, so a caller learns about it here.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	if _, _, err := New(server.URL).Content(context.Background(), "org_1", "file_1", "token"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Content() error = %v, want ErrNotFound", err)
	}
}

func TestContentRefusesMoreThanFilesEverStores(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", maxObjectBytes+1)))
	}))
	defer server.Close()

	if _, _, err := New(server.URL).Content(context.Background(), "org_1", "file_1", "token"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Content() error = %v, want ErrInvalid", err)
	}
}

func TestFilesAnswersMapToReasonsACallerActsOn(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized:        domain.ErrUnauthorized,
		http.StatusForbidden:           domain.ErrForbidden,
		http.StatusNotFound:            domain.ErrNotFound,
		http.StatusTooManyRequests:     domain.ErrRateLimited,
		http.StatusBadGateway:          domain.ErrProviderUnavailable,
		http.StatusUnprocessableEntity: domain.ErrInvalid,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := New(server.URL).Objects(context.Background(), "org_1", "token", Query{})
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("Objects() on %d error = %v, want %v", status, err, want)
		}
	}
}

func TestFilesNotAnsweringIsNotTheCallersFault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()

	if _, err := New(address).Objects(context.Background(), "org_1", "token", Query{}); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Objects() error = %v, want ErrProviderUnavailable", err)
	}
}

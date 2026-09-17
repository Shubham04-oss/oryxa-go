package filesclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

func TestFetchReadsMetadataThenContentAsTheCaller(t *testing.T) {
	var seen []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request)
		switch request.URL.Path {
		case "/v1/organizations/org_1/files/file_1":
			_, _ = writer.Write([]byte(`{"id":"file_1","filename":"notes.md","content_type":"text/markdown"}`))
		case "/v1/organizations/org_1/files/file_1/content":
			// The content type a citation carries comes from the metadata
			// call, not from whatever this endpoint happens to answer with.
			writer.Header().Set("Content-Type", "application/octet-stream")
			_, _ = writer.Write([]byte("# Notes\n\nSome text."))
		default:
			t.Errorf("unexpected request to %s", request.URL.Path)
		}
	}))
	defer server.Close()

	content, err := New(server.URL).Fetch(context.Background(), "org_1", "file_1", authclient.Credentials{Authorization: "Bearer token-1"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if content.ID != "file_1" || content.Filename != "notes.md" || content.ContentType != "text/markdown" || content.Body != "# Notes\n\nSome text." {
		t.Fatalf("content = %+v", content)
	}
	if len(seen) != 2 {
		t.Fatalf("requests made = %d, want 2 (metadata, then content)", len(seen))
	}
	for _, request := range seen {
		if request.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatalf("%s was not fetched as the caller: %v", request.URL.Path, request.Header)
		}
	}
}

func TestFetchFallsBackToTheAccessCookie(t *testing.T) {
	var seen []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request)
		if strings.HasSuffix(request.URL.Path, "/content") {
			_, _ = writer.Write([]byte("body"))
			return
		}
		_, _ = writer.Write([]byte(`{"id":"file_1","filename":"a.txt","content_type":"text/plain"}`))
	}))
	defer server.Close()

	if _, err := New(server.URL).Fetch(context.Background(), "org_1", "file_1", authclient.Credentials{AccessCookie: "session-1"}); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	for _, request := range seen {
		if request.Header.Get("Authorization") != "" {
			t.Fatalf("Authorization = %q, want none when only a cookie was given", request.Header.Get("Authorization"))
		}
		cookie, err := request.Cookie(authclient.AccessCookieName)
		if err != nil || cookie.Value != "session-1" {
			t.Fatalf("%s cookie = %v, %v, want session-1", request.URL.Path, cookie, err)
		}
	}
}

func TestFetchWithoutCredentialsIsRefusedWithoutAsking(t *testing.T) {
	// A member who cannot read a file cannot index it either, without rag
	// having to know why — but there is nothing to check locally here beyond
	// credentials being present at all, so this much is refused before a
	// request is ever sent to an address that does not exist.
	if _, err := New("http://files.invalid").Fetch(context.Background(), "org_1", "file_1", authclient.Credentials{}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Fetch() error = %v, want ErrUnauthorized", err)
	}
}

func TestFetchMapsFilesStatuses(t *testing.T) {
	tests := map[int]error{
		http.StatusUnauthorized: domain.ErrUnauthorized,
		http.StatusForbidden:    domain.ErrForbidden,
		http.StatusNotFound:     domain.ErrNotFound,
	}
	for status, want := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := New(server.URL).Fetch(context.Background(), "org_1", "file_1", authclient.Credentials{Authorization: "Bearer token"})
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("status %d: error = %v, want %v", status, err, want)
		}
	}
}

func TestFetchOnAnUnrecognisedStatusIsNotMistakenForACallerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := New(server.URL).Fetch(context.Background(), "org_1", "file_1", authclient.Credentials{Authorization: "Bearer token"})
	if err == nil {
		t.Fatal("Fetch() error = nil, want a refusal")
	}
	for _, sentinel := range []error{domain.ErrUnauthorized, domain.ErrForbidden, domain.ErrNotFound, domain.ErrInvalid} {
		if errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want it not to claim to be %v", err, sentinel)
		}
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v, want it to name the status", err)
	}
}

func TestFetchRefusesAFileLargerThanRagWillIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/content") {
			_, _ = writer.Write([]byte(strings.Repeat("x", domain.MaxDocumentBytes+1)))
			return
		}
		_, _ = writer.Write([]byte(`{"id":"file_1","filename":"big.txt","content_type":"text/plain"}`))
	}))
	defer server.Close()

	if _, err := New(server.URL).Fetch(context.Background(), "org_1", "file_1", authclient.Credentials{Authorization: "Bearer token"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Fetch() error = %v, want ErrInvalid", err)
	}
}

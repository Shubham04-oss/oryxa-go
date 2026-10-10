package appsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

func TestPagesAsksTheConnectionsOwnRouteAsTheMember(t *testing.T) {
	var seen *http.Request
	var body PageRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request
		_ = json.NewDecoder(request.Body).Decode(&body)
		_, _ = writer.Write([]byte(`{"items": []}`))
	}))
	defer server.Close()

	_, err := New(server.URL).Pages(context.Background(), "org_1", "apc_1", "member-token",
		PageRequest{Target: map[string]string{"reader": "issues"}, Limit: 1})
	if err != nil {
		t.Fatalf("Pages() error = %v", err)
	}
	if seen.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", seen.Method)
	}
	if want := "/v1/organizations/org_1/apps/connections/apc_1/pages"; seen.URL.Path != want {
		t.Fatalf("path = %q, want %q", seen.URL.Path, want)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer member-token" {
		t.Fatalf("Authorization = %q, want the member's own token", got)
	}
	if got := seen.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := seen.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
	if body.Target["reader"] != "issues" || body.Limit != 1 || body.Full {
		t.Fatalf("body = %+v, want the check's own single item", body)
	}
}

// TestPageRequestIsTheWireAppsReads is what holds this mirror to the contract
// it duplicates: the JSON is compared whole, so a field renamed, dropped or
// respelled on one side fails here rather than at a member's first pass.
func TestPageRequestIsTheWireAppsReads(t *testing.T) {
	watermark := time.Date(2026, time.September, 29, 9, 0, 0, 0, time.UTC)
	asked, err := json.Marshal(PageRequest{
		Target:    map[string]string{"reader": "issues", "repository": "acme/api"},
		Cursor:    "page-2",
		Watermark: &watermark,
		Token:     "start-token",
		Full:      true,
		Limit:     50,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"target":{"reader":"issues","repository":"acme/api"},"cursor":"page-2",` +
		`"watermark":"2026-09-29T09:00:00Z","token":"start-token","full":true,"limit":50}`
	if string(asked) != want {
		t.Fatalf("PageRequest JSON =\n%s\nwant\n%s", asked, want)
	}
	// What a first page asks for carries none of the optional fields, so a
	// reader cannot mistake an absent window for an empty one.
	asked, err = json.Marshal(PageRequest{Target: map[string]string{"reader": "issues"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(asked) != `{"target":{"reader":"issues"},"full":false,"limit":1}` {
		t.Fatalf("PageRequest JSON = %s, want only what a first page asks for", asked)
	}
}

func TestPagesReadsAPageAsTheReaderGaveIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"items": [{
				"external_id": "42", "kind": "issue", "title": "Flaky test",
				"url": "https://example.test/issues/42", "mime": "text/markdown",
				"content": "IyB0aXRsZQ==",
				"metadata": {"state": "open"},
				"updated_at": "2026-09-29T09:00:00Z"
			}],
			"cursor": "page-2", "watermark": "2026-09-29T09:00:00Z", "token": "start-token",
			"removed": ["17"]
		}`))
	}))
	defer server.Close()

	page, err := New(server.URL).Pages(context.Background(), "org_1", "apc_1", "t", PageRequest{Limit: 1})
	if err != nil {
		t.Fatalf("Pages() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("page = %+v, want one item", page)
	}
	item := page.Items[0]
	if item.ExternalID != "42" || item.Kind != "issue" || item.Title != "Flaky test" ||
		item.URL != "https://example.test/issues/42" || item.MIME != "text/markdown" ||
		string(item.Content) != "# title" || item.Metadata["state"] != "open" {
		t.Fatalf("item = %+v, want the issue as the reader gave it", item)
	}
	if item.UpdatedAt == nil || !item.UpdatedAt.Equal(time.Date(2026, time.September, 29, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("UpdatedAt = %v, want the moment the reader gave", item.UpdatedAt)
	}
	if page.Cursor != "page-2" || page.Token != "start-token" ||
		page.Watermark == nil || !page.Watermark.Equal(*item.UpdatedAt) {
		t.Fatalf("page = %+v, want the walk's own place carried on", page)
	}
	if len(page.Removed) != 1 || page.Removed[0] != "17" {
		t.Fatalf("Removed = %v, want what the reader said is gone", page.Removed)
	}
}

func TestPagesRefusesACallerWithNothingToIdentifyThem(t *testing.T) {
	// No request is worth making: apps would only say the same thing back.
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()

	if _, err := New(server.URL).Pages(context.Background(), "org_1", "apc_1", "  ", PageRequest{Limit: 1}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Pages() error = %v, want ErrUnauthorized", err)
	}
	if called {
		t.Fatal("Pages() asked apps with no member token to place it")
	}
}

func TestPagesRefusesAnAnswerThatIsNotJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("not json"))
	}))
	defer server.Close()

	if _, err := New(server.URL).Pages(context.Background(), "org_1", "apc_1", "t", PageRequest{Limit: 1}); err == nil {
		t.Fatal("Pages() error = nil, want a refusal")
	}
}

func TestPagesRefusesAPageHeavierThanAppsMayAnswer(t *testing.T) {
	// 6 MiB is what a page may weigh. A heavier one is apps' own doing, and
	// is refused rather than read into memory to be discarded.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"items": [{"external_id": "1", "content": "`))
		_, _ = writer.Write(make([]byte, maxPageBytes))
	}))
	defer server.Close()

	_, err := New(server.URL).Pages(context.Background(), "org_1", "apc_1", "t", PageRequest{Limit: 1})
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Pages() error = %v, want ErrProviderUnavailable", err)
	}
}

// TestPagesAnswersMapToReasonsAPassActsOn is what each refusal is read as: a
// run backs off, gives up, or pauses every source on the connection, and the
// code is what says which.
func TestPagesAnswersMapToReasonsAPassActsOn(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		code   string
		want   error
	}{
		{name: "the member's own token is refused", status: 401, code: "unauthorized", want: domain.ErrUnauthorized},
		{name: "another member's connection", status: 404, code: "not_found", want: domain.ErrNotFound},
		{name: "the member may not read it", status: 403, code: "forbidden", want: domain.ErrNotFound},
		{name: "the credential is spent", status: 409, code: "needs_reconnect", want: domain.ErrNeedsReconnect},
		{name: "the connection is not connected", status: 409, code: "not_connected", want: domain.ErrNeedsReconnect},
		{name: "a conflict says nothing about the credential", status: 409, want: domain.ErrInvalid},
		{name: "the target or reader is not one apps can read", status: 400, code: "invalid_request", want: domain.ErrInvalid},
		{name: "too much, too fast", status: 429, code: "rate_limited", want: domain.ErrRateLimited},
		{name: "apps is down", status: 503, code: "unavailable", want: domain.ErrProviderUnavailable},
		{name: "apps fell over", status: 500, want: domain.ErrProviderUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := "{}"
			if test.code != "" {
				body = fmt.Sprintf(`{"error": {"code": %q, "message": "refused"}}`, test.code)
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(body))
			}))
			defer server.Close()

			_, err := New(server.URL).Pages(context.Background(), "org_1", "apc_1", "t", PageRequest{Limit: 1})
			if !errors.Is(err, test.want) {
				t.Fatalf("Pages() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPagesNotAnsweringIsNotTheSourcesFault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()

	if _, err := New(address).Pages(context.Background(), "org_1", "apc_1", "t", PageRequest{Limit: 1}); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Pages() error = %v, want ErrProviderUnavailable", err)
	}
}

// TestPagesNeverQuoteTheMemberTokenBack keeps the token out of everything a
// failure says. An answer is read for its code and nothing else, so even an
// apps that echoed the token back would not put it in a run's log line.
func TestPagesNeverQuoteTheMemberTokenBack(t *testing.T) {
	const token = "member-token-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.URL.Query().Get("access_token"); got != "" {
			t.Errorf("a credential must never travel in the query: got %q", got)
		}
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(writer, `{"error": {"code": "invalid_request", "message": %q}}`, "saw "+token)
	}))
	defer server.Close()

	_, err := New(server.URL).Pages(context.Background(), "org_1", "apc_1", token, PageRequest{Limit: 1})
	if err == nil {
		t.Fatal("Pages() error = nil, want a refusal")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error quotes the member's token: %v", err)
	}
}

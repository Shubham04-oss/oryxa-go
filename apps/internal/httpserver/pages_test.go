package httpserver

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// The pages route is how knowledge reads a member's Oryxa connections: a
// member token, only the connection's own member, a PageRequest in and a Page
// out, and neither more than a page can be.
func TestPagesRouteServesThePageAndRefusesTheRest(t *testing.T) {
	t.Parallel()
	const path = "/v1/organizations/org_1/apps/connections/apc_oryxa/pages"
	watermark := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	fake := &fakeService{page: integration.Page{
		Items:     []integration.Item{{ExternalID: "acme/api#1", Kind: "issue", Title: "Flaky test"}},
		Cursor:    "page-2",
		Watermark: &watermark,
	}}
	server := newServer(t, fake)
	const body = `{"target":{"reader":"issues"},"limit":1}`

	t.Run("the page itself", func(t *testing.T) {
		recorder := call(server, http.MethodPost, path, body, "Authorization", "Bearer member")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
		}
		var page integration.Page
		if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ExternalID != "acme/api#1" || page.Cursor != "page-2" {
			t.Fatalf("page = %+v", page)
		}
		if page.Watermark == nil || !page.Watermark.Equal(watermark) {
			t.Fatalf("watermark = %v", page.Watermark)
		}
		// The request reaches the application whole, under its own keys.
		if fake.pageRequest.Target["reader"] != "issues" || fake.pageRequest.Limit != 1 {
			t.Fatalf("the application was asked for %+v", fake.pageRequest)
		}
		if fake.organization != "org_1" {
			t.Fatalf("the application was asked for organization %q", fake.organization)
		}
	})

	t.Run("without a token", func(t *testing.T) {
		recorder := call(server, http.MethodPost, path, body)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401: %s", recorder.Code, recorder.Body)
		}
	})

	t.Run("a body over the page size", func(t *testing.T) {
		oversized := `{"target":{"reader":"issues"},"cursor":"` + strings.Repeat("x", integration.MaxPageBytes) + `"}`
		recorder := call(server, http.MethodPost, path, oversized, "Authorization", "Bearer member")
		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413: %s", recorder.Code, recorder.Body)
		}
	})

	t.Run("a field the page request does not take", func(t *testing.T) {
		recorder := call(server, http.MethodPost, path,
			`{"target":{"reader":"issues"},"order":"desc"}`, "Authorization", "Bearer member")
		if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_request" {
			t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
		}
	})

	t.Run("a body that is not an object", func(t *testing.T) {
		recorder := call(server, http.MethodPost, path, `["issues"]`, "Authorization", "Bearer member")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
		}
	})

	t.Run("a NUL in a cursor", func(t *testing.T) {
		recorder := call(server, http.MethodPost, path,
			`{"target":{"reader":"issues"},"cursor":"a`+jsonNUL+`b"}`, "Authorization", "Bearer member")
		if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_request" {
			t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
		}
	})

	t.Run("another member's connection", func(t *testing.T) {
		failing := &fakeService{err: domain.ErrNotFound}
		recorder := call(newServer(t, failing), http.MethodPost, path, body, "Authorization", "Bearer other")
		if recorder.Code != http.StatusNotFound || errorCode(t, recorder) != "not_found" {
			t.Fatalf("status = %d code = %q, want 404: %s", recorder.Code, errorCode(t, recorder), recorder.Body)
		}
	})

	t.Run("an expired connection", func(t *testing.T) {
		failing := &fakeService{err: domain.ErrNeedsReconnect}
		recorder := call(newServer(t, failing), http.MethodPost, path, body, "Authorization", "Bearer member")
		if recorder.Code != http.StatusConflict || errorCode(t, recorder) != "needs_reconnect" {
			t.Fatalf("status = %d code = %q, want 409: %s", recorder.Code, errorCode(t, recorder), recorder.Body)
		}
	})

	t.Run("only a POST is served", func(t *testing.T) {
		recorder := call(server, http.MethodGet, path, "", "Authorization", "Bearer member")
		if recorder.Code == http.StatusOK {
			t.Fatal("a GET was served")
		}
	})
}

// The targets route and the pages route are the same connection's two reads,
// and both are member routes: an unauthenticated caller reaches neither.
func TestTargetsAndPagesAreBothMemberRoutes(t *testing.T) {
	t.Parallel()
	server := newServer(t, &fakeService{})
	for _, target := range []string{
		"/v1/organizations/org_1/apps/connections/apc_oryxa/targets",
		"/v1/organizations/org_1/apps/connections/apc_oryxa/pages",
	} {
		recorder := call(server, http.MethodPost, target, `{}`)
		if recorder.Code == http.StatusOK {
			t.Fatalf("%s was served without a token", target)
		}
	}
	if !slices.Contains(server.Patterns(), "POST /v1/organizations/{organizationID}/apps/connections/{connectionID}/pages") {
		t.Fatalf("the pages route is not registered: %v", server.Patterns())
	}
}

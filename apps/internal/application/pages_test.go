package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// A page is read through the connection's own integration, by the member the
// connection belongs to, and a Page comes back one for one with knowledge's
// own sources.Page.
func TestPagesAreReadThroughTheConnectionsOwnReader(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	own.pages[""] = integration.Page{
		Items:  []integration.Item{{ExternalID: "acme/api#1", Kind: "issue", Title: "Flaky test"}},
		Cursor: "page-2",
	}
	own.pages["page-2"] = integration.Page{
		Items: []integration.Item{{ExternalID: "acme/api#2", Kind: "issue", Title: "Slow test"}},
	}
	ctx := context.Background()
	target := map[string]string{integration.TargetReader: "issues"}

	page, err := h.service.Pages(ctx, memberToken, "org_1", "apc_oryxa", integration.PageRequest{Target: target, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ExternalID != "acme/api#1" || page.Cursor != "page-2" {
		t.Fatalf("page = %+v", page)
	}
	// The cursor carries the pass on, so the reader drains and resumes.
	page, err = h.service.Pages(ctx, memberToken, "org_1", "apc_oryxa", integration.PageRequest{Target: target, Cursor: "page-2", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ExternalID != "acme/api#2" || page.Cursor != "" {
		t.Fatalf("the second page = %+v", page)
	}
	if calls := h.signin.sessionCalls(); len(calls) != 2 || calls[0] != "apc_oryxa" {
		t.Fatalf("signin was asked for sessions %v", calls)
	}
	if len(h.composio.proxied) != 0 {
		t.Fatalf("composio's proxy was used: %+v", h.composio.proxied)
	}
}

// Another member's page is not found, exactly as another member's connection
// is, because a page is read through the connection's own credential.
func TestPagesAreOnlyForTheConnectionsOwnMember(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_mine", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	own.pages[""] = integration.Page{Items: []integration.Item{{ExternalID: "acme/api#1"}}}
	request := integration.PageRequest{Target: map[string]string{integration.TargetReader: "issues"}}
	ctx := context.Background()

	for _, credentials := range []struct {
		name  string
		token authclient.Credentials
	}{
		{"another member", otherToken}, {"an admin", adminToken},
	} {
		if _, err := h.service.Pages(ctx, credentials.token, "org_1", "apc_mine", request); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("a page for %s = %v, want not found", credentials.name, err)
		}
	}
	// An agent acting for the member may read it, as it may run their tools.
	if _, err := h.service.Pages(ctx, agentToken, "org_1", "apc_mine", request); err != nil {
		t.Fatalf("an agent reading its member's page = %v", err)
	}
	// A member token for another organization is refused before any of this.
	if _, err := h.service.Pages(ctx, memberToken, "org_2", "apc_mine", request); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("another organization = %v, want forbidden", err)
	}
}

// A page past what the route can carry is refused, so a reader is never asked
// to fit its answer into a limit it is told about nowhere.
func TestPagesRefuseAPageLargerThanTheRouteCarries(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	own.pages[""] = integration.Page{Items: []integration.Item{{
		ExternalID: "acme/api#1", Kind: "issue", Content: make([]byte, integration.MaxPageBytes+1),
	}}}
	_, err := h.service.Pages(context.Background(), memberToken, "org_1", "apc_oryxa",
		integration.PageRequest{Target: map[string]string{integration.TargetReader: "issues"}})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an oversized page = %v, want invalid", err)
	}
	if !strings.Contains(err.Error(), "fewer items per page") {
		t.Fatalf("the refusal says %q, which does not tell a reader what to do", err)
	}
}

// A page request that names no reader, or one the manifest does not declare,
// is refused before a session is built.
func TestPagesRefuseARequestItCannotServe(t *testing.T) {
	t.Parallel()
	h, _ := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()

	cases := map[string]integration.PageRequest{
		"no reader at all":              {},
		"an empty reader name":          {Target: map[string]string{integration.TargetReader: "  "}},
		"a reader that is not declared": {Target: map[string]string{integration.TargetReader: "transcripts"}},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.Pages(ctx, memberToken, "org_1", "apc_oryxa", request); err == nil {
				t.Fatal("the request was served")
			}
		})
	}
	if calls := h.signin.sessionCalls(); len(calls) != 0 {
		t.Fatalf("a refused request built a session: %v", calls)
	}
}

// A reader's own failure becomes apps' own reason, so knowledge reads one set
// of words whichever broker answered.
func TestPagesMapAReadersOwnErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		failure error
		is      error
	}{
		{"a refused credential", integration.ErrNeedsReconnect, domain.ErrNeedsReconnect},
		{"something that is not there", integration.ErrNotFound, domain.ErrNotFound},
		{"a rate limit", &integration.RateLimited{}, domain.ErrRateLimited},
		{"a refusal in the app's own words", &integration.AppError{Message: "the folder is gone"}, domain.ErrInvalid},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h, own := newToolHarness(t)
			h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
			own.readErr = test.failure
			_, err := h.service.Pages(context.Background(), memberToken, "org_1", "apc_oryxa",
				integration.PageRequest{Target: map[string]string{integration.TargetReader: "issues"}})
			if !errors.Is(err, test.is) {
				t.Fatalf("Pages = %v, want %v", err, test.is)
			}
		})
	}
}

// An expired connection cannot be read through, and says so, as every other
// path over it does.
func TestPagesRefuseAnExpiredConnection(t *testing.T) {
	t.Parallel()
	h, _ := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionExpired, integration.KindOAuth2Code)
	_, err := h.service.Pages(context.Background(), memberToken, "org_1", "apc_oryxa",
		integration.PageRequest{Target: map[string]string{integration.TargetReader: "issues"}})
	if !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("Pages = %v, want needs reconnect", err)
	}
}

// A toolkit with no reader is not one knowledge can read through, and says so
// rather than serving an empty page that looks like an app with nothing in it.
func TestPagesRefuseAToolkitWithNoReader(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.oryxaConnection("apc_oryxa", "user-1", "github", domain.ConnectionActive, integration.KindOAuth2Code)
	_, err := h.service.Pages(context.Background(), memberToken, "org_1", "apc_oryxa",
		integration.PageRequest{Target: map[string]string{integration.TargetReader: "repositories"}})
	if !errors.Is(err, domain.ErrNotConnectable) {
		t.Fatalf("Pages = %v, want not connectable", err)
	}
}

// Targets are listed from the same reader, in the shape Composio's proxy
// answers them in, so the picker cannot tell the two brokers apart.
func TestConnectionTargetsOverAnOryxaConnectionComeFromTheReader(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	own.targets = []integration.Target{
		{Kind: "repository", ID: "acme/api", Name: "acme/api", Description: "The API",
			Target: json.RawMessage(`{"repository":"acme/api"}`)},
	}
	targets, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", "apc_oryxa")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Kind != "repository" || targets[0].Name != "acme/api" {
		t.Fatalf("targets = %+v", targets)
	}
	if string(targets[0].Target) != `{"repository":"acme/api"}` {
		t.Fatalf("the target payload is %s", targets[0].Target)
	}
	// A listing is a read, so it goes through a session too.
	if calls := h.signin.sessionCalls(); len(calls) != 1 || calls[0] != "apc_oryxa" {
		t.Fatalf("signin was asked for sessions %v", calls)
	}
	if len(h.composio.proxied) != 0 {
		t.Fatalf("composio's proxy was used: %+v", h.composio.proxied)
	}
}

// A toolkit whose integration declares no reader answers an empty list: there
// is nothing here to index, which is a fact about the app rather than a
// failure, and a picker wants to show that rather than a problem.
func TestConnectionTargetsOverAnOryxaConnectionWithNoReaderAreEmpty(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.oryxaConnection("apc_oryxa", "user-1", "github", domain.ConnectionActive, integration.KindOAuth2Code)
	targets, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", "apc_oryxa")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %+v, want none", targets)
	}
}

// A Composio connection's targets are still read through Composio's proxy.
func TestConnectionTargetsOverAComposioConnectionAreUnchanged(t *testing.T) {
	t.Parallel()
	h, _ := newToolHarness(t)
	h.composioConnection("apc_composio", "user-1", "notion")
	targets, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", "apc_composio")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %+v, want none from an empty answer", targets)
	}
	if len(h.composio.proxied) != 1 {
		t.Fatalf("composio was proxied %d times", len(h.composio.proxied))
	}
}

// A Composio connection is never read through the pages route, even for a
// toolkit whose integration has a reader.
func TestPagesRefuseAComposioConnection(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.composioConnection("apc_composio", "user-1", "acme")
	_, err := h.service.Pages(context.Background(), memberToken, "org_1", "apc_composio",
		integration.PageRequest{Target: map[string]string{integration.TargetReader: "issues"}})
	if !errors.Is(err, domain.ErrNotConnectable) || len(own.allCalls()) != 0 {
		t.Fatalf("Pages = %v after %d calls, want not connectable and nothing read", err, len(own.allCalls()))
	}
}

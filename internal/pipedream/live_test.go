package pipedream

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLive calls the real Pipedream project named in the environment, to
// prove the client against the API and not only against what the docs say.
// It is skipped unless PIPEDREAM_LIVE=1, and it only reads, or makes a
// Connect token that expires unused: nothing is deployed, run or deleted.
// Nothing it logs holds a secret, a token or a link.
//
//	set -a; source ~/.config/oryxa/pipedream.env; set +a
//	PIPEDREAM_LIVE=1 go test ./internal/pipedream -run TestLive -v
func TestLive(t *testing.T) {
	if os.Getenv("PIPEDREAM_LIVE") != "1" {
		t.Skip("PIPEDREAM_LIVE is not 1")
	}
	client, err := New(Config{
		ProjectID: os.Getenv("PIPEDREAM_PROJECT_ID"), Environment: os.Getenv("PIPEDREAM_ENVIRONMENT"),
		ClientID: os.Getenv("PIPEDREAM_CLIENT_ID"), ClientSecret: os.Getenv("PIPEDREAM_CLIENT_SECRET"),
	})
	if err != nil {
		t.Skipf("the live project is not configured: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := client.accessToken(ctx); err != nil {
		t.Fatalf("token: %v", err)
	}

	apps, err := client.ListApps(ctx, AppQuery{Search: "gmail", Limit: 2})
	if err != nil || len(apps.Items) == 0 {
		t.Fatalf("ListApps = %d apps, %v", len(apps.Items), err)
	}
	t.Logf("ListApps: %d apps, first %q, next cursor set: %v", len(apps.Items), apps.Items[0].NameSlug, apps.NextCursor != "")
	if apps.NextCursor != "" {
		next, err := client.ListApps(ctx, AppQuery{Search: "gmail", Limit: 2, After: apps.NextCursor})
		if err != nil {
			t.Fatalf("ListApps(after) = %v", err)
		}
		t.Logf("ListApps page 2: %d apps, next cursor set: %v", len(next.Items), next.NextCursor != "")
	}

	actions, err := client.ListActions(ctx, ComponentQuery{App: "gmail", Limit: 100})
	if err != nil || len(actions.Items) == 0 {
		t.Fatalf("ListActions = %d actions, %v", len(actions.Items), err)
	}
	readOnly := 0
	for _, action := range actions.Items {
		if action.Key == "" || action.Version == "" {
			t.Fatalf("an action came back without a key or version: %+v", action)
		}
		if action.ReadOnly() {
			readOnly++
		}
	}
	t.Logf("ListActions(gmail): %d actions, %d read-only, type %q, next cursor set: %v",
		len(actions.Items), readOnly, actions.Items[0].Type, actions.NextCursor != "")

	// Paging by a small limit must reach the same actions, ending on its own.
	paged, after := 0, ""
	for range 20 {
		page, err := client.ListActions(ctx, ComponentQuery{App: "gmail", Limit: 8, After: after})
		if err != nil {
			t.Fatalf("ListActions(paged) = %v", err)
		}
		paged += len(page.Items)
		if after = page.NextCursor; after == "" {
			break
		}
	}
	if paged != len(actions.Items) || after != "" {
		t.Fatalf("paging by 8 reached %d actions, want %d", paged, len(actions.Items))
	}

	triggers, err := client.ListTriggers(ctx, ComponentQuery{App: "gmail"})
	if err != nil || len(triggers.Items) == 0 {
		t.Fatalf("ListTriggers = %d triggers, %v", len(triggers.Items), err)
	}
	t.Logf("ListTriggers(gmail): %d triggers, type %q", len(triggers.Items), triggers.Items[0].Type)

	accounts, err := client.ListAccounts(ctx, "oryxa-live-smoke-test", "gmail")
	if err != nil || len(accounts) != 0 {
		t.Fatalf("ListAccounts(a user with none) = %d accounts, %v", len(accounts), err)
	}
	_, err = client.GetAccount(ctx, "apn_oryxaLiveMissing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetAccount(missing) = %v, want ErrNotFound", err)
	}

	token, err := client.CreateConnectToken(ctx, "oryxa-live-smoke-test", "", "", 5*time.Minute)
	if err != nil {
		t.Fatalf("CreateConnectToken = %v", err)
	}
	link, err := url.Parse(token.ConnectLinkURL)
	if err != nil || link.Scheme != "https" || !strings.HasPrefix(token.Token, "ctok_") {
		t.Fatalf("the connect token or its link is not what the docs describe")
	}
	query := make([]string, 0, len(link.Query()))
	for name := range link.Query() {
		query = append(query, name)
	}
	t.Logf("CreateConnectToken: link host %s, path %s, query names %v, expires in %s",
		link.Host, link.Path, query, time.Until(token.ExpiresAt).Round(time.Minute))
}

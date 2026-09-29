package sources

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// fakeApps answers one connection's reader, recording what it was asked and
// as whom.
type fakeApps struct {
	answer appsclient.Page
	err    error
	asked  []appsclient.PageRequest
	// askedAs is the token each call was made with, so a test can hold that
	// the member's own is what travels.
	askedAs  []string
	askedFor string
	askedCon string
}

func (apps *fakeApps) Pages(_ context.Context, organizationID, connectionID, accessToken string, request appsclient.PageRequest) (appsclient.Page, error) {
	apps.asked = append(apps.asked, request)
	apps.askedAs = append(apps.askedAs, accessToken)
	apps.askedFor, apps.askedCon = organizationID, connectionID
	if apps.err != nil {
		return appsclient.Page{}, apps.err
	}
	return apps.answer, nil
}

// fakeProxy is whatever a toolkit was read by before it could be read through
// apps, scripted so a test can tell which of the two answered.
type fakeProxy struct {
	provider  string
	page      Page
	targetErr error
	calls     []Request
}

func (proxy *fakeProxy) Provider() string { return proxy.provider }

func (proxy *fakeProxy) Target(map[string]string) error { return proxy.targetErr }

func (proxy *fakeProxy) Fetch(_ context.Context, request Request) (Page, error) {
	proxy.calls = append(proxy.calls, request)
	return proxy.page, nil
}

func appsRequest() Request {
	return Request{
		Source: domain.Source{
			ID: "src_1", OrganizationID: "org_1", Provider: "github", ConnectionID: "apc_1",
			Target: map[string]string{"reader": "issues", "repository": "acme/api"},
		},
		Credential: domain.Credential{AccessToken: "member-token"},
		Limit:      100,
	}
}

func TestAppsFetchesThroughTheConnectionsOwnPagesRoute(t *testing.T) {
	updated := time.Date(2026, time.September, 29, 9, 0, 0, 0, time.UTC)
	apps := &fakeApps{answer: appsclient.Page{
		Items: []appsclient.Item{{
			ExternalID: "42", Kind: "issue", Title: "Flaky test", URL: "https://example.test/42",
			MIME: "text/markdown", Content: []byte("# title"), Metadata: map[string]string{"state": "open"},
			UpdatedAt: &updated,
		}},
		Cursor: "page-2", Watermark: &updated, Token: "start-token", Removed: []string{"17"},
	}}

	page, err := NewApps(apps).Fetch(context.Background(), appsRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if apps.askedFor != "org_1" || apps.askedCon != "apc_1" {
		t.Fatalf("asked for %s/%s, want this organization's own connection", apps.askedFor, apps.askedCon)
	}
	// The member's own token is the credential: apps verifies it with auth and
	// answers only for the connection's own member.
	if len(apps.askedAs) != 1 || apps.askedAs[0] != "member-token" {
		t.Fatalf("asked as %v, want the member's own token", apps.askedAs)
	}
	if len(page.Items) != 1 {
		t.Fatalf("page = %+v, want one item", page)
	}
	item := page.Items[0]
	if item.ExternalID != "42" || item.Kind != "issue" || item.Title != "Flaky test" ||
		item.URL != "https://example.test/42" || item.MIME != "text/markdown" ||
		string(item.Content) != "# title" || item.Metadata["state"] != "open" {
		t.Fatalf("item = %+v, want the issue as the reader gave it", item)
	}
	if item.UpdatedAt == nil || !item.UpdatedAt.Equal(updated) {
		t.Fatalf("UpdatedAt = %v, want what the reader gave", item.UpdatedAt)
	}
	// Unchanged is never claimed: there is no Held here, so every item is read
	// and the content hash is what settles it.
	if item.Unchanged {
		t.Fatal("an apps item must never be marked unchanged: nothing here was held back")
	}
	if page.Cursor != "page-2" || page.Token != "start-token" ||
		page.Watermark == nil || !page.Watermark.Equal(updated) {
		t.Fatalf("page = %+v, want the walk's own place carried on", page)
	}
	if len(page.Removed) != 1 || page.Removed[0] != "17" {
		t.Fatalf("Removed = %v, want what the reader said is gone", page.Removed)
	}
}

func TestAppsAsksForWhatChangedSinceTheLastDrainedPass(t *testing.T) {
	watermark := time.Date(2026, time.September, 29, 9, 0, 0, 0, time.UTC)
	apps := &fakeApps{}
	request := appsRequest()
	request.Cursor = domain.Cursor{Value: "page-2", Watermark: &watermark}

	if _, err := NewApps(apps).Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	asked := apps.asked[0]
	if asked.Full {
		t.Fatal("asked for a full pass on an incremental one")
	}
	if asked.Cursor != "page-2" {
		t.Fatalf("Cursor = %q, want the walk resumed where it stopped", asked.Cursor)
	}
	if asked.Watermark == nil || !asked.Watermark.Equal(watermark) {
		t.Fatalf("Watermark = %v, want the window the last pass left", asked.Watermark)
	}
	if asked.Limit != 100 {
		t.Fatalf("Limit = %d, want the page size asked for", asked.Limit)
	}
	if asked.Target["reader"] != "issues" || asked.Target["repository"] != "acme/api" {
		t.Fatalf("Target = %v, want the target the source names", asked.Target)
	}

	// The target is copied rather than shared, so nothing done with what a
	// reader was sent could edit the source it belongs to.
	asked.Target["reader"] = "edited"
	if request.Source.Target["reader"] != "issues" {
		t.Fatal("the target the source holds was edited through the request")
	}
}

func TestAppsCarriesATokenWhereAReaderReadsByOne(t *testing.T) {
	apps := &fakeApps{}
	request := appsRequest()
	request.Cursor = domain.Cursor{Token: "start-token"}

	if _, err := NewApps(apps).Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if apps.asked[0].Token != "start-token" || apps.asked[0].Watermark != nil {
		t.Fatalf("asked for %+v, want the opaque token alone", apps.asked[0])
	}
}

func TestAppsAsksForEverythingOnAFullPass(t *testing.T) {
	watermark := time.Date(2026, time.September, 29, 9, 0, 0, 0, time.UTC)
	apps := &fakeApps{}
	request := appsRequest()
	request.Full = true
	// A full sweep keeps the walk's own place, so a pass that outgrew one run's
	// page budget carries on rather than starting over. The window the last
	// drained pass left is not part of it: everything counts.
	request.Cursor = domain.Cursor{Value: "page-2", Watermark: &watermark, Token: "start-token"}

	if _, err := NewApps(apps).Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	asked := apps.asked[0]
	if !asked.Full || asked.Cursor != "page-2" {
		t.Fatalf("asked for %+v, want a full pass resumed where the walk stands", asked)
	}
	if asked.Watermark != nil || asked.Token != "" {
		t.Fatalf("asked for %+v, want no window on a full pass", asked)
	}
}

func TestAppsKeepsAPageInsideWhatItAsksFor(t *testing.T) {
	apps := &fakeApps{}
	request := appsRequest()
	request.Limit = 5000

	if _, err := NewApps(apps).Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if apps.asked[0].Limit != appsMaxPage {
		t.Fatalf("Limit = %d, want at most %d", apps.asked[0].Limit, appsMaxPage)
	}
}

func TestAppsNeedsAReaderInEveryTarget(t *testing.T) {
	extractor := NewApps(&fakeApps{})
	if err := extractor.Target(map[string]string{"reader": "issues", "repository": "acme/api"}); err != nil {
		t.Fatalf("Target() error = %v", err)
	}
	for _, target := range []map[string]string{
		nil,
		{},
		{"reader": " "},
		{"repository": "acme/api"},
	} {
		if err := extractor.Target(target); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Target(%v) error = %v, want ErrInvalid", target, err)
		}
	}
}

// TestAppsChecksWithOneItem is the plan's Checker: a single item of the
// target, from the beginning, is the whole of it. A check that asked for more,
// or resumed an earlier pass, would be a pass wearing a checker's name.
func TestAppsChecksWithOneItem(t *testing.T) {
	watermark := time.Date(2026, time.September, 29, 9, 0, 0, 0, time.UTC)
	apps := &fakeApps{}
	request := appsRequest()
	request.Cursor = domain.Cursor{Value: "page-2", Watermark: &watermark, Token: "start-token"}

	if err := NewApps(apps).Check(context.Background(), request); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	asked := apps.asked[0]
	if asked.Limit != 1 || asked.Full || asked.Cursor != "" || asked.Watermark != nil || asked.Token != "" {
		t.Fatalf("asked for %+v, want the beginning of the target and nothing more", asked)
	}
	if asked.Target["reader"] != "issues" {
		t.Fatalf("asked for %v, want the target the source names", asked.Target)
	}
}

func TestAppsRefusesATargetItCannotRead(t *testing.T) {
	// What apps says about a target no reader can read is what a member is
	// told before anything is registered, rather than a source that fails on
	// every pass once it is.
	apps := &fakeApps{err: fmt.Errorf("apps answered 400: %w", domain.ErrInvalid)}

	if err := NewApps(apps).Check(context.Background(), appsRequest()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Check() error = %v, want ErrInvalid", err)
	}
}

func TestAppsRefusesASourceWithNoConnectionToReadThrough(t *testing.T) {
	apps := &fakeApps{}
	request := appsRequest()
	request.Source.ConnectionID = ""

	if _, err := NewApps(apps).Fetch(context.Background(), request); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Fetch() error = %v, want ErrInvalid", err)
	}
	if len(apps.asked) != 0 {
		t.Fatal("Fetch() asked apps about a source with no connection")
	}
}

func TestAppsStepsOverAnItemWithNoId(t *testing.T) {
	// The app's own name for a document is what makes it the same document
	// between passes. An item without one is not a document, and writing it
	// would put every such item under one empty id.
	apps := &fakeApps{answer: appsclient.Page{Items: []appsclient.Item{
		{ExternalID: " ", Title: "unnamed"},
		{ExternalID: "42", Title: "Flaky test"},
	}}}

	page, err := NewApps(apps).Fetch(context.Background(), appsRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ExternalID != "42" {
		t.Fatalf("page = %+v, want only the item the app named", page)
	}
}

func TestRegisterAppsMountsAReaderBesideEveryToolkit(t *testing.T) {
	github, notion := &fakeProxy{provider: "github"}, &fakeProxy{provider: "notion"}
	files := &fakeProxy{provider: "files"}
	registry := Registry{"github": github, "notion": notion, "files": files}
	apps := &fakeApps{}

	if got := RegisterApps(registry, apps); len(got) != 3 {
		t.Fatalf("registry = %v, want every provider still mounted", got)
	}
	if registry["files"] != Extractor(files) {
		t.Fatal("files is the organization's own storage, not a toolkit: it must keep its own extractor")
	}
	for _, toolkit := range []string{"github", "notion"} {
		extractor, err := registry.Extractor(toolkit)
		if err != nil {
			t.Fatalf("Extractor(%q) error = %v", toolkit, err)
		}
		reader, ok := extractor.(AppsReader)
		if !ok || !reader.ReadsThroughApps() {
			t.Fatalf("the extractor for %q does not read through apps", toolkit)
		}
	}
	if RegisterApps(Registry{}, nil) == nil {
		t.Fatal("RegisterApps() with no client must leave a registry, not answer nil")
	}
}

// TestEachSourceIsReadByWhoeverHoldsItsCredential is the rule the mounted
// pair exists for: a Composio-backed source is read by the extractor this
// toolkit has always had, exactly as before, and only an Oryxa one is read
// through apps.
func TestEachSourceIsReadByWhoeverHoldsItsCredential(t *testing.T) {
	for _, test := range []struct {
		name     string
		composio string
		wantAs   string
	}{
		{name: "a Composio account is proxied by Composio", composio: "ca_1", wantAs: "composio"},
		{name: "anything else is read as the member", composio: "", wantAs: "apps"},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := &fakeProxy{provider: "github", page: Page{Cursor: "from-composio"}}
			apps := &fakeApps{answer: appsclient.Page{Cursor: "from-apps"}}
			extractor, err := RegisterApps(Registry{"github": proxy}, apps).Extractor("github")
			if err != nil {
				t.Fatal(err)
			}
			request := appsRequest()
			request.Source.ComposioAccountID = test.composio

			page, err := extractor.Fetch(context.Background(), request)
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if test.wantAs == "composio" {
				if page.Cursor != "from-composio" || len(apps.asked) != 0 {
					t.Fatalf("page = %+v, apps asked %d times, want Composio's proxy alone", page, len(apps.asked))
				}
				return
			}
			if page.Cursor != "from-apps" || len(proxy.calls) != 0 {
				t.Fatalf("page = %+v, proxy called %d times, want the apps reader alone", page, len(proxy.calls))
			}
		})
	}
}

// TestEachTargetIsCheckedAgainstTheBrokerBehindIt keeps a target from being
// read as something it is not: one naming an apps reader is refused on a
// Composio connection, where that name says nothing, and a Composio target is
// refused by apps, which cannot read it.
func TestEachTargetIsCheckedAgainstTheBrokerBehindIt(t *testing.T) {
	proxy := &fakeProxy{provider: "github", targetErr: MissingTarget("a repository, as owner/name")}
	reader := &fakeApps{err: fmt.Errorf("apps answered 400: %w", domain.ErrInvalid)}
	extractor := &perBroker{toolkit: "github", composio: proxy, apps: NewApps(reader)}

	appsTarget := appsRequest()
	appsTarget.Source.ComposioAccountID = "ca_1"
	if err := extractor.Check(context.Background(), appsTarget); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Check() on a Composio connection error = %v, want ErrInvalid", err)
	}
	if len(reader.asked) != 0 {
		t.Fatal("a Composio-backed source must not be checked through apps")
	}

	composioTarget := appsRequest()
	composioTarget.Source.Target = map[string]string{"repository": "acme/api"}
	if err := extractor.Check(context.Background(), composioTarget); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Check() on an Oryxa connection error = %v, want ErrInvalid", err)
	}
	if len(reader.asked) != 1 {
		t.Fatalf("asked apps %d times, want the check to have reached it", len(reader.asked))
	}
}

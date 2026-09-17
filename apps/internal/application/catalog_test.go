package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

func TestCatalogSaysWhatCanBeConnected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.toolkits["github"] = composio.Toolkit{
		Slug: "github", Name: "GitHub", AuthSchemes: []string{"OAUTH2"},
		ComposioManagedAuthSchemes: []string{"OAUTH2"}, ToolsCount: 120, TriggersCount: 8,
		Categories: []composio.Category{{ID: "developer-tools", Name: "Developer Tools"}},
	}
	h.composio.toolkits["stripe"] = composio.Toolkit{
		Slug: "stripe", Name: "Stripe", AuthSchemes: []string{"OAUTH2", "API_KEY"},
	}
	h.composio.toolkits["oauthonly"] = composio.Toolkit{Slug: "oauthonly", AuthSchemes: []string{"OAUTH2"}}
	h.composio.toolkits["hackernews"] = composio.Toolkit{Slug: "hackernews", NoAuth: true}

	views, _, err := h.service.Catalog(context.Background(), CatalogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	connectable := map[string]bool{}
	for _, view := range views {
		connectable[view.Toolkit] = view.Connectable
	}
	for toolkit, want := range map[string]bool{
		"github":     true,  // Composio holds the app
		"stripe":     true,  // the member types an API key in
		"oauthonly":  false, // would need an OAuth app of ours
		"hackernews": false, // nothing to connect
	} {
		if connectable[toolkit] != want {
			t.Errorf("%s connectable = %v, want %v", toolkit, connectable[toolkit], want)
		}
	}

	// One app, described.
	view, err := h.service.CatalogToolkit(context.Background(), "GitHub")
	if err != nil {
		t.Fatal(err)
	}
	if view.Name != "GitHub" || view.ToolsCount != 120 || view.TriggersCount != 8 {
		t.Fatalf("view = %+v", view)
	}
	if len(view.Categories) != 1 || view.Categories[0].ID != "developer-tools" {
		t.Fatalf("categories = %+v", view.Categories)
	}
	if _, err := h.service.CatalogToolkit(context.Background(), "nosuchapp"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown app = %v, want not found", err)
	}
	if _, err := h.service.CatalogToolkit(context.Background(), "NOT A SLUG"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad slug = %v, want invalid", err)
	}
}

func TestCatalogToolsLeaveOutWhatIsDeprecated(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.tools["GITHUB_LIST_ISSUES"] = composio.Tool{
		Slug: "GITHUB_LIST_ISSUES", Toolkit: "github", Tags: []string{composio.HintReadOnly},
		InputParameters: json.RawMessage(`{"type":"object"}`),
	}
	h.composio.tools["GITHUB_OLD_TOOL"] = composio.Tool{Slug: "GITHUB_OLD_TOOL", Toolkit: "github", Deprecated: true}

	views, _, err := h.service.CatalogTools(context.Background(), "github", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Tool != "GITHUB_LIST_ISSUES" || !views[0].ReadOnly {
		t.Fatalf("views = %+v", views)
	}
	if string(views[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("input schema = %s", views[0].InputSchema)
	}
}

func TestCatalogIsEmptyWithoutApps(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	views, cursor, err := h.service.Catalog(context.Background(), CatalogQuery{})
	if err != nil || cursor != "" {
		t.Fatalf("catalog = %v, %q", err, cursor)
	}
	// A JSON body says [] rather than null.
	if views == nil {
		t.Fatal("views are nil")
	}
	triggers, _, err := h.service.CatalogTriggers(context.Background(), "github", "", 0)
	if err != nil || triggers == nil {
		t.Fatalf("triggers = %+v, %v", triggers, err)
	}
	tools, _, err := h.service.CatalogTools(context.Background(), "github", "", "", 0)
	if err != nil || tools == nil {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
}

func TestCatalogSaysWhenComposioIsUnreachable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.toolkitsErr = composio.ErrUnavailable

	if _, _, err := h.service.Catalog(context.Background(), CatalogQuery{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

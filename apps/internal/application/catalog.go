package application

import (
	"context"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// ToolkitView is an app in the catalog, as a member browses it.
type ToolkitView struct {
	Toolkit     string         `json:"toolkit"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Logo        string         `json:"logo,omitempty"`
	Categories  []CategoryView `json:"categories"`
	AuthSchemes []string       `json:"auth_schemes"`
	// Connectable says a member can connect it with what this project holds:
	// Composio's own app, or a credential the member types in.
	Connectable   bool `json:"connectable"`
	NoAuth        bool `json:"no_auth,omitempty"`
	ToolsCount    int  `json:"tools_count"`
	TriggersCount int  `json:"triggers_count"`
}

type CategoryView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toolkitView(toolkit composio.Toolkit) ToolkitView {
	view := ToolkitView{
		Toolkit: toolkit.Slug, Name: toolkit.Name, Description: toolkit.Description, Logo: toolkit.Logo,
		Categories: []CategoryView{}, AuthSchemes: toolkit.AuthSchemes, NoAuth: toolkit.NoAuth,
		ToolsCount: toolkit.ToolsCount, TriggersCount: toolkit.TriggersCount,
	}
	if view.AuthSchemes == nil {
		view.AuthSchemes = []string{}
	}
	for _, category := range toolkit.Categories {
		view.Categories = append(view.Categories, CategoryView{ID: category.ID, Name: category.Name})
	}
	view.Connectable = len(toolkit.ComposioManagedAuthSchemes) > 0
	for _, scheme := range toolkit.AuthSchemes {
		if slices.Contains(userSuppliedSchemes, scheme) {
			view.Connectable = true
		}
	}
	return view
}

// CatalogQuery narrows the catalog.
type CatalogQuery struct {
	Search   string
	Category string
	Cursor   string
	Limit    int
}

// maxCatalogPage bounds one page of the catalog.
const maxCatalogPage = 100

// Catalog lists Composio's apps. It holds nothing of any organization, so it
// needs no caller.
func (s *Service) Catalog(ctx context.Context, query CatalogQuery) ([]ToolkitView, string, error) {
	limit := query.Limit
	if limit <= 0 || limit > maxCatalogPage {
		limit = maxCatalogPage
	}
	page, err := s.composio.Toolkits(ctx, composio.ToolkitQuery{
		Search: strings.TrimSpace(query.Search), Category: strings.TrimSpace(query.Category),
		Cursor: query.Cursor, Limit: limit,
	})
	if err != nil {
		return nil, "", composioError("list the catalog", err)
	}
	views := make([]ToolkitView, 0, len(page.Items))
	for _, toolkit := range page.Items {
		views = append(views, toolkitView(toolkit))
	}
	return views, page.NextCursor, nil
}

// CatalogToolkit describes one app.
func (s *Service) CatalogToolkit(ctx context.Context, slug string) (ToolkitView, error) {
	slug, err := domain.CheckToolkit(slug)
	if err != nil {
		return ToolkitView{}, err
	}
	toolkit, err := s.composio.Toolkit(ctx, slug)
	if err != nil {
		return ToolkitView{}, composioError("read the app", err)
	}
	return toolkitView(toolkit), nil
}

// CatalogTools lists an app's tools, featured first when there is no search.
func (s *Service) CatalogTools(ctx context.Context, slug, search, cursor string, limit int) ([]ToolView, string, error) {
	slug, err := domain.CheckToolkit(slug)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > maxCatalogPage {
		limit = maxCatalogPage
	}
	page, err := s.composio.Tools(ctx, composio.ToolQuery{Toolkit: slug, Search: strings.TrimSpace(search), Cursor: cursor, Limit: limit})
	if err != nil {
		return nil, "", composioError("list the app's tools", err)
	}
	views := make([]ToolView, 0, len(page.Items))
	for _, tool := range page.Items {
		if !tool.Deprecated {
			views = append(views, toolView(tool))
		}
	}
	return views, page.NextCursor, nil
}

// CatalogTriggers lists the events an app can deliver.
func (s *Service) CatalogTriggers(ctx context.Context, slug, cursor string, limit int) ([]TriggerTypeView, string, error) {
	slug, err := domain.CheckToolkit(slug)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	page, err := s.composio.TriggerTypes(ctx, composio.TriggerTypeQuery{Toolkits: []string{slug}, Cursor: cursor, Limit: limit})
	if err != nil {
		return nil, "", composioError("list the app's triggers", err)
	}
	views := make([]TriggerTypeView, 0, len(page.Items))
	for _, trigger := range page.Items {
		views = append(views, TriggerTypeView{
			Trigger: trigger.Slug, Name: trigger.Name, Description: trigger.Description,
			Instructions: trigger.Instructions, Toolkit: trigger.Toolkit, Kind: trigger.Kind,
			ConfigSchema: trigger.Config, ManualSetup: trigger.RequiresWebhookEndpointSetup,
		})
	}
	return views, page.NextCursor, nil
}

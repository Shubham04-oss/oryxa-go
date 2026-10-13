package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
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
	// Broker is who a new connect to it goes through: composio, oryxa or
	// pipedream.
	Broker string `json:"broker"`
}

type CategoryView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toolkitView(toolkit composio.Toolkit) ToolkitView {
	view := ToolkitView{
		Toolkit: toolkit.Slug, Name: toolkit.Name, Description: toolkit.Description, Logo: toolkit.Logo,
		Categories: []CategoryView{}, AuthSchemes: toolkit.AuthSchemes, NoAuth: toolkit.NoAuth,
		ToolsCount: toolkit.ToolsCount, TriggersCount: toolkit.TriggersCount, Broker: domain.BrokerComposio,
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

// Catalog lists Composio's apps, then the ones only Pipedream has. It holds
// nothing of any organization, so it needs no caller.
func (s *Service) Catalog(ctx context.Context, query CatalogQuery) ([]ToolkitView, string, error) {
	limit := query.Limit
	if limit <= 0 || limit > maxCatalogPage {
		limit = maxCatalogPage
	}
	if offset, ok := strings.CutPrefix(query.Cursor, pipedreamCursor); ok && s.pipedream != nil {
		at, err := strconv.Atoi(offset)
		if err != nil {
			return nil, "", fmt.Errorf("the cursor is not one the catalog answered: %w", domain.ErrInvalid)
		}
		return s.pipedreamPage(ctx, query, at, limit)
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
		view := toolkitView(toolkit)
		view.Broker = s.catalogBroker(view.Toolkit, view.Broker)
		views = append(views, view)
	}
	next := page.NextCursor
	if next == "" && s.pipedream != nil {
		// Composio's apps are done; Pipedream's own follow, if any match.
		// The catalog stays Composio's alone while Pipedream's cannot be read.
		if more, _, err := s.pipedreamPage(ctx, query, 0, 1); err != nil {
			s.logger.WarnContext(ctx, "list Pipedream's apps in the catalog", "error", err)
		} else if len(more) > 0 {
			next = pipedreamCursor + "0"
		}
	}
	return views, next, nil
}

// catalogBroker is the broker the catalog shows for an app its broker
// lists: the one a pin or Oryxa's own sign-in sends it to instead, if any.
func (s *Service) catalogBroker(toolkit, listed string) string {
	if broker, ok := s.toolkitBrokers[toolkit]; ok {
		return broker
	}
	if s.oryxa != nil && s.oryxa.enabled[toolkit] {
		return domain.BrokerOryxa
	}
	return listed
}

// CatalogToolkit describes one app.
func (s *Service) CatalogToolkit(ctx context.Context, slug string) (ToolkitView, error) {
	slug, err := domain.CheckToolkit(slug)
	if err != nil {
		return ToolkitView{}, err
	}
	toolkit, err := s.composio.Toolkit(ctx, slug)
	if errors.Is(err, composio.ErrNotFound) && s.pipedream != nil {
		catalog, catalogErr := s.pipedreamCatalog(ctx)
		if catalogErr != nil {
			return ToolkitView{}, catalogErr
		}
		if app, ok := catalog.bySlug[slug]; ok {
			return s.pipedreamView(app), nil
		}
	}
	if err != nil {
		return ToolkitView{}, composioError("read the app", err)
	}
	view := toolkitView(toolkit)
	view.Broker = s.catalogBroker(slug, view.Broker)
	return view, nil
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

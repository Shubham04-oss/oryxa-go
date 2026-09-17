package composio

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Toolkit is one app in Composio's catalog, such as github or gmail.
type Toolkit struct {
	Slug string
	Name string
	// Type is native for Composio's own toolkits, custom for a project's.
	Type        string
	Description string
	Logo        string
	AppURL      string
	Categories  []Category
	// AuthSchemes are how the app can be connected, such as OAUTH2 or
	// API_KEY. ComposioManagedAuthSchemes are those Composio holds its own
	// app for, so they need no credentials of Oryxa's.
	AuthSchemes                []string
	ComposioManagedAuthSchemes []string
	// NoAuth says the app needs no connection at all.
	NoAuth        bool
	ToolsCount    int
	TriggersCount int
	Version       string
}

// Category groups toolkits, such as developer-tools.
type Category struct {
	ID   string
	Name string
}

type toolkitWire struct {
	Slug                       string   `json:"slug"`
	Name                       string   `json:"name"`
	Type                       string   `json:"type"`
	AuthSchemes                []string `json:"auth_schemes"`
	ComposioManagedAuthSchemes []string `json:"composio_managed_auth_schemes"`
	NoAuth                     bool     `json:"no_auth"`
	Meta                       struct {
		Description string  `json:"description"`
		Logo        string  `json:"logo"`
		AppURL      *string `json:"app_url"`
		Categories  []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"categories"`
		ToolsCount    float64 `json:"tools_count"`
		TriggersCount float64 `json:"triggers_count"`
		Version       string  `json:"version"`
	} `json:"meta"`
}

func (wire toolkitWire) toolkit() Toolkit {
	toolkit := Toolkit{
		Slug: wire.Slug, Name: wire.Name, Type: wire.Type,
		Description: wire.Meta.Description, Logo: wire.Meta.Logo,
		AuthSchemes: wire.AuthSchemes, ComposioManagedAuthSchemes: wire.ComposioManagedAuthSchemes,
		NoAuth: wire.NoAuth, ToolsCount: int(wire.Meta.ToolsCount), TriggersCount: int(wire.Meta.TriggersCount),
		Version: wire.Meta.Version,
	}
	if wire.Meta.AppURL != nil {
		toolkit.AppURL = *wire.Meta.AppURL
	}
	for _, category := range wire.Meta.Categories {
		// The listing names a category by id, a single toolkit by slug.
		id := category.ID
		if id == "" {
			id = category.Slug
		}
		toolkit.Categories = append(toolkit.Categories, Category{ID: id, Name: category.Name})
	}
	return toolkit
}

// ToolkitQuery narrows a toolkit listing.
type ToolkitQuery struct {
	Search   string
	Category string
	Cursor   string
	Limit    int
}

// Toolkits lists Composio's catalog, a page at a time.
func (client *Client) Toolkits(ctx context.Context, query ToolkitQuery) (Page[Toolkit], error) {
	values := url.Values{}
	if query.Search != "" {
		values.Set("search", query.Search)
	}
	if query.Category != "" {
		values.Set("category", query.Category)
	}
	var answer struct {
		Items      []toolkitWire `json:"items"`
		NextCursor *string       `json:"next_cursor"`
	}
	if err := client.call(ctx, http.MethodGet, "/toolkits", pageQuery(values, query.Cursor, query.Limit), nil, &answer); err != nil {
		return Page[Toolkit]{}, err
	}
	page := Page[Toolkit]{NextCursor: cursorOf(answer.NextCursor)}
	for _, wire := range answer.Items {
		page.Items = append(page.Items, wire.toolkit())
	}
	return page, nil
}

// Toolkit reads one toolkit, including which of its auth schemes Composio
// manages.
func (client *Client) Toolkit(ctx context.Context, slug string) (Toolkit, error) {
	var wire toolkitWire
	if err := client.call(ctx, http.MethodGet, "/toolkits/"+pathEscape(slug), nil, nil, &wire); err != nil {
		return Toolkit{}, err
	}
	return wire.toolkit(), nil
}

// AuthConfig is how a project connects one toolkit: with Composio's own app,
// or with credentials the project supplies.
type AuthConfig struct {
	ID                string
	Name              string
	Toolkit           string
	AuthScheme        string
	IsComposioManaged bool
	// Enabled is false for a config the project disabled.
	Enabled bool
}

type authConfigWire struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Toolkit struct {
		Slug string `json:"slug"`
	} `json:"toolkit"`
	AuthScheme        string `json:"auth_scheme"`
	IsComposioManaged bool   `json:"is_composio_managed"`
	Status            string `json:"status"`
}

func (wire authConfigWire) authConfig() AuthConfig {
	return AuthConfig{
		ID: wire.ID, Name: wire.Name, Toolkit: wire.Toolkit.Slug, AuthScheme: wire.AuthScheme,
		IsComposioManaged: wire.IsComposioManaged, Enabled: !strings.EqualFold(wire.Status, "DISABLED"),
	}
}

// AuthConfigQuery narrows an auth config listing.
type AuthConfigQuery struct {
	Toolkit string
	Cursor  string
	Limit   int
}

// AuthConfigs lists the project's auth configs.
func (client *Client) AuthConfigs(ctx context.Context, query AuthConfigQuery) (Page[AuthConfig], error) {
	values := url.Values{}
	if query.Toolkit != "" {
		values.Set("toolkit_slug", query.Toolkit)
	}
	var answer struct {
		Items      []authConfigWire `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := client.call(ctx, http.MethodGet, "/auth_configs", pageQuery(values, query.Cursor, query.Limit), nil, &answer); err != nil {
		return Page[AuthConfig]{}, err
	}
	page := Page[AuthConfig]{NextCursor: cursorOf(answer.NextCursor)}
	for _, wire := range answer.Items {
		page.Items = append(page.Items, wire.authConfig())
	}
	return page, nil
}

// CreateManagedAuthConfig creates an auth config that connects toolkit with
// Composio's own app, which asks nothing of Oryxa.
func (client *Client) CreateManagedAuthConfig(ctx context.Context, toolkit, name string) (AuthConfig, error) {
	return client.createAuthConfig(ctx, toolkit, map[string]any{
		"type": "use_composio_managed_auth", "name": name, "credentials": map[string]any{},
	})
}

// CreateUserSuppliedAuthConfig creates an auth config for a scheme whose
// credentials the connecting member supplies, such as their own API key,
// when Composio's hosted page asks them for it.
func (client *Client) CreateUserSuppliedAuthConfig(ctx context.Context, toolkit, scheme, name string) (AuthConfig, error) {
	return client.createAuthConfig(ctx, toolkit, map[string]any{
		"type": "use_custom_auth", "authScheme": scheme, "name": name, "credentials": map[string]any{},
	})
}

func (client *Client) createAuthConfig(ctx context.Context, toolkit string, config map[string]any) (AuthConfig, error) {
	var answer struct {
		Toolkit struct {
			Slug string `json:"slug"`
		} `json:"toolkit"`
		AuthConfig struct {
			ID                string `json:"id"`
			AuthScheme        string `json:"auth_scheme"`
			IsComposioManaged bool   `json:"is_composio_managed"`
		} `json:"auth_config"`
	}
	body := map[string]any{"toolkit": map[string]string{"slug": toolkit}, "auth_config": config}
	if err := client.call(ctx, http.MethodPost, "/auth_configs", nil, body, &answer); err != nil {
		return AuthConfig{}, err
	}
	name, _ := config["name"].(string)
	return AuthConfig{
		ID: answer.AuthConfig.ID, Name: name, Toolkit: toolkit, AuthScheme: answer.AuthConfig.AuthScheme,
		IsComposioManaged: answer.AuthConfig.IsComposioManaged, Enabled: true,
	}, nil
}

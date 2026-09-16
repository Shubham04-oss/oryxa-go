package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

// IntegrationsConfig configures IntegrationsToolboxes.
type IntegrationsConfig struct {
	// URL is integrations' own URL: the catalogue is GET {URL}/v1/catalog and
	// a provider's MCP endpoint is POST {URL}/v1/mcp/{provider}.
	URL string
	// HTTP is the client every request this package makes — the catalogue
	// fetch and every MCP call — goes out on. Defaults to http.DefaultClient
	// when nil.
	HTTP *http.Client
	// Tokens mints an agent token for a session's user when a run carries no
	// member access token of its own. See memberauth.TokenSource.
	Tokens memberauth.TokenSource
}

// integrationsCatalog is the shape of GET /v1/catalog that building these
// toolboxes needs: enough of integrations' providers.Description to name a
// toolbox, carry its skill, and know which of its tools write. Primary
// cannot import integrations' internal packages (Go's internal visibility
// rule keeps a tree's internal packages to that tree), so this is its own
// minimal, structurally compatible decode of the same JSON — see
// integrations/internal/providers/describe.go for the source shape.
type integrationsCatalog struct {
	Providers []catalogProvider `json:"providers"`
}

type catalogProvider struct {
	Name    string          `json:"name"`
	Skill   string          `json:"skill"`
	Actions []catalogAction `json:"actions"`
}

type catalogAction struct {
	Name   string `json:"name"`
	Writes bool   `json:"writes"`
}

// IntegrationsToolboxes fetches integrations' catalogue once and returns one
// Toolbox per provider that has at least one action: its tools are that
// provider's MCP endpoint at {URL}/v1/mcp/{provider}, authenticated as the
// session's member (memberauth), and every tool but those the catalogue names
// as reads requires confirmation before it runs. A provider with no actions
// is skipped — there is nothing for its toolbox to do.
//
// An unreachable catalogue is returned as an error rather than a partial
// result; the caller (cmd/devui) logs it and runs without integrations
// toolboxes rather than failing to start.
func IntegrationsToolboxes(ctx context.Context, config IntegrationsConfig) ([]Toolbox, error) {
	baseURL := strings.TrimRight(config.URL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("agents: IntegrationsToolboxes requires a URL")
	}
	httpClient := config.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	catalog, err := fetchIntegrationsCatalog(ctx, httpClient, baseURL)
	if err != nil {
		return nil, fmt.Errorf("fetch integrations catalog: %w", err)
	}

	credentialProvider := memberauth.NewCredentialProvider(config.Tokens)
	var toolboxes []Toolbox
	for _, provider := range catalog.Providers {
		if provider.Name == "" || len(provider.Actions) == 0 {
			continue
		}
		toolset, err := integrationsToolset(baseURL, provider, httpClient, credentialProvider)
		if err != nil {
			return nil, fmt.Errorf("integrations toolbox %q: %w", provider.Name, err)
		}
		toolboxes = append(toolboxes, Toolbox{
			Name:         provider.Name,
			Description:  integrationsDescription(provider),
			Instructions: provider.Skill,
			Toolset:      toolset,
		})
	}
	return toolboxes, nil
}

// fetchIntegrationsCatalog reads integrations' catalogue once.
func fetchIntegrationsCatalog(ctx context.Context, httpClient *http.Client, baseURL string) (integrationsCatalog, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/catalog", nil)
	if err != nil {
		return integrationsCatalog{}, err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return integrationsCatalog{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return integrationsCatalog{}, fmt.Errorf("status %d", response.StatusCode)
	}
	var catalog integrationsCatalog
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&catalog); err != nil {
		return integrationsCatalog{}, fmt.Errorf("decode: %w", err)
	}
	return catalog, nil
}

// integrationsToolset builds provider's MCP toolset, authenticated with
// credentialProvider and requiring confirmation for all but its reads, then
// wraps it so the session's member reaches that credential provider (see
// memberauth.WrapToolset), and lists its tools only once.
func integrationsToolset(baseURL string, provider catalogProvider, httpClient *http.Client, credentialProvider *memberauth.CredentialProvider) (tool.Toolset, error) {
	// A tool's name is provider_action, matching the toolbox naming rule and
	// how integrations/internal/mcpserver names the MCP tools it serves,
	// which adds provider_connections to every provider's actions.
	reads := map[string]bool{provider.Name + "_connections": true}
	for _, action := range provider.Actions {
		if !action.Writes {
			reads[provider.Name+"_"+action.Name] = true
		}
	}
	inner, err := mcptoolset.New(mcptoolset.Config{
		Transport: &mcp.StreamableClientTransport{
			Endpoint:   baseURL + "/v1/mcp/" + provider.Name,
			HTTPClient: httpClient,
		},
		Auth: credentialProvider,
		// A tool the catalogue does not name as a read asks first, so a write
		// integrations gained after this catalogue was read never runs
		// unconfirmed.
		RequireConfirmationProvider: func(toolName string, _ any) bool {
			return !reads[toolName]
		},
	})
	if err != nil {
		return nil, err
	}
	return &listedOnce{Toolset: memberauth.WrapToolset(inner)}, nil
}

// listedOnce keeps a toolset's first successful listing. Integrations serves
// every member the same tools, from its catalogue, so only the first listing,
// as whichever member asks first, goes over the network: a loaded toolbox
// costs nothing per model step, and a member whose tools cannot be listed as
// them still gets tools whose calls each say why they failed. A failed
// listing is not kept.
type listedOnce struct {
	tool.Toolset

	mu    sync.Mutex
	tools []tool.Tool
}

func (l *listedOnce) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	l.mu.Lock()
	tools := l.tools
	l.mu.Unlock()
	if tools != nil {
		return tools, nil
	}
	tools, err := l.Toolset.Tools(ctx)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tools == nil {
		l.tools = tools
	}
	return l.tools, nil
}

// integrationsDescription is the toolbox's one-line description: the
// provider, as its skill's heading names it, and the actions it can run.
func integrationsDescription(provider catalogProvider) string {
	names := make([]string, 0, len(provider.Actions))
	for _, action := range provider.Actions {
		names = append(names, action.Name)
	}
	return fmt.Sprintf("%s, through the member's own connection: %s.", displayName(provider), strings.Join(names, ", "))
}

// displayName is the provider's name as its skill's heading gives it, such as
// GitHub for github, or its catalogue name when the skill has no heading.
func displayName(provider catalogProvider) string {
	heading, _, _ := strings.Cut(provider.Skill, "\n")
	if name, ok := strings.CutPrefix(heading, "# "); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return provider.Name
}

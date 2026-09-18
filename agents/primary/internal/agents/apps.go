package agents

import (
	_ "embed"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

// The apps toolbox ships with its skill, embedded and carried in Krude's
// system prompt while the toolbox is loaded, exactly as sandbox's and
// browser's are (toolboxes.go).
//
//go:embed toolboxes/apps.md
var appsInstructions string

// AppsConfig configures AppsToolbox.
type AppsConfig struct {
	// URL is the apps service's own URL: its MCP endpoint is
	// POST {URL}/v1/mcp/apps.
	URL string
	// HTTP is the client every MCP call this toolbox makes goes out on.
	// Defaults to http.DefaultClient when nil.
	HTTP *http.Client
	// Tokens mints an agent token for a session's user when a run carries no
	// member access token of its own. See memberauth.TokenSource.
	Tokens memberauth.TokenSource
}

// AppsToolbox names the one toolbox that reaches every app a member has
// connected through Composio: its tools are the apps service's MCP endpoint
// at {config.URL}/v1/mcp/apps, authenticated as the session's member
// (memberauth.WrapToolset) and listed once — the apps service serves every
// member the same tools, so only the first listing, as whichever member
// loads it first, goes over the network. Every tool but its three reads —
// apps_connections, apps_search_tools and apps_run_read_tool — requires
// confirmation before it runs, so a write, or a tool this toolbox does not
// yet know about, always asks first.
//
// Building the toolbox makes no network call of its own: the MCP client
// connects, and lists tools, lazily on first use.
func AppsToolbox(config AppsConfig) (Toolbox, error) {
	baseURL := strings.TrimRight(config.URL, "/")
	if baseURL == "" {
		return Toolbox{}, fmt.Errorf("agents: AppsToolbox requires a URL")
	}
	httpClient := config.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	// A tool's name is apps_action, matching the toolbox naming rule and how
	// the apps service's MCP endpoint names its tools. Everything but these
	// three reads asks first.
	reads := map[string]bool{
		"apps_connections":   true,
		"apps_search_tools":  true,
		"apps_run_read_tool": true,
	}
	inner, err := mcptoolset.New(mcptoolset.Config{
		Transport: &mcp.StreamableClientTransport{
			Endpoint:   baseURL + "/v1/mcp/apps",
			HTTPClient: httpClient,
		},
		Auth: memberauth.NewCredentialProvider(config.Tokens),
		RequireConfirmationProvider: func(toolName string, _ any) bool {
			return !reads[toolName]
		},
	})
	if err != nil {
		return Toolbox{}, fmt.Errorf("apps toolbox: %w", err)
	}

	return Toolbox{
		Name:         "apps",
		Description:  "Every app the member has connected through Composio — their real GitHub repos, files, email, calendars, CRMs and the rest — reached through their own connection: search a toolkit's tools, read data, and make changes with confirmation.",
		Instructions: appsInstructions,
		Toolset:      &listedOnce{Toolset: memberauth.WrapToolset(inner)},
	}, nil
}

// listedOnce keeps a toolset's first successful listing. The apps service
// serves every member the same tools, so only the first listing, as
// whichever member asks first, goes over the network: a loaded toolbox costs
// nothing per model step, and a member whose tools cannot be listed as them
// still gets tools whose calls each say why they failed. A failed listing is
// not kept.
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

var _ tool.Toolset = (*listedOnce)(nil)

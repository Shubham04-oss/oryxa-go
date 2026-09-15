package agents

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolutils"
)

// loadedToolsKey is the session state key listing the toolboxes Krude has
// loaded in this session.
const loadedToolsKey = "loaded_tools"

// Toolbox is a named set of tools Krude loads when a task needs them, such as
// one provider's actions. Every tool in it must be named after the toolbox, as
// in slack_post_message, so loaded tools never collide with Krude's own. A
// toolbox without a Toolset is listed but cannot be loaded yet.
type Toolbox struct {
	Name        string
	Description string
	Toolset     tool.Toolset
}

// Providers returns the provider toolboxes Krude lists today. Their tools will
// use the connections service for credentials; they are not wired yet, so
// none of them can be loaded.
func Providers() []Toolbox {
	return []Toolbox{
		{Name: "slack", Description: "Messages, reactions and channels in the member's Slack workspace."},
		{Name: "github", Description: "Issues, comments and pull requests in the member's GitHub repositories."},
		{Name: "google", Description: "Gmail, Google Calendar and Google Drive for the member's Google account."},
		{Name: "microsoft", Description: "Outlook mail, Outlook Calendar and OneDrive for the member's Microsoft account."},
	}
}

type toolCatalog struct {
	toolboxes []Toolbox
}

func newToolCatalog(toolboxes []Toolbox) (*toolCatalog, error) {
	seen := map[string]bool{}
	for _, box := range toolboxes {
		if box.Name == "" || box.Name != strings.ToLower(strings.TrimSpace(box.Name)) {
			return nil, fmt.Errorf("toolbox name %q must be lowercase with no spaces", box.Name)
		}
		if seen[box.Name] {
			return nil, fmt.Errorf("toolbox %q is listed twice", box.Name)
		}
		seen[box.Name] = true
	}
	return &toolCatalog{toolboxes: toolboxes}, nil
}

func (c *toolCatalog) find(name string) (Toolbox, bool) {
	for _, box := range c.toolboxes {
		if box.Name == name {
			return box, true
		}
	}
	return Toolbox{}, false
}

// loaded returns the native ADK toolset of every toolbox loaded in this
// session. Its Tools method handles toolboxes loaded before an invocation;
// ProcessRequest also makes a toolbox loaded during an invocation available
// on the next model step.
func (c *toolCatalog) loaded() tool.Toolset {
	return loadedToolset{catalog: c}
}

type loadedToolset struct {
	catalog *toolCatalog
}

func (l loadedToolset) Name() string { return "loaded_tools" }

func (l loadedToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	var tools []tool.Tool
	for _, name := range loadedToolboxes(ctx.ReadonlyState()) {
		box, ok := l.catalog.find(name)
		if !ok || box.Toolset == nil {
			continue
		}
		boxTools, err := box.Toolset.Tools(ctx)
		if err != nil {
			return nil, fmt.Errorf("toolbox %q: %w", name, err)
		}
		tools = append(tools, boxTools...)
	}
	return tools, nil
}

// ProcessRequest participates in ADK's native toolset preprocessing. ADK
// resolves Tools once at the start of an invocation, but preprocesses the
// registered toolsets before every model step. Packing tools that appeared in
// state since that first resolution lets load_tools take effect immediately.
func (l loadedToolset) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	tools, err := l.Tools(ctx)
	if err != nil {
		return err
	}
	for _, loadedTool := range tools {
		if _, alreadyPacked := req.Tools[loadedTool.Name()]; alreadyPacked {
			continue
		}
		declarative, ok := loadedTool.(toolutils.Tool)
		if !ok {
			return fmt.Errorf("tool %q cannot be dynamically loaded", loadedTool.Name())
		}
		if err := toolutils.PackTool(req, declarative); err != nil {
			return err
		}
	}
	return nil
}

// loadedToolboxes reads the loaded toolbox names from state. A value read back
// from storage arrives as []any rather than []string.
func loadedToolboxes(state session.ReadonlyState) []string {
	value, err := state.Get(loadedToolsKey)
	if err != nil {
		return nil
	}
	switch names := value.(type) {
	case []string:
		return names
	case []any:
		var result []string
		for _, name := range names {
			if text, ok := name.(string); ok {
				result = append(result, text)
			}
		}
		return result
	}
	return nil
}

type listToolsArgs struct{}

type listedToolbox struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Loaded      bool   `json:"loaded"`
	Available   bool   `json:"available"`
}

type listToolsResult struct {
	Toolboxes []listedToolbox `json:"toolboxes"`
}

func (c *toolCatalog) listTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "list_tools",
		Description: "Lists the toolboxes you can load, such as Slack or GitHub, and which are loaded or not available yet.",
	}, func(ctx agent.Context, _ listToolsArgs) (listToolsResult, error) {
		loaded := loadedToolboxes(ctx.ReadonlyState())
		result := listToolsResult{Toolboxes: []listedToolbox{}}
		for _, box := range c.toolboxes {
			result.Toolboxes = append(result.Toolboxes, listedToolbox{
				Name:        box.Name,
				Description: box.Description,
				Loaded:      slices.Contains(loaded, box.Name),
				Available:   box.Toolset != nil,
			})
		}
		return result, nil
	})
}

type loadToolsArgs struct {
	Names []string `json:"names" jsonschema:"Names of the toolboxes to load, as list_tools gives them."`
}

type unavailableToolbox struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type loadToolsResult struct {
	Loaded      []string             `json:"loaded"`
	Unavailable []unavailableToolbox `json:"unavailable,omitempty"`
}

func (c *toolCatalog) loadTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "load_tools",
		Description: "Loads toolboxes by name. Their tools are yours from your next step onward in this session.",
	}, func(ctx agent.Context, args loadToolsArgs) (loadToolsResult, error) {
		loaded := loadedToolboxes(ctx.ReadonlyState())
		var unavailable []unavailableToolbox
		for _, requested := range args.Names {
			name := strings.ToLower(strings.TrimSpace(requested))
			box, ok := c.find(name)
			if !ok {
				unavailable = append(unavailable, unavailableToolbox{Name: requested, Reason: "there is no toolbox with this name"})
				continue
			}
			if box.Toolset == nil {
				unavailable = append(unavailable, unavailableToolbox{Name: name, Reason: "its tools are not connected yet"})
				continue
			}
			if slices.Contains(loaded, name) {
				continue
			}
			if reason := misnamedTool(ctx, box); reason != "" {
				unavailable = append(unavailable, unavailableToolbox{Name: name, Reason: reason})
				continue
			}
			loaded = append(loaded, name)
		}
		if err := ctx.State().Set(loadedToolsKey, loaded); err != nil {
			return loadToolsResult{}, err
		}
		return loadToolsResult{Loaded: append([]string{}, loaded...), Unavailable: unavailable}, nil
	})
}

// misnamedTool reports a toolbox whose tools are not all named after it, which
// could otherwise collide with Krude's own tools once loaded.
func misnamedTool(ctx agent.ReadonlyContext, box Toolbox) string {
	tools, err := box.Toolset.Tools(ctx)
	if err != nil {
		return "its tools could not be listed"
	}
	for _, t := range tools {
		if !strings.HasPrefix(t.Name(), box.Name+"_") {
			return fmt.Sprintf("its tool %q is not named after the toolbox", t.Name())
		}
		if _, ok := t.(toolutils.Tool); !ok {
			return fmt.Sprintf("its tool %q cannot be dynamically loaded", t.Name())
		}
	}
	return ""
}

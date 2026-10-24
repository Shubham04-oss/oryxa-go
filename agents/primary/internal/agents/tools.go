package agents

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolutils"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

// loadedToolsKey is the session state key listing the toolboxes Krude has
// loaded in this session.
const loadedToolsKey = "loaded_tools"

// Toolbox is a named set of tools Krude loads when a task needs them, such as
// AgentCore's sandbox or browser. Every tool in it must be named after the
// toolbox, as in sandbox_execute_code, so loaded tools never collide with
// Krude's own. A toolbox without a Toolset is listed but cannot be loaded yet.
//
// Instructions is the toolbox's skill: how its tools are meant to be used.
// While the toolbox is loaded, Krude's system prompt carries it.
type Toolbox struct {
	Name         string
	Description  string
	Instructions string
	Toolset      tool.Toolset
}

// Close releases what the toolbox's toolset holds open, such as the apps
// toolbox's MCP session, when its toolset is an io.Closer, and is nil
// otherwise. The toolbox's tools fail once it is closed.
func (b Toolbox) Close() error {
	if closer, ok := b.Toolset.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type toolCatalog struct {
	toolboxes []Toolbox
	// preloaded loads every toolbox from the first model call, whatever the
	// session has loaded, for an agent that has no list_tools or load_tools:
	// one whose work, and so everything it may use, is known before it runs.
	preloaded bool
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

// loadedNames are the toolboxes loaded in a session whose state is state:
// every one in a preloaded catalog, and otherwise those load_tools, or the
// run, loaded.
func (c *toolCatalog) loadedNames(state session.ReadonlyState) []string {
	if !c.preloaded {
		return loadedToolboxes(state)
	}
	names := make([]string, 0, len(c.toolboxes))
	for _, box := range c.toolboxes {
		names = append(names, box.Name)
	}
	return names
}

func (c *toolCatalog) find(name string) (Toolbox, bool) {
	for _, box := range c.toolboxes {
		if box.Name == name {
			return box, true
		}
	}
	return Toolbox{}, false
}

// toolboxFor returns the toolbox whose tools a name would be among, so a
// dynamic sub-agent never takes a name a toolbox tool has or could have.
// Every toolbox tool is named after its toolbox, as load_tools requires, so
// the name alone answers it: no toolbox is asked for its tools, which for
// some is a call over the network as the member.
func (c *toolCatalog) toolboxFor(name string) (Toolbox, bool) {
	for _, box := range c.toolboxes {
		if strings.HasPrefix(name, box.Name+"_") {
			return box, true
		}
	}
	return Toolbox{}, false
}

// loaded returns the native ADK toolset of every toolbox loaded in this
// session. ADK asks a toolset for its tools before every model step, so a
// toolbox load_tools loads during an invocation is there on the next step.
// A loaded toolbox whose tools cannot be listed right
// now, such as a member-authenticated one after the member withdrew their
// grant, is left out rather than failing the turn; load_tools says why.
func (c *toolCatalog) loaded() tool.Toolset {
	return loadedToolset{catalog: c}
}

type loadedToolset struct {
	catalog *toolCatalog
}

func (l loadedToolset) Name() string { return "loaded_tools" }

func (l loadedToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	var tools []tool.Tool
	for _, name := range l.catalog.loadedNames(ctx.ReadonlyState()) {
		box, ok := l.catalog.find(name)
		if !ok || box.Toolset == nil {
			continue
		}
		boxTools, err := box.Toolset.Tools(ctx)
		if err != nil {
			continue
		}
		// load_tools refuses a toolbox whose tools are not all named after
		// it; a preloaded one never passes through load_tools, so it is left
		// out here instead, rather than risk a tool colliding with the
		// agent's own.
		if l.catalog.preloaded && misnamed(box, boxTools) != "" {
			continue
		}
		tools = append(tools, boxTools...)
	}
	return tools, nil
}

// LoadToolboxes is the state change that loads the named toolboxes in a
// session whose state is state, keeping those already loaded: what
// load_tools records, for a run whose work is known before it starts. Their
// tools are there from the run's first model call, and their skills are in
// its instruction. A name no toolbox has loads nothing, as with load_tools.
func LoadToolboxes(state session.ReadonlyState, names ...string) map[string]any {
	loaded := slices.Clone(loadedToolboxes(state))
	for _, requested := range names {
		name := strings.ToLower(strings.TrimSpace(requested))
		if name != "" && !slices.Contains(loaded, name) {
			loaded = append(loaded, name)
		}
	}
	return map[string]any{loadedToolsKey: loaded}
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
		Description: "Lists the toolboxes you can load, such as sandbox or browser, and which are loaded or not available yet.",
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
		Description: "Loads toolboxes by name. Their tools are yours from your next step onward, and each loaded toolbox's instructions join your system prompt.",
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
			// Checked even when already loaded: a loaded toolbox whose tools
			// cannot be listed now is left out of the turn, and this is where
			// the model learns why.
			if reason := misnamedTool(ctx, box); reason != "" {
				unavailable = append(unavailable, unavailableToolbox{Name: name, Reason: reason})
				continue
			}
			if !slices.Contains(loaded, name) {
				loaded = append(loaded, name)
			}
		}
		if err := ctx.State().Set(loadedToolsKey, loaded); err != nil {
			return loadToolsResult{}, err
		}
		return loadToolsResult{Loaded: append([]string{}, loaded...), Unavailable: unavailable}, nil
	})
}

// misnamedTool reports a toolbox whose tools are not all named after it, which
// could otherwise collide with Krude's own tools once loaded, or whose tools
// cannot be listed now.
func misnamedTool(ctx agent.ReadonlyContext, box Toolbox) string {
	tools, err := box.Toolset.Tools(ctx)
	if explained := memberauth.Explain(err); explained != nil {
		return "its tools could not be listed: " + explained.Error()
	}
	if err != nil {
		return "its tools could not be listed right now"
	}
	return misnamed(box, tools)
}

// misnamed reports a tool of box's that is not named after it, or cannot be
// packed into a model request as a toolbox tool must be.
func misnamed(box Toolbox, tools []tool.Tool) string {
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

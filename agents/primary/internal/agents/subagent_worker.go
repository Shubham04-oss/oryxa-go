package agents

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	spawned "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/subagents"
)

// The tools of each family a sub-agent may be given, by name, and no
// others: a toolbox that gains a tool does not give it to sub-agents until
// it is listed here. None sends, posts or changes anything, and none asks
// the member to confirm, which nobody could do for a sub-agent.
var (
	subagentKnowledgeTools = []string{"knowledge_collections", "knowledge_search", "knowledge_sources"}
	subagentAppsTools      = []string{"apps_connections", "apps_search_tools", "apps_run_read_tool"}
)

// SubagentToolboxes are what single-agent Krude's sub-agents may be given,
// each nil when this deployment has none: the knowledge and apps toolboxes
// as unattended runs have them, read-only. Sub-agents have no sandbox: its
// code can reach the network, and what a sub-agent read could carry planted
// instructions to send out.
type SubagentToolboxes struct {
	Knowledge *Toolbox
	Apps      *Toolbox
}

// SubagentBuilder builds the sub-agents single-agent Krude starts
// (internal/subagents): on the work role's model, the lite tier, with the
// tool families their brief allowed and nothing else. web_search is Krude's
// own, on the research role's model.
func SubagentBuilder(router *modelrouter.Router, boxes SubagentToolboxes) (spawned.Builder, error) {
	if router == nil {
		return nil, errors.New("a model router is required")
	}
	return &subagentBuilder{work: router.For(modelrouter.RoleWork), research: router.For(modelrouter.RoleResearch), boxes: boxes}, nil
}

type subagentBuilder struct {
	work, research model.LLM
	boxes          SubagentToolboxes
}

func usable(box *Toolbox) bool { return box != nil && box.Toolset != nil }

func (b *subagentBuilder) Families() []string {
	families := []string{}
	for _, family := range spawned.Families {
		switch family {
		case spawned.ToolKnowledge:
			if usable(b.boxes.Knowledge) {
				families = append(families, family)
			}
		case spawned.ToolAppsRead:
			if usable(b.boxes.Apps) {
				families = append(families, family)
			}
		case spawned.ToolWebSearch:
			families = append(families, family)
		}
	}
	return families
}

func (b *subagentBuilder) Build(worker spawned.Worker) (agent.Agent, error) {
	allowed := []string{spawned.AskToolName}
	tools := slices.Clone(worker.Own)
	var toolboxes []Toolbox
	available := b.Families()
	for _, family := range worker.Tools {
		if !slices.Contains(available, family) {
			return nil, fmt.Errorf("a sub-agent cannot have %q here", family)
		}
		switch family {
		case spawned.ToolKnowledge:
			toolboxes = append(toolboxes, only(*b.boxes.Knowledge, subagentKnowledgeTools))
			allowed = append(allowed, subagentKnowledgeTools...)
		case spawned.ToolAppsRead:
			toolboxes = append(toolboxes, only(*b.boxes.Apps, subagentAppsTools))
			allowed = append(allowed, subagentAppsTools...)
		case spawned.ToolWebSearch:
			research := b.research
			if worker.Meter != nil {
				research = worker.Meter(research)
			}
			search, err := webSearchTool(research)
			if err != nil {
				return nil, err
			}
			tools = append(tools, search)
			allowed = append(allowed, webSearchName)
		}
	}
	// Every toolbox it has is loaded, with its skill, from its first model
	// call: it has no list_tools or load_tools, and nothing else to load.
	catalog := &toolCatalog{toolboxes: toolboxes, preloaded: true}
	instruction := worker.Instruction
	return llmagent.New(llmagent.Config{
		Name:        spawned.WorkerName,
		Description: "A sub-agent Krude started for one assignment.",
		Model:       b.work,
		// A provider returns the instruction as written, so braces in it are
		// not read as session state placeholders.
		InstructionProvider: func(ctx agent.ReadonlyContext) (string, error) {
			var rendered strings.Builder
			rendered.WriteString(instruction)
			rendered.WriteString("\n")
			appendToolboxSkills(&rendered, catalog, ctx.ReadonlyState())
			return rendered.String(), nil
		},
		BeforeModelCallbacks: worker.BeforeModel,
		AfterModelCallbacks:  worker.AfterModel,
		// The allowlist is checked again on every call, whatever a toolset
		// lists; and what it reads is recorded on the run of Krude's that
		// set it working, which Krude's answer is judged by.
		BeforeToolCallbacks:      append([]llmagent.BeforeToolCallback{onlyTools(allowed), recordRunSource}, worker.BeforeTool...),
		AfterToolCallbacks:       worker.AfterTool,
		Tools:                    tools,
		Toolsets:                 []tool.Toolset{catalog.loaded()},
		DisallowTransferToParent: true,
		DisallowTransferToPeers:  true,
	})
}

// only is box with its toolset cut down to names.
func only(box Toolbox, names []string) Toolbox {
	box.Toolset = tool.FilterToolset(box.Toolset, tool.AllowedToolsPredicate(names))
	return box
}

// onlyTools refuses any tool call outside allowed, without running it.
func onlyTools(allowed []string) llmagent.BeforeToolCallback {
	return func(_ agent.Context, called tool.Tool, _ map[string]any) (map[string]any, error) {
		if called != nil && slices.Contains(allowed, called.Name()) {
			return nil, nil
		}
		name := "a tool"
		if called != nil {
			name = called.Name()
		}
		return map[string]any{"error": name + " is not one of your tools. Use only the tools you were given."}, nil
	}
}

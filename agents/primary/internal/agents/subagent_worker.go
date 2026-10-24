package agents

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolutils"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	spawned "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/subagents"
)

// The tools of each family a sub-agent may be given, by name, and no
// others: a toolbox that gains a tool does not give it to sub-agents until
// it is listed here. The reads send, post and change nothing; the sandbox
// runs code, network included, which is why a brief has it alone
// (subagents' checkBrief). None asks the member to confirm, which nobody
// could do for a sub-agent.
var (
	subagentKnowledgeTools = []string{"knowledge_collections", "knowledge_search", "knowledge_sources"}
	subagentAppsTools      = []string{"apps_connections", "apps_search_tools", "apps_run_read_tool"}
	// The sandbox without removing files, resetting it (both confirmed) or
	// saving a file as an artifact of the conversation.
	subagentSandboxTools = []string{"sandbox_execute_code", "sandbox_execute_command", "sandbox_list_files", "sandbox_read_files", "sandbox_write_files"}
)

// SubagentToolboxes are what single-agent Krude's sub-agents may be given,
// each nil when this deployment has none: the knowledge and apps toolboxes
// as unattended runs have them, read-only, and the conversation's sandbox,
// the toolset Krude's own sandbox toolbox has, which is not.
type SubagentToolboxes struct {
	Knowledge *Toolbox
	Apps      *Toolbox
	Sandbox   *Toolbox
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
		case spawned.ToolSandbox:
			if usable(b.boxes.Sandbox) {
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
	if slices.Contains(worker.Tools, spawned.ToolSandbox) && len(worker.Tools) > 1 {
		return nil, errors.New("a sub-agent with the sandbox has no other tools: code there can reach the network")
	}
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
		case spawned.ToolSandbox:
			box := only(*b.boxes.Sandbox, subagentSandboxTools)
			box.Toolset = threadToolset{inner: box.Toolset, thread: worker.Thread}
			toolboxes = append(toolboxes, box)
			allowed = append(allowed, subagentSandboxTools...)
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

// threadToolset is a sub-agent's view of the conversation's sandbox: each
// of its tools runs as though in the thread's own session, so it reaches the
// sandbox Krude uses there (the edge's thread room, AgentCore's session per
// app, member and thread), as Krude itself.
type threadToolset struct {
	inner  tool.Toolset
	thread spawned.Thread
}

func (t threadToolset) Name() string { return t.inner.Name() }

func (t threadToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	tools, err := t.inner.Tools(threadReadonly{ReadonlyContext: ctx, thread: t.thread})
	if err != nil {
		return nil, err
	}
	wrapped := make([]tool.Tool, len(tools))
	for i, inner := range tools {
		wrapped[i] = threadTool{inner: inner, thread: t.thread}
	}
	return wrapped, nil
}

type threadReadonly struct {
	agent.ReadonlyContext
	thread spawned.Thread
}

func (c threadReadonly) AppName() string   { return c.thread.App }
func (c threadReadonly) SessionID() string { return c.thread.ID }
func (c threadReadonly) AgentName() string { return RootName }

type threadContext struct {
	agent.Context
	thread spawned.Thread
}

func (c threadContext) AppName() string   { return c.thread.App }
func (c threadContext) SessionID() string { return c.thread.ID }
func (c threadContext) AgentName() string { return RootName }

type threadTool struct {
	inner  tool.Tool
	thread spawned.Thread
}

func (t threadTool) Name() string        { return t.inner.Name() }
func (t threadTool) Description() string { return t.inner.Description() }
func (t threadTool) IsLongRunning() bool { return t.inner.IsLongRunning() }

func (t threadTool) Declaration() *genai.FunctionDeclaration {
	if declared, ok := t.inner.(toolutils.Tool); ok {
		return declared.Declaration()
	}
	return nil
}

func (t threadTool) ProcessRequest(_ agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, t)
}

func (t threadTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	runner, ok := t.inner.(interface {
		Run(agent.Context, any) (map[string]any, error)
	})
	if !ok {
		return nil, fmt.Errorf("tool %q cannot be run", t.inner.Name())
	}
	return runner.Run(threadContext{Context: ctx, thread: t.thread}, args)
}

var (
	_ toolutils.Tool = threadTool{}
	_ tool.Toolset   = threadToolset{}
)

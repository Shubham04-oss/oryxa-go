package agents

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolutils"
)

// subagentsKey is the session state key holding the sub-agents Krude created
// for this conversation.
const subagentsKey = "subagents"

const (
	// maxSubagents bounds how many dynamic sub-agents one session holds.
	maxSubagents = 12
	// maxSubagentNameRunes bounds a sub-agent's name, which is also the tool
	// name Krude calls it by.
	maxSubagentNameRunes = 30
	// maxSubagentDescriptionRunes bounds the one line Krude picks a sub-agent
	// by, and maxSubagentInstructionRunes the prompt the sub-agent runs on.
	maxSubagentDescriptionRunes = 200
	maxSubagentInstructionRunes = 2000
)

var subagentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// reservedSubagentNames are the names a dynamic sub-agent may not take: the
// agent tree's own names and every tool Krude or ADK already declares.
var reservedSubagentNames = map[string]bool{
	RootName:       true,
	plannerName:    true,
	researcherName: true,
	"user":         true,

	"list_tools":   true,
	"load_tools":   true,
	"list_helpers": true,
	"spawn_helper": true,

	"create_subagent": true,
	"list_subagents":  true,
	"remove_subagent": true,

	"load_artifacts":    true,
	"transfer_to_agent": true,
	"finish_task":       true,
	"task_completed":    true,
}

// subagent is one sub-agent Krude created in this conversation. It is a prompt
// and a name: the model writes who it is and how it works at creation, and
// gives it a complete task each time it calls it.
type subagent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Instruction string `json:"instruction"`
}

// subagents owns a session's dynamic sub-agents: the tools that create, list
// and drop them, and the toolset that exposes each one to Krude as a callable
// agent tool (ADK's agenttool).
//
// A sub-agent runs in its own runner with its own throwaway session, so it
// sees only the task Krude hands it, not the conversation. It gets no tools:
// anything that sends, posts or changes something stays with Krude, which can
// confirm with the member.
type subagents struct {
	llm     model.LLM
	catalog *toolCatalog
}

func newSubagents(llm model.LLM, catalog *toolCatalog) *subagents {
	return &subagents{llm: llm, catalog: catalog}
}

type createSubagentArgs struct {
	Name        string `json:"name" jsonschema:"A lowercase name the sub-agent is called by, such as fact_checker."`
	Description string `json:"description" jsonschema:"One line describing what the sub-agent is for."`
	Instruction string `json:"instruction" jsonschema:"Who the sub-agent is and how it works. It sees nothing but this instruction and the task you give it when you call it."`
}

type subagentSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type createSubagentResult struct {
	Subagent subagentSummary `json:"subagent"`
	Note     string          `json:"note"`
}

func (s *subagents) createTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "create_subagent",
		Description: "Creates a reusable sub-agent for this conversation. You write who it is and how it works, and from your next step you can call it by name with a complete task. It sees only that task, has no tools, and never sends, posts or changes anything.",
	}, func(ctx agent.Context, args createSubagentArgs) (createSubagentResult, error) {
		spec, err := s.validSpec(ctx, args)
		if err != nil {
			return createSubagentResult{}, err
		}
		sessionSpecs := append(subagentsFromState(ctx.ReadonlyState()), spec)
		if err := ctx.State().Set(subagentsKey, sessionSpecs); err != nil {
			return createSubagentResult{}, err
		}
		return createSubagentResult{
			Subagent: subagentSummary{Name: spec.Name, Description: spec.Description},
			Note:     fmt.Sprintf("Call %s by name from your next step, with everything it needs.", spec.Name),
		}, nil
	})
}

// validSpec checks the request against the state and the current names, so a
// sub-agent can be called later by exactly the name it was created with.
func (s *subagents) validSpec(ctx agent.Context, args createSubagentArgs) (subagent, error) {
	name := strings.TrimSpace(args.Name)
	if err := validateSubagentName(name); err != nil {
		return subagent{}, err
	}
	description := boundedText(args.Description, maxSubagentDescriptionRunes)
	if description == "" {
		return subagent{}, errors.New("the sub-agent needs a one-line description")
	}
	instruction := strings.TrimSpace(args.Instruction)
	if instruction == "" {
		return subagent{}, errors.New("the sub-agent needs an instruction")
	}
	if utf8.RuneCountInString(instruction) > maxSubagentInstructionRunes {
		return subagent{}, fmt.Errorf("the instruction is longer than %d characters", maxSubagentInstructionRunes)
	}

	existing := subagentsFromState(ctx.ReadonlyState())
	if len(existing) >= maxSubagents {
		return subagent{}, fmt.Errorf("this conversation already has %d sub-agents; remove one first", maxSubagents)
	}
	for _, spec := range existing {
		if spec.Name == name {
			return subagent{}, fmt.Errorf("a sub-agent named %q already exists", name)
		}
	}
	if conflict, err := s.catalog.hasTool(ctx, name); err != nil {
		return subagent{}, err
	} else if conflict {
		return subagent{}, fmt.Errorf("a toolbox already has a tool named %q; pick another name", name)
	}
	return subagent{Name: name, Description: description, Instruction: instruction}, nil
}

func validateSubagentName(name string) error {
	if name == "" {
		return errors.New("the sub-agent needs a name")
	}
	if utf8.RuneCountInString(name) > maxSubagentNameRunes {
		return fmt.Errorf("the name is longer than %d characters", maxSubagentNameRunes)
	}
	if !subagentNamePattern.MatchString(name) {
		return fmt.Errorf("the name %q must be lowercase letters, digits and underscores, starting with a letter", name)
	}
	if reservedSubagentNames[name] {
		return fmt.Errorf("the name %q is reserved", name)
	}
	return nil
}

type listSubagentsArgs struct{}

type listSubagentsResult struct {
	Subagents []subagentSummary `json:"subagents"`
	Note      string            `json:"note"`
}

func (s *subagents) listTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "list_subagents",
		Description: "Lists the sub-agents you created in this conversation, with what each is for.",
	}, func(ctx agent.Context, _ listSubagentsArgs) (listSubagentsResult, error) {
		result := listSubagentsResult{
			Subagents: []subagentSummary{},
			Note:      "create_subagent adds one; call one by name with a complete task; remove_subagent drops one you no longer need.",
		}
		for _, spec := range subagentsFromState(ctx.ReadonlyState()) {
			result.Subagents = append(result.Subagents, subagentSummary{Name: spec.Name, Description: spec.Description})
		}
		return result, nil
	})
}

type removeSubagentArgs struct {
	Name string `json:"name" jsonschema:"The name of the sub-agent to remove."`
}

type removeSubagentResult struct {
	Removed   string            `json:"removed"`
	Subagents []subagentSummary `json:"subagents"`
}

func (s *subagents) removeTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "remove_subagent",
		Description: "Removes a sub-agent you created in this conversation. It can no longer be called.",
	}, func(ctx agent.Context, args removeSubagentArgs) (removeSubagentResult, error) {
		name := strings.TrimSpace(args.Name)
		existing := subagentsFromState(ctx.ReadonlyState())
		remaining := make([]subagent, 0, len(existing))
		found := false
		for _, spec := range existing {
			if spec.Name == name {
				found = true
				continue
			}
			remaining = append(remaining, spec)
		}
		if !found {
			return removeSubagentResult{}, fmt.Errorf("no sub-agent named %q", name)
		}
		if err := ctx.State().Set(subagentsKey, remaining); err != nil {
			return removeSubagentResult{}, err
		}
		result := removeSubagentResult{Removed: name, Subagents: []subagentSummary{}}
		for _, spec := range remaining {
			result.Subagents = append(result.Subagents, subagentSummary{Name: spec.Name, Description: spec.Description})
		}
		return result, nil
	})
}

// Name implements tool.Toolset.
func (s *subagents) Name() string { return "subagents" }

// Tools returns nothing on purpose. A sub-agent can be created or removed
// while Krude is working, so this toolset packs its tools in ProcessRequest,
// which ADK runs before every model step against the session's current state;
// the model sees a change on its very next step. Returning the tools here
// would let ADK pack a sub-agent that was just removed for the rest of the
// invocation.
func (s *subagents) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	return nil, nil
}

// ProcessRequest packs one agent tool per registered sub-agent, so anything
// created (or dropped) since the last step is in effect for this one.
func (s *subagents) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	for _, spec := range subagentsFromState(ctx.ReadonlyState()) {
		if _, packed := req.Tools[spec.Name]; packed {
			continue
		}
		built, err := s.agentTool(spec)
		if err != nil {
			return err
		}
		declarative, ok := built.(toolutils.Tool)
		if !ok {
			return fmt.Errorf("sub-agent %q cannot be declared", spec.Name)
		}
		if err := toolutils.PackTool(req, declarative); err != nil {
			return err
		}
	}
	return nil
}

// agentTool wraps the sub-agent's prompt as an agent tool. ADK runs it in its
// own runner and returns its final answer to Krude. The worker is chat-mode:
// it is the root of that runner, and it has no tools, so one model turn is
// the whole call.
func (s *subagents) agentTool(spec subagent) (tool.Tool, error) {
	worker, err := llmagent.New(llmagent.Config{
		Name:        spec.Name,
		Description: spec.Description,
		Model:       s.llm,
		// A provider returns the instruction as written, so braces in it are
		// not read as session state placeholders.
		InstructionProvider:      func(agent.ReadonlyContext) (string, error) { return spec.Instruction, nil },
		DisallowTransferToParent: true,
		DisallowTransferToPeers:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("build sub-agent %q: %w", spec.Name, err)
	}
	return agenttool.New(worker, nil), nil
}

// subagentsFromState reads the session's sub-agents. A value read back from
// storage arrives as []any of maps rather than []subagent.
func subagentsFromState(state session.ReadonlyState) []subagent {
	value, err := state.Get(subagentsKey)
	if err != nil {
		return nil
	}
	switch stored := value.(type) {
	case []subagent:
		return stored
	case []any:
		specs := make([]subagent, 0, len(stored))
		for _, entry := range stored {
			switch item := entry.(type) {
			case subagent:
				specs = append(specs, item)
			case map[string]any:
				spec := subagent{Name: stringValue(item["name"]), Description: stringValue(item["description"]), Instruction: stringValue(item["instruction"])}
				if spec.Name == "" {
					continue
				}
				specs = append(specs, spec)
			}
		}
		return specs
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

package agents

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/workflow"
)

// helperPreamble opens every temporary helper's instruction. The helper cannot
// reach the member, so anything that sends, posts or changes something goes
// back to Krude to confirm first.
const helperPreamble = `You are a temporary helper that Krude, a member's assistant, started for one task. Do the task you are given completely and on your own: you cannot ask anyone a question, and nobody sees your work except the result you return. Never send, post or change anything in another system; prepare it and return it so Krude can confirm it with the member. Return a clear, complete result.`

var helperNameCleaner = regexp.MustCompile(`[^a-z0-9_]+`)

type helpers struct {
	llm       model.LLM
	subAgents []agent.Agent
	spawned   atomic.Int64
}

func newHelpers(llm model.LLM, subAgents []agent.Agent) *helpers {
	return &helpers{llm: llm, subAgents: subAgents}
}

type listHelpersArgs struct{}

type listedHelper struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Works       string `json:"works"`
}

type listHelpersResult struct {
	Dedicated []listedHelper `json:"dedicated"`
	Temporary string         `json:"temporary"`
}

func (h *helpers) listTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "list_helpers",
		Description: "Lists your dedicated sub-agents and how each works, and explains temporary helpers.",
	}, func(agent.Context, listHelpersArgs) (listHelpersResult, error) {
		result := listHelpersResult{
			Temporary: "spawn_helper starts a temporary helper for work that needs an agent of its own or is too big for one agent. Helpers started in the same turn work at the same time and return a result without talking to the member.",
		}
		for _, subAgent := range h.subAgents {
			result.Dedicated = append(result.Dedicated, listedHelper{Name: subAgent.Name(), Description: subAgent.Description(), Works: "runs on its own and returns a result"})
		}
		return result, nil
	})
}

type spawnHelperArgs struct {
	Name        string `json:"name" jsonschema:"A short name for the helper, such as drafter or triage."`
	Instruction string `json:"instruction" jsonschema:"Who the helper is and how it should work."`
	Task        string `json:"task" jsonschema:"The complete task. The helper sees nothing but this and its instruction."`
}

type spawnHelperResult struct {
	Helper string `json:"helper"`
	Result any    `json:"result"`
}

func (h *helpers) spawnTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "spawn_helper",
		Description: "Starts a temporary helper agent for one task that needs an agent of its own or is too big for one agent, and returns its result. Start several in one turn to work on independent parts at the same time.",
	}, func(ctx agent.Context, args spawnHelperArgs) (spawnHelperResult, error) {
		instruction, task := strings.TrimSpace(args.Instruction), strings.TrimSpace(args.Task)
		if instruction == "" || task == "" {
			return spawnHelperResult{}, errors.New("a helper needs both an instruction and a task")
		}
		name := h.helperName(args.Name)
		full := helperPreamble + "\n\n" + instruction
		helper, err := llmagent.New(llmagent.Config{
			Name:        name,
			Description: "A temporary helper Krude started for one task.",
			Model:       h.llm,
			Mode:        llmagent.ModeSingleTurn,
			// A provider returns the instruction as written, so braces in it are
			// not read as session state placeholders.
			InstructionProvider:      func(agent.ReadonlyContext) (string, error) { return full, nil },
			DisallowTransferToParent: true,
			DisallowTransferToPeers:  true,
		})
		if err != nil {
			return spawnHelperResult{}, err
		}
		node, err := workflow.NewAgentNode(helper, workflow.NodeConfig{})
		if err != nil {
			return spawnHelperResult{}, err
		}
		result, err := workflow.RunNode[any](ctx, node, task, workflow.WithUseSubBranch())
		if err != nil {
			return spawnHelperResult{}, fmt.Errorf("helper %s: %w", name, err)
		}
		return spawnHelperResult{Helper: name, Result: result}, nil
	})
}

// helperName turns the name the model chose into a unique agent name, such as
// helper_drafter_3.
func (h *helpers) helperName(requested string) string {
	cleaned := strings.Trim(helperNameCleaner.ReplaceAllString(strings.ToLower(requested), "_"), "_")
	if len(cleaned) > 30 {
		cleaned = strings.Trim(cleaned[:30], "_")
	}
	if cleaned == "" {
		cleaned = "task"
	}
	return fmt.Sprintf("helper_%s_%d", cleaned, h.spawned.Add(1))
}

package agents

import (
	_ "embed"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
)

//go:embed instructions/planner.md
var plannerInstruction string

// planSchema is the plan the planner returns. A step runs once the steps it
// depends on have finished, so steps with no dependencies can run together.
// "helper" asks Krude to start a temporary helper for the step.
var planSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"steps": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"agent": {
						Type: genai.TypeString,
						Enum: []string{RootName, researcherName, teamsName, "helper"},
					},
					"task": {
						Type:        genai.TypeString,
						Description: "What the sub-agent should do, written so it can act without seeing the conversation.",
					},
					"depends_on": {
						Type:        genai.TypeArray,
						Items:       &genai.Schema{Type: genai.TypeInteger},
						Description: "Numbers of the earlier steps, counting from 1, whose results this step needs.",
					},
				},
				Required: []string{"agent", "task"},
			},
		},
	},
	Required: []string{"steps"},
}

// planner turns a request that needs several steps or sub-agents into a plan.
// It runs on its own and returns the plan to Krude, which runs the steps.
func planner(llm model.LLM) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:                     plannerName,
		Description:              "Turns a request that needs several steps into a plan that gives each step to Krude, researcher, teams or a temporary helper.",
		Model:                    llm,
		Mode:                     llmagent.ModeSingleTurn,
		Instruction:              plannerInstruction,
		OutputSchema:             planSchema,
		DisallowTransferToParent: true,
		DisallowTransferToPeers:  true,
	})
}

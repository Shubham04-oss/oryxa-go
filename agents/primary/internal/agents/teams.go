package agents

import (
	_ "embed"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
)

//go:embed instructions/teams.md
var teamsInstruction string

// teams finds teams and people in the member's organization and calls them in.
// It is a task agent, so it may ask the member which team they meant before it
// returns its result through finish_task. The teams service tools it will use
// are not wired yet.
func teams(llm model.LLM) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:                     teamsName,
		Description:              "Finds teams and people in the member's Oryxa organization and calls them in.",
		Model:                    llm,
		Mode:                     llmagent.ModeTask,
		Instruction:              teamsInstruction,
		DisallowTransferToParent: true,
		DisallowTransferToPeers:  true,
	})
}

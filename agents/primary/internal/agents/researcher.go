package agents

import (
	_ "embed"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/geminitool"
)

//go:embed instructions/researcher.md
var researcherInstruction string

// researcher answers from the live web through Gemini's Google Search
// grounding. Search is a Gemini built-in tool, which Gemini does not always
// accept next to function tools, so it gets an agent of its own, and transfers
// stay off so ADK does not add transfer_to_agent beside it.
func researcher(llm model.LLM) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:                     researcherName,
		Description:              "Answers a question from the live web using Google Search, and names its sources.",
		Model:                    llm,
		Mode:                     llmagent.ModeSingleTurn,
		Instruction:              researcherInstruction,
		Tools:                    []tool.Tool{geminitool.GoogleSearch{}},
		DisallowTransferToParent: true,
		DisallowTransferToPeers:  true,
	})
}

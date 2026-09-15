package agents

import (
	_ "embed"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/geminitool"
)

//go:embed instructions/researcher.md
var researcherInstruction string

// researcher answers from the live web through Gemini's native tools: Google
// Search grounding for questions, and URL context for links it is given. Both
// are built into the model: Gemini invokes them server-side and returns
// grounding metadata, so nothing runs in the primary service and no function
// tool competes with them. That is also why the researcher is an agent of its
// own, with transfers off.
func researcher(llm model.LLM) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:                     researcherName,
		Description:              "Answers a question from the live web using Google Search, reads the links it is given, and names its sources.",
		Model:                    llm,
		Mode:                     llmagent.ModeSingleTurn,
		Instruction:              researcherInstruction,
		Tools:                    []tool.Tool{geminitool.GoogleSearch{}, urlContext()},
		DisallowTransferToParent: true,
		DisallowTransferToPeers:  true,
	})
}

// urlContext is Gemini's native URL reader. It fetches and grounds on pages
// the request names, and returns URL context metadata with what it read.
func urlContext() tool.Tool {
	return geminitool.New(
		"url_context",
		"Reads a URL the researcher is given, natively in Gemini.",
		&genai.Tool{URLContext: &genai.URLContext{}},
	)
}

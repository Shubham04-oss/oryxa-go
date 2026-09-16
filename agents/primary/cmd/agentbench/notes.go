package main

import (
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// notesToolbox is a bench fixture: a loadable toolbox whose skill is long
// enough to take the prompt prefix over Vertex's explicit cache floor, so a
// run can show a real cache hit when the toolbox is loaded.
func notesToolbox() agents.Toolbox {
	return agents.Toolbox{
		Name:         "notes",
		Description:  "Keeps short notes for the conversation.",
		Instructions: strings.Repeat("Keep every note short and exact, and always use notes_add instead of writing a note in your answer. ", 160),
		Toolset:      notesToolset{},
	}
}

type notesToolset struct{}

func (notesToolset) Name() string { return "notes" }

func (notesToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	notesAdd, err := functiontool.New(functiontool.Config{
		Name: "notes_add", Description: "Stores one note.",
	}, func(_ agent.Context, args notesArgs) (notesResult, error) {
		return notesResult{Stored: args.Text}, nil
	})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{notesAdd}, nil
}

type notesArgs struct {
	Text string `json:"text" jsonschema:"The note to store."`
}

type notesResult struct {
	Stored string `json:"stored"`
}

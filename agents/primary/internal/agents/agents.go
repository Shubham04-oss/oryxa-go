// Package agents defines primary's agents: Krude, the assistant members talk
// to, the dedicated sub-agents Krude hands work to, and the tools, helpers and
// skills Krude reaches for while it works.
package agents

import (
	"context"
	_ "embed"
	"errors"
	"strings"
	"text/template"
	"unicode/utf8"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/loadartifactstool"
)

// RootName names Krude, the agent every conversation starts with. ADK also uses
// it as the app name.
const RootName = "krude"

// The dedicated sub-agents' names. Krude calls each one by name, and the
// planner assigns steps to them by name.
const (
	plannerName    = "planner"
	researcherName = "researcher"
	teamsName      = "teams"
)

// Session state keys Krude's instruction reads. Whoever creates a session sets
// the member's name, a short description of them and their organization's
// name. Krude sets the nickname itself when the member gives it one; the user:
// prefix keeps it with the member across their sessions.
const (
	UserNameKey  = "user_name"
	UserAboutKey = "user_about"
	OrgNameKey   = "org_name"
	NicknameKey  = "user:agent_nickname"
)

// maxStateTextRunes bounds a member-written value placed in Krude's
// instruction, which is kept to one short line.
const maxStateTextRunes = 60

//go:embed instructions/krude.md
var krudeInstructionText string

// krudeInstruction is a Go text/template rendered on every turn by instruction.
// The sub-agents' instructions are ADK templates instead: {key} is replaced
// with that session state value, and a turn fails when the key is missing
// unless it is written {key?}.
var krudeInstruction = template.Must(template.New("krude").Parse(krudeInstructionText))

// Gemini returns the named Gemini model on Vertex AI in the given Google Cloud
// project and location. It authenticates with Application Default Credentials:
// locally the login from `gcloud auth application-default login`, elsewhere the
// file GOOGLE_APPLICATION_CREDENTIALS names, such as a workload identity
// federation configuration. An API key in the environment is never used.
func Gemini(ctx context.Context, name, project, location string) (model.LLM, error) {
	if project == "" {
		return nil, errors.New("a Google Cloud project is required: the agents reach Gemini through Vertex AI")
	}
	return gemini.NewModel(ctx, name, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  project,
		Location: location,
	})
}

// Root returns Krude with everything it works with:
//
//   - dedicated sub-agents it calls like tools: planner and researcher run on
//     their own, and those called in the same model turn run concurrently;
//     teams is a task agent, which may ask the member a question first;
//   - the toolboxes it can load, such as one provider's actions;
//   - helpers: list_helpers describes the sub-agents, and spawn_helper runs a
//     temporary agent for work that needs its own agent or is too big for one;
//   - skills, whose full instructions it loads only when one fits;
//   - set_nickname, for the nickname the member gives it.
func Root(ctx context.Context, llm model.LLM, toolboxes []Toolbox) (agent.Agent, error) {
	catalog, err := newToolCatalog(toolboxes)
	if err != nil {
		return nil, err
	}
	skills, err := newSkills(ctx)
	if err != nil {
		return nil, err
	}

	var subAgents []agent.Agent
	for _, build := range []func(model.LLM) (agent.Agent, error){planner, researcher, teams} {
		subAgent, err := build(llm)
		if err != nil {
			return nil, err
		}
		subAgents = append(subAgents, subAgent)
	}

	// Helpers work with what Krude has loaded, but never spawn helpers of their
	// own or set Krude's nickname.
	helpers := newHelpers(llm, subAgents, []tool.Toolset{catalog.loaded(), skills})
	tools, err := buildTools(setNicknameTool, catalog.listTool, catalog.loadTool, helpers.listTool, helpers.spawnTool, loadArtifactsTool)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:                RootName,
		Description:         "Krude, a member's assistant.",
		Model:               llm,
		InstructionProvider: instruction,
		SubAgents:           subAgents,
		Tools:               tools,
		Toolsets:            []tool.Toolset{catalog.loaded(), skills},
	})
}

func loadArtifactsTool() (tool.Tool, error) {
	return loadartifactstool.New(), nil
}

func buildTools(builders ...func() (tool.Tool, error)) ([]tool.Tool, error) {
	tools := make([]tool.Tool, 0, len(builders))
	for _, build := range builders {
		built, err := build()
		if err != nil {
			return nil, err
		}
		tools = append(tools, built)
	}
	return tools, nil
}

// instruction renders Krude's instruction for the member in this session.
func instruction(ctx agent.ReadonlyContext) (string, error) {
	state := ctx.ReadonlyState()
	var rendered strings.Builder
	err := krudeInstruction.Execute(&rendered, struct{ UserName, UserAbout, OrgName, Nickname string }{
		UserName:  stateText(state, UserNameKey, "the member you are talking with"),
		UserAbout: stateText(state, UserAboutKey, ""),
		OrgName:   stateText(state, OrgNameKey, "their organization"),
		Nickname:  stateText(state, NicknameKey, ""),
	})
	return rendered.String(), err
}

// stateText returns the state value at key as one short line, or fallback when
// the key is missing or holds no text.
func stateText(state session.ReadonlyState, key, fallback string) string {
	value, err := state.Get(key)
	if err != nil {
		return fallback
	}
	text, ok := value.(string)
	if !ok {
		return fallback
	}
	if text = oneLine(text); text == "" {
		return fallback
	}
	return text
}

// oneLine collapses text to a single line of at most maxStateTextRunes runes.
func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > maxStateTextRunes {
		text = string([]rune(text)[:maxStateTextRunes])
	}
	return text
}

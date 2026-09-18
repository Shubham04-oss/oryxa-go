// Package agents defines primary's agents: Krude, the assistant members talk
// to, the dedicated sub-agents Krude hands work to, and the helpers Krude
// starts for work that needs an agent of its own.
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

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

// RootName names Krude, the agent every conversation starts with. ADK also uses
// it as the app name.
const RootName = "krude"

// The dedicated sub-agents' names. Krude calls each one by name, and the
// planner assigns steps to them by name.
const (
	plannerName    = "planner"
	researcherName = "researcher"
)

// Session state keys Krude's instruction reads. The session runtime seeds them
// from the member's auth record when the session is created.
const (
	UserNameKey       = "user_name"
	UserAboutKey      = "user_about"
	UserEmailKey      = "user_email"
	UserRoleKey       = "user_role"
	OrgNameKey        = "org_name"
	OrganizationIDKey = "organization_id"
)

// maxStateTextRunes bounds a member-written value placed in Krude's
// instruction, which is kept to one short line.
const maxStateTextRunes = 60

//go:embed instructions/krude.md
var krudeInstructionText string

//go:embed instructions/krude_unattended.md
var unattendedInstructionText string

// krudeInstruction is a Go text/template rendered on every turn by instruction.
// The sub-agents' instructions are ADK templates instead: {key} is replaced
// with that session state value, and a turn fails when the key is missing
// unless it is written {key?}.
var krudeInstruction = template.Must(template.New("krude").Parse(krudeInstructionText))

// unattendedInstruction is Krude's instruction for a run the member is not
// there for, rendered the same way.
var unattendedInstruction = template.Must(template.New("krude-unattended").Parse(unattendedInstructionText))

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

// GeminiClient returns a Gen AI client on the same Vertex AI project and
// location as the agents, for services that share their configuration such as
// explicit prompt caching.
func GeminiClient(ctx context.Context, project, location string) (*genai.Client, error) {
	if project == "" {
		return nil, errors.New("a Google Cloud project is required: Vertex AI serves the models")
	}
	return genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  project,
		Location: location,
	})
}

// Root returns Krude with everything it works with:
//
//   - dedicated sub-agents it calls like tools: planner and researcher run on
//     their own, and those called in the same model turn run concurrently;
//   - sub-agents Krude writes itself: create_subagent registers a specialist
//     for the session, and it is callable by name from the next step;
//   - toolboxes it loads on demand: list_tools shows them and load_tools loads
//     the ones a task needs, such as the AgentCore sandbox or browser;
//   - helpers: list_helpers describes the sub-agents, and spawn_helper runs a
//     temporary agent for work that needs its own agent or is too big for one.
//
// The router decides which model each role runs on: the member's conversation
// on the main model, planning and single-turn work on the lite model, and
// grounded research on the main model.
func Root(router *modelrouter.Router, toolboxes []Toolbox) (agent.Agent, error) {
	if router == nil {
		return nil, errors.New("a model router is required")
	}
	catalog, err := newToolCatalog(toolboxes)
	if err != nil {
		return nil, err
	}

	plannerAgent, err := planner(router.For(modelrouter.RolePlan))
	if err != nil {
		return nil, err
	}
	researcherAgent, err := researcher(router.For(modelrouter.RoleResearch))
	if err != nil {
		return nil, err
	}
	subAgents := []agent.Agent{plannerAgent, researcherAgent}

	work := router.For(modelrouter.RoleWork)
	// Helpers never spawn helpers of their own.
	helpers := newHelpers(work, subAgents)
	// Dynamic sub-agents: Krude creates, calls and drops them in a session.
	subagents := newSubagents(work, catalog)
	tools, err := buildTools(catalog.listTool, catalog.loadTool, userDetailsTool, subagents.createTool, subagents.listTool, subagents.removeTool, helpers.listTool, helpers.spawnTool, loadArtifactsTool, saveArtifactTool)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:                RootName,
		Description:         "Krude, a member's assistant.",
		Model:               router.For(modelrouter.RoleChat),
		InstructionProvider: instructionFrom(krudeInstruction, catalog),
		SubAgents:           subAgents,
		Tools:               tools,
		Toolsets:            []tool.Toolset{catalog.loaded(), subagents},
	})
}

// UnattendedRoot returns Krude for a run the member is not there for, such
// as a wake-up. What reaches Krude there is text from the member's apps,
// which anyone who can email them or open an issue wrote, and nobody watches
// what it does with it. So nothing in such a run may be able to send data
// anywhere that text could name: no web research, no sub-agents or helpers
// (which could do research of their own), no saved files, and only the
// toolboxes given. The caller passes those that reach nothing outside, such
// as knowledge and the apps' reads.
//
// It answers to the same name as Root, since it takes turns with Root in the
// same thread.
func UnattendedRoot(router *modelrouter.Router, toolboxes []Toolbox) (agent.Agent, error) {
	if router == nil {
		return nil, errors.New("a model router is required")
	}
	catalog, err := newToolCatalog(toolboxes)
	if err != nil {
		return nil, err
	}
	tools, err := buildTools(catalog.listTool, catalog.loadTool, userDetailsTool, loadArtifactsTool)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:                RootName,
		Description:         "Krude, reporting to a member who is away.",
		Model:               router.For(modelrouter.RoleChat),
		InstructionProvider: instructionFrom(unattendedInstruction, catalog),
		Tools:               tools,
		Toolsets:            []tool.Toolset{catalog.loaded()},
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

// instructionFrom renders an instruction of Krude's for the member in this
// session, then appends the skill of every loaded toolbox, so a toolbox that
// is loaded brings its usage instructions with it.
func instructionFrom(instruction *template.Template, catalog *toolCatalog) func(agent.ReadonlyContext) (string, error) {
	return func(ctx agent.ReadonlyContext) (string, error) {
		state := ctx.ReadonlyState()
		var rendered strings.Builder
		err := instruction.Execute(&rendered, struct{ UserName, UserAbout, OrgName, Member string }{
			UserName:  stateText(state, UserNameKey, "the member you are talking with"),
			UserAbout: stateText(state, UserAboutKey, ""),
			OrgName:   stateText(state, OrgNameKey, "their organization"),
			Member:    memberDetails(state),
		})
		if err != nil {
			return "", err
		}
		appendToolboxSkills(&rendered, catalog, state)
		return rendered.String(), nil
	}
}

// memberDetails renders the member's account details as the prompt's bullet
// list.
func memberDetails(state session.ReadonlyState) string {
	lines := []string{"- Name: " + stateText(state, UserNameKey, "the member you are talking with")}
	for _, optional := range []struct{ label, key string }{
		{"About", UserAboutKey},
		{"Email", UserEmailKey},
		{"Role", UserRoleKey},
	} {
		if value := stateText(state, optional.key, ""); value != "" {
			lines = append(lines, "- "+optional.label+": "+value)
		}
	}
	lines = append(lines, "- Organization: "+stateText(state, OrgNameKey, "their organization"))
	if id := stateText(state, OrganizationIDKey, ""); id != "" {
		lines = append(lines, "- Organization ID: "+id)
	}
	return strings.Join(lines, "\n")
}

// appendToolboxSkills adds the skill of every loaded toolbox to the rendered
// instruction.
func appendToolboxSkills(rendered *strings.Builder, catalog *toolCatalog, state session.ReadonlyState) {
	var skills []string
	for _, name := range loadedToolboxes(state) {
		box, ok := catalog.find(name)
		if !ok || strings.TrimSpace(box.Instructions) == "" {
			continue
		}
		skills = append(skills, "### "+box.Name+"\n\n"+strings.TrimSpace(box.Instructions))
	}
	if len(skills) == 0 {
		return
	}
	rendered.WriteString("\n## Loaded toolboxes\n\nEach loaded toolbox comes with its skill. Follow it, and when you are unsure how to use one of its tools, read it again instead of guessing.\n\n")
	rendered.WriteString(strings.Join(skills, "\n\n"))
	rendered.WriteString("\n")
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
	return boundedText(text, maxStateTextRunes)
}

// boundedText collapses text to a single line of at most max runes.
func boundedText(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > max {
		text = string([]rune(text)[:max])
	}
	return text
}

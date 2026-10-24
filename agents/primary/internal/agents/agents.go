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

	"cloud.google.com/go/auth"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/loadartifactstool"
	"google.golang.org/adk/v2/tool/loadmemorytool"

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
	// AgentIDKey, AgentNameKey and AgentKindKey carry a persistent agent's
	// stable identity separately from ADK's immutable machine name.
	AgentIDKey   = "managed_agent_id"
	AgentNameKey = "managed_agent_name"
	AgentKindKey = "managed_agent_kind"
)

// maxStateTextRunes bounds a member-written value placed in Krude's
// instruction, which is kept to one short line.
const maxStateTextRunes = 60

//go:embed instructions/krude.md
var krudeInstructionText string

//go:embed instructions/krude_single.md
var singleInstructionText string

//go:embed instructions/krude_unattended.md
var unattendedInstructionText string

//go:embed instructions/krude_room.md
var roomInstructionText string

// krudeInstruction is a Go text/template rendered on every turn by instruction.
// The sub-agents' instructions are ADK templates instead: {key} is replaced
// with that session state value, and a turn fails when the key is missing
// unless it is written {key?}.
var krudeInstruction = template.Must(template.New("krude").Parse(krudeInstructionText))

// singleInstruction is Krude's instruction when it is one agent
// (SingleAgentRoot), rendered the same way.
var singleInstruction = template.Must(template.New("krude-single").Parse(singleInstructionText))

// unattendedInstruction is Krude's instruction for a run the member is not
// there for, rendered the same way.
var unattendedInstruction = template.Must(template.New("krude-unattended").Parse(unattendedInstructionText))

// roomInstruction is Krude's instruction for its turn in a room, rendered
// the same way.
var roomInstruction = template.Must(template.New("krude-room").Parse(roomInstructionText))

// Gemini returns the named Gemini model on Vertex AI in the given Google Cloud
// project and location. It authenticates with credentials when there are any,
// such as those internal/gcpauth federates from AWS, and otherwise with
// Application Default Credentials: locally the login from `gcloud auth
// application-default login`, elsewhere the file GOOGLE_APPLICATION_CREDENTIALS
// names. An API key in the environment is never used.
//
// A call Vertex answers with 429, 500, 502, 503 or 504 is tried again, up to
// four attempts in all unless WithAttempts says otherwise, after waits that
// double from about two seconds. Only the request is repeated. Once Vertex has
// started a streamed answer, an error ends the stream as it always did, so a
// retry never yields what was already yielded.
func Gemini(ctx context.Context, name, project, location string, credentials *auth.Credentials, options ...GeminiOption) (model.LLM, error) {
	if project == "" {
		return nil, errors.New("a Google Cloud project is required: the agents reach Gemini through Vertex AI")
	}
	config := geminiConfig{retries: defaultRetries}
	for _, option := range options {
		option(&config)
	}
	llm, err := gemini.NewModel(ctx, name, &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		Project:     project,
		Location:    location,
		Credentials: credentials,
		HTTPOptions: genai.HTTPOptions{RetryOptions: config.retries.options()},
	})
	if err != nil {
		return nil, err
	}
	return repairedHistory{llm}, nil
}

// GeminiClient returns a Gen AI client on the same Vertex AI project and
// location as the agents, for services that share their configuration such as
// explicit prompt caching. It authenticates as Gemini does. It does not retry:
// a cache that cannot be made is skipped and the request goes out uncached at
// once, and the next request tries again.
func GeminiClient(ctx context.Context, project, location string, credentials *auth.Credentials) (*genai.Client, error) {
	if project == "" {
		return nil, errors.New("a Google Cloud project is required: Vertex AI serves the models")
	}
	return genai.NewClient(ctx, &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		Project:     project,
		Location:    location,
		Credentials: credentials,
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
// When remembers is true Krude is given load_memory, which reads what it
// learned about the member in earlier threads. Pass it only where a memory
// service is actually on the runner: without one the tool is there to be
// called and fails every time.
func Root(router *modelrouter.Router, toolboxes []Toolbox, remembers bool) (agent.Agent, error) {
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
	builders := []func() (tool.Tool, error){catalog.listTool, catalog.loadTool, userDetailsTool, currentScreenTool, subagents.createTool, subagents.listTool, subagents.removeTool, helpers.listTool, helpers.spawnTool, loadArtifactsTool, saveArtifactTool}
	if remembers {
		builders = append(builders, loadMemoryTool)
	}
	tools, err := buildTools(builders...)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:                RootName,
		Description:         "Krude, a member's assistant.",
		Model:               router.Answer(modelrouter.RoleChat),
		InstructionProvider: instructionFrom(krudeInstruction, catalog, false),
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{recordRunSource},
		SubAgents:           subAgents,
		Tools:               tools,
		Toolsets:            []tool.Toolset{catalog.loaded(), subagents},
	})
}

// SingleAgentRoot returns Krude as one agent with tools, for
// AGENTS_PRIMARY_SINGLE_AGENT (docs/design/agent-runtime.md, R1). It has no
// sub-agents of any kind: no planner, researcher, helpers or sub-agents of
// its own, and nothing to start them with. In their place it has
//
//   - web_search, a search-only agent wrapped as a tool, on the research
//     role's model (webSearchTool says why it is an agent at all);
//   - plan, its own todo list for the thread, kept in session state;
//   - extra, when given: the tools it manages sub-agents of its own with
//     (internal/subagents' spawn_agent, message_agent, agent_status and
//     stop_agent). Those sub-agents are not ADK sub-agents of Krude's: each
//     runs in a session of its own, and Krude reads only their records.
//
// Everything else is Root's: the toolbox catalog, the member's details, the
// screen, artifacts, memory when remembers is true, and the run's source
// record. Root itself is unchanged, and is what runs while the flag is off.
func SingleAgentRoot(router *modelrouter.Router, toolboxes []Toolbox, remembers bool, extra ...tool.Tool) (agent.Agent, error) {
	if router == nil {
		return nil, errors.New("a model router is required")
	}
	catalog, err := newToolCatalog(toolboxes)
	if err != nil {
		return nil, err
	}
	webSearch := func() (tool.Tool, error) { return webSearchTool(router.For(modelrouter.RoleResearch)) }
	builders := []func() (tool.Tool, error){catalog.listTool, catalog.loadTool, userDetailsTool, currentScreenTool, webSearch, planTool(sessionPlanStore{}), loadArtifactsTool, saveArtifactTool}
	if remembers {
		builders = append(builders, loadMemoryTool)
	}
	tools, err := buildTools(builders...)
	if err != nil {
		return nil, err
	}
	tools = append(tools, extra...)
	return llmagent.New(llmagent.Config{
		Name:                RootName,
		Description:         "Krude, a member's assistant.",
		Model:               router.Answer(modelrouter.RoleChat),
		InstructionProvider: instructionWith(singleInstruction, catalog, false, len(extra) > 0),
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{recordRunSource},
		Tools:               tools,
		Toolsets:            []tool.Toolset{catalog.loaded()},
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
	return unattendedRoot(router, toolboxes, nil)
}

// RoomRoot returns Krude for its turn in a room its member's team shares,
// whether its member is reading the room or away: what steers it is what
// everyone in the room wrote, and everyone in the room reads what it says.
// So it answers from the room alone. It reaches none of the member's apps,
// knowledge, memory, files or requests; it has no web research, sub-agents
// or helpers; and it has only the toolboxes given, each loaded, with its
// skill, from its first model call, with nothing to list, load or look up.
// The caller gives it the room toolbox, which files what it cannot answer
// with its member.
//
// It answers to the same name as Root, since it takes turns in a thread of
// its member's.
func RoomRoot(router *modelrouter.Router, toolboxes []Toolbox) (agent.Agent, error) {
	if router == nil {
		return nil, errors.New("a model router is required")
	}
	catalog, err := newToolCatalog(toolboxes)
	if err != nil {
		return nil, err
	}
	catalog.preloaded = true
	return llmagent.New(llmagent.Config{
		Name:                RootName,
		Description:         "Krude, speaking for a member in a room their team shares.",
		Model:               router.Answer(modelrouter.RoleChat),
		InstructionProvider: instructionFrom(roomInstruction, catalog, false),
		Toolsets:            []tool.Toolset{catalog.loaded()},
	})
}

// delivery is how a run that delivers one interaction runs.
type delivery struct {
	// thinking is the thinking level asked of the model on each of the run's
	// calls; none leaves the model's own default.
	thinking genai.ThinkingLevel
}

// config is the generation config of the run's model calls: none, as for
// every other run, unless a thinking level is set.
func (delivery *delivery) config() *genai.GenerateContentConfig {
	if delivery == nil || delivery.thinking == "" {
		return nil
	}
	return &genai.GenerateContentConfig{ThinkingConfig: &genai.ThinkingConfig{ThinkingLevel: delivery.thinking}}
}

// DeliveringRoot returns UnattendedRoot for a run that delivers one
// interaction. What such a run may reach is decided before it starts, by
// what the member's agent has allowed, so every toolbox given is loaded, with
// its skill, from the run's first model call, and there is nothing to list or
// load: no list_tools or load_tools, and no model call spent on either. Nor
// is it offered get_user_details: its instruction carries every detail of the
// member it works for, the run is too short for them to scroll out of sight,
// and the message that wakes it names the member on the other side of the
// request, whom get_user_details never described.
//
// thinking, when set, is the thinking level asked of the model on each of
// the run's calls, as a Gemini 3 model takes it; left empty, the request
// carries no thinking config and the model thinks as it does by default.
func DeliveringRoot(router *modelrouter.Router, toolboxes []Toolbox, thinking genai.ThinkingLevel) (agent.Agent, error) {
	return unattendedRoot(router, toolboxes, &delivery{thinking: thinking})
}

func unattendedRoot(router *modelrouter.Router, toolboxes []Toolbox, delivering *delivery) (agent.Agent, error) {
	if router == nil {
		return nil, errors.New("a model router is required")
	}
	catalog, err := newToolCatalog(toolboxes)
	if err != nil {
		return nil, err
	}
	builders := []func() (tool.Tool, error){catalog.listTool, catalog.loadTool, userDetailsTool, loadArtifactsTool}
	if delivering != nil {
		// A delivery works from its request and what its reach loads, and
		// no more: load_artifacts would list the member's own files, the
		// user: ones every thread of theirs shares, into a run another
		// member's words steer.
		catalog.preloaded = true
		builders = nil
	}
	tools, err := buildTools(builders...)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:                  RootName,
		Description:           "Krude, reporting to a member who is away.",
		Model:                 router.Answer(modelrouter.RoleChat),
		GenerateContentConfig: delivering.config(),
		AfterToolCallbacks:    delivering.afterTools(),
		BeforeToolCallbacks:   []llmagent.BeforeToolCallback{recordRunSource},
		InstructionProvider:   instructionFrom(unattendedInstruction, catalog, delivering != nil),
		Tools:                 tools,
		Toolsets:              []tool.Toolset{catalog.loaded()},
	})
}

func loadArtifactsTool() (tool.Tool, error) {
	return loadartifactstool.New(), nil
}

// loadMemoryTool lets Krude look up what it learned about the member in their
// earlier threads. It is a tool rather than preload_memory, which searches on
// every model request and appends what it finds to the system instruction:
// that is a memory read per model step, and it would rewrite the stable
// prefix the prompt cache exists to hold (see Prompt caching in the README).
func loadMemoryTool() (tool.Tool, error) {
	return loadmemorytool.New(), nil
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
// is loaded brings its usage instructions with it. delivering is whether the
// run delivers one interaction (DeliveringRoot).
func instructionFrom(instruction *template.Template, catalog *toolCatalog, delivering bool) func(agent.ReadonlyContext) (string, error) {
	return instructionWith(instruction, catalog, delivering, false)
}

// instructionWith is instructionFrom for an instruction that also says how
// to work with sub-agents, when spawns is true: single-agent Krude's, given
// the tools that manage them.
func instructionWith(instruction *template.Template, catalog *toolCatalog, delivering, spawns bool) func(agent.ReadonlyContext) (string, error) {
	return func(ctx agent.ReadonlyContext) (string, error) {
		state := ctx.ReadonlyState()
		var rendered strings.Builder
		err := instruction.Execute(&rendered, struct {
			UserName, UserAbout, OrgName, Member, AgentName string
			// Delivering is a run that delivers one interaction: every
			// toolbox it may use is loaded already, and it has no
			// get_user_details.
			Delivering bool
			// Subagents is single-agent Krude given spawn_agent and the
			// tools beside it.
			Subagents bool
		}{
			UserName:   stateText(state, UserNameKey, "the member you are talking with"),
			UserAbout:  stateText(state, UserAboutKey, ""),
			OrgName:    stateText(state, OrgNameKey, "their organization"),
			Member:     memberDetails(state),
			AgentName:  stateText(state, AgentNameKey, "Krude"),
			Delivering: delivering,
			Subagents:  spawns,
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
	for _, name := range catalog.loadedNames(state) {
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

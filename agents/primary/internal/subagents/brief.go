package subagents

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"
)

// Bounds on what a brief, a message and a result hold.
const (
	maxObjectiveRunes = 2000
	maxOutputRunes    = 2000
	maxContextRunes   = 16000
	maxMessageRunes   = 8000
	maxQuestionRunes  = 2000
	maxProgressRunes  = 200
	maxErrorRunes     = 500
	// maxResultRunes bounds the result Krude reads; a longer one is kept
	// whole, up to maxFullResultRunes, and saved as an artifact of the
	// thread the next time Krude reads it.
	maxResultRunes     = 4000
	resultPreviewRunes = 3500
	maxFullResultRunes = 200_000

	maxFiles      = 5
	maxFileBytes  = 1 << 20
	maxFilesBytes = 4 << 20
)

// Brief is spawn_agent's arguments (docs/design/agent-runtime.md, 2).
type Brief struct {
	Objective string   `json:"objective" jsonschema:"What to find out or produce, in one or two sentences."`
	Output    string   `json:"output" jsonschema:"What to return, and its shape: for example a table of vendor, price and source, or five bullet points with a link each."`
	Context   string   `json:"context,omitempty" jsonschema:"Everything the sub-agent needs to know. It sees nothing else: not this conversation, not the member, not what you already found."`
	Tools     []string `json:"tools,omitempty" jsonschema:"The tools it may use, from: knowledge (the organization's documents), apps_read (read the member's connected apps), web_search (the live web). Give only what the objective needs; all of them only read, and none can send, post or change anything."`
	Files     []string `json:"files,omitempty" jsonschema:"Names of this conversation's files (artifacts) to hand it, read-only, at most five."`
	Budget    Budget   `json:"budget,omitempty" jsonschema:"How much it may spend on this assignment: model_calls (default 30, at most 100) and minutes (default 5, at most 15)."`
	Wait      bool     `json:"wait,omitempty" jsonschema:"true waits until it finishes, fails or asks you a question, and returns that. false returns its id at once, so several can work at the same time; read them later with agent_status."`
}

// Budget is how much one assignment of a sub-agent's may spend.
type Budget struct {
	ModelCalls int `json:"model_calls,omitempty" jsonschema:"Model calls it may make, default 30, at most 100."`
	Minutes    int `json:"minutes,omitempty" jsonschema:"Minutes it may work, default 5, at most 15."`
}

// brief is a Brief checked and bounded, with what was changed to fit.
type brief struct {
	objective, output, context string
	tools, files               []string
	budget                     Budget
	notes                      []string
}

// checkBrief refuses a brief that cannot be run as sent and bounds the rest.
// available are the tool families this deployment can give.
func checkBrief(in Brief, available []string) (brief, error) {
	checked := brief{
		objective: strings.TrimSpace(in.Objective),
		output:    strings.TrimSpace(in.Output),
		context:   strings.TrimSpace(in.Context),
	}
	switch {
	case checked.objective == "":
		return brief{}, errors.New("a brief needs an objective")
	case utf8.RuneCountInString(checked.objective) > maxObjectiveRunes:
		return brief{}, fmt.Errorf("the objective is longer than %d characters", maxObjectiveRunes)
	case checked.output == "":
		return brief{}, errors.New("a brief needs an output: what to return, and its shape")
	case utf8.RuneCountInString(checked.output) > maxOutputRunes:
		return brief{}, fmt.Errorf("the output is longer than %d characters", maxOutputRunes)
	case utf8.RuneCountInString(checked.context) > maxContextRunes:
		return brief{}, fmt.Errorf("the context is longer than %d characters", maxContextRunes)
	}
	tools, err := checkTools(in.Tools, available)
	if err != nil {
		return brief{}, err
	}
	checked.tools = tools
	for _, raw := range in.Files {
		name := strings.TrimSpace(raw)
		if name == "" || slices.Contains(checked.files, name) {
			continue
		}
		checked.files = append(checked.files, name)
	}
	if len(checked.files) > maxFiles {
		return brief{}, fmt.Errorf("a brief hands over at most %d files", maxFiles)
	}
	checked.budget, checked.notes = checkBudget(in.Budget)
	return checked, nil
}

// checkTools keeps the named families, in Families' order, refusing a name
// that is not one, or one this deployment cannot give.
func checkTools(names []string, available []string) ([]string, error) {
	wanted := map[string]bool{}
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if !slices.Contains(Families, name) {
			return nil, fmt.Errorf("%q is not a tool a sub-agent can have; choose from %s", raw, strings.Join(Families, ", "))
		}
		if !slices.Contains(available, name) {
			return nil, fmt.Errorf("%q is not available here; choose from %s", name, strings.Join(availableFamilies(available), ", "))
		}
		wanted[name] = true
	}
	tools := []string{}
	for _, family := range Families {
		if wanted[family] {
			tools = append(tools, family)
		}
	}
	return tools, nil
}

func availableFamilies(available []string) []string {
	families := []string{}
	for _, family := range Families {
		if slices.Contains(available, family) {
			families = append(families, family)
		}
	}
	if len(families) == 0 {
		return []string{"none: brief it to reason from the context alone"}
	}
	return families
}

func checkBudget(in Budget) (Budget, []string) {
	var notes []string
	out := in
	if out.ModelCalls <= 0 {
		out.ModelCalls = DefaultModelCalls
	}
	if out.ModelCalls > MaxModelCalls {
		notes = append(notes, fmt.Sprintf("model_calls was lowered to the most a sub-agent may have, %d", MaxModelCalls))
		out.ModelCalls = MaxModelCalls
	}
	if out.Minutes <= 0 {
		out.Minutes = DefaultMinutes
	}
	if out.Minutes > MaxMinutes {
		notes = append(notes, fmt.Sprintf("minutes was lowered to the most a sub-agent may have, %d", MaxMinutes))
		out.Minutes = MaxMinutes
	}
	return out, notes
}

// briefContent is the sub-agent's first message: its brief, and the files
// handed to it after it.
func briefContent(checked brief, files []*genai.Part) *genai.Content {
	var text strings.Builder
	text.WriteString("Your brief from Krude.\n\nObjective:\n")
	text.WriteString(checked.objective)
	text.WriteString("\n\nReturn:\n")
	text.WriteString(checked.output)
	if checked.context != "" {
		text.WriteString("\n\nContext:\n")
		text.WriteString(checked.context)
	}
	if len(checked.files) > 0 {
		text.WriteString("\n\nFiles handed to you, read-only, below: ")
		text.WriteString(strings.Join(checked.files, ", "))
	}
	parts := []*genai.Part{genai.NewPartFromText(text.String())}
	parts = append(parts, files...)
	return &genai.Content{Role: genai.RoleUser, Parts: parts}
}

// messageContent is what Krude sent a sub-agent after its brief: answers to
// its question, or follow-ups, in the order sent.
func messageContent(messages []string) *genai.Content {
	return genai.NewContentFromText("Krude's reply:\n\n"+strings.Join(messages, "\n\n"), genai.RoleUser)
}

// View is what Krude is shown of a sub-agent: its record without its
// transcript or anything only bookkeeping needs.
type View struct {
	ID             string     `json:"id"`
	State          string     `json:"state"`
	Objective      string     `json:"objective"`
	Tools          []string   `json:"tools"`
	Question       string     `json:"question,omitempty"`
	Progress       string     `json:"progress,omitempty"`
	Result         string     `json:"result,omitempty"`
	ResultArtifact string     `json:"result_artifact,omitempty"`
	Error          string     `json:"error,omitempty"`
	ModelCalls     int        `json:"model_calls"`
	Budget         Budget     `json:"budget"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Note           string     `json:"note"`
}

func viewOf(record Record, now time.Time) View {
	state := record.stateAt(now)
	view := View{
		ID: record.ID, State: state, Objective: record.Objective, Tools: nonNil(record.Tools),
		ModelCalls: record.ModelCalls,
		Budget:     Budget{ModelCalls: record.BudgetModelCalls, Minutes: record.BudgetMinutes},
	}
	if state != StateExpired {
		expires := record.ExpiresAt.UTC()
		view.ExpiresAt = &expires
	}
	switch state {
	case StateRunning:
		view.Progress = record.Progress
		view.Note = "It is working. Read it again with agent_status, or wait for it with message_agent and wait true."
	case StateWaiting:
		view.Question = record.Question
		view.Note = "It stopped to ask you this. Answer it with message_agent; nobody else will. Ask the member only if you cannot answer it yourself."
	case StateDone:
		view.Result, view.ResultArtifact = record.Result, record.ResultArtifact
		view.Note = "It finished. message_agent asks it a follow-up, with its work so far. You own whatever is done with this result: check it before you rely on it."
		if record.ResultArtifact != "" {
			view.Note = "It finished; its full result is the file named in result_artifact, which load_artifacts reads. " + view.Note
		}
	case StateFailed:
		view.Error = record.Error
		view.Note = "It failed and cannot be resumed. Start a new one with a better brief only if you still need this."
	case StateStopped:
		view.Error = record.Error
		view.Note = "It was stopped and cannot be resumed."
	case StateExpired:
		view.Note = "It expired, with its work, after a long time without activity. Start a new one if you still need it."
	}
	return view
}

// bounded collapses nothing but trims text to at most limit runes.
func bounded(text string, limit int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

// oneLine collapses text to one line of at most limit runes.
func oneLine(text string, limit int) string {
	return bounded(strings.Join(strings.Fields(text), " "), limit)
}

package subagents

import (
	"context"
	"errors"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

func (s *MemoryStore) record(t *testing.T, id string) Record {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		t.Fatalf("no record %s", id)
	}
	return record
}

// mapState is session state over a map.
type mapState map[string]any

func (s mapState) Get(key string) (any, error) {
	value, ok := s[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (s mapState) All() iter.Seq2[string, any] { return maps.All(map[string]any(s)) }

// fakeArtifacts is a thread's files.
type fakeArtifacts struct {
	mu    sync.Mutex
	parts map[string]*genai.Part
	saved []string
}

func (a *fakeArtifacts) Save(_ context.Context, name string, data *genai.Part) (*artifact.SaveResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.parts == nil {
		a.parts = map[string]*genai.Part{}
	}
	a.parts[name] = data
	a.saved = append(a.saved, name)
	return &artifact.SaveResponse{Version: 1}, nil
}

func (a *fakeArtifacts) List(context.Context) (*artifact.ListResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &artifact.ListResponse{FileNames: slices.Sorted(maps.Keys(a.parts))}, nil
}

func (a *fakeArtifacts) Load(_ context.Context, name string) (*artifact.LoadResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	part, ok := a.parts[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return &artifact.LoadResponse{Part: part}, nil
}

func (a *fakeArtifacts) LoadVersion(ctx context.Context, name string, _ int) (*artifact.LoadResponse, error) {
	return a.Load(ctx, name)
}

// toolCtx is the context one of Krude's tool calls has, in thread_1 of
// member_1 in org_1.
type toolCtx struct {
	agent.StrictContextMock
	state     mapState
	artifacts *fakeArtifacts
	thread    string
}

func (c *toolCtx) UserID() string                       { return "member_1" }
func (c *toolCtx) AppName() string                      { return "krude" }
func (c *toolCtx) SessionID() string                    { return c.thread }
func (c *toolCtx) ReadonlyState() session.ReadonlyState { return c.state }
func (c *toolCtx) Artifacts() agent.Artifacts           { return c.artifacts }

var testThread = Thread{OrganizationID: "org_1", UserID: "member_1", App: "krude", ID: "thread_1"}

// respondFunc answers one model call of a sub-agent's.
type respondFunc func(ctx context.Context, request *model.LLMRequest) (*genai.Content, error)

// fakeModel answers with respond, and reports a fixed usage per call.
type fakeModel struct {
	respond respondFunc
	mu      sync.Mutex
	calls   []*model.LLMRequest
}

func (m *fakeModel) Name() string { return "fake-lite" }

func (m *fakeModel) GenerateContent(ctx context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.mu.Lock()
		m.calls = append(m.calls, request)
		respond := m.respond
		m.mu.Unlock()
		content, err := respond(ctx, request)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&model.LLMResponse{Content: content, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 10, CandidatesTokenCount: 5, TotalTokenCount: 15,
		}}, nil)
	}
}

func (m *fakeModel) requests() []*model.LLMRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

func answer(text string) respondFunc {
	return func(context.Context, *model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText(text, genai.RoleModel), nil
	}
}

func call(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call_" + name, Name: name, Args: args}}}}
}

// lastUserText is the newest text a sub-agent was sent, by Krude or a tool.
func lastUserText(request *model.LLMRequest) string {
	for i := len(request.Contents) - 1; i >= 0; i-- {
		content := request.Contents[i]
		if content.Role != genai.RoleUser {
			continue
		}
		var text strings.Builder
		for _, part := range content.Parts {
			text.WriteString(part.Text)
		}
		if text.Len() > 0 {
			return text.String()
		}
	}
	return ""
}

func hasFunctionResponse(request *model.LLMRequest, name string) bool {
	last := request.Contents[len(request.Contents)-1]
	for _, part := range last.Parts {
		if part.FunctionResponse != nil && part.FunctionResponse.Name == name {
			return true
		}
	}
	return false
}

// fakeBuilder builds a sub-agent on its model, with Worker's tools and
// extra, and records what it was asked for.
type fakeBuilder struct {
	llm      model.LLM
	families []string
	extra    []tool.Tool
	// search, when set, answers web_search for a worker allowed it.
	search model.LLM
	mu     sync.Mutex
	built  []Worker
}

func (b *fakeBuilder) Families() []string { return b.families }

func (b *fakeBuilder) Build(worker Worker) (agent.Agent, error) {
	b.mu.Lock()
	b.built = append(b.built, worker)
	b.mu.Unlock()
	tools := append(slices.Clone(worker.Own), b.extra...)
	if b.search != nil && slices.Contains(worker.Tools, ToolWebSearch) {
		// web_search as the real builder makes it: an agent of its own, on
		// a model the worker's Meter wraps.
		searcher := worker.Meter(b.search)
		search, err := functiontool.New(functiontool.Config{Name: ToolWebSearch, Description: "Searches the web."}, func(ctx agent.Context, _ struct{}) (map[string]any, error) {
			for response, err := range searcher.GenerateContent(ctx, &model.LLMRequest{}, false) {
				if err != nil {
					return nil, err
				}
				return map[string]any{"result": response.Content.Parts[0].Text}, nil
			}
			return nil, errors.New("no answer")
		})
		if err != nil {
			return nil, err
		}
		tools = append(tools, search)
	}
	return llmagent.New(llmagent.Config{
		Name:                 WorkerName,
		Model:                b.llm,
		InstructionProvider:  func(agent.ReadonlyContext) (string, error) { return worker.Instruction, nil },
		Tools:                tools,
		BeforeModelCallbacks: worker.BeforeModel,
		AfterModelCallbacks:  worker.AfterModel,
		BeforeToolCallbacks:  worker.BeforeTool,
		AfterToolCallbacks:   worker.AfterTool,
	})
}

type testManager struct {
	*Manager
	store    *MemoryStore
	sessions session.Service
	model    *fakeModel
	builder  *fakeBuilder
	files    *fakeArtifacts
	clock    *clock
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

func newTestManager(t *testing.T, respond respondFunc, configure ...func(*Config)) *testManager {
	t.Helper()
	llm := &fakeModel{respond: respond}
	store := NewMemoryStore()
	sessions := session.InMemoryService()
	builder := &fakeBuilder{llm: llm, families: slices.Clone(Families)}
	config := Config{Store: store, Sessions: sessions, Builder: builder}
	for _, option := range configure {
		option(&config)
	}
	manager, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	clock := &clock{now: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	manager.now = clock.Now
	return &testManager{Manager: manager, store: store, sessions: sessions, model: llm, builder: builder, files: &fakeArtifacts{}, clock: clock}
}

// run opens a run's scope on ctx and returns a tool context in it, and the
// run's end.
func (m *testManager) run(t *testing.T, ctx context.Context, runID string) (*toolCtx, func()) {
	t.Helper()
	scoped, end := m.Begin(ctx, runID)
	t.Cleanup(end)
	return &toolCtx{
		StrictContextMock: agent.NewStrictContextMock(scoped),
		state:             mapState{"organization_id": "org_1", "managed_agent_id": "agent_1"},
		artifacts:         m.files,
		thread:            "thread_1",
	}, end
}

func testBrief(wait bool, tools ...string) Brief {
	return Brief{Objective: "Find the price of the widget.", Output: "One line with the price and its source.", Context: "The member sells widgets.", Tools: tools, Wait: wait}
}

// eventually waits for done to hold.
func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (m *testManager) idle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.running) == 0
}

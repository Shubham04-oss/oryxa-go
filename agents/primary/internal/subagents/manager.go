package subagents

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"
)

// workerInstruction is every sub-agent's instruction. Its brief is its first
// message, so the instruction is the same for each, and stays the same when
// it is resumed.
const workerInstruction = `You are a sub-agent that Krude, a member's assistant, started for one assignment. Your brief is your first message, and it is all you know: you do not see the conversation, the member, or anything Krude found. Work on your own with the tools you have, and give as your final answer what the brief's Return asks for, in that shape.

- If something only Krude or the member could tell you blocks the work, call ask_parent with one clear question instead of guessing, and stop. Krude's answer comes as your next message. Never ask about anything you can find out yourself.
- You read and you compute. You never send, post, publish or change anything outside your sandbox, and you have no tool that could. If the work needs a change made, say in your result what should change, and Krude will decide.
- Your model calls and minutes are limited. Use only the tools the objective needs, and answer as soon as you can.
- Say where each fact came from (a document, an app record, a web page) so Krude can check it, and say plainly what you could not find.
- A later message from Krude may ask a follow-up. Answer it from what you already found, and do more work only where it needs it.`

// The reasons a sub-agent stops working that are not its own doing.
var (
	errParentEnded = errors.New("the run that set it working ended")
	errStopped     = errors.New("Krude stopped it")
	errBudgetCalls = errors.New("it used its whole budget of model calls")
	errBudgetTime  = errors.New("it ran past its budget of minutes")
	errNoScope     = errors.New("sub-agents work only inside one of your runs")
	errRunEnding   = errors.New("this run is ending, so it cannot start a sub-agent")
)

// stopWait bounds how long ending a run, or stop_agent, waits for a
// sub-agent to stop and record that it did.
const stopWait = 30 * time.Second

// writeTimeout bounds one write of a record a sub-agent makes after the
// context it worked on is gone.
const writeTimeout = 10 * time.Second

// Config is what a Manager runs with.
type Config struct {
	Store Store
	// Sessions keeps every sub-agent's transcript, under AppName. It should
	// not report session lifecycle events: a sub-agent's session is not a
	// thread.
	Sessions session.Service
	Builder  Builder
	// PerRun and PerProcess bound the sub-agents working at once in one run
	// and in the process; TTL is how long one is kept after its last
	// activity. Zero takes the defaults.
	PerRun     int
	PerProcess int
	TTL        time.Duration
	Logger     *slog.Logger
}

// Manager runs the process's sub-agents: Krude's tools for them, and the
// scope of each run that ends the ones it set working.
type Manager struct {
	store      Store
	sessions   session.Service
	builder    Builder
	perRun     int
	perProcess int
	ttl        time.Duration
	logger     *slog.Logger
	tracer     trace.Tracer
	now        func() time.Time
	// minute is how long a budget's minute lasts: a minute, but for tests.
	minute time.Duration

	// mu guards running, every scope's children and closed, and every
	// child's pending, exiting and question.
	mu      sync.Mutex
	running map[string]*child
	// records serializes the tools' read-modify-write of a record no
	// goroutine of this process is working on.
	records sync.Mutex
}

// New returns the manager.
func New(config Config) (*Manager, error) {
	switch {
	case config.Store == nil:
		return nil, errors.New("subagents: a store is required")
	case config.Sessions == nil:
		return nil, errors.New("subagents: a session service is required")
	case config.Builder == nil:
		return nil, errors.New("subagents: a builder is required")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Manager{
		store:      config.Store,
		sessions:   config.Sessions,
		builder:    config.Builder,
		perRun:     cmp.Or(config.PerRun, DefaultPerRun),
		perProcess: cmp.Or(config.PerProcess, DefaultPerProcess),
		ttl:        cmp.Or(config.TTL, DefaultTTL),
		logger:     logger,
		tracer:     otel.Tracer("github.com/Shubham04-oss/oryxa-go/agents/primary/internal/subagents"),
		now:        time.Now,
		minute:     time.Minute,
		running:    map[string]*child{},
	}, nil
}

// runScope is one run of Krude's: the sub-agents it set working, which stop
// when it ends.
type runScope struct {
	runID    string
	ctx      context.Context
	cancel   context.CancelCauseFunc
	once     sync.Once
	closed   bool
	children map[string]*child
}

type scopeKey struct{}

func scopeFrom(ctx context.Context) *runScope {
	scope, _ := ctx.Value(scopeKey{}).(*runScope)
	return scope
}

// Begin opens a run's scope: the context it returns is the one the run's
// tools start sub-agents on, and end stops every sub-agent the run set
// working that is still at it, waiting (a bounded while) until each has
// recorded that it stopped, so that their model calls are on the run's
// usage before the run is priced. A sub-agent also stops as soon as ctx is
// done. end may be called more than once.
func (m *Manager) Begin(ctx context.Context, runID string) (context.Context, func()) {
	scopeContext, cancel := context.WithCancelCause(ctx)
	scope := &runScope{runID: runID, ctx: scopeContext, cancel: cancel, children: map[string]*child{}}
	return context.WithValue(ctx, scopeKey{}, scope), func() { m.endRun(scope) }
}

func (m *Manager) endRun(scope *runScope) {
	scope.once.Do(func() {
		m.mu.Lock()
		scope.closed = true
		children := make([]*child, 0, len(scope.children))
		for _, c := range scope.children {
			children = append(children, c)
		}
		m.mu.Unlock()
		scope.cancel(errParentEnded)
		deadline := time.NewTimer(stopWait)
		defer deadline.Stop()
		for _, c := range children {
			select {
			case <-c.finished:
			case <-deadline.C:
				m.logger.Warn("a sub-agent did not stop within the wait when its run ended", "run_id", scope.runID, "subagent_id", c.id)
				return
			}
		}
	})
}

// child is a sub-agent this process is working on: from the moment a tool
// reserves it to the moment its goroutine has recorded how its work ended.
type child struct {
	id    string
	scope *runScope
	// ctx is what it works on: the values of the tool call that set it
	// working, cancelled by its scope's end or stop_agent, never by the end
	// of that tool call.
	ctx      context.Context
	cancel   context.CancelCauseFunc
	stopRun  func() bool
	finished chan struct{}

	// Guarded by Manager.mu.
	pending  []string
	exiting  bool
	question string

	// calls is this assignment's model calls, and before the ones made in
	// earlier assignments.
	calls  atomic.Int64
	before int
}

func newChild(base context.Context, id string, scope *runScope) *child {
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(base))
	c := &child{id: id, scope: scope, ctx: ctx, cancel: cancel, finished: make(chan struct{})}
	c.stopRun = context.AfterFunc(scope.ctx, func() { cancel(errParentEnded) })
	return c
}

// discard undoes newChild for one that never started.
func (c *child) discard() {
	c.stopRun()
	c.cancel(nil)
	close(c.finished)
}

// reserve takes a working slot for c in its scope and in the process.
func (m *Manager) reserve(c *child) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case c.scope.closed || c.scope.ctx.Err() != nil:
		return errRunEnding
	case len(c.scope.children) >= m.perRun:
		return fmt.Errorf("%d sub-agents are already working in this run, the most there may be; wait for one, or stop one you no longer need", m.perRun)
	case len(m.running) >= m.perProcess:
		return errors.New("too many sub-agents are working right now; wait for one of yours, or do this yourself")
	}
	if _, taken := m.running[c.id]; taken {
		return fmt.Errorf("sub-agent %s is already working", c.id)
	}
	m.running[c.id] = c
	c.scope.children[c.id] = c
	return nil
}

func (m *Manager) release(c *child) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running[c.id] == c {
		delete(m.running, c.id)
	}
	delete(c.scope.children, c.id)
}

// active is the child this process is working on with id, if any, and
// whether it is past taking more messages.
func (m *Manager) active(id string) (*child, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.running[id]
	if c == nil {
		return nil, false
	}
	return c, c.exiting
}

// start sets c working on content in a goroutine of its own.
func (m *Manager) start(c *child, record Record, content *genai.Content) {
	go func() {
		defer close(c.finished)
		defer c.cancel(nil)
		defer c.stopRun()
		defer m.release(c)
		m.work(c, record, content)
	}()
}

// work runs assignments until there is no message waiting for the next one,
// then records how the last ended.
func (m *Manager) work(c *child, record Record, content *genai.Content) {
	for {
		ended := m.turn(c, &record, content)
		m.mu.Lock()
		if (ended.State == StateDone || ended.State == StateWaiting) && len(c.pending) > 0 && c.ctx.Err() == nil {
			// Krude wrote while it worked: that is its next assignment,
			// with a fresh budget, as any assignment has.
			content = messageContent(c.pending)
			c.pending, c.question = nil, ""
			m.mu.Unlock()
			record = ended
			record.State, record.Question, record.Result, record.FullResult = StateRunning, "", "", ""
			record.Progress = "reading your reply"
			continue
		}
		c.exiting = true
		m.mu.Unlock()
		record = ended
		writeContext, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), writeTimeout)
		if err := m.store.Update(writeContext, record, StateRunning); err != nil {
			m.logger.ErrorContext(writeContext, "record how a sub-agent's work ended", "subagent_id", record.ID, "error", err)
		}
		cancel()
		m.logState(c.ctx, record)
		return
	}
}

// turn runs one assignment, and returns the record as it ended: done,
// waiting on Krude, failed or stopped, with what it spent added.
func (m *Manager) turn(c *child, record *Record, content *genai.Content) Record {
	budget := time.Duration(record.BudgetMinutes) * m.minute
	ctx, cancel := context.WithTimeoutCause(c.ctx, budget, errBudgetTime)
	defer cancel()
	ctx, span := m.tracer.Start(ctx, "subagent.turn", trace.WithAttributes(
		attribute.String("oryxa.subagent.id", record.ID),
		attribute.String("oryxa.thread.id", record.Thread.ID),
		attribute.String("oryxa.run.id", c.scope.runID),
	))
	defer span.End()

	c.calls.Store(0)
	c.before = record.ModelCalls
	spent := usage.NewCounter()
	final, failure := m.drive(ctx, c, record, spent, content)

	ended := *record
	ended.ModelCalls += int(min(c.calls.Load(), int64(record.BudgetModelCalls)))
	total := spent.Total()
	ended.InputTokens += int64(total.InputTokens)
	ended.OutputTokens += int64(total.OutputTokens)
	ended.CachedTokens += int64(total.CachedTokens)
	ended.TotalTokens += int64(total.TotalTokens)
	now := m.now()
	ended.UpdatedAt, ended.ExpiresAt = now, now.Add(m.ttl)
	ended.Progress, ended.Question, ended.Error = "", "", ""
	ended.Result, ended.FullResult, ended.ResultArtifact = "", "", ""

	m.mu.Lock()
	question := c.question
	m.mu.Unlock()
	switch {
	case question != "" && failure == nil:
		ended.State, ended.Question = StateWaiting, question
	case failure == nil && strings.TrimSpace(final) != "":
		ended.State = StateDone
		ended.Result, ended.FullResult = condensed(final)
	case c.ctx.Err() != nil:
		ended.State, ended.Error = StateStopped, oneLine(context.Cause(c.ctx).Error(), maxErrorRunes)
	case errors.Is(failure, errBudgetCalls) || c.calls.Load() > int64(record.BudgetModelCalls):
		ended.State = StateFailed
		ended.Error = fmt.Sprintf("%s (%d) before it finished", errBudgetCalls, record.BudgetModelCalls)
	case ctx.Err() != nil && errors.Is(context.Cause(ctx), errBudgetTime):
		ended.State = StateFailed
		ended.Error = fmt.Sprintf("%s (%d) before it finished", errBudgetTime, record.BudgetMinutes)
	case failure != nil:
		ended.State, ended.Error = StateFailed, oneLine("it ran into an error: "+failure.Error(), maxErrorRunes)
	default:
		ended.State, ended.Error = StateFailed, "it finished without returning anything"
	}
	span.SetAttributes(attribute.String("oryxa.subagent.state", ended.State), attribute.Int("oryxa.subagent.model_calls", ended.ModelCalls))
	if ended.State == StateFailed {
		span.SetStatus(codes.Error, ended.Error)
	}
	return ended
}

// drive runs the sub-agent's runner on content and returns its final text.
func (m *Manager) drive(ctx context.Context, c *child, record *Record, spent *usage.Counter, content *genai.Content) (string, error) {
	ask, err := askTool(m, c)
	if err != nil {
		return "", err
	}
	worker, err := m.builder.Build(Worker{
		Instruction: workerInstruction,
		Tools:       record.Tools,
		Thread:      record.Thread,
		Own:         []tool.Tool{ask},
		BeforeModel: []llmagent.BeforeModelCallback{countCall(c, record.BudgetModelCalls)},
		AfterModel:  []llmagent.AfterModelCallback{countUsage(spent)},
		AfterTool:   []llmagent.AfterToolCallback{m.noteProgress(c, record.ID)},
	})
	if err != nil {
		return "", fmt.Errorf("build the sub-agent: %w", err)
	}
	if worker.Name() != WorkerName {
		return "", fmt.Errorf("the sub-agent is named %q, not %q", worker.Name(), WorkerName)
	}
	work, err := runner.New(runner.Config{AppName: AppName, Agent: worker, SessionService: m.sessions})
	if err != nil {
		return "", fmt.Errorf("build the sub-agent's runner: %w", err)
	}
	var final string
	for event, err := range work.Run(ctx, record.Thread.UserID, sessionID(*record), content, agent.RunConfig{}) {
		if err != nil {
			return "", err
		}
		if event == nil || event.Partial || event.Author != WorkerName || event.Content == nil {
			continue
		}
		if text := contentText(event.Content); text != "" {
			final = text
		}
	}
	return final, nil
}

// countCall counts the sub-agent's model calls, and refuses the one past its
// budget: ADK v2.6 has no cap on model calls of its own.
func countCall(c *child, budget int) llmagent.BeforeModelCallback {
	return func(agent.Context, *model.LLMRequest) (*model.LLMResponse, error) {
		if c.calls.Add(1) > int64(budget) {
			return nil, errBudgetCalls
		}
		return nil, nil
	}
}

// countUsage adds up what the sub-agent's own model calls spent. The run's
// counter, on the context, prices the same calls into the run (usage.Counted
// on the router's models); this is the sub-agent's own total.
func countUsage(spent *usage.Counter) llmagent.AfterModelCallback {
	return func(_ agent.Context, response *model.LLMResponse, responseErr error) (*model.LLMResponse, error) {
		if responseErr == nil && response != nil && !response.Partial {
			spent.Add(WorkerName, response.UsageMetadata)
		}
		return nil, nil
	}
}

// noteProgress keeps the progress note Krude reads current: the last tool
// the sub-agent used, and how many model calls it has made.
func (m *Manager) noteProgress(c *child, id string) llmagent.AfterToolCallback {
	return func(ctx agent.Context, called tool.Tool, _, _ map[string]any, _ error) (map[string]any, error) {
		if called == nil || called.Name() == AskToolName {
			return nil, nil
		}
		calls := c.before + int(c.calls.Load())
		note := oneLine(fmt.Sprintf("%d model calls so far; last used %s", calls, called.Name()), maxProgressRunes)
		writeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		defer cancel()
		if err := m.store.SetProgress(writeContext, id, note, calls, m.now().Add(m.ttl)); err != nil {
			m.logger.WarnContext(ctx, "note a sub-agent's progress", "subagent_id", id, "error", err)
		}
		return nil, nil
	}
}

// condensed is the result Krude reads, and the whole of it when that is
// longer, to be saved as an artifact of the thread.
func condensed(text string) (string, string) {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= maxResultRunes {
		return text, ""
	}
	full := text
	if len(runes) > maxFullResultRunes {
		full = string(runes[:maxFullResultRunes]) + "\n\n[cut off: the result was longer than this]"
	}
	preview := string(runes[:resultPreviewRunes]) + "\n\n[…the result goes on; the whole of it is in result_artifact]"
	return preview, full
}

func contentText(content *genai.Content) string {
	var text strings.Builder
	for _, part := range content.Parts {
		if part != nil && !part.Thought {
			text.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(text.String())
}

func sessionID(record Record) string { return record.Thread.ID + "/" + record.ID }

func newID() (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create a sub-agent id: %w", err)
	}
	return "sa_" + base64.RawURLEncoding.EncodeToString(random), nil
}

// logState is the log line each state change makes: ids, state and what it
// spent, never its brief or its result.
func (m *Manager) logState(ctx context.Context, record Record) {
	m.logger.InfoContext(ctx, "sub-agent "+record.State,
		"subagent_id", record.ID, "thread_id", record.Thread.ID, "run_id", record.ParentRunID,
		"state", record.State, "model_calls", record.ModelCalls, "total_tokens", record.TotalTokens,
		"tools", strings.Join(record.Tools, ","), "reason", record.Error)
}

package subagents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// identityKeys are the thread's state a sub-agent's session starts with:
// who it acts for, so its toolboxes act as the member (memberauth), and the
// edge places its sandbox calls in the member's organization. They are
// literals, mirrored from internal/agents and memberauth, as edgeclient's
// are.
var identityKeys = []string{"organization_id", "managed_agent_id", "managed_agent_kind", "managed_agent_grant"}

// maxListed bounds how many sub-agents agent_status lists, newest first.
const maxListed = 32

// Tools are Krude's four: spawn_agent, message_agent, agent_status and
// stop_agent.
func (m *Manager) Tools() ([]tool.Tool, error) {
	spawn, err := functiontool.New(functiontool.Config{
		Name:        SpawnToolName,
		Description: "Starts a sub-agent on one assignment from a complete brief, and returns its id, or with wait true its result. It sees only the brief and the files you hand it, works with the read-only tools you allow it, and never sends, posts or changes anything. Start several in one turn for independent questions; they work at the same time.",
	}, func(ctx agent.Context, args Brief) (View, error) {
		return m.spawn(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	message, err := functiontool.New(functiontool.Config{
		Name:        MessageToolName,
		Description: "Sends one of this conversation's sub-agents a message: the answer to the question it asked, a correction, or a follow-up to a finished one, which picks up with its work so far. With wait true, returns once it has finished, failed or asked again.",
	}, func(ctx agent.Context, args messageArgs) (View, error) {
		return m.message(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	status, err := functiontool.New(functiontool.Config{
		Name:        StatusToolName,
		Description: "Reads this conversation's sub-agents: the state of each, the question one waits on you for, its progress, and a finished one's result. Give an id for one, or none for all.",
	}, func(ctx agent.Context, args statusArgs) (statusResult, error) {
		return m.status(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	stop, err := functiontool.New(functiontool.Config{
		Name:        StopToolName,
		Description: "Stops one of this conversation's sub-agents you no longer need. A stopped one cannot be resumed.",
	}, func(ctx agent.Context, args stopArgs) (View, error) {
		return m.stop(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{spawn, message, status, stop}, nil
}

type messageArgs struct {
	ID      string `json:"id" jsonschema:"The sub-agent's id, as spawn_agent or agent_status gave it."`
	Message string `json:"message" jsonschema:"What to tell it: the answer to its question, a correction, or a follow-up. It sees nothing else of the conversation."`
	Wait    bool   `json:"wait,omitempty" jsonschema:"true waits until it finishes, fails or asks again, and returns that."`
}

type statusArgs struct {
	ID string `json:"id,omitempty" jsonschema:"One sub-agent's id; leave it out for all of this conversation's."`
}

type statusResult struct {
	Agents []View `json:"agents"`
	Note   string `json:"note,omitempty"`
}

type stopArgs struct {
	ID string `json:"id" jsonschema:"The sub-agent's id."`
}

// threadOf is the thread a tool call is in, and whose.
func threadOf(ctx agent.ReadonlyContext) (Thread, error) {
	organization, _ := stateString(ctx.ReadonlyState(), "organization_id")
	thread := Thread{OrganizationID: organization, UserID: ctx.UserID(), App: ctx.AppName(), ID: ctx.SessionID()}
	if !thread.valid() {
		return Thread{}, errors.New("this conversation has no member of an organization, so it cannot have sub-agents")
	}
	return thread, nil
}

func stateString(state session.ReadonlyState, key string) (string, bool) {
	if state == nil {
		return "", false
	}
	value, err := state.Get(key)
	if err != nil {
		return "", false
	}
	text, ok := value.(string)
	return text, ok && text != ""
}

func identity(state session.ReadonlyState) map[string]any {
	carried := map[string]any{}
	for _, key := range identityKeys {
		if value, ok := stateString(state, key); ok {
			carried[key] = value
		}
	}
	return carried
}

func (m *Manager) spawn(ctx agent.Context, in Brief) (View, error) {
	scope := scopeFrom(ctx)
	if scope == nil {
		return View{}, errNoScope
	}
	thread, err := threadOf(ctx)
	if err != nil {
		return View{}, err
	}
	checked, err := checkBrief(in, m.builder.Families())
	if err != nil {
		return View{}, err
	}
	files, err := handOver(ctx, checked.files)
	if err != nil {
		return View{}, err
	}
	m.sweep(ctx, thread)

	id, err := newID()
	if err != nil {
		return View{}, err
	}
	now := m.now()
	record := Record{
		ID: id, Thread: thread, ParentRunID: scope.runID,
		Objective: checked.objective, Output: checked.output, Tools: checked.tools, Files: checked.files,
		State: StateRunning, Progress: "starting",
		BudgetModelCalls: checked.budget.ModelCalls, BudgetMinutes: checked.budget.Minutes,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(m.ttl),
	}
	c := newChild(ctx, id, scope)
	if err := m.reserve(c); err != nil {
		c.discard()
		return View{}, err
	}
	abandon := func(cause error) (View, error) {
		m.release(c)
		c.discard()
		return View{}, cause
	}
	if _, err := m.sessions.Create(ctx, &session.CreateRequest{
		AppName: AppName, UserID: thread.UserID, SessionID: sessionID(record), State: identity(ctx.ReadonlyState()),
	}); err != nil {
		return abandon(fmt.Errorf("open the sub-agent's session: %w", err))
	}
	if err := m.store.Create(ctx, record); err != nil {
		m.deleteSession(ctx, record)
		return abandon(err)
	}
	m.logState(ctx, record)
	m.start(c, record, briefContent(checked, files))

	notes := checked.notes
	if !in.Wait {
		view := viewOf(record, now)
		view.Note = strings.Join(append(notes, view.Note), " ")
		return view, nil
	}
	view, err := m.await(ctx, c, thread, id)
	if err != nil {
		return View{}, err
	}
	if len(notes) > 0 {
		view.Note = strings.Join(append(notes, view.Note), " ")
	}
	return view, nil
}

// await waits for c to stop working, then reads it. Its assignment ends by
// its budget at the latest; Krude's own run ending ends the wait too.
func (m *Manager) await(ctx agent.Context, c *child, thread Thread, id string) (View, error) {
	select {
	case <-c.finished:
	case <-ctx.Done():
		return View{}, context.Cause(ctx)
	}
	return m.read(ctx, thread, id)
}

// read is one record as Krude sees it. A finished one whose whole result is
// too long for it is saved as an artifact of the thread first, so that the
// file is the run's like any other it keeps.
func (m *Manager) read(ctx agent.Context, thread Thread, id string) (View, error) {
	m.records.Lock()
	defer m.records.Unlock()
	record, err := m.store.Get(ctx, thread, id)
	if err != nil {
		return View{}, m.explain(err, id)
	}
	record = m.keepResult(ctx, record)
	return viewOf(record, m.now()), nil
}

// keepResult saves a done record's whole result as an artifact of the
// thread when it is too long for the record, and names it there. Holding
// m.records.
func (m *Manager) keepResult(ctx agent.Context, record Record) Record {
	if record.State != StateDone || record.FullResult == "" || record.ResultArtifact != "" || ctx.Artifacts() == nil {
		return record
	}
	name := "subagent-" + record.ID + ".md"
	if _, err := ctx.Artifacts().Save(ctx, name, genai.NewPartFromBytes([]byte(record.FullResult), "text/markdown")); err != nil {
		m.logger.WarnContext(ctx, "save a sub-agent's result as an artifact", "subagent_id", record.ID, "error", err)
		return record
	}
	kept := record
	kept.ResultArtifact, kept.FullResult = name, ""
	if err := m.store.Update(ctx, kept, StateDone); err != nil {
		m.logger.WarnContext(ctx, "name a sub-agent's result artifact", "subagent_id", record.ID, "error", err)
	}
	return kept
}

func (m *Manager) explain(err error, id string) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("this conversation has no sub-agent %q; it may have expired and been removed", id)
	}
	return err
}

func (m *Manager) message(ctx agent.Context, in messageArgs) (View, error) {
	scope := scopeFrom(ctx)
	if scope == nil {
		return View{}, errNoScope
	}
	thread, err := threadOf(ctx)
	if err != nil {
		return View{}, err
	}
	id := strings.TrimSpace(in.ID)
	text := strings.TrimSpace(in.Message)
	switch {
	case text == "":
		return View{}, errors.New("the message is empty")
	case len([]rune(text)) > maxMessageRunes:
		return View{}, fmt.Errorf("a message holds at most %d characters", maxMessageRunes)
	}

	m.records.Lock()
	record, err := m.store.Get(ctx, thread, id)
	if err != nil {
		m.records.Unlock()
		return View{}, m.explain(err, id)
	}
	if c, queued := m.queue(scope, id, text); c != nil {
		m.records.Unlock()
		if !queued {
			return View{}, errors.New("that sub-agent is working for another run")
		}
		if in.Wait {
			return m.await(ctx, c, thread, id)
		}
		view := viewOf(record, m.now())
		view.Note = "It is working; it reads your message as soon as its current step ends."
		return view, nil
	}
	if record, err = m.store.Get(ctx, thread, id); err != nil {
		m.records.Unlock()
		return View{}, m.explain(err, id)
	}
	now := m.now()
	switch state := record.stateAt(now); state {
	case StateWaiting, StateDone:
	case StateRunning:
		// Nothing in this process works on it, so the run that set it
		// working ended without its record saying so.
		record = m.markStopped(ctx, record, "its run ended before it finished")
		m.records.Unlock()
		return View{}, fmt.Errorf("sub-agent %s was stopped when its run ended, and cannot be resumed; start a new one", id)
	case StateExpired:
		m.records.Unlock()
		return View{}, fmt.Errorf("sub-agent %s expired, with its work; start a new one", id)
	default:
		m.records.Unlock()
		return View{}, fmt.Errorf("sub-agent %s is %s and cannot be resumed; start a new one", id, state)
	}
	record = m.keepResult(ctx, record)
	from := record.State
	c := newChild(ctx, id, scope)
	if err := m.reserve(c); err != nil {
		m.records.Unlock()
		c.discard()
		return View{}, err
	}
	record.State, record.ParentRunID = StateRunning, scope.runID
	record.Question, record.Result, record.FullResult, record.ResultArtifact, record.Error = "", "", "", "", ""
	record.Progress = "reading your message"
	record.UpdatedAt, record.ExpiresAt = now, now.Add(m.ttl)
	if err := m.store.Update(ctx, record, from); err != nil {
		m.records.Unlock()
		m.release(c)
		c.discard()
		if errors.Is(err, ErrStateChanged) {
			return View{}, fmt.Errorf("sub-agent %s changed meanwhile; read it with agent_status", id)
		}
		return View{}, err
	}
	m.records.Unlock()
	m.logState(ctx, record)
	m.start(c, record, messageContent([]string{text}))
	if in.Wait {
		return m.await(ctx, c, thread, id)
	}
	view := viewOf(record, now)
	view.Note = "It is working on your message. Read it later with agent_status."
	return view, nil
}

// queue hands text to the child working on id in this process, if there is
// one still taking messages, and reports whether it took it. A child past
// taking them is waited for, and reported as none.
func (m *Manager) queue(scope *runScope, id, text string) (*child, bool) {
	m.mu.Lock()
	c := m.running[id]
	switch {
	case c == nil:
		m.mu.Unlock()
		return nil, false
	case c.exiting:
		m.mu.Unlock()
		waitFor(c)
		return nil, false
	case c.scope != scope:
		m.mu.Unlock()
		return c, false
	}
	c.pending = append(c.pending, text)
	m.mu.Unlock()
	return c, true
}

func waitFor(c *child) bool {
	timer := time.NewTimer(stopWait)
	defer timer.Stop()
	select {
	case <-c.finished:
		return true
	case <-timer.C:
		return false
	}
}

func (m *Manager) markStopped(ctx context.Context, record Record, reason string) Record {
	from := record.State
	now := m.now()
	record.State, record.Error, record.Progress, record.Question = StateStopped, reason, "", ""
	record.UpdatedAt, record.ExpiresAt = now, now.Add(m.ttl)
	if err := m.store.Update(ctx, record, from); err != nil {
		m.logger.WarnContext(ctx, "record a sub-agent stopped", "subagent_id", record.ID, "error", err)
		return record
	}
	m.logState(ctx, record)
	return record
}

func (m *Manager) status(ctx agent.Context, in statusArgs) (statusResult, error) {
	thread, err := threadOf(ctx)
	if err != nil {
		return statusResult{}, err
	}
	if id := strings.TrimSpace(in.ID); id != "" {
		view, err := m.read(ctx, thread, id)
		if err != nil {
			return statusResult{}, err
		}
		return statusResult{Agents: []View{view}}, nil
	}
	m.records.Lock()
	defer m.records.Unlock()
	records, err := m.store.List(ctx, thread)
	if err != nil {
		return statusResult{}, err
	}
	result := statusResult{Agents: []View{}}
	if len(records) > maxListed {
		result.Note = fmt.Sprintf("Only the newest %d of this conversation's %d sub-agents are listed.", maxListed, len(records))
		records = records[len(records)-maxListed:]
	}
	now := m.now()
	for _, record := range records {
		result.Agents = append(result.Agents, viewOf(m.keepResult(ctx, record), now))
	}
	if len(records) == 0 {
		result.Note = "This conversation has no sub-agents."
	}
	return result, nil
}

func (m *Manager) stop(ctx agent.Context, in stopArgs) (View, error) {
	thread, err := threadOf(ctx)
	if err != nil {
		return View{}, err
	}
	id := strings.TrimSpace(in.ID)
	m.records.Lock()
	record, err := m.store.Get(ctx, thread, id)
	if err != nil {
		m.records.Unlock()
		return View{}, m.explain(err, id)
	}
	if c, _ := m.active(id); c != nil {
		m.records.Unlock()
		c.cancel(errStopped)
		if !waitFor(c) {
			return View{}, fmt.Errorf("sub-agent %s is stopping, and has not yet recorded that it stopped", id)
		}
		return m.read(ctx, thread, id)
	}
	defer m.records.Unlock()
	if record, err = m.store.Get(ctx, thread, id); err != nil {
		return View{}, m.explain(err, id)
	}
	if state := record.stateAt(m.now()); slices.Contains([]string{StateRunning, StateWaiting, StateDone}, state) {
		reason := errStopped.Error()
		if state == StateRunning {
			reason = "its run ended before it finished"
		}
		record = m.markStopped(ctx, record, reason)
	}
	return viewOf(record, m.now()), nil
}

// sweep removes the thread's sub-agents that expired more than a TTL ago,
// with their transcripts: agent_status has said they expired for that long.
// It is best effort.
func (m *Manager) sweep(ctx context.Context, thread Thread) {
	records, err := m.store.List(ctx, thread)
	if err != nil {
		m.logger.WarnContext(ctx, "list a thread's sub-agents to sweep", "thread_id", thread.ID, "error", err)
		return
	}
	cutoff := m.now().Add(-m.ttl)
	var gone []Record
	for _, record := range records {
		if record.State != StateRunning && record.ExpiresAt.Before(cutoff) {
			gone = append(gone, record)
		}
	}
	if err := m.remove(ctx, gone); err != nil {
		m.logger.WarnContext(ctx, "sweep a thread's expired sub-agents", "thread_id", thread.ID, "error", err)
	}
}

// remove deletes records and their transcripts, stopping any this process
// is working on first.
func (m *Manager) remove(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if c, _ := m.active(record.ID); c != nil {
			c.cancel(errStopped)
			waitFor(c)
		}
		if err := m.deleteSession(ctx, record); err != nil {
			return err
		}
		ids = append(ids, record.ID)
	}
	return m.store.Delete(ctx, ids...)
}

func (m *Manager) deleteSession(ctx context.Context, record Record) error {
	err := m.sessions.Delete(ctx, &session.DeleteRequest{AppName: AppName, UserID: record.Thread.UserID, SessionID: sessionID(record)})
	if err != nil && !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("delete a sub-agent's session: %w", err)
	}
	return nil
}

// ForgetThread removes a thread's sub-agents and their transcripts, for a
// thread that was deleted.
func (m *Manager) ForgetThread(ctx context.Context, organizationID, userID, app, threadID string) error {
	records, err := m.store.List(ctx, Thread{OrganizationID: organizationID, UserID: userID, App: app, ID: threadID})
	if err != nil {
		return err
	}
	return m.remove(ctx, records)
}

// Forget removes every sub-agent of a member's in an organization, or of
// every member's when userID is empty, with their transcripts.
func (m *Manager) Forget(ctx context.Context, organizationID, userID string) error {
	if organizationID == "" {
		return errors.New("subagents: forgetting needs an organization")
	}
	records, err := m.store.ListMember(ctx, organizationID, userID)
	if err != nil {
		return err
	}
	return m.remove(ctx, records)
}

// handOver reads the thread's files a brief names, to attach to its first
// message read-only: a sub-agent has no load_artifacts, which would list
// the member's other files too, so it reads what it is handed and nothing
// else.
func handOver(ctx agent.Context, names []string) ([]*genai.Part, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if ctx.Artifacts() == nil {
		return nil, errors.New("this conversation keeps no files to hand over")
	}
	var parts []*genai.Part
	total := 0
	for _, name := range names {
		loaded, err := ctx.Artifacts().Load(ctx, name)
		if err != nil || loaded == nil || loaded.Part == nil {
			return nil, fmt.Errorf("this conversation has no file %q to hand over; load_artifacts lists them", name)
		}
		part := loaded.Part
		size := len(part.Text)
		if part.InlineData != nil {
			size += len(part.InlineData.Data)
		}
		if part.FileData != nil || (part.Text == "" && part.InlineData == nil) {
			return nil, fmt.Errorf("%q cannot be handed over", name)
		}
		if size > maxFileBytes {
			return nil, fmt.Errorf("%q is larger than the %d MiB a file handed to a sub-agent may be", name, maxFileBytes>>20)
		}
		if total += size; total > maxFilesBytes {
			return nil, fmt.Errorf("the files come to more than the %d MiB a sub-agent may be handed", maxFilesBytes>>20)
		}
		parts = append(parts, genai.NewPartFromText("File "+name+":"), part)
	}
	return parts, nil
}

type askArgs struct {
	Question string `json:"question" jsonschema:"One clear question, complete on its own: Krude sees this and not your work."`
}

type askResult struct {
	Status string `json:"status"`
}

// askTool is a sub-agent's ask_parent: it records the question and ends the
// assignment there, so the sub-agent waits on Krude rather than guessing.
func askTool(m *Manager, c *child) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        AskToolName,
		Description: "Asks Krude, who briefed you, one question you cannot answer from your brief or your tools, instead of guessing. Your work pauses until Krude answers; the answer is your next message.",
	}, func(ctx agent.Context, args askArgs) (askResult, error) {
		question := bounded(args.Question, maxQuestionRunes)
		if question == "" {
			return askResult{}, errors.New("the question is empty")
		}
		m.mu.Lock()
		c.question = question
		m.mu.Unlock()
		// The assignment ends on this response, without another model call.
		ctx.Actions().SkipSummarization = true
		return askResult{Status: "Your question was sent to Krude. Stop here: its answer comes as your next message."}, nil
	})
}

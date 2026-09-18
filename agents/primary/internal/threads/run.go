package threads

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/compaction"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"
)

// maxFailureRunes bounds the error a failed run's event keeps.
const maxFailureRunes = 1000

// finishTimeout bounds recording how a run ended, which happens after the
// request that started it may already be gone.
const finishTimeout = 10 * time.Second

// errLeaseLost reports that a run stopped holding its thread, because it went
// too long without renewing and another run took the thread over.
var errLeaseLost = errors.New("the run lost its hold on the thread")

// RunInput is what a member sends a thread: a message, or their answers to
// the confirmations it is waiting on. Exactly one of the two.
type RunInput struct {
	Message       string
	Confirmations []Answer
	// StartedBy is who starts the run, agentsv1.StartedByMember unless set.
	StartedBy string
	// Wakeup names the batch of app events a message carries, for a run
	// that those events started rather than the member. The message then
	// shows as from their apps, not from them.
	Wakeup string
}

// Answer is the member's decision on one confirmation.
type Answer struct {
	// ID is the confirmation's call ID.
	ID        string
	Confirmed bool
}

// Run is a run that holds its thread and is ready to execute. Execute or
// Discard it, once: either records how it ended and frees the thread.
type Run struct {
	service    *Service
	member     Member
	threadID   string
	id         string
	startedBy  string
	content    *genai.Content
	stateDelta map[string]any
	started    time.Time
	// remembering is the run's view of memory, scoped to the member's
	// organization. It is built when the run starts driving, and nil when
	// the service has no memory.
	remembering memory.Service
	once        sync.Once
}

// ID is the run's own ID, as its events and records name it.
func (run *Run) ID() string { return run.id }

// StartRun checks what the member sent against the thread, then takes the
// thread for a new run. Nothing has run yet when it returns: the caller
// executes the run, or discards it if it can no longer be streamed.
func (service *Service) StartRun(ctx context.Context, member Member, threadID string, input RunInput) (*Run, error) {
	message := strings.TrimSpace(input.Message)
	switch {
	case message == "" && len(input.Confirmations) == 0:
		return nil, fmt.Errorf("%w: send a message or answer the waiting confirmations", ErrInvalid)
	case message != "" && len(input.Confirmations) > 0:
		return nil, fmt.Errorf("%w: send a message or answer confirmations, not both at once", ErrInvalid)
	case utf8.RuneCountInString(message) > maxMessageRunes:
		return nil, fmt.Errorf("%w: a message holds at most %d characters", ErrInvalid, maxMessageRunes)
	case input.Wakeup != "" && message == "":
		return nil, fmt.Errorf("%w: a wake-up carries a message", ErrInvalid)
	}
	startedBy := cmp.Or(input.StartedBy, agentsv1.StartedByMember)
	if startedBy != agentsv1.StartedByMember && startedBy != agentsv1.StartedByTrigger {
		return nil, fmt.Errorf("%w: a run is started by a member or a trigger", ErrInvalid)
	}

	// The thread must be the member's before this run takes it, even for a
	// moment, or naming another member's thread could hold it from them.
	if _, err := service.thread(ctx, member, threadID); err != nil {
		return nil, err
	}
	run := &Run{service: service, member: member, threadID: threadID, startedBy: startedBy}
	var err error
	if run.id, err = newRunID(); err != nil {
		return nil, err
	}
	if err := service.runs.Start(ctx, runstore.Run{
		ID:             run.id,
		SessionID:      threadID,
		UserID:         member.UserID,
		OrganizationID: member.OrganizationID,
		StartedBy:      startedBy,
	}, service.lease); err != nil {
		if errors.Is(err, runstore.ErrRunInProgress) {
			return nil, ErrRunInProgress
		}
		return nil, fmt.Errorf("start run: %w", err)
	}
	// What the thread waits on is read only now that this run holds it: read
	// before, a run finishing in between could leave an answer applied twice,
	// or a confirmation it just asked for unanswered.
	if err := run.prepare(ctx, message, input); err != nil {
		if cancelErr := service.runs.Cancel(context.WithoutCancel(ctx), run.id); cancelErr != nil {
			service.logger.WarnContext(ctx, "release a run that never ran", "run_id", run.id, "error", cancelErr)
		}
		return nil, err
	}
	run.started = time.Now()
	return run, nil
}

// prepare reads the thread the run now holds and turns what the member sent
// into what the agent reads: their message, or their answers to every
// confirmation the thread waits on.
func (run *Run) prepare(ctx context.Context, message string, input RunInput) error {
	service := run.service
	stored, err := service.thread(ctx, run.member, run.threadID)
	if err != nil {
		return err
	}
	pending := pendingConfirmations(sessionEvents(stored), service.agent.Name())
	if message != "" {
		if len(pending) > 0 {
			return ErrConfirmationPending
		}
		run.content = genai.NewContentFromText(message, genai.RoleUser)
		run.stateDelta = map[string]any{}
		if stateString(stored.State(), stateTitle) == "" {
			run.stateDelta[stateTitle] = titleFrom(message)
		}
		if input.Wakeup != "" {
			run.stateDelta[stateWakeup] = input.Wakeup
		}
		return nil
	}
	content, err := answerContent(input.Confirmations, pending)
	if err != nil {
		return err
	}
	run.content = content
	return nil
}

// answerContent is the member's answers as ADK reads them: one function
// response per confirmation. Every waiting confirmation has to be answered,
// and nothing else: an answer to a confirmation that is not waiting would be
// ignored, and one left unanswered would leave its tool neither run nor
// refused.
func answerContent(answers []Answer, pending []string) (*genai.Content, error) {
	if len(pending) == 0 {
		return nil, fmt.Errorf("%w: nothing in this thread is waiting for an answer", ErrInvalid)
	}
	answered := make(map[string]bool, len(answers))
	parts := make([]*genai.Part, 0, len(answers))
	for _, answer := range answers {
		if !slices.Contains(pending, answer.ID) {
			return nil, fmt.Errorf("%w: %q is not waiting for an answer", ErrInvalid, answer.ID)
		}
		if answered[answer.ID] {
			return nil, fmt.Errorf("%w: %q is answered twice", ErrInvalid, answer.ID)
		}
		answered[answer.ID] = true
		parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID:       answer.ID,
			Name:     toolconfirmation.FunctionCallName,
			Response: map[string]any{"confirmed": answer.Confirmed},
		}})
	}
	for _, id := range pending {
		if !answered[id] {
			return nil, fmt.Errorf("%w: answer every waiting confirmation together; %q has no answer", ErrInvalid, id)
		}
	}
	return &genai.Content{Role: genai.RoleUser, Parts: parts}, nil
}

// Execute runs Krude on the thread, calling emit with every item as it
// happens, then records how the run ended. emit is called from this
// goroutine only; when it fails, the member has gone, and the run stops.
func (run *Run) Execute(ctx context.Context, emit func(Item) error) {
	executed := false
	run.once.Do(func() {
		executed = true
		run.execute(ctx, emit)
	})
	if !executed {
		run.service.logger.ErrorContext(ctx, "a run was executed after it had already ended", "run_id", run.id)
	}
}

// Discard ends a run that never ran, such as one whose stream could not be
// opened, so its thread is free again.
func (run *Run) Discard(ctx context.Context, reason string) {
	run.once.Do(func() {
		run.finish(ctx, usage.NewCounter(), errors.New(reason))
	})
}

func (run *Run) execute(ctx context.Context, emit func(Item) error) {
	service := run.service
	counter := usage.NewCounter()
	runContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	runContext, cancelTimeout := context.WithTimeoutCause(runContext, service.runTimeout,
		fmt.Errorf("the run took longer than %s: %w", service.runTimeout, context.DeadlineExceeded))
	defer cancelTimeout()
	runContext = usage.WithCounter(runContext, counter)
	if run.member.AccessToken != "" {
		runContext = memberauth.WithAccessToken(runContext, run.member.AccessToken)
	}

	stopLease := run.holdThread(ctx, cancel)
	defer stopLease()
	gone := false
	send := func(item Item) {
		if gone {
			return
		}
		if err := emit(item); err != nil {
			gone = true
			cancel(fmt.Errorf("the member stopped listening: %w", context.Canceled))
		}
	}
	send(Item{Type: ItemRunStarted, RunID: run.id, At: time.Now().UTC()})

	failure := run.drive(runContext, send)
	if failure == nil && runContext.Err() != nil {
		failure = context.Cause(runContext)
	}
	stopLease()

	run.finish(ctx, counter, failure)
	if failure != nil {
		send(Item{Type: ItemRunFailed, RunID: run.id, At: time.Now().UTC(), Error: memberFacing(failure, service.runTimeout)})
		return
	}
	total := counter.Total()
	send(Item{Type: ItemRunCompleted, RunID: run.id, At: time.Now().UTC(), Usage: &total})
	// Remembering happens once the member has their answer: it is worth a
	// round trip to Memory Bank, but never one the member waits through.
	if run.remembering != nil {
		run.remember(ctx, run.remembering)
	}
}

// drive runs the agent and streams what it yields, returning the error the
// run ended with, if any.
func (run *Run) drive(ctx context.Context, send func(Item)) error {
	service := run.service
	answering := service.agent
	if run.startedBy == agentsv1.StartedByTrigger {
		// Nobody is watching this run, and text from the member's apps
		// steers it: only the agent that can reach nothing outside runs it.
		answering = service.unattended
	}
	// The run's memory is bound to the member's organization before the run
	// starts. ADK addresses memory by user alone, and a member can belong to
	// more than one organization, so the scope cannot be left to be read off
	// each request: this view answers within it or not at all.
	if service.memory != nil {
		scoped, err := service.memory.For(adkmemory.Scope{
			OrganizationID: run.member.OrganizationID,
			UserID:         run.member.UserID,
		})
		if err != nil {
			return fmt.Errorf("scope memory to the member's organization: %w", err)
		}
		run.remembering = scoped
	}
	adkRunner, err := runner.New(runner.Config{
		AppName:         service.appName,
		Agent:           answering,
		SessionService:  service.sessions,
		ArtifactService: service.artifacts,
		MemoryService:   run.remembering,
	})
	if err != nil {
		return fmt.Errorf("build runner: %w", err)
	}
	options := []runner.RunOption{runner.WithYieldUserMessage()}
	if len(run.stateDelta) > 0 {
		options = append(options, runner.WithStateDelta(run.stateDelta))
	}
	var failure error
	for event, err := range adkRunner.Run(ctx, run.member.UserID, run.threadID, run.content,
		agent.RunConfig{StreamingMode: agent.StreamingModeSSE}, options...) {
		if err != nil {
			// Compaction is bookkeeping after the answer, not the answer.
			if errors.Is(err, compaction.ErrCompaction) {
				service.logger.WarnContext(ctx, "thread compaction failed", "run_id", run.id, "error", err)
				continue
			}
			if failure == nil {
				failure = err
			}
			continue
		}
		for _, item := range streamItems(event) {
			send(item)
		}
	}
	return failure
}

// rememberTimeout bounds handing a finished conversation to memory, which
// happens after the member has already been answered.
const rememberTimeout = 15 * time.Second

// remember gives memory what this run added to the thread, so what the
// member told Krude here is there for their next thread. Only this run's own
// turns are sent: the ones before it were remembered when they happened, and
// a thread re-read from the beginning after every run costs more to extract
// from the longer it gets.
//
// It is bookkeeping after the answer, like compaction: a conversation that
// was not remembered is worth a log line, never a failed run, and it is not
// cancelled with the request the member has already been answered on.
func (run *Run) remember(ctx context.Context, remembering memory.Service) {
	service := run.service
	rememberContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rememberTimeout)
	defer cancel()
	got, err := service.sessions.Get(rememberContext, &session.GetRequest{
		AppName: service.appName, UserID: run.member.UserID, SessionID: run.threadID,
	})
	if err != nil || got == nil || got.Session == nil {
		service.logger.WarnContext(rememberContext, "read a run's thread to remember it",
			"run_id", run.id, "thread_id", run.threadID, "error", err)
		return
	}
	if err := remembering.AddSessionToMemory(rememberContext, adkmemory.Since(got.Session, run.started)); err != nil {
		service.logger.WarnContext(rememberContext, "a run's conversation was not remembered",
			"run_id", run.id, "thread_id", run.threadID, "error", err)
	}
}

// renewTimeout bounds one renewal, so a stalled database cannot hold a run
// past the lease it thinks it still has.
const renewTimeout = 10 * time.Second

// holdThread renews the run's hold on its thread until the returned function
// is called. A run that finds it lost the hold, or has gone a whole lease
// without renewing it, stops: another run may be answering the thread now.
func (run *Run) holdThread(ctx context.Context, cancel context.CancelCauseFunc) func() {
	service := run.service
	holdContext, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(max(service.lease/3, time.Second))
		defer ticker.Stop()
		renewed := time.Now()
		for {
			select {
			case <-holdContext.Done():
				return
			case <-ticker.C:
			}
			renewContext, cancelRenew := context.WithTimeout(holdContext, renewTimeout)
			err := service.runs.Renew(renewContext, run.id, service.lease)
			cancelRenew()
			switch {
			case holdContext.Err() != nil:
				return
			case err == nil:
				renewed = time.Now()
			case errors.Is(err, runstore.ErrNotRunning):
				service.logger.WarnContext(holdContext, "a run lost its hold on its thread and stops", "run_id", run.id)
				cancel(errLeaseLost)
				return
			case time.Since(renewed) >= service.lease:
				service.logger.WarnContext(holdContext, "a run could not renew its hold on its thread for a whole lease and stops", "run_id", run.id, "error", err)
				cancel(errLeaseLost)
				return
			default:
				// The next renewal tries again, well inside the lease.
				service.logger.WarnContext(holdContext, "renew a run's hold on its thread", "run_id", run.id, "error", err)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			stop()
			<-done
		})
	}
}

// finish records how the run ended, and what it cost, with the event that
// announces it.
func (run *Run) finish(ctx context.Context, counter *usage.Counter, failure error) {
	service := run.service
	now := time.Now().UTC()
	models, cost := service.price(counter.Models())
	total := counter.Total()
	duration := time.Since(run.started).Milliseconds()
	if run.started.IsZero() {
		duration = 0
	}

	outcome := runstore.Outcome{Status: runstore.StatusCompleted, Usage: total, CostMicros: cost.TotalMicros}
	var detailType string
	var detail any
	if failure == nil {
		detailType = agentsv1.RunCompletedDetailType
		detail = agentsv1.RunCompleted{
			RunID: run.id, SessionID: run.threadID, UserID: run.member.UserID, OrganizationID: run.member.OrganizationID,
			StartedBy: run.startedBy, Usage: total, Cost: cost, Models: models,
			DurationMillis: duration, CompletedAt: now,
		}
	} else {
		reason := bounded(failure.Error(), maxFailureRunes)
		outcome.Status = runstore.StatusFailed
		outcome.Error = reason
		detailType = agentsv1.RunFailedDetailType
		detail = agentsv1.RunFailed{
			RunID: run.id, SessionID: run.threadID, UserID: run.member.UserID, OrganizationID: run.member.OrganizationID,
			StartedBy: run.startedBy, Error: reason, Usage: total, Cost: cost, Models: models,
			DurationMillis: duration, FailedAt: now,
		}
		service.logger.WarnContext(ctx, "run failed", "run_id", run.id, "thread_id", run.threadID, "error", reason)
	}

	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	event, err := events.NewEvent(detailType, detail, now)
	if err != nil {
		service.logger.ErrorContext(finishContext, "build a run's event", "run_id", run.id, "error", err)
		return
	}
	if err := service.runs.Finish(finishContext, run.id, outcome, event); err != nil {
		// The thread frees itself once the hold runs out.
		service.logger.ErrorContext(finishContext, "record how a run ended", "run_id", run.id, "error", err)
	}
}

// price prices each model's usage, in a stable order, and totals what the
// priced ones cost. A model the table does not know is still listed, with
// its tokens and no cost.
func (service *Service) price(byModel map[string]agentsv1.Usage) ([]agentsv1.ModelUsage, agentsv1.Cost) {
	names := make([]string, 0, len(byModel))
	for name := range byModel {
		names = append(names, name)
	}
	sort.Strings(names)
	models := make([]agentsv1.ModelUsage, 0, len(names))
	var total agentsv1.Cost
	for _, name := range names {
		spent := byModel[name]
		cost, prices, priced := service.prices.Cost(name, spent)
		models = append(models, agentsv1.ModelUsage{Model: name, Usage: spent, Cost: cost, Prices: prices, Priced: priced})
		if !priced {
			service.logger.Warn("a run used a model the price table does not know", "model", name)
			continue
		}
		total.InputMicros += cost.InputMicros
		total.OutputMicros += cost.OutputMicros
		total.CachedMicros += cost.CachedMicros
		total.TotalMicros += cost.TotalMicros
	}
	return models, total
}

// memberFacing is what the member is told about a failed run: what they can
// act on, never the error itself, which the run's record keeps.
func memberFacing(failure error, timeout time.Duration) string {
	switch {
	case errors.Is(failure, context.DeadlineExceeded):
		return fmt.Sprintf("Krude took longer than %s and was stopped.", timeout)
	case errors.Is(failure, errLeaseLost):
		return "Another run took over this thread."
	case errors.Is(failure, context.Canceled):
		return "The run was cancelled."
	default:
		return "Krude ran into a problem and stopped. Try again."
	}
}

// titleFrom names a thread after the first line of its first message.
func titleFrom(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return bounded(strings.Join(strings.Fields(line), " "), maxTitleRunes)
}

func bounded(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

func newRunID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create run id: %w", err)
	}
	return "run_" + base64.RawURLEncoding.EncodeToString(random), nil
}

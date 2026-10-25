package threads

import (
	"context"
	"errors"
	"maps"
	"time"

	"google.golang.org/adk/v2/session"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// A run's brackets, run_started and run_completed or run_failed, are streamed
// as it happens and stored in its thread too, so the thread's history shows
// where each run began and ended, how long it took, what it spent and why it
// failed. Each is an event of metadata alone, the way a Requests note is,
// which nothing reads as a message and the model never sees.
//
// A bracket's id is its run's id with ".started" or ".ended": the stored
// event's id, and the streamed item's id as well, so a client that streamed a
// run and then reads its history can tell the two copies apart by id alone. A
// run that ended before brackets were stored has none in its history.
//
// A run_started can have no end after it. A run that lost its hold on its
// thread records nothing more, since another run may be writing the thread
// (FailureTakenOver is only ever streamed); a process can stop mid-run; and
// the closing bracket's write can fail. Such a start means the run ended
// without a record, not that it still runs: a client knows a run is still
// answering from its stream, never from history.
const (
	// runBracketKey is the custom metadata a bracket is kept under.
	runBracketKey = "oryxa_run"
	// recordInvocation names the invocation a bracket's event belongs to. It
	// is no run of the agent's: the event carries metadata and state, and
	// nothing the model reads.
	recordInvocation = "oryxa-run-record"
)

// The codes a failed run's bracket says it failed with, in Code, beside the
// words for the member in Error.
const (
	// FailureInterrupted is a run Oryxa stopped as it restarted: worth
	// starting again.
	FailureInterrupted = "interrupted"
	// FailureTimeout is a run that took longer than a run may.
	FailureTimeout = "timeout"
	// FailureStopped is a run the member stopped.
	FailureStopped = "stopped"
	// FailureCancelled is a run whose caller went away.
	FailureCancelled = "cancelled"
	// FailureTakenOver is a run that lost its hold on its thread.
	FailureTakenOver = "taken_over"
	// FailureError is any other failure.
	FailureError = "error"
)

// runBracket is what a bracket's event keeps. When it happened is the
// event's own time.
type runBracket struct {
	Type        string          `json:"type"`
	RunID       string          `json:"run_id"`
	Usage       *agentsv1.Usage `json:"usage,omitempty"`
	Error       string          `json:"error,omitempty"`
	Code        string          `json:"code,omitempty"`
	Interrupted bool            `json:"interrupted,omitempty"`
}

// bracketID is the id of a run's bracket of type itemType.
func bracketID(runID, itemType string) string {
	if itemType == ItemRunStarted {
		return runID + ".started"
	}
	return runID + ".ended"
}

// bracketAt is now, as precisely as ADK's PostgreSQL service keeps an event's
// time, so a bracket streams and reads back at the same moment.
func bracketAt() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// failureCode is the code a failed run's bracket gives for failure.
func failureCode(failure error) string {
	switch {
	case Interrupted(failure):
		return FailureInterrupted
	case errors.Is(failure, context.DeadlineExceeded):
		return FailureTimeout
	case errors.Is(failure, errLeaseLost):
		return FailureTakenOver
	case errors.Is(failure, ErrStopped):
		return FailureStopped
	case errors.Is(failure, context.Canceled):
		return FailureCancelled
	default:
		return FailureError
	}
}

// record stores one of the run's brackets in its thread, and with the one
// that ends it, the tally of the thread's files and of the confirmations
// waiting in it. It is written while the run
// still holds its thread, so nothing else writes the thread in between. It is
// bookkeeping beside the answer: a failure is a log line, never a failed run,
// and leaves the bracket only streamed. An interrupted run's record has half
// the drain's grace, so recording how the run ended (Run.finish), which frees
// the thread, still fits in what remains.
func (run *Run) record(ctx context.Context, bracket Item) {
	service := run.service
	timeout := finishTimeout
	if bracket.Interrupted {
		timeout = min(timeout, interruptGrace/2)
	}
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	stored, err := service.readThread(recordContext, run.member, run.threadID, 1)
	if err != nil {
		service.logger.WarnContext(recordContext, "read a run's thread to record it", "run_id", run.id, "thread_id", run.threadID, "error", err)
		return
	}
	event := session.NewEvent(recordContext, recordInvocation)
	event.ID = bracket.ID
	event.Timestamp = bracket.At
	// As the member's, as a rename's is: ADK passes over the member's events
	// when it picks the agent to resume, and would log any other author it
	// does not know on every run.
	event.Author = adkUserAuthor
	event.CustomMetadata = map[string]any{runBracketKey: runBracket{
		Type: bracket.Type, RunID: bracket.RunID, Usage: bracket.Usage,
		Error: bracket.Error, Code: bracket.Code, Interrupted: bracket.Interrupted,
	}}
	if bracket.Type != ItemRunStarted {
		delta := run.artifacts.changes(stored.State())
		if pending := run.confirmations.changes(stored.State()); pending != nil {
			if delta == nil {
				delta = map[string]any{}
			}
			maps.Copy(delta, pending)
		}
		event.Actions.StateDelta = delta
	}
	if err := service.sessions.AppendEvent(recordContext, stored, event); err != nil {
		service.logger.WarnContext(recordContext, "record a run's bracket in its thread", "run_id", run.id, "thread_id", run.threadID, "type", bracket.Type, "error", err)
	}
}

// bracketOf reads the bracket an event holds, if it holds one.
func bracketOf(event *session.Event) (Item, bool) {
	raw, ok := event.CustomMetadata[runBracketKey]
	if !ok || event.Content != nil {
		return Item{}, false
	}
	var bracket runBracket
	if !decodeInto(raw, &bracket) || bracket.RunID == "" {
		return Item{}, false
	}
	switch bracket.Type {
	case ItemRunStarted, ItemRunCompleted, ItemRunFailed:
	default:
		return Item{}, false
	}
	return Item{
		Type: bracket.Type, ID: event.ID, At: at(event), RunID: bracket.RunID, Usage: bracket.Usage,
		Error: bracket.Error, Code: bracket.Code, Interrupted: bracket.Interrupted,
	}, true
}

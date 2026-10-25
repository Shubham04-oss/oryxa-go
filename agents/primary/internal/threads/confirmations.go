package threads

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
)

// stateConfirmationsPending is how many tool confirmations wait on the member
// in a thread, so "what needs you" can be counted across every chat without
// reading any thread's history. A run writes it as it ends, beside the
// thread's files (Run.record). A thread whose last run ended before it existed
// has none: PendingConfirmations reads that thread's history once instead.
const stateConfirmationsPending = "thread_confirmations_pending"

// confirmationTally is the confirmations a thread's agent asked for and the
// ones its member answered, so far. It follows pendingConfirmations' rules:
// only the agent running the thread asks, and only the member answers.
type confirmationTally struct {
	agent    string
	asked    map[string]bool
	answered map[string]bool
}

func tallyConfirmations(events []*session.Event, agentName string) confirmationTally {
	tally := confirmationTally{agent: agentName, asked: map[string]bool{}, answered: map[string]bool{}}
	for _, event := range events {
		tally.add(event)
	}
	return tally
}

// add counts what one stored event asks or answers. A partial event counts
// nothing: its finished event says it again.
func (tally *confirmationTally) add(event *session.Event) {
	if event == nil || event.Partial || event.Content == nil {
		return
	}
	for _, part := range event.Content.Parts {
		switch {
		case part == nil:
		case event.Author == tally.agent && part.FunctionCall != nil &&
			part.FunctionCall.Name == toolconfirmation.FunctionCallName:
			tally.asked[part.FunctionCall.ID] = true
		case event.Author == adkUserAuthor && part.FunctionResponse != nil &&
			part.FunctionResponse.Name == toolconfirmation.FunctionCallName:
			tally.answered[part.FunctionResponse.ID] = true
		}
	}
}

func (tally confirmationTally) pending() int {
	count := 0
	for id := range tally.asked {
		if !tally.answered[id] {
			count++
		}
	}
	return count
}

// changes is the state that brings the thread's count up to the tally, or nil
// when it already says it.
func (tally confirmationTally) changes(state session.ReadonlyState) map[string]any {
	if count, counted := stateInt(state, stateConfirmationsPending); counted && count == tally.pending() {
		return nil
	}
	return map[string]any{stateConfirmationsPending: tally.pending()}
}

// PendingThread is a chat with tool confirmations waiting on its member.
type PendingThread struct {
	Thread
	// PendingConfirmations is how many confirmations wait in it: at least one.
	PendingConfirmations int `json:"pending_confirmations"`
}

// PendingConfirmations lists the member's chats in which a tool waits for
// them to approve or decline it, the most recently active first. It reads the
// count each run leaves in its thread's state; a thread that has none, last
// run before the count was kept, has its history read once and the answer
// remembered for as long as the thread does not change.
func (service *Service) PendingConfirmations(ctx context.Context, member Member) ([]PendingThread, error) {
	listed, err := service.sessions.List(ctx, &session.ListRequest{AppName: service.appName, UserID: member.UserID})
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	waiting := []PendingThread{}
	for _, stored := range listed.Sessions {
		if stored.UserID() != member.UserID || stateString(stored.State(), adksession.StateOrganizationID) != member.OrganizationID {
			continue
		}
		thread := threadOf(stored)
		if thread.Origin != OriginChat {
			continue
		}
		count, ok := stateInt(stored.State(), stateConfirmationsPending)
		if !ok {
			if count, err = service.legacyPending(ctx, member, thread); err != nil {
				return nil, err
			}
		}
		if count > 0 {
			waiting = append(waiting, PendingThread{Thread: thread, PendingConfirmations: count})
		}
	}
	slices.SortStableFunc(waiting, func(a, b PendingThread) int {
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), strings.Compare(a.ID, b.ID))
	})
	return waiting, nil
}

// legacyPending counts a thread's waiting confirmations from its history, for
// a thread whose state does not hold the count yet. The answer is kept until
// the thread changes: its updated time is part of what is remembered.
func (service *Service) legacyPending(ctx context.Context, member Member, thread Thread) (int, error) {
	key := legacyKey{app: service.appName, user: member.UserID, thread: thread.ID}
	if count, ok := service.legacyCounts.get(key, thread.UpdatedAt); ok {
		return count, nil
	}
	stored, err := service.thread(ctx, member, thread.ID)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := len(pendingConfirmations(sessionEvents(stored), service.agent.Name()))
	service.legacyCounts.put(key, thread.UpdatedAt, count)
	return count, nil
}

type legacyKey struct{ app, user, thread string }

type legacyCount struct {
	updatedAt time.Time
	count     int
}

// maxLegacyCounts bounds what legacyCounts remembers. Past it the memory is
// dropped whole: a thread read again is a thread read once more, not a wrong
// count.
const maxLegacyCounts = 10_000

type legacyCounts struct {
	mu     sync.Mutex
	counts map[legacyKey]legacyCount
}

func (counts *legacyCounts) get(key legacyKey, updatedAt time.Time) (int, bool) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	known, ok := counts.counts[key]
	if !ok || !known.updatedAt.Equal(updatedAt) {
		return 0, false
	}
	return known.count, true
}

func (counts *legacyCounts) put(key legacyKey, updatedAt time.Time, count int) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	if counts.counts == nil || len(counts.counts) >= maxLegacyCounts {
		counts.counts = map[legacyKey]legacyCount{}
	}
	counts.counts[key] = legacyCount{updatedAt: updatedAt, count: count}
}

package interactiondelivery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

type fakeDeliveries struct {
	mu       sync.Mutex
	pending  []Delivery
	done     []string
	dropped  []string
	released map[string]bool
	threads  map[string]string
	// failures counts each delivery's failed attempts, which drop it once
	// they reach the waker's most, as the store's Release does.
	failures map[string]int
}

func (store *fakeDeliveries) Claim(context.Context, time.Duration, int) ([]Delivery, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.pending) == 0 {
		return nil, nil
	}
	delivery := store.pending[0]
	store.pending = store.pending[1:]
	return []Delivery{delivery}, nil
}

func (store *fakeDeliveries) Done(_ context.Context, eventID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.done = append(store.done, eventID)
	return nil
}

func (store *fakeDeliveries) Drop(_ context.Context, eventID, _ string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.dropped = append(store.dropped, eventID)
	return nil
}

func (store *fakeDeliveries) Release(_ context.Context, eventID string, failed bool, most int, _ time.Duration, _ string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.released == nil {
		store.released, store.failures = map[string]bool{}, map[string]int{}
	}
	store.released[eventID] = failed
	if failed {
		store.failures[eventID]++
	}
	if store.failures[eventID] >= most {
		store.dropped = append(store.dropped, eventID)
		return true, nil
	}
	return false, nil
}

func (*fakeDeliveries) Purge(context.Context, time.Duration) error { return nil }

func (store *fakeDeliveries) Thread(_ context.Context, requestID, agentID string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.threads[requestID+"/"+agentID], nil
}

func (store *fakeDeliveries) SetThread(_ context.Context, delivery Delivery, threadID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.threads == nil {
		store.threads = map[string]string{}
	}
	store.threads[delivery.RequestID+"/"+delivery.RecipientAgentID] = threadID
	return nil
}

func (store *fakeDeliveries) ClearThread(_ context.Context, requestID, agentID, expected string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := requestID + "/" + agentID
	if store.threads[key] == expected {
		delete(store.threads, key)
	}
	return nil
}

type fakeRequests struct {
	mu       sync.Mutex
	requests map[string]agents.Interaction
	// closed are the requests CloseApproved failed, in order.
	closed []string
}

func (requests *fakeRequests) Get(_ context.Context, id string) (agents.Interaction, error) {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	request, ok := requests.requests[id]
	if !ok {
		return agents.Interaction{}, errors.New("agent request not found")
	}
	return request, nil
}

// CloseApproved fails an approved request with the reason, as the ledger
// does, and leaves any other as it is.
func (requests *fakeRequests) CloseApproved(_ context.Context, id, reason string, at time.Time) (agents.Interaction, error) {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	request, ok := requests.requests[id]
	switch {
	case !ok:
		return agents.Interaction{}, interactions.ErrNotFound
	case request.Status != interactions.StatusApproved:
		return agents.Interaction{}, interactions.ErrConflict
	}
	request.Status, request.Revision = interactions.StatusFailed, request.Revision+1
	request.Result = &agents.InteractionResult{Status: interactions.StatusFailed, Summary: reason, CompletedAt: at}
	requests.requests[id] = request
	requests.closed = append(requests.closed, id)
	return request, nil
}

// CloseExpired closes every approved request whose expiry passed after
// since and by at, the longest expired first, as the ledger does.
func (requests *fakeRequests) CloseExpired(ctx context.Context, reason string, since, at time.Time, limit int) ([]agents.Interaction, error) {
	requests.mu.Lock()
	var expired []agents.Interaction
	for _, request := range requests.requests {
		if request.Status == interactions.StatusApproved && !request.ExpiresAt.After(at) && request.ExpiresAt.After(since) {
			expired = append(expired, request)
		}
	}
	requests.mu.Unlock()
	slices.SortFunc(expired, func(a, b agents.Interaction) int { return a.ExpiresAt.Compare(b.ExpiresAt) })
	var closed []agents.Interaction
	for _, request := range expired[:min(len(expired), limit)] {
		failed, err := requests.CloseApproved(ctx, request.ID, reason, at)
		if err != nil {
			return closed, err
		}
		closed = append(closed, failed)
	}
	return closed, nil
}

func (requests *fakeRequests) get(id string) agents.Interaction {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	return requests.requests[id]
}

func (requests *fakeRequests) setStatus(id, status string) {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	request := requests.requests[id]
	request.Status = status
	requests.requests[id] = request
}

type fakeRun struct {
	id        string
	onExecute func()
	fail      bool
	discarded bool
}

func (run *fakeRun) ID() string { return run.id }

func (run *fakeRun) Execute(_ context.Context, emit func(threads.Item) error) {
	if run.onExecute != nil {
		run.onExecute()
	}
	itemType := threads.ItemRunCompleted
	if run.fail {
		itemType = threads.ItemRunFailed
	}
	_ = emit(threads.Item{Type: itemType, RunID: run.id})
}

func (run *fakeRun) Discard(context.Context, string) { run.discarded = true }

type startedCall struct {
	member   threads.Member
	agentID  string
	threadID string
	input    threads.RunInput
	reach    managedagents.UnattendedProfile
}

type fakeAgentRuns struct {
	mu        sync.Mutex
	calls     []startedCall
	next      int
	onExecute func()
	fail      bool
	err       error
}

func (runs *fakeAgentRuns) StartUnattendedRun(_ context.Context, member threads.Member, agentID, threadID string, input threads.RunInput, reach managedagents.UnattendedProfile) (managedagents.StartedRun, error) {
	runs.mu.Lock()
	defer runs.mu.Unlock()
	runs.calls = append(runs.calls, startedCall{member: member, agentID: agentID, threadID: threadID, input: input, reach: reach})
	if runs.err != nil {
		return managedagents.StartedRun{}, runs.err
	}
	runs.next++
	if threadID == "" {
		threadID = "thread_1"
	}
	return managedagents.StartedRun{ThreadID: threadID, Run: &fakeRun{
		id: "run_" + time.Now().Format("150405.000000000"), onExecute: runs.onExecute, fail: runs.fail,
	}}, nil
}

func testDelivery(eventID, kind string, revision int64) Delivery {
	return Delivery{
		EventID: eventID, RequestID: "irq_1", OrganizationID: "org_1",
		RecipientUserID: "user_b", RecipientAgentID: "agt_b", Kind: kind,
		Status: "pending", Revision: revision, ChangedAt: time.Unix(100, 0).UTC(),
	}
}

func testRequest(status string) agents.Interaction {
	return agents.Interaction{
		ID: "irq_1", OrganizationID: "org_1", Status: status,
		Task: "TOP SECRET REQUEST BODY", ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestTargetLifecycleUsesOneStableThreadAndRequiresLedgerTransitions(t *testing.T) {
	store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_1", KindTargetRequested, 1)}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest("pending")}}
	runs := &fakeAgentRuns{onExecute: func() { requests.setStatus("irq_1", "approved") }}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)

	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.done) != 1 || store.done[0] != "evt_1" || len(runs.calls) != 1 {
		t.Fatalf("done=%v calls=%+v released=%v", store.done, runs.calls, store.released)
	}
	first := runs.calls[0]
	if first.member.UserID != "user_b" || first.member.OrganizationID != "org_1" || first.member.AccessToken != "" ||
		first.agentID != "agt_b" || first.threadID != "" || first.input.StartedBy != agentsv1.StartedByTrigger ||
		!strings.Contains(first.input.Message, `request_id "irq_1"`) || strings.Contains(first.input.Message, "TOP SECRET") {
		t.Fatalf("first call=%+v", first)
	}

	approved := testDelivery("evt_2", KindTargetApproved, 2)
	approved.Status = "approved"
	store.pending = []Delivery{approved}
	runs.onExecute = func() { requests.setStatus("irq_1", "completed") }
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.done) != 2 || len(runs.calls) != 2 || runs.calls[1].threadID != "thread_1" {
		t.Fatalf("done=%v calls=%+v threads=%v", store.done, runs.calls, store.threads)
	}
}

func TestTargetRunWithoutARecordedTransitionIsRetriedAsFailure(t *testing.T) {
	store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_1", KindTargetRequested, 1)}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest("pending")}}
	waker := NewWaker(store, &fakeAgentRuns{}, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if failed, ok := store.released["evt_1"]; !ok || !failed || len(store.done) != 0 {
		t.Fatalf("released=%v done=%v", store.released, store.done)
	}
}

func TestSupersededTargetDeliveryNeedsNoSecondRun(t *testing.T) {
	store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_1", KindTargetRequested, 1)}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest("completed")}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.done) != 1 || len(runs.calls) != 0 {
		t.Fatalf("done=%v calls=%v", store.done, runs.calls)
	}
}

func TestRequesterNotificationCompletesWithTheRun(t *testing.T) {
	delivery := testDelivery("evt_3", KindRequesterCompleted, 3)
	delivery.RecipientUserID, delivery.RecipientAgentID, delivery.Status = "user_a", "agt_a", "completed"
	store := &fakeDeliveries{pending: []Delivery{delivery}}
	expired := testRequest("completed")
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": expired}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.done) != 1 || len(runs.calls) != 1 || runs.calls[0].member.UserID != "user_a" {
		t.Fatalf("done=%v calls=%+v", store.done, runs.calls)
	}
}

func TestDeliveryMessageNeverCarriesInteractionContent(t *testing.T) {
	for _, kind := range []string{KindTargetRequested, KindTargetApproved, KindRequesterRejected, KindRequesterCompleted} {
		message := Message(testDelivery("evt_1", kind, 1))
		for _, want := range []string{"interactions", "irq_1", "untrusted cross-user data"} {
			if !strings.Contains(message, want) {
				t.Errorf("%s message lacks %q: %s", kind, want, message)
			}
		}
	}
}

// TestEveryDeliveryRunStartsWithItsInteractionsToolboxLoaded: whatever the
// delivery, its run works through the interactions toolbox, so the run
// starts with it loaded and its message never sends the model to load it.
func TestEveryDeliveryRunStartsWithItsInteractionsToolboxLoaded(t *testing.T) {
	for _, kind := range []string{KindTargetRequested, KindTargetApproved, KindRequesterRejected, KindRequesterCompleted} {
		delivery := testDelivery("evt_1", kind, 1)
		store := &fakeDeliveries{pending: []Delivery{delivery}}
		status := interactions.StatusPending
		if kind == KindTargetApproved {
			status = interactions.StatusApproved
		}
		requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest(status)}}
		runs := &fakeAgentRuns{onExecute: func() { requests.setStatus("irq_1", interactions.StatusCompleted) }}
		waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
		if err := waker.WakeDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(runs.calls) != 1 {
			t.Fatalf("%s: calls=%+v, want one run", kind, runs.calls)
		}
		input := runs.calls[0].input
		if !slices.Equal(input.Toolboxes, []string{agents.InteractionsToolboxName}) {
			t.Errorf("%s: the run starts with %v loaded, want only the interactions toolbox", kind, input.Toolboxes)
		}
		if strings.Contains(input.Message, "load the interactions toolbox") || !strings.Contains(input.Message, "already loaded") {
			t.Errorf("%s: the message still sends the model to load its toolbox: %s", kind, input.Message)
		}
	}
}

// TestOnlyApprovedWorkReadsTheMembersApps: the member is not here, so what
// their agent may reach is what it has already approved. Deciding on a new
// request, and reporting an outcome to one's own member, run without the
// apps toolbox at all; and every delivery runs as one, offered nothing that
// lists the member's other requests.
func TestOnlyApprovedWorkReadsTheMembersApps(t *testing.T) {
	cases := []struct {
		kind  string
		reach managedagents.UnattendedProfile
	}{
		{KindTargetRequested, managedagents.DeliveryQuiet},
		{KindTargetApproved, managedagents.DeliveryReads},
		{KindRequesterRejected, managedagents.DeliveryQuiet},
		{KindRequesterCompleted, managedagents.DeliveryQuiet},
	}
	for _, testCase := range cases {
		got := reach(testCase.kind)
		if got != testCase.reach {
			t.Errorf("reach(%s) = %q, want %q", testCase.kind, got, testCase.reach)
		}
		if reads := got.ReadsApps(); reads != (testCase.kind == KindTargetApproved) || !got.Delivers() {
			t.Errorf("reach(%s) reads apps = %t, delivers = %t; want apps only for approved work, and a delivery always", testCase.kind, reads, got.Delivers())
		}
	}
}

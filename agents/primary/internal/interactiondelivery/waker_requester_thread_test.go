package interactiondelivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

func originRequest() agents.Interaction {
	request := testRequest(interactions.StatusCompleted)
	request.Requester = agents.InteractionParty{UserID: "user_a", AgentID: "agt_a"}
	request.RequesterThreadID = "thread_origin"
	request.RequesterThreadAppName = agents.RootName
	return request
}

func requesterDelivery(kind string) Delivery {
	delivery := testDelivery("evt_outcome", kind, 3)
	delivery.RecipientUserID, delivery.RecipientAgentID = "user_a", "agt_a"
	return delivery
}

func TestRequesterOutcomesContinueTheirOriginatingConversation(t *testing.T) {
	for _, kind := range []string{KindRequesterCompleted, KindRequesterRejected} {
		t.Run(kind, func(t *testing.T) {
			request := originRequest()
			if kind == KindRequesterRejected {
				request.Status = interactions.StatusRejected
			}
			store := &fakeDeliveries{pending: []Delivery{requesterDelivery(kind)}}
			runs := &fakeAgentRuns{}
			waker := NewWaker(store, runs, &fakeRequests{requests: map[string]agents.Interaction{request.ID: request}}, Config{Lease: time.Minute}, nil)
			if err := waker.WakeDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(runs.calls) != 1 || runs.calls[0].threadID != "thread_origin" || !runs.calls[0].input.Delivery.PrimaryChat ||
				store.threads["irq_1/agt_a"] != "thread_origin" || len(store.done) != 1 {
				t.Fatalf("calls=%+v threads=%v done=%v", runs.calls, store.threads, store.done)
			}
		})
	}
}

func TestBusyOrConfirmationPendingOriginWaitsWithoutLosingAnAttempt(t *testing.T) {
	for _, refusal := range []error{threads.ErrRunInProgress, threads.ErrConfirmationPending} {
		t.Run(refusal.Error(), func(t *testing.T) {
			request := originRequest()
			delivery := requesterDelivery(KindRequesterCompleted)
			store := &fakeDeliveries{}
			runs := &fakeAgentRuns{err: refusal}
			waker := NewWaker(store, runs, &fakeRequests{requests: map[string]agents.Interaction{request.ID: request}}, Config{Lease: time.Minute}, nil)
			for range maxAttempts + 1 {
				store.pending = []Delivery{delivery}
				if err := waker.WakeDue(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if len(store.done) != 0 || len(store.dropped) != 0 || store.released[delivery.EventID] ||
				store.failures[delivery.EventID] != 0 || len(store.threads) != 0 {
				t.Fatalf("delivery lost while waiting: %+v", store)
			}
			for _, call := range runs.calls {
				if call.threadID != "thread_origin" {
					t.Fatalf("busy origin allocated a separate conversation: %+v", call)
				}
			}
			runs.err = nil
			store.pending = []Delivery{delivery}
			if err := waker.WakeDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(store.done) != 1 || runs.calls[len(runs.calls)-1].threadID != "thread_origin" {
				t.Fatalf("delivery never resumed in origin: calls=%+v done=%v", runs.calls, store.done)
			}
		})
	}
}

type deletedOriginRuns struct{ fakeAgentRuns }

func (runs *deletedOriginRuns) StartUnattendedRun(ctx context.Context, member threads.Member, agentID, threadID string, input threads.RunInput, profile managedagents.UnattendedProfile) (managedagents.StartedRun, error) {
	if threadID == "thread_origin" {
		return managedagents.StartedRun{}, threads.ErrNotFound
	}
	return runs.fakeAgentRuns.StartUnattendedRun(ctx, member, agentID, threadID, input, profile)
}

func TestDeletedRequesterOriginGetsOneStableReplacement(t *testing.T) {
	request := originRequest()
	delivery := requesterDelivery(KindRequesterCompleted)
	store := &fakeDeliveries{}
	runs := &deletedOriginRuns{}
	waker := NewWaker(store, runs, &fakeRequests{requests: map[string]agents.Interaction{request.ID: request}}, Config{}, nil)
	for range 2 {
		store.pending = []Delivery{delivery}
		if err := waker.WakeDue(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(runs.calls) != 2 || runs.calls[0].threadID != "" || runs.calls[1].threadID != "thread_1" ||
		store.threads["irq_1/agt_a"] != "thread_1" || len(store.done) != 2 {
		t.Fatalf("calls=%+v threads=%v done=%v", runs.calls, store.threads, store.done)
	}
}

func TestOriginIsNeverUsedForTargetOrAnotherRecipient(t *testing.T) {
	request := originRequest()
	for _, delivery := range []Delivery{
		testDelivery("evt_target", KindTargetApproved, 2),
		testDelivery("evt_other", KindRequesterCompleted, 3),
	} {
		store := &fakeDeliveries{}
		runs := &fakeAgentRuns{}
		waker := NewWaker(store, runs, nil, Config{}, nil)
		_, err := waker.start(context.Background(), delivery, request, "outcome", managedagents.DeliveryReport)
		if err != nil || len(runs.calls) != 1 || runs.calls[0].threadID != "" || runs.calls[0].input.Delivery.PrimaryChat {
			t.Fatalf("delivery=%+v calls=%+v err=%v", delivery, runs.calls, err)
		}
	}
	// A missing thread does not hide a different error behind a replacement.
	runs := &fakeAgentRuns{err: threads.ErrConfirmationPending}
	waker := NewWaker(&fakeDeliveries{}, runs, nil, Config{}, nil)
	_, err := waker.start(context.Background(), requesterDelivery(KindRequesterCompleted), request, "outcome", managedagents.DeliveryReport)
	if !errors.Is(err, threads.ErrConfirmationPending) || len(runs.calls) != 1 {
		t.Fatalf("calls=%+v err=%v", runs.calls, err)
	}
}

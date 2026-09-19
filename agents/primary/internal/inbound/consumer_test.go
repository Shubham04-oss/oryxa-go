package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	batchingv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/batching/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/wakeups"
)

type fakeRecorder struct {
	events  []wakeups.Event
	seen    map[string]bool
	batches int
}

func (recorder *fakeRecorder) RecordBatch(_ context.Context, events []wakeups.Event) (int, error) {
	if recorder.seen == nil {
		recorder.seen = map[string]bool{}
	}
	recorder.batches++
	recorded := 0
	for _, event := range events {
		if recorder.seen[event.ID] {
			continue
		}
		recorder.seen[event.ID] = true
		recorder.events = append(recorder.events, event)
		recorded++
	}
	return recorded, nil
}

func delivery(t *testing.T, source, detailType, eventID string, data any) string {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(map[string]any{"event_id": eventID, "version": 1, "data": json.RawMessage(encoded)})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"id": "eb-1", "source": source, "detail-type": detailType, "detail": json.RawMessage(detail)})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestSubscriptionsCoverEveryHandledEvent(t *testing.T) {
	subscriptions := Subscriptions()
	if strings.Join(subscriptions[batchingv1.Source], ",") != batchingv1.TriggerBatchReadyDetailType {
		t.Fatalf("batching subscriptions = %v", subscriptions[batchingv1.Source])
	}
	// Batching decides when triggers wake Krude; hearing them straight from
	// apps as well would wake Krude for each on its own.
	if _, ok := subscriptions[appsv1.Source]; ok {
		t.Fatalf("agents still subscribes to apps' triggers: %v", subscriptions[appsv1.Source])
	}
	if strings.Join(subscriptions[authv1.Source], ",") != authv1.MemberRemovedDetailType+","+authv1.OrganizationDeletedDetailType {
		t.Fatalf("auth subscriptions = %v", subscriptions[authv1.Source])
	}
}

func readyBatch(memberID string, triggers ...batchingv1.Trigger) batchingv1.TriggerBatchReady {
	return batchingv1.TriggerBatchReady{
		BatchID: "tbatch_1", OrganizationID: "org_1", MemberID: memberID, Lane: batchingv1.LaneDigest,
		EventCount: len(triggers), FlushReason: "target", ReadyAt: time.Unix(200, 0).UTC(), Triggers: triggers,
	}
}

func gmailTrigger(eventID, subject string) batchingv1.Trigger {
	return batchingv1.Trigger{
		EventID: eventID, TriggerID: "tr_1", ConnectionID: "apc_1", Toolkit: "gmail", Trigger: "GMAIL_NEW_MESSAGE",
		Data: json.RawMessage(`{"subject":"` + subject + `"}`), ReceivedAt: time.Unix(100, 0).UTC(),
	}
}

// TestABatchIsRecordedWholeAndOnce: a batch's triggers are kept together, by
// their own app events' ids, and the bus delivering the batch again adds
// nothing.
func TestABatchIsRecordedWholeAndOnce(t *testing.T) {
	recorder := &fakeRecorder{}
	consumer := NewConsumer(recorder, nil, nil)
	batch := readyBatch("user_1", gmailTrigger("evt_apps_1", "Launch"), gmailTrigger("evt_apps_2", "Review"))
	body := delivery(t, batchingv1.Source, batchingv1.TriggerBatchReadyDetailType, "evt_batch_1", batch)
	for range 2 {
		if err := consumer.Handle(context.Background(), body); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.events) != 2 {
		t.Fatalf("recorded %d events, want the batch's two once", len(recorder.events))
	}
	if got := recorder.events[0]; got.ID != "evt_apps_1" || got.OrganizationID != "org_1" || got.MemberID != "user_1" ||
		got.Toolkit != "gmail" || string(got.Data) != `{"subject":"Launch"}` || !got.ReceivedAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("recorded = %+v", got)
	}
	// Recorded as one, never trigger by trigger.
	if recorder.batches != 2 {
		t.Fatalf("recorded in %d calls, want one per delivery", recorder.batches)
	}

	if err := consumer.Handle(context.Background(), delivery(t, batchingv1.Source, batchingv1.TriggerBatchReadyDetailType, "evt_batch_2",
		readyBatch("", gmailTrigger("evt_apps_3", "Nobody")))); err != nil {
		t.Fatalf("a batch for nobody = %v, want it dropped without a retry", err)
	}
	if err := consumer.Handle(context.Background(), delivery(t, "oryxa.elsewhere", batchingv1.TriggerBatchReadyDetailType, "evt_batch_3", batch)); err == nil {
		t.Fatal("a batch from another source was accepted")
	}
	if len(recorder.events) != 2 {
		t.Fatalf("recorded %d events, want nothing more", len(recorder.events))
	}
}

// TestATriggerStraightFromAppsIsLeftToBatching covers the changeover: one
// still on the queue from before agents consumed batches is acknowledged and
// left alone, since batching hands it over in a batch.
func TestATriggerStraightFromAppsIsLeftToBatching(t *testing.T) {
	recorder := &fakeRecorder{}
	consumer := NewConsumer(recorder, nil, nil)
	trigger := appsv1.TriggerReceived{
		TriggerID: "tr_1", OrganizationID: "org_1", MemberID: "user_1", Toolkit: "gmail", Trigger: "GMAIL_NEW_MESSAGE",
		Data: json.RawMessage(`{"subject":"Launch"}`), ReceivedAt: time.Unix(100, 0).UTC(),
	}
	if err := consumer.Handle(context.Background(), delivery(t, appsv1.Source, appsv1.TriggerReceivedDetailType, "evt_1", trigger)); err != nil {
		t.Fatalf("a direct trigger = %v, want it acknowledged", err)
	}
	if len(recorder.events) != 0 {
		t.Fatalf("recorded %d events from a direct trigger, want none", len(recorder.events))
	}
}

func TestAMemberLeavingOrAnOrganizationGoingIsForgottenEverywhere(t *testing.T) {
	var calls []string
	forgetter := func(name string, fail error) Forgetter {
		return func(_ context.Context, organizationID, memberID string) error {
			calls = append(calls, name+" "+organizationID+" "+memberID)
			return fail
		}
	}
	consumer := NewConsumer(&fakeRecorder{}, []Forgetter{forgetter("threads", nil), forgetter("wakeups", nil)}, nil)

	if err := consumer.Handle(context.Background(), delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_1",
		authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user_1"})); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Handle(context.Background(), delivery(t, authv1.Source, authv1.OrganizationDeletedDetailType, "evt_2",
		authv1.OrganizationDeleted{OrganizationID: "org_2"})); err != nil {
		t.Fatal(err)
	}
	want := "threads org_1 user_1|wakeups org_1 user_1|threads org_2 |wakeups org_2 "
	if got := strings.Join(calls, "|"); got != want {
		t.Fatalf("forgot %q, want %q", got, want)
	}

	// A forgetter that fails makes the event fail, so the queue delivers it
	// again, and every other forgetter still runs.
	calls = nil
	failing := NewConsumer(&fakeRecorder{}, []Forgetter{forgetter("threads", errors.New("database down")), forgetter("wakeups", nil)}, nil)
	err := failing.Handle(context.Background(), delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_3",
		authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user_1"}))
	if err == nil || !strings.Contains(err.Error(), "database down") || len(calls) != 2 {
		t.Fatalf("failing forget = %v after %v", err, calls)
	}
	if err := consumer.Handle(context.Background(), delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_4",
		authv1.MemberRemoved{OrganizationID: "org_1"})); err == nil {
		t.Fatal("a removed member with no user was accepted")
	}
}

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

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/wakeups"
)

type fakeRecorder struct {
	events []wakeups.Event
	seen   map[string]bool
}

func (recorder *fakeRecorder) Record(_ context.Context, event wakeups.Event) (bool, error) {
	if recorder.seen == nil {
		recorder.seen = map[string]bool{}
	}
	if recorder.seen[event.ID] {
		return false, nil
	}
	recorder.seen[event.ID] = true
	recorder.events = append(recorder.events, event)
	return true, nil
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
	if strings.Join(subscriptions[appsv1.Source], ",") != appsv1.TriggerReceivedDetailType {
		t.Fatalf("apps subscriptions = %v", subscriptions[appsv1.Source])
	}
	if strings.Join(subscriptions[authv1.Source], ",") != authv1.MemberRemovedDetailType+","+authv1.OrganizationDeletedDetailType {
		t.Fatalf("auth subscriptions = %v", subscriptions[authv1.Source])
	}
}

func TestATriggerIsRecordedOnce(t *testing.T) {
	recorder := &fakeRecorder{}
	consumer := NewConsumer(recorder, nil, nil)
	trigger := appsv1.TriggerReceived{
		TriggerID: "tr_1", OrganizationID: "org_1", MemberID: "user_1", Toolkit: "gmail", Trigger: "GMAIL_NEW_MESSAGE",
		Data: json.RawMessage(`{"subject":"Launch"}`), ReceivedAt: time.Unix(100, 0).UTC(),
	}
	body := delivery(t, appsv1.Source, appsv1.TriggerReceivedDetailType, "evt_1", trigger)
	for range 2 {
		if err := consumer.Handle(context.Background(), body); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.events) != 1 {
		t.Fatalf("recorded %d events, want one", len(recorder.events))
	}
	if got := recorder.events[0]; got.ID != "evt_1" || got.MemberID != "user_1" || got.Toolkit != "gmail" || string(got.Data) != `{"subject":"Launch"}` {
		t.Fatalf("recorded = %+v", got)
	}
	nobody := trigger
	nobody.MemberID = ""
	if err := consumer.Handle(context.Background(), delivery(t, appsv1.Source, appsv1.TriggerReceivedDetailType, "evt_2", nobody)); err != nil {
		t.Fatalf("an event for nobody = %v, want it dropped without a retry", err)
	}
	if err := consumer.Handle(context.Background(), delivery(t, "oryxa.elsewhere", appsv1.TriggerReceivedDetailType, "evt_3", trigger)); err == nil {
		t.Fatal("an event from another source was accepted")
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

package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	batchingv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/batching/v1"
	teamsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactiondelivery"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/organizationagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/wakeups"
)

type fakeRecorder struct {
	events  []wakeups.Event
	seen    map[string]bool
	batches int
}

type fakeInteractionRecorder struct {
	deliveries []interactiondelivery.Delivery
	seen       map[string]bool
}

type fakeOrganizationRecorder struct {
	deliveries []organizationagents.Delivery
	seen       map[string]bool
}

func (recorder *fakeOrganizationRecorder) Record(_ context.Context, delivery organizationagents.Delivery) (bool, error) {
	if recorder.seen == nil {
		recorder.seen = map[string]bool{}
	}
	if recorder.seen[delivery.EventID] {
		return false, nil
	}
	recorder.seen[delivery.EventID] = true
	recorder.deliveries = append(recorder.deliveries, delivery)
	return true, nil
}

func (recorder *fakeInteractionRecorder) Record(_ context.Context, delivery interactiondelivery.Delivery) (bool, error) {
	if recorder.seen == nil {
		recorder.seen = map[string]bool{}
	}
	if recorder.seen[delivery.EventID] {
		return false, nil
	}
	recorder.seen[delivery.EventID] = true
	recorder.deliveries = append(recorder.deliveries, delivery)
	return true, nil
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
	if strings.Join(subscriptions[agentsv1.Source], ",") != agentsv1.InteractionRequestedDetailType+","+agentsv1.InteractionDecidedDetailType+","+agentsv1.InteractionCompletedDetailType+","+agentsv1.TeamspaceEntryPublishedDetailType {
		t.Fatalf("agents subscriptions = %v", subscriptions[agentsv1.Source])
	}
	if strings.Join(subscriptions[teamsv1.Source], ",") != teamsv1.TeamDeletedDetailType+","+teamsv1.MemberRemovedDetailType {
		t.Fatalf("teams subscriptions = %v", subscriptions[teamsv1.Source])
	}
}

func TestTeamspacePublicationRoutesOnlyIdentityAndIsIdempotent(t *testing.T) {
	recorder := &fakeOrganizationRecorder{}
	consumer := NewConsumer(&fakeRecorder{}, nil, nil, nil).WithOrganizationSynthesisRecorder(recorder)
	publishedAt := time.Unix(300, 0).UTC()
	published := agentsv1.TeamspaceEntryPublished{
		EntryID: "tse_1", TeamspaceID: "tsp_1", OrganizationID: "org_1",
		AuthorUserID: "user_1", AuthorAgentID: "agt_1", Kind: "decision", PublishedAt: publishedAt,
	}
	body := delivery(t, agentsv1.Source, agentsv1.TeamspaceEntryPublishedDetailType, "evt_teamspace_1", published)
	for range 2 {
		if err := consumer.Handle(context.Background(), body); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.deliveries) != 1 {
		t.Fatalf("deliveries=%+v, want one", recorder.deliveries)
	}
	got := recorder.deliveries[0]
	if got.EventID != "evt_teamspace_1" || got.EntryID != "tse_1" || got.TeamspaceID != "tsp_1" ||
		got.OrganizationID != "org_1" || got.AuthorUserID != "user_1" || got.AuthorAgentID != "agt_1" ||
		got.Kind != "decision" || !got.PublishedAt.Equal(publishedAt) {
		t.Fatalf("delivery=%+v", got)
	}

	missing := NewConsumer(&fakeRecorder{}, nil, nil, nil)
	if err := missing.Handle(context.Background(), body); err == nil {
		t.Fatal("publication without an organization synthesis recorder was accepted")
	}
}

func TestInteractionLifecycleRoutesOnlyIdentityAndState(t *testing.T) {
	recorder := &fakeInteractionRecorder{}
	consumer := NewConsumer(&fakeRecorder{}, recorder, nil, nil)
	changed := agentsv1.InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1",
		RequesterUserID: "user_a", RequesterAgentID: "agt_a",
		TargetUserID: "user_b", TargetAgentID: "agt_b",
		Status: "pending", Revision: 1, ChangedAt: time.Unix(100, 0).UTC(),
	}
	requested := delivery(t, agentsv1.Source, agentsv1.InteractionRequestedDetailType, "evt_requested", changed)
	if err := consumer.Handle(context.Background(), requested); err != nil {
		t.Fatal(err)
	}
	// Repeating the bus delivery remains one durable job.
	if err := consumer.Handle(context.Background(), requested); err != nil {
		t.Fatal(err)
	}
	changed.Status, changed.Revision = "approved", 2
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionDecidedDetailType, "evt_approved", changed)); err != nil {
		t.Fatal(err)
	}
	changed.Status = "rejected"
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionDecidedDetailType, "evt_rejected", changed)); err != nil {
		t.Fatal(err)
	}
	changed.Status, changed.Revision = "completed", 3
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionCompletedDetailType, "evt_completed", changed)); err != nil {
		t.Fatal(err)
	}

	if len(recorder.deliveries) != 4 {
		t.Fatalf("deliveries=%+v", recorder.deliveries)
	}
	want := []struct{ kind, user, agent string }{
		{interactiondelivery.KindTargetRequested, "user_b", "agt_b"},
		{interactiondelivery.KindTargetApproved, "user_b", "agt_b"},
		{interactiondelivery.KindRequesterRejected, "user_a", "agt_a"},
		{interactiondelivery.KindRequesterCompleted, "user_a", "agt_a"},
	}
	for index, expected := range want {
		got := recorder.deliveries[index]
		if got.Kind != expected.kind || got.RecipientUserID != expected.user || got.RecipientAgentID != expected.agent || got.RequestID != "irq_1" {
			t.Errorf("delivery %d=%+v, want %+v", index, got, expected)
		}
	}

	changed.Status = "pending"
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionCompletedDetailType, "evt_bad", changed)); err == nil {
		t.Fatal("completed event with pending status was accepted")
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
	consumer := NewConsumer(recorder, nil, nil, nil)
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
	consumer := NewConsumer(recorder, nil, nil, nil)
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
	consumer := NewConsumer(&fakeRecorder{}, nil, []Forgetter{forgetter("threads", nil), forgetter("wakeups", nil)}, nil)

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
	// again, but forgetAll now stops at the first failure instead of running
	// the rest: a later forgetter can delete rows an earlier, failed one
	// still needs to find its own state when the event is retried (see the
	// comment on forgetAll and on interactiondelivery.Store.AffectedThreads),
	// so running it anyway would let the retry silently miss work.
	calls = nil
	failing := NewConsumer(&fakeRecorder{}, nil, []Forgetter{forgetter("threads", errors.New("database down")), forgetter("wakeups", nil)}, nil)
	err := failing.Handle(context.Background(), delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_3",
		authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user_1"}))
	if err == nil || !strings.Contains(err.Error(), "database down") || len(calls) != 1 {
		t.Fatalf("failing forget = %v after %v, want only the failing forgetter to have run", err, calls)
	}
	if err := consumer.Handle(context.Background(), delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_4",
		authv1.MemberRemoved{OrganizationID: "org_1"})); err == nil {
		t.Fatal("a removed member with no user was accepted")
	}
}

func TestADeletedHumanTeamRemovesItsTeamspace(t *testing.T) {
	var organizationID, teamID string
	consumer := NewConsumer(&fakeRecorder{}, nil, nil, nil).WithTeamForgetter(func(_ context.Context, organization, team string) error {
		organizationID, teamID = organization, team
		return nil
	})
	if err := consumer.Handle(context.Background(), delivery(t, teamsv1.Source, teamsv1.TeamDeletedDetailType, "evt_team_deleted",
		teamsv1.TeamDeleted{OrganizationID: "org_1", TeamID: "team_1"})); err != nil {
		t.Fatal(err)
	}
	if organizationID != "org_1" || teamID != "team_1" {
		t.Fatalf("cleanup organization=%q team=%q", organizationID, teamID)
	}

	missing := NewConsumer(&fakeRecorder{}, nil, nil, nil)
	if err := missing.Handle(context.Background(), delivery(t, teamsv1.Source, teamsv1.TeamDeletedDetailType, "evt_missing_cleanup",
		teamsv1.TeamDeleted{OrganizationID: "org_1", TeamID: "team_1"})); err == nil {
		t.Fatal("team deletion without a teamspace cleanup handler was accepted")
	}
}

func TestALeavingTeamMemberWithdrawsPrivateAgentContributions(t *testing.T) {
	var organizationID, teamID, userID string
	consumer := NewConsumer(&fakeRecorder{}, nil, nil, nil).WithTeamMemberForgetter(func(_ context.Context, organization, team, user string) error {
		organizationID, teamID, userID = organization, team, user
		return nil
	})
	if err := consumer.Handle(context.Background(), delivery(t, teamsv1.Source, teamsv1.MemberRemovedDetailType, "evt_team_member_removed",
		teamsv1.MemberRemoved{OrganizationID: "org_1", TeamID: "team_1", UserID: "user_1"})); err != nil {
		t.Fatal(err)
	}
	if organizationID != "org_1" || teamID != "team_1" || userID != "user_1" {
		t.Fatalf("cleanup organization=%q team=%q user=%q", organizationID, teamID, userID)
	}
}

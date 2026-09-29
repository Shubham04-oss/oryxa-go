package inbound

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
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

// signingHarness holds one signer per source agents listens to — batching,
// agents, auth, teams — and a verifier that trusts them, so a delivery is
// signed the way the service it names would have signed it.
var (
	harnessOnce  sync.Once
	harnessErr   error
	harnessSigns = map[string]*eventauth.Signer{}
	harnessVerif *eventauth.Verifier
)

func signingHarness() (*eventauth.Verifier, error) {
	harnessOnce.Do(func() {
		keys := map[string]*ecdsa.PublicKey{}
		for _, source := range []string{
			batchingv1.Source, agentsv1.Source, authv1.Source, teamsv1.Source, appsv1.Source,
		} {
			service, _ := strings.CutPrefix(source, "oryxa.")
			signer, key, err := eventauth.NewLocalSigner(service, time.Now)
			if err != nil {
				harnessErr = err
				return
			}
			harnessSigns[source] = signer
			keys[service] = key
		}
		harnessVerif = eventauth.NewVerifier(keys, nil, time.Now)
	})
	return harnessVerif, harnessErr
}

// testVerifier is the verifier every consumer in this file mounts with.
func testVerifier(t *testing.T) *eventauth.Verifier {
	t.Helper()
	verifier, err := signingHarness()
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func delivery(t *testing.T, source, detailType, eventID string, data any) string {
	t.Helper()
	return deliveryAt(t, source, detailType, eventID, "", data)
}

// deliveryAt is delivery for an envelope that says when the event happened,
// as occurredAt, which delivery leaves out.
func deliveryAt(t *testing.T, source, detailType, eventID, occurredAt string, data any) string {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signingHarness(); err != nil {
		t.Fatal(err)
	}
	// A source no harness signer exists for — one nothing publishes as —
	// stays unsigned, which is one more thing the consumer must refuse.
	detail := map[string]any{"event_id": eventID, "version": 1, "data": json.RawMessage(encoded)}
	if occurredAt != "" {
		detail["occurred_at"] = occurredAt
	}
	if signer, ok := harnessSigns[source]; ok {
		signature, err := signer.Sign(source, detailType, eventID, 1, occurredAt, encoded)
		if err != nil {
			t.Fatal(err)
		}
		detail["sig"] = signature
	}
	wrapped, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"id": "eb-1", "source": source, "detail-type": detailType, "detail": json.RawMessage(wrapped)})
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
	// apps as well would wake Krude for each on its own. What agents hears from
	// apps is the Slack surface's three events, and nothing else.
	if strings.Join(subscriptions[appsv1.Source], ",") != appsv1.SlackMessageReceivedDetailType+","+appsv1.SlackActionReceivedDetailType+","+appsv1.SlackInstallChangedDetailType {
		t.Fatalf("apps subscriptions = %v, want the Slack surface's message, click and install events and no trigger", subscriptions[appsv1.Source])
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
	consumer, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	consumer = consumer.WithOrganizationSynthesisRecorder(recorder)
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

	missing, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.Handle(context.Background(), body); err == nil {
		t.Fatal("publication without an organization synthesis recorder was accepted")
	}
}

func TestInteractionLifecycleRoutesOnlyIdentityAndState(t *testing.T) {
	recorder := &fakeInteractionRecorder{}
	consumer, err := NewConsumer(&fakeRecorder{}, recorder, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
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

// TestAnAnswerInReviewWakesNobodyAndItsOutcomeWakesTheRequester: an answer
// held for its addressed member's review is acknowledged without a
// delivery, since the member reviews it in person; a withheld one, and one
// sent, are reported to the member who asked, as any outcome is.
func TestAnAnswerInReviewWakesNobodyAndItsOutcomeWakesTheRequester(t *testing.T) {
	recorder := &fakeInteractionRecorder{}
	consumer, err := NewConsumer(&fakeRecorder{}, recorder, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	nudges := 0
	consumer.WithInteractionNudge(func() { nudges++ })
	changed := agentsv1.InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1",
		RequesterUserID: "user_a", RequesterAgentID: "agt_a",
		TargetUserID: "user_b", TargetAgentID: "agt_b",
		Status: "review", Revision: 3, ChangedAt: time.Unix(100, 0).UTC(),
	}
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionCompletedDetailType, "evt_review", changed)); err != nil {
		t.Fatalf("an answer in review: %v", err)
	}
	if len(recorder.deliveries) != 0 || nudges != 0 {
		t.Fatalf("an answer in review recorded %+v and nudged %d times, want nothing", recorder.deliveries, nudges)
	}
	for revision, status := range map[int64]string{4: "withheld", 5: "completed"} {
		changed.Status, changed.Revision = status, revision
		if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionCompletedDetailType, "evt_"+status, changed)); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		got := recorder.deliveries[len(recorder.deliveries)-1]
		if got.Kind != interactiondelivery.KindRequesterCompleted || got.RecipientUserID != "user_a" || got.RecipientAgentID != "agt_a" || got.Status != status {
			t.Fatalf("%s delivered %+v, want the requester told", status, got)
		}
	}
	changed.Status = "archived"
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionCompletedDetailType, "evt_archived", changed)); err == nil {
		t.Fatal("a completed event with a status nobody knows was accepted")
	}
}

// TestANewInteractionDeliveryNudgesTheWaker: the consumer tells the waker of
// each delivery it records, as it records it, so the agent is woken at once;
// a delivery the bus repeats is not news.
func TestANewInteractionDeliveryNudgesTheWaker(t *testing.T) {
	consumer, err := NewConsumer(&fakeRecorder{}, &fakeInteractionRecorder{}, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	nudges := 0
	consumer.WithInteractionNudge(func() { nudges++ })
	changed := agentsv1.InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1",
		RequesterUserID: "user_a", RequesterAgentID: "agt_a",
		TargetUserID: "user_b", TargetAgentID: "agt_b",
		Status: "pending", Revision: 1, ChangedAt: time.Unix(100, 0).UTC(),
	}
	requested := delivery(t, agentsv1.Source, agentsv1.InteractionRequestedDetailType, "evt_requested", changed)
	for range 2 {
		if err := consumer.Handle(context.Background(), requested); err != nil {
			t.Fatal(err)
		}
	}
	if nudges != 1 {
		t.Fatalf("nudged %d times for one delivery sent twice, want once", nudges)
	}
	changed.Status, changed.Revision = "approved", 2
	if err := consumer.Handle(context.Background(), delivery(t, agentsv1.Source, agentsv1.InteractionDecidedDetailType, "evt_approved", changed)); err != nil {
		t.Fatal(err)
	}
	if nudges != 2 {
		t.Fatalf("nudged %d times for two deliveries, want twice", nudges)
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
	consumer, err := NewConsumer(recorder, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
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
	consumer, err := NewConsumer(recorder, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
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
	consumer, err := NewConsumer(&fakeRecorder{}, nil, []Forgetter{forgetter("threads", nil), forgetter("wakeups", nil)}, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}

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
	failing, err := NewConsumer(&fakeRecorder{}, nil, []Forgetter{forgetter("threads", errors.New("database down")), forgetter("wakeups", nil)}, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = failing.Handle(context.Background(), delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_3",
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
	consumer, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	consumer = consumer.WithTeamForgetter(func(_ context.Context, organization, team string) error {
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

	missing, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.Handle(context.Background(), delivery(t, teamsv1.Source, teamsv1.TeamDeletedDetailType, "evt_missing_cleanup",
		teamsv1.TeamDeleted{OrganizationID: "org_1", TeamID: "team_1"})); err == nil {
		t.Fatal("team deletion without a teamspace cleanup handler was accepted")
	}
}

func TestALeavingTeamMemberWithdrawsPrivateAgentContributions(t *testing.T) {
	var organizationID, teamID, userID string
	var removedAt time.Time
	consumer, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	consumer = consumer.WithTeamMemberForgetter(func(_ context.Context, organization, team, user string, at time.Time) error {
		organizationID, teamID, userID, removedAt = organization, team, user, at
		return nil
	})
	// Teams' clock gives nanoseconds, and the teamspace's purge keeps them.
	left := time.Date(2026, time.September, 24, 10, 0, 0, 123456789, time.UTC)
	if err := consumer.Handle(context.Background(), deliveryAt(t, teamsv1.Source, teamsv1.MemberRemovedDetailType, "evt_team_member_removed",
		"2026-09-24T10:00:05Z", teamsv1.MemberRemoved{OrganizationID: "org_1", TeamID: "team_1", UserID: "user_1", RemovedAt: left})); err != nil {
		t.Fatal(err)
	}
	if organizationID != "org_1" || teamID != "team_1" || userID != "user_1" || !removedAt.Equal(left) {
		t.Fatalf("cleanup organization=%q team=%q user=%q removed_at=%s", organizationID, teamID, userID, removedAt)
	}
}

// A membership's end bounds what the person's side conversations in the
// teamspace lose. A Teams event that does not say when ended when it
// happened, and one that says neither is refused rather than guessed at.
func TestALeavingTeamMembersRemovalTimeIsWhenTheEventHappenedWhenItHasNone(t *testing.T) {
	var removedAt []time.Time
	consumer, err := NewConsumer(&fakeRecorder{}, nil, nil, testVerifier(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	consumer = consumer.WithTeamMemberForgetter(func(_ context.Context, _, _, _ string, at time.Time) error {
		removedAt = append(removedAt, at)
		return nil
	})
	removed := teamsv1.MemberRemoved{OrganizationID: "org_1", TeamID: "team_1", UserID: "user_1"}
	if err := consumer.Handle(context.Background(), deliveryAt(t, teamsv1.Source, teamsv1.MemberRemovedDetailType, "evt_untimed",
		"2026-09-24T10:00:05.5Z", removed)); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.September, 24, 10, 0, 5, 500000000, time.UTC); len(removedAt) != 1 || !removedAt[0].Equal(want) {
		t.Fatalf("removed_at = %v, want the event's own time, %s", removedAt, want)
	}
	err = consumer.Handle(context.Background(), delivery(t, teamsv1.Source, teamsv1.MemberRemovedDetailType, "evt_timeless", removed))
	if err == nil || !strings.Contains(err.Error(), "when the membership ended") || len(removedAt) != 1 {
		t.Fatalf("an event saying neither = %v, forgot %d times; want it refused, and nothing forgotten", err, len(removedAt))
	}
}

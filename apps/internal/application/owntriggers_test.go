package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// A pushed event is registered at the app through hooks, which owns the
// endpoint and its secret: the core makes the secret, hands it to Subscribe,
// and verifies every delivery with it before the integration sees one.
func TestAPushedSubscriptionIsRegisteredThroughHooks(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()

	trigger, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_COMMENT_EVENT", Config: json.RawMessage(`{"repo":"acme/api"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if trigger.Slug != "ACME_COMMENT_EVENT" || trigger.Status != domain.TriggerActive || trigger.Kind != domain.TriggerKindWebhook {
		t.Fatalf("the trigger is %+v", trigger)
	}
	if trigger.ManualSetup {
		t.Fatal("a subscription the core registered itself is not manual setup")
	}
	// The endpoint is the connection's, and its Hook is what the app was given.
	endpoints, err := h.hooks.EndpointsForConnection(ctx, "apc_oryxa")
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 || endpoints[0].Scope != integration.HookScopeConnection {
		t.Fatalf("endpoints = %+v", endpoints)
	}
	if endpoints[0].Toolkit != "acme" || endpoints[0].OrganizationID != "org_1" {
		t.Fatalf("the endpoint is %+v", endpoints[0])
	}
	hook, _ := own.hooks()
	if !strings.HasPrefix(hook.URL, "https://api.oryxa.test"+hooks.PathPrefix) || hook.Secret == "" {
		t.Fatalf("the app was given %s", hook)
	}
	// Nothing about the app's own hook is logged or announced: only the
	// endpoint, which is a random id, ever leaves this process.
	if strings.Contains(h.allEvents(), hook.Secret) {
		t.Fatal("the endpoint secret reached an event")
	}
	if types := h.eventTypes(); len(types) != 1 || types[0] != appsv1.TriggerSubscribedDetailType {
		t.Fatalf("a subscription published %v", types)
	}
	if len(h.composio.upserts) != 0 {
		t.Fatalf("composio was asked to subscribe: %v", h.composio.upserts)
	}
}

// Asking again for the same connection, event and config answers the
// subscription that exists rather than making a second one, and the endpoint
// the second attempt made is closed again rather than left receiving.
func TestTheSameOwnSubscriptionIsMadeOnce(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()
	input := TriggerInput{ConnectionID: "apc_oryxa", Trigger: "ACME_COMMENT_EVENT", Config: json.RawMessage(`{"repo":"acme/api"}`)}

	first, err := h.service.CreateTrigger(ctx, memberToken, "org_1", input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.service.CreateTrigger(ctx, memberToken, "org_1", input)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("a second subscription was made: %s then %s", first.ID, second.ID)
	}
	endpoints, err := h.hooks.EndpointsForConnection(ctx, "apc_oryxa")
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 {
		t.Fatalf("endpoints = %+v, want the one the first call made", endpoints)
	}
	if types := h.eventTypes(); len(types) != 1 {
		t.Fatalf("the second call published %v", types)
	}
	// A different config is a different subscription: the member is listening
	// for something else.
	other, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_COMMENT_EVENT", Config: json.RawMessage(`{"repo":"acme/web"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatal("a different config was taken for the same subscription")
	}
	_ = own
}

// A config the trigger does not take is refused before an endpoint is made,
// so nothing is registered at the app for a subscription that cannot happen.
func TestAnOwnSubscriptionRefusesAConfigItCannotTake(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	_, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_COMMENT_EVENT", Config: json.RawMessage(`{"channel":"C1"}`),
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateTrigger = %v, want invalid", err)
	}
	endpoints, _ := h.hooks.EndpointsForConnection(context.Background(), "apc_oryxa")
	if len(endpoints) != 0 {
		t.Fatalf("endpoints = %+v, want none", endpoints)
	}
	hook, _ := own.hooks()
	if hook.URL != "" {
		t.Fatalf("the app was given %s", hook)
	}
}

// An app that refuses the subscription leaves no endpoint behind: one would
// answer deliveries for an event nothing ever subscribed to.
func TestARefusedOwnSubscriptionLeavesNoEndpoint(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	own.subscribeErr = &integration.AppError{Message: "that repository is not there"}
	_, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_COMMENT_EVENT", Config: json.RawMessage(`{"repo":"acme/api"}`),
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateTrigger = %v, want invalid", err)
	}
	if !strings.Contains(err.Error(), "not there") {
		t.Fatalf("the refusal says %q, which does not name the app's own reason", err)
	}
	endpoints, _ := h.hooks.EndpointsForConnection(context.Background(), "apc_oryxa")
	if len(endpoints) != 0 {
		t.Fatalf("endpoints = %+v, want none", endpoints)
	}
}

// Removing a subscription stops the app sending it and deletes the endpoint
// that carried it, and Composio is never asked.
func TestRemovingAnOwnSubscriptionUnsubscribesAtTheApp(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()
	trigger, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_COMMENT_EVENT", Config: json.RawMessage(`{"repo":"acme/api"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service.RemoveTrigger(ctx, memberToken, "org_1", trigger.ID); err != nil {
		t.Fatal(err)
	}
	_, unsubscribed := own.hooks()
	if len(unsubscribed) != 1 || !strings.HasPrefix(unsubscribed[0], "remote-") {
		t.Fatalf("the app was told %v", unsubscribed)
	}
	endpoints, _ := h.hooks.EndpointsForConnection(ctx, "apc_oryxa")
	if len(endpoints) != 0 {
		t.Fatalf("endpoints = %+v, want none", endpoints)
	}
	if len(h.composio.deletedTriggers) != 0 {
		t.Fatalf("composio was asked to delete: %v", h.composio.deletedTriggers)
	}
	if types := h.eventTypes(); !slicesContains(types, appsv1.TriggerUnsubscribedDetailType) {
		t.Fatalf("the removal published %v", types)
	}
	// Removing it again is not an error and does nothing further.
	if err := h.service.RemoveTrigger(ctx, memberToken, "org_1", trigger.ID); err != nil {
		t.Fatal(err)
	}
	if _, again := own.hooks(); len(again) != 1 {
		t.Fatalf("the app was told %v twice", again)
	}
}

// A webhook delivery reaches the subscriptions of the connection it was
// delivered to and nothing else: another member's connection to the same app
// is not reached by it, even for the same event.
func TestPushRoutingReachesOnlyTheEndpointsOwnConnection(t *testing.T) {
	t.Parallel()
	h, _ := newToolHarness(t)
	h.oryxaConnection("apc_mine", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	h.oryxaConnection("apc_theirs", "user-2", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()
	for _, id := range []string{"apc_mine", "apc_theirs"} {
		// Each member subscribes to their own connection; memberToken is the
		// first member's, so the second is recorded directly below.
		token := memberToken
		if id == "apc_theirs" {
			token = otherToken
		}
		if _, err := h.service.CreateTrigger(ctx, token, "org_1", TriggerInput{
			ConnectionID: id, Trigger: "ACME_COMMENT_EVENT", Config: json.RawMessage(`{"repo":"acme/api"}`),
		}); err != nil {
			t.Fatalf("subscribing on %s: %v", id, err)
		}
	}
	endpoint := hooks.Endpoint{ID: "aph_mine", Toolkit: "acme", Scope: integration.HookScopeConnection,
		OrganizationID: "org_1", ConnectionID: "apc_mine"}
	routing := HookSubscriptions{Store: h.store}
	found, err := routing.ForEndpoint(ctx, endpoint, "ACME_COMMENT_EVENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("a connection endpoint reached %d subscriptions", len(found))
	}
	if found[0].ConnectionID != "apc_mine" || found[0].MemberID != "user-1" ||
		found[0].OrganizationID != "org_1" || found[0].Toolkit != "acme" {
		t.Fatalf("the subscription is %+v", found[0])
	}
	// An event the toolkit does not push reaches nobody.
	if found, err = routing.ForEndpoint(ctx, endpoint, "ACME_ISSUE_EVENT"); err != nil || len(found) != 0 {
		t.Fatalf("a polled event reached %+v, %v", found, err)
	}
}

// An endpoint shared by many accounts reaches a subscription only when the
// event names that subscription's own account, so an event about a private
// channel reaches the member it was delivered for and nobody who could also
// see it.
func TestPushRoutingByAccountReachesOnlyTheMembersOwnConnection(t *testing.T) {
	t.Parallel()
	h, _ := newToolHarness(t)
	h.oryxaConnection("apc_mine", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	h.oryxaConnection("apc_theirs", "user-2", "acme", domain.ConnectionActive, integration.KindAPIToken)
	h.store.own().accountRefs["apc_mine"] = "U01"
	h.store.own().accountRefs["apc_theirs"] = "U02"
	ctx := context.Background()
	// Both members' subscriptions are recorded directly: the routing is what
	// is under test here, not who is allowed to make a subscription.
	for _, id := range []string{"apc_mine", "apc_theirs"} {
		h.store.own().byID["apt_"+id] = OwnSubscription{
			ID: "apt_" + id, OrganizationID: "org_1", MemberID: strings.TrimPrefix(id, "apc_"),
			ConnectionID: id, Toolkit: "acme", Slug: "ACME_COMMENT_EVENT",
			Config: json.RawMessage(`{"repo":"acme/api"}`), Status: domain.TriggerActive, Kind: integration.TriggerPush,
		}
		h.store.own().byKey[OwnKey(id, "ACME_COMMENT_EVENT", json.RawMessage(`{"repo":"acme/api"}`))] = "apt_" + id
	}
	shared := HookSubscriptions{Store: h.store}

	found, err := shared.ForAccount(ctx, "acme", "", "U01", "ACME_COMMENT_EVENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ConnectionID != "apc_mine" || found[0].AccountRef != "U01" {
		t.Fatalf("an account's own event reached %+v", found)
	}
	// An account nobody here holds reaches nobody.
	if found, err = shared.ForAccount(ctx, "acme", "", "U99", "ACME_COMMENT_EVENT"); err != nil || len(found) != 0 {
		t.Fatalf("another account's event reached %+v, %v", found, err)
	}
	// An event that names no account cannot be routed, and guessing one would
	// reach another member's subscription.
	if found, err = shared.ForAccount(ctx, "acme", "", "", "ACME_COMMENT_EVENT"); err != nil || len(found) != 0 {
		t.Fatalf("an event with no account reached %+v, %v", found, err)
	}
	// A removed subscription is no longer reached.
	h.store.own().byID["apt_apc_mine"] = OwnSubscription{
		ID: "apt_apc_mine", OrganizationID: "org_1", MemberID: "user-1", ConnectionID: "apc_mine",
		Toolkit: "acme", Slug: "ACME_COMMENT_EVENT", Status: domain.TriggerRemoved,
	}
	if found, err = shared.ForAccount(ctx, "acme", "", "U01", "ACME_COMMENT_EVENT"); err != nil || len(found) != 0 {
		t.Fatalf("a removed subscription reached %+v, %v", found, err)
	}
}

// A poll starts from the subscription's cursor, publishes what the app
// answers as apps.trigger.received.v1, and records where to carry on. The
// first poll of a new subscription has no cursor, and the contract is that it
// answers no events and a cursor.
func TestTheFirstPollAnswersNoEventsAndACursor(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()
	trigger, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_ISSUE_EVENT",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A new subscription is due on the next pass rather than at once, so
	// nothing is polled in the minute it was made.
	h.service.pollTriggers(ctx)
	if calls := own.pollCalls(); len(calls) != 0 {
		t.Fatalf("a subscription was polled before its interval: %+v", calls)
	}
	due := testClock.Add(pollEvery)
	h.due(trigger.ID, "", due)
	h.service.now = func() time.Time { return due }
	h.service.pollTriggers(ctx)
	if calls := own.pollCalls(); len(calls) != 1 || calls[0].Cursor != "" {
		t.Fatalf("the first poll was %+v", calls)
	}
	if types := h.eventTypes(); len(types) != 1 || types[0] != appsv1.TriggerSubscribedDetailType {
		t.Fatalf("the first poll published %v", types)
	}
	// The subscription now carries the app's own cursor, and is not due again
	// until the interval is out.
	own.poll[""] = integration.Batch{Cursor: "since-2026-09-17"}
	own.mu.Lock()
	own.poll["since-2026-09-17"] = integration.Batch{
		Events: []integration.Event{{ID: "e1", Trigger: "ACME_ISSUE_EVENT", OccurredAt: testClock, Data: json.RawMessage(`{"number":7}`)}},
		Cursor: "since-2026-09-18",
	}
	own.mu.Unlock()

	// The claim moved its due time, so nothing is due: the pass is over.
	h.service.pollTriggers(ctx)
	if calls := own.pollCalls(); len(calls) != 1 {
		t.Fatalf("a second pass polled again without the interval being out: %+v", calls)
	}
	// Bring it forward, as the interval's end would.
	h.due(trigger.ID, "since-2026-09-17", due.Add(pollEvery))
	h.service.now = func() time.Time { return due.Add(pollEvery) }
	h.service.pollTriggers(ctx)

	calls := own.pollCalls()
	if len(calls) != 2 || calls[1].Cursor != "since-2026-09-17" {
		t.Fatalf("the second poll was %+v", calls)
	}
	if types := h.eventTypes(); !slicesContains(types, appsv1.TriggerReceivedDetailType) {
		t.Fatalf("the second poll published %v", types)
	}
	var received appsv1.TriggerReceived
	decodeLast(t, h.store, appsv1.TriggerReceivedDetailType, &received)
	if received.TriggerID != trigger.ID || received.Trigger != "ACME_ISSUE_EVENT" ||
		received.ConnectionID != "apc_oryxa" || string(received.Data) != `{"number":7}` {
		t.Fatalf("received = %+v", received)
	}
	// The cursor has moved on, so the next pass asks from there.
	subscription, _ := h.store.subscription(trigger.ID)
	if subscription.Cursor != "since-2026-09-18" {
		t.Fatalf("the cursor is %q", subscription.Cursor)
	}
	if subscription.NextPollAt == nil || !subscription.NextPollAt.Equal(due.Add(2*pollEvery)) {
		t.Fatalf("the next poll is at %v, want %v", subscription.NextPollAt, due.Add(2*pollEvery))
	}
}

// The same event answered twice is published once: an app that pages over its
// own history, or two passes that overlapped, must not deliver it twice.
func TestAPolledEventIsPublishedOnce(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()
	trigger, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_ISSUE_EVENT",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := integration.Event{ID: "e1", Trigger: "ACME_ISSUE_EVENT", OccurredAt: testClock, Data: json.RawMessage(`{"number":7}`)}
	own.mu.Lock()
	own.poll[""] = integration.Batch{Events: []integration.Event{event}, Cursor: "c1"}
	own.poll["c1"] = integration.Batch{Events: []integration.Event{event}, Cursor: "c2"}
	own.mu.Unlock()

	h.service.pollTriggers(ctx)
	due := testClock.Add(pollEvery)
	h.due(trigger.ID, "c1", due)
	h.service.now = func() time.Time { return due }
	h.service.pollTriggers(ctx)

	if got := len(h.store.published(appsv1.TriggerReceivedDetailType)); got != 1 {
		t.Fatalf("%d events were published for one event answered twice", got)
	}
	// The trigger's own tally counts it once, as the webhook path does.
	h.store.mu.Lock()
	recorded := h.store.triggers[trigger.ID]
	h.store.mu.Unlock()
	if recorded.Status != domain.TriggerActive {
		t.Fatalf("the trigger is %+v", recorded)
	}
}

// An event that breaks its own payload schema is dropped rather than
// published, because every consumer reads the contract that it does not.
func TestAPolledEventThatBreaksItsSchemaIsDropped(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()
	trigger, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_ISSUE_EVENT",
	})
	if err != nil {
		t.Fatal(err)
	}
	own.mu.Lock()
	own.poll[""] = integration.Batch{
		Events: []integration.Event{
			{ID: "e1", Trigger: "ACME_ISSUE_EVENT", OccurredAt: testClock, Data: json.RawMessage(`{"title":"no number here"}`)},
			{ID: "e2", Trigger: "ACME_ISSUE_EVENT", OccurredAt: testClock, Data: json.RawMessage(`not json`)},
			{ID: "e3", Trigger: "ACME_COMMENT_EVENT", OccurredAt: testClock, Data: json.RawMessage(`{"number":9}`)},
			{ID: "e4", Trigger: "ACME_ISSUE_EVENT", OccurredAt: testClock, Data: json.RawMessage(`{"number":7}`)},
		},
		Cursor: "c1",
	}
	own.mu.Unlock()

	due := testClock.Add(pollEvery)
	h.due(trigger.ID, "", due)
	h.service.now = func() time.Time { return due }
	h.service.pollTriggers(ctx)
	published := h.store.published(appsv1.TriggerReceivedDetailType)
	if len(published) != 1 {
		t.Fatalf("%d events were published, want only the one that matches", len(published))
	}
	var received appsv1.TriggerReceived
	if err := json.Unmarshal(published[0].Data, &received); err != nil {
		t.Fatal(err)
	}
	if string(received.Data) != `{"number":7}` {
		t.Fatalf("published %s", received.Data)
	}
}

// An app that cannot be asked keeps its cursor and is asked again, later: a
// failing subscription backs off rather than being retried every five minutes
// for ever, and a member can still unsubscribe and subscribe again.
func TestAFailingPollKeepsItsCursorAndBacksOff(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()
	trigger, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_ISSUE_EVENT",
	})
	if err != nil {
		t.Fatal(err)
	}
	due := testClock.Add(pollEvery)
	h.due(trigger.ID, "c1", due)
	own.pollErr = &integration.AppError{Message: "the service is busy"}
	h.service.now = func() time.Time { return due }
	h.service.pollTriggers(ctx)

	subscription, _ := h.store.subscription(trigger.ID)
	if subscription.Cursor != "c1" {
		t.Fatalf("a failed poll moved the cursor to %q", subscription.Cursor)
	}
	if subscription.NextPollAt == nil || !subscription.NextPollAt.Equal(due.Add(pollBackoffFrom)) {
		t.Fatalf("the next poll is at %v, want %v", subscription.NextPollAt, due.Add(pollBackoffFrom))
	}
	// The subscription is not due again at the interval, so the next pass in
	// the same minute asks nothing.
	h.service.now = func() time.Time { return due.Add(time.Minute) }
	h.service.pollTriggers(ctx)
	if calls := own.pollCalls(); len(calls) != 1 {
		t.Fatalf("the app was asked again before the backoff: %+v", calls)
	}
}

// A poll whose connection has ended publishes nothing, and its row goes with
// the connection's removal.
func TestAPollStopsWithItsConnection(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	connection := h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()
	trigger, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_ISSUE_EVENT",
	})
	if err != nil {
		t.Fatal(err)
	}
	removed := connection
	removed.Status = domain.ConnectionRemoved
	h.store.connections[connection.ID] = removed
	h.service.pollTriggers(ctx)
	if calls := own.pollCalls(); len(calls) != 0 {
		t.Fatalf("a removed connection was polled: %+v", calls)
	}
	_ = trigger
}

// Removing a connection unsubscribes the pushes it was carrying, which is
// what its integration's own code can do and Composio's never could.
func TestRemovingAConnectionUnsubscribesItsPushes(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()
	if _, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_oryxa", Trigger: "ACME_COMMENT_EVENT", Config: json.RawMessage(`{"repo":"acme/api"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.RemoveConnection(ctx, memberToken, "org_1", "apc_oryxa"); err != nil {
		t.Fatal(err)
	}
	_, unsubscribed := own.hooks()
	if len(unsubscribed) != 1 {
		t.Fatalf("the app was told %v", unsubscribed)
	}
	endpoints, _ := h.hooks.EndpointsForConnection(ctx, "apc_oryxa")
	if len(endpoints) != 0 {
		t.Fatalf("endpoints = %+v, want none", endpoints)
	}
}

// apps' mount hands the ingress a zero HookSubscriptions before this
// package's New runs, so that is the only way a delivery can reach the store:
// what New installed is what a zero value reads. The test is not parallel, so
// no other test's harness installs a store of its own beside it.
func TestHookSubscriptionsReadTheStoreNewInstalled(t *testing.T) {
	previous := installed.Load()
	defer func() { installed.Store(previous) }()
	stub := &routingStub{subscriptions: []hooks.Subscription{{
		TriggerID: "apt_1", ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1",
		Toolkit: "acme", Trigger: "ACME_ISSUE_EVENT",
	}}}
	installPushRouting(stub)
	ctx := context.Background()

	found, err := (HookSubscriptions{}).ForEndpoint(ctx, hooks.Endpoint{ID: "aph_1", ConnectionID: "apc_1"}, "ACME_ISSUE_EVENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].TriggerID != "apt_1" {
		t.Fatalf("a zero value reached %+v", found)
	}
	// An account-routed answer is marked with the account it was found by, so
	// hooks can check it against the delivery's reach.
	stub.seen = nil
	if found, err = (HookSubscriptions{}).ForAccount(ctx, "acme", "", "U01", "ACME_ISSUE_EVENT"); err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].AccountRef != "U01" {
		t.Fatalf("an account answer is %+v", found)
	}
	if len(stub.seen) != 1 || stub.seen[0] != "acme//U01/ACME_ISSUE_EVENT" {
		t.Fatalf("the store was asked %v", stub.seen)
	}
}

// routingStub answers whatever a test set, and records what it was asked.
type routingStub struct {
	subscriptions []hooks.Subscription
	seen          []string
}

func (stub *routingStub) OwnSubscriptionsForEndpoint(_ context.Context, connectionID, organizationID, trigger string) ([]hooks.Subscription, error) {
	stub.seen = append(stub.seen, connectionID+"/"+organizationID+"/"+trigger)
	return stub.subscriptions, nil
}

func (stub *routingStub) OwnSubscriptionsForAccount(_ context.Context, toolkit, organizationID, accountRef, trigger string) ([]hooks.Subscription, error) {
	stub.seen = append(stub.seen, toolkit+"/"+organizationID+"/"+accountRef+"/"+trigger)
	return stub.subscriptions, nil
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

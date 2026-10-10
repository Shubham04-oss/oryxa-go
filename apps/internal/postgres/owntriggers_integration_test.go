package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
)

// ownConnection is an active Oryxa connection, which is the only kind a
// subscription through an integration can sit on.
func ownConnection(t *testing.T, store *Store, organizationID, memberID, toolkit string) domain.Connection {
	t.Helper()
	return activate(t, store, oryxaConnection(t, store, organizationID, memberID, toolkit, testClock))
}

// ownAccountConnection is an active Oryxa connection whose credential names
// the account accountRef, which is what a delivery delivered to an endpoint
// shared by many accounts is routed by.
func ownAccountConnection(t *testing.T, store *Store, organizationID, memberID, accountRef string) domain.Connection {
	t.Helper()
	connection := oryxaConnection(t, store, organizationID, memberID, "acme", testClock)
	credential := signin.Credential{
		ConnectionID: connection.ID, OrganizationID: organizationID, MemberID: memberID, Toolkit: "acme",
		Kind: integration.KindAPIToken, Version: 1,
		Settings: map[string]string{
			integration.SettingAccountLabel: "acme_house", integration.SettingAccountRef: accountRef,
		},
		Scopes: []string{"read"},
	}
	if err := store.InsertCredential(context.Background(), credential); err != nil {
		t.Fatalf("InsertCredential() = %v", err)
	}
	return activate(t, store, connection)
}

func ownSubscription(t *testing.T, store *Store, connection domain.Connection, slug string, kind string, config string, due *time.Time) application.OwnSubscription {
	t.Helper()
	subscription := application.OwnSubscription{
		ID: "apt_" + randomHex(8), OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, Slug: slug, Config: json.RawMessage(config),
		Status: domain.TriggerActive, Broker: domain.BrokerOryxa, Kind: kind,
		NextPollAt: due, CreatedAt: testClock, UpdatedAt: testClock,
	}
	created, ok, err := store.CreateOwnTrigger(context.Background(), subscription,
		testEvent(t, "apps.trigger.subscribed.v1"))
	if err != nil || !ok {
		t.Fatalf("CreateOwnTrigger() = %+v, %v, %v, want created", created, ok, err)
	}
	return created
}

// A subscription through an integration has no Composio instance, so it is
// keyed on its own connection, event and config, and every one of them may
// carry the empty instance id without colliding.
func TestOwnSubscriptionsAreKeyedOnTheirOwnKey(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	connection := ownConnection(t, store, "org_1", "member_1", "acme")

	first := ownSubscription(t, store, connection, "ACME_ISSUE_EVENT", integration.TriggerPoll, `{"repo":"acme/api"}`, nil)
	second := ownSubscription(t, store, connection, "ACME_COMMENT_EVENT", integration.TriggerPush, `{"repo":"acme/api"}`, nil)
	if first.ID == second.ID {
		t.Fatal("two events shared one subscription")
	}
	// The same connection, event and config is one subscription, whichever
	// replica asked and whichever way the config is written.
	again := application.OwnSubscription{
		ID: "apt_" + randomHex(8), OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, Slug: "ACME_ISSUE_EVENT",
		Config: json.RawMessage(`{"repo": "acme/api"}`), Status: domain.TriggerActive,
		Broker: domain.BrokerOryxa, Kind: integration.TriggerPoll, CreatedAt: testClock, UpdatedAt: testClock,
	}
	found, created, err := store.CreateOwnTrigger(ctx, again, testEvent(t, "apps.trigger.subscribed.v1"))
	if err != nil {
		t.Fatal(err)
	}
	if created || found.ID != first.ID {
		t.Fatalf("a second subscription was made: %+v, %v", found, created)
	}
	// A different config is a different subscription, which is what lets a
	// member watch two repositories at once.
	other := ownSubscription(t, store, connection, "ACME_ISSUE_EVENT", integration.TriggerPoll, `{"repo":"acme/web"}`, nil)
	if other.ID == first.ID {
		t.Fatal("a different config was taken for the same subscription")
	}
	// A Composio trigger on the same connection is untouched by any of it, and
	// its own instance index still holds.
	composio := testTrigger(t, store, connection, "ct_"+randomHex(8))
	own, isOwn, err := store.OwnSubscription(ctx, connection.OrganizationID, composio.ID)
	if err != nil {
		t.Fatal(err)
	}
	if isOwn {
		t.Fatalf("a Composio trigger was read as one of an integration's: %+v", own)
	}
	if _, isOwn, err = store.OwnSubscription(ctx, connection.OrganizationID, first.ID); err != nil || !isOwn {
		t.Fatalf("OwnSubscription(%s) is own = %v, %v", first.ID, isOwn, err)
	}
}

// A subscription is refused once its connection has ended, for the reason
// CreateTrigger refuses a Composio one: a pass racing a disconnect must not
// leave a row that looks live and that removal's own cleanup never sees.
func TestOwnSubscriptionIsRefusedOnAConnectionThatHasEnded(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	connection := ownConnection(t, store, "org_1", "member_1", "acme")
	ended := connection
	ended.Status = domain.ConnectionRemoved
	if _, err := store.RemoveConnection(ctx, connection.ID, "member_left", testClock,
		func(domain.Connection, []domain.Trigger) ([]domain.Event, error) {
			return nil, nil
		}); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.CreateOwnTrigger(ctx, application.OwnSubscription{
		ID: "apt_" + randomHex(8), OrganizationID: ended.OrganizationID, MemberID: ended.MemberID,
		ConnectionID: ended.ID, Toolkit: ended.Toolkit, Slug: "ACME_ISSUE_EVENT", Config: json.RawMessage(`{}`),
		Status: domain.TriggerActive, Broker: domain.BrokerOryxa, Kind: integration.TriggerPoll,
		CreatedAt: testClock, UpdatedAt: testClock,
	}, testEvent(t, "apps.trigger.subscribed.v1"))
	if err == nil {
		t.Fatal("a subscription was made on a connection that had ended")
	}
}

// A connection endpoint's deliveries reach that connection's own
// subscriptions and nothing else.
func TestOwnSubscriptionsForEndpointReachOnlyThatConnection(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	mine := ownConnection(t, store, "org_1", "member_1", "acme")
	theirs := ownConnection(t, store, "org_1", "member_2", "acme")
	ownSubscription(t, store, mine, "ACME_ISSUE_EVENT", integration.TriggerPoll, `{}`, nil)
	ownSubscription(t, store, theirs, "ACME_ISSUE_EVENT", integration.TriggerPoll, `{}`, nil)
	// Another organization's connection to the same app, which an
	// organization endpoint must not reach.
	elsewhere := ownConnection(t, store, "org_2", "member_1", "acme")
	ownSubscription(t, store, elsewhere, "ACME_ISSUE_EVENT", integration.TriggerPoll, `{}`, nil)

	found, err := store.OwnSubscriptionsForEndpoint(ctx, mine.ID, mine.OrganizationID, "ACME_ISSUE_EVENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ConnectionID != mine.ID || found[0].MemberID != "member_1" {
		t.Fatalf("a connection endpoint reached %+v", found)
	}
	if found[0].Toolkit != "acme" || found[0].Trigger != "ACME_ISSUE_EVENT" || found[0].OrganizationID != "org_1" {
		t.Fatalf("the subscription is %+v", found[0])
	}
	// An event the toolkit does not deliver reaches nobody.
	if found, err = store.OwnSubscriptionsForEndpoint(ctx, mine.ID, mine.OrganizationID, "ACME_OTHER_EVENT"); err != nil {
		t.Fatal(err)
	} else if len(found) != 0 {
		t.Fatalf("another event reached %+v", found)
	}
	// A removed subscription is no longer reached.
	if removed, err := store.RemoveTrigger(ctx, found2ID(t, store, mine.ID, "ACME_ISSUE_EVENT"),
		testClock, testEvent(t, "apps.trigger.unsubscribed.v1")); err != nil || !removed {
		t.Fatalf("RemoveTrigger() = %v, %v", removed, err)
	}
	if found, err = store.OwnSubscriptionsForEndpoint(ctx, mine.ID, mine.OrganizationID, "ACME_ISSUE_EVENT"); err != nil {
		t.Fatal(err)
	} else if len(found) != 0 {
		t.Fatalf("a removed subscription reached %+v", found)
	}
	// A Composio trigger on the same connection is not one of these.
	testTrigger(t, store, mine, "ct_"+randomHex(8))
	if found, err = store.OwnSubscriptionsForEndpoint(ctx, mine.ID, mine.OrganizationID, "GITHUB_ISSUE_CREATED"); err != nil {
		t.Fatal(err)
	} else if len(found) != 0 {
		t.Fatalf("a Composio trigger reached %+v", found)
	}
}

// found2ID is the id of the live subscription to slug on a connection, which
// the tests above need only to remove it again.
func found2ID(t *testing.T, store *Store, connectionID, slug string) string {
	t.Helper()
	var id string
	if err := store.pool.QueryRow(context.Background(),
		`SELECT id FROM apps_triggers WHERE connection_id = $1 AND slug = $2 AND status = 'active'`,
		connectionID, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// An endpoint shared by many accounts reaches a subscription only when the
// event names the account that subscription's connection is itself, which is
// what keeps an event about a private channel from reaching every member who
// could also see it.
func TestOwnSubscriptionsForAccountReachOnlyThatAccount(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	mine := ownAccountConnection(t, store, "org_1", "member_1", "U01")
	theirs := ownAccountConnection(t, store, "org_1", "member_2", "U02")
	elsewhere := ownAccountConnection(t, store, "org_2", "member_1", "U01")
	for _, connection := range []domain.Connection{mine, theirs, elsewhere} {
		ownSubscription(t, store, connection, "ACME_ISSUE_EVENT", integration.TriggerPush, `{}`, nil)
	}

	found, err := store.OwnSubscriptionsForAccount(ctx, "acme", "", "U01", "ACME_ISSUE_EVENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("an account's own event reached %d subscriptions: %+v", len(found), found)
	}
	// One in each organization, and nobody else's.
	organizations := map[string]bool{}
	for _, subscription := range found {
		organizations[subscription.OrganizationID] = true
	}
	if !organizations["org_1"] || !organizations["org_2"] {
		t.Fatalf("the organizations reached are %v", organizations)
	}
	for _, subscription := range found {
		if subscription.ConnectionID == theirs.ID {
			t.Fatal("another member's connection was reached")
		}
	}
	// An organization endpoint stays inside its own organization.
	found, err = store.OwnSubscriptionsForAccount(ctx, "acme", "org_1", "U01", "ACME_ISSUE_EVENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].OrganizationID != "org_1" {
		t.Fatalf("an organization's endpoint reached %+v", found)
	}
	// An account nobody holds reaches nobody.
	if found, err = store.OwnSubscriptionsForAccount(ctx, "acme", "", "U99", "ACME_ISSUE_EVENT"); err != nil {
		t.Fatal(err)
	} else if len(found) != 0 {
		t.Fatalf("another account reached %+v", found)
	}
	// A connection with no credential holds no account ref, so it is never
	// reached by a delivery that names one: an account the app never named is
	// not one to guess at.
	without := ownConnection(t, store, "org_1", "member_3", "acme")
	ownSubscription(t, store, without, "ACME_ISSUE_EVENT", integration.TriggerPush, `{}`, nil)
	if found, err = store.OwnSubscriptionsForAccount(ctx, "acme", "", "U01", "ACME_ISSUE_EVENT"); err != nil {
		t.Fatal(err)
	} else if len(found) != 2 {
		t.Fatalf("a connection with no credential was or was not reached: %+v", found)
	}
}

// A poll is claimed once, and the claim moves the due time so a second
// replica's next pass leaves the subscription alone.
func TestClaimPollsLeasesEachSubscriptionOnce(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	connection := ownConnection(t, store, "org_1", "member_1", "acme")
	now := testClock.Add(time.Minute)
	due := testClock.Add(time.Minute)
	polled := ownSubscription(t, store, connection, "ACME_ISSUE_EVENT", integration.TriggerPoll, `{"repo":"a"}`, &due)
	// A pushed subscription is never claimed, whatever its due time says.
	ownSubscription(t, store, connection, "ACME_COMMENT_EVENT", integration.TriggerPush, `{}`, &due)
	// One that is not due yet is not claimed either.
	later := ownSubscription(t, store, connection, "ACME_OTHER_EVENT", integration.TriggerPoll, `{"repo":"b"}`, nil)
	if later.NextPollAt != nil {
		t.Fatal("the subscription was given a due time it should not have")
	}

	claimed, err := store.ClaimPolls(ctx, now, now.Add(2*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != polled.ID {
		t.Fatalf("claimed %+v, want only the due poll", claimed)
	}
	if claimed[0].Cursor != "" || claimed[0].Broker != domain.BrokerOryxa {
		t.Fatalf("the claimed subscription is %+v", claimed[0])
	}
	// A second pass at the same moment finds nothing: the claim moved it.
	if claimed, err = store.ClaimPolls(ctx, now, now.Add(2*time.Minute), 10); err != nil {
		t.Fatal(err)
	} else if len(claimed) != 0 {
		t.Fatalf("a second claim took %+v", claimed)
	}
	// The lease has run out, so it may be claimed again.
	if claimed, err = store.ClaimPolls(ctx, now.Add(3*time.Minute), now.Add(5*time.Minute), 10); err != nil {
		t.Fatal(err)
	} else if len(claimed) != 1 || claimed[0].ID != polled.ID {
		t.Fatalf("after the lease, claimed %+v", claimed)
	}
}

// SavePoll is where the cursor lives, and it releases the claim by moving the
// due time on: a subscription whose due time has moved is not due again.
func TestSavePollRecordsTheCursorAndReleasesTheClaim(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	connection := ownConnection(t, store, "org_1", "member_1", "acme")
	due := testClock.Add(time.Minute)
	subscription := ownSubscription(t, store, connection, "ACME_ISSUE_EVENT", integration.TriggerPoll, `{}`, &due)
	if _, err := store.ClaimPolls(ctx, testClock.Add(time.Minute), testClock.Add(2*time.Minute), 10); err != nil {
		t.Fatal(err)
	}
	next := testClock.Add(10 * time.Minute)
	if err := store.SavePoll(ctx, subscription.ID, "since-2026-09-17", next); err != nil {
		t.Fatal(err)
	}
	read, ok, err := store.OwnSubscription(ctx, connection.OrganizationID, subscription.ID)
	if err != nil || !ok {
		t.Fatalf("OwnSubscription() = %+v, %v, %v", read, ok, err)
	}
	if read.Cursor != "since-2026-09-17" {
		t.Fatalf("the cursor is %q", read.Cursor)
	}
	if read.NextPollAt == nil || !read.NextPollAt.Equal(next) {
		t.Fatalf("the next poll is at %v, want %v", read.NextPollAt, next)
	}
	if claimed, err := store.ClaimPolls(ctx, testClock.Add(2*time.Minute), testClock.Add(3*time.Minute), 10); err != nil {
		t.Fatal(err)
	} else if len(claimed) != 0 {
		t.Fatalf("a saved poll was claimed again at once: %+v", claimed)
	}
	// The poll's tally is the trigger's own, as a pushed delivery's is.
	// The same event answered twice is published once, and the trigger's own
	// tally counts it once, as a pushed delivery's is.
	first, err := store.RecordTriggerDelivery(ctx, "trigger:"+subscription.ID+":e1",
		"apps.trigger.received.v1", subscription.ID, testClock, testEvent(t, "apps.trigger.received.v1"))
	if err != nil || !first {
		t.Fatalf("RecordTriggerDelivery() = %v, %v, want recorded", first, err)
	}
	again, err := store.RecordTriggerDelivery(ctx, "trigger:"+subscription.ID+":e1",
		"apps.trigger.received.v1", subscription.ID, testClock, testEvent(t, "apps.trigger.received.v1"))
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("the same event was published twice")
	}
	trigger, err := store.Trigger(ctx, connection.OrganizationID, subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.DeliveryCount != 1 {
		t.Fatalf("the trigger counted %d deliveries for one event", trigger.DeliveryCount)
	}
}

// The new columns default the way a Composio row has always been left, so
// every row written before the migration still reads as Composio's.
func TestTriggerBrokerDefaultsToComposio(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	pending, _ := testConnection(t, store, "org_1", "member_1")
	connection := activate(t, store, pending)
	testTrigger(t, store, connection, "ct_"+randomHex(8))

	var broker, cursor, ownKey string
	var nextPollAt, claimedUntil *time.Time
	if err := store.pool.QueryRow(ctx, `
		SELECT broker, cursor, own_key, next_poll_at, poll_claimed_until
		FROM apps_triggers WHERE connection_id = $1`, connection.ID,
	).Scan(&broker, &cursor, &ownKey, &nextPollAt, &claimedUntil); err != nil {
		t.Fatal(err)
	}
	if broker != "" || cursor != "" || ownKey != "" || nextPollAt != nil || claimedUntil != nil {
		t.Fatalf("a Composio trigger reads as %q/%q/%q/%v/%v", broker, cursor, ownKey, nextPollAt, claimedUntil)
	}
}

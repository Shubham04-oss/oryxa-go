package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// awaiting records a connection that has just become active and is owed its
// subscriptions, the way the callback leaves one.
func (h harness) awaiting(id, toolkit, accountID string) domain.Connection {
	connection := h.activeConnection(id, "user-1", toolkit, accountID)
	connection.AutoSubscribe = true
	h.store.connections[id] = connection
	return connection
}

// TestConnectingSubscribesToEverythingTheAppDelivers is the whole point: a
// member connects an app and starts hearing from it, rather than being handed
// a list of its events to pick from.
func TestConnectingSubscribesToEverythingTheAppDelivers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.awaiting("apc_1", "github", "ca_1")
	// Composio answers the same instance for the same account and trigger,
	// so each needs its own id for the store to keep them apart.
	h.composio.upsertPerSlug = true

	h.service.maintain(context.Background())

	subscribed := map[string]domain.Trigger{}
	for _, trigger := range h.store.triggers {
		subscribed[trigger.Slug] = trigger
	}
	// The GitHub definitions the harness holds: one needing a repository,
	// one needing nothing.
	if len(subscribed) != 1 {
		t.Fatalf("subscribed to %d events, want the one that needs no configuration: %v", len(subscribed), subscribed)
	}
	star, ok := subscribed["GITHUB_STAR_ADDED_EVENT"]
	if !ok {
		t.Fatalf("did not subscribe to the event needing no configuration: %v", subscribed)
	}
	if star.MemberID != connection.MemberID || star.ConnectionID != connection.ID {
		t.Fatalf("the subscription is not the connection's own: %+v", star)
	}
	if star.Kind != domain.TriggerKindPoll {
		t.Fatalf("kind = %q, want what the catalog said", star.Kind)
	}
	// It announces itself like any other subscription.
	if events := h.store.published(appsv1.TriggerSubscribedDetailType); len(events) != 1 {
		t.Fatalf("subscribed events = %d, want 1", len(events))
	}
	// And the connection is marked, so the next pass leaves it alone.
	if h.store.connections[connection.ID].AutoSubscribedAt == nil {
		t.Fatal("the connection was not marked subscribed")
	}
}

// TestAnEventNeedingConfigurationIsLeftForTheMember covers the one thing
// connecting cannot decide: a trigger whose schema names a repository or a
// channel has no answer that would not be a guess.
func TestAnEventNeedingConfigurationIsLeftForTheMember(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.awaiting("apc_1", "github", "ca_1")
	h.composio.upsertPerSlug = true

	h.service.maintain(context.Background())

	for _, trigger := range h.store.triggers {
		if trigger.Slug == "GITHUB_COMMIT_EVENT" {
			t.Fatal("subscribed to an event whose configuration nobody supplied")
		}
	}
	// Nothing was sent to Composio for it either.
	for _, upsert := range h.composio.upserts {
		if strings.HasPrefix(upsert, "GITHUB_COMMIT_EVENT@") {
			t.Fatalf("asked Composio to subscribe it anyway: %v", h.composio.upserts)
		}
	}
}

// TestSubscribingRunsOnceEvenIfTheWorkerRunsAgain keeps a member from being
// resubscribed on every maintenance tick.
func TestSubscribingRunsOnceEvenIfTheWorkerRunsAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.awaiting("apc_1", "github", "ca_1")
	h.composio.upsertPerSlug = true

	for range 3 {
		h.service.maintain(context.Background())
	}
	if calls := len(h.composio.upserts); calls != 1 {
		t.Fatalf("asked Composio %d times, want once", calls)
	}
	if events := h.store.published(appsv1.TriggerSubscribedDetailType); len(events) != 1 {
		t.Fatalf("subscribed events = %d, want 1", len(events))
	}
}

// TestAConnectionThatSaidNoIsNotSubscribed covers the member's own choice,
// which outranks the deployment's default.
func TestAConnectionThatSaidNoIsNotSubscribed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.awaiting("apc_1", "github", "ca_1")
	connection.AutoSubscribe = false
	h.store.connections[connection.ID] = connection

	h.service.maintain(context.Background())

	if len(h.store.triggers) != 0 {
		t.Fatalf("subscribed anyway: %v", h.store.triggers)
	}
	if h.store.connections[connection.ID].AutoSubscribedAt != nil {
		t.Fatal("a connection that wanted nothing was marked as having been subscribed")
	}
}

// TestADeploymentCanTurnItOff covers APPS_AUTO_SUBSCRIBE=false.
func TestADeploymentCanTurnItOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.service.autoSubscribe = false
	h.awaiting("apc_1", "github", "ca_1")

	h.service.maintain(context.Background())

	if len(h.store.triggers) != 0 {
		t.Fatalf("subscribed although the deployment turned it off: %v", h.store.triggers)
	}
}

// TestConnectRecordsWhatTheMemberChose covers the two ways the choice is
// made, since the connection carries it from the moment it is created.
func TestConnectRecordsWhatTheMemberChose(t *testing.T) {
	t.Parallel()
	no := false
	yes := true
	for name, testCase := range map[string]struct {
		deploymentOff bool
		asked         *bool
		want          bool
	}{
		"by default, everything":         {want: true},
		"the member said no":             {asked: &no, want: false},
		"the deployment said no":         {deploymentOff: true, want: false},
		"the member said yes regardless": {deploymentOff: true, asked: &yes, want: true},
		"the member said no regardless":  {asked: &no, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.service.autoSubscribe = !testCase.deploymentOff
			h.composio.toolkits["github"] = composio.Toolkit{Slug: "github", ComposioManagedAuthSchemes: []string{"OAUTH2"}}

			connection, _, err := h.service.Connect(context.Background(), memberToken, "org_1",
				ConnectInput{Toolkit: "github", AutoSubscribe: testCase.asked})
			if err != nil {
				t.Fatal(err)
			}
			if connection.AutoSubscribe != testCase.want {
				t.Fatalf("auto subscribe = %v, want %v", connection.AutoSubscribe, testCase.want)
			}
		})
	}
}

// TestSubscribingIsBoundedSoConnectingCannotCommitSomeoneToHundreds is the
// safety rail: every subscription that fires wakes Krude, which costs a run,
// so connecting an app has a ceiling on what it signs a member up for.
func TestSubscribingIsBoundedSoConnectingCannotCommitSomeoneToHundreds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var many []composio.TriggerType
	for index := range AutoSubscribeLimit + 10 {
		many = append(many, composio.TriggerType{
			Slug:    "BIGAPP_EVENT_" + string(rune('A'+index%26)) + string(rune('A'+index/26)),
			Toolkit: "bigapp", Kind: "webhook", Config: json.RawMessage(`{"type":"object","properties":{}}`),
		})
	}
	h.composio.triggerTypes["bigapp"] = many
	h.awaiting("apc_1", "bigapp", "ca_1")
	h.composio.upsertPerSlug = true

	h.service.maintain(context.Background())

	if len(h.store.triggers) != AutoSubscribeLimit {
		t.Fatalf("subscribed to %d events, want the ceiling of %d", len(h.store.triggers), AutoSubscribeLimit)
	}
}

// TestAnAppThatCannotBeReadIsTriedAgain keeps a Composio outage during the
// pass from silently costing a member every subscription they were owed.
func TestAnAppThatCannotBeReadIsTriedAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.awaiting("apc_1", "github", "ca_1")
	h.composio.triggerTypeErr = composio.ErrUnavailable

	h.service.maintain(context.Background())
	if h.store.connections[connection.ID].AutoSubscribedAt != nil {
		t.Fatal("a connection nothing could be read for was marked done")
	}

	// Composio comes back, and the next pass does the work.
	h.composio.triggerTypeErr = nil
	h.composio.upsertPerSlug = true
	h.service.maintain(context.Background())
	if len(h.store.triggers) == 0 {
		t.Fatal("the retry subscribed nothing")
	}
	if h.store.connections[connection.ID].AutoSubscribedAt == nil {
		t.Fatal("the retry did not mark the connection done")
	}
}

// TestAnAppThatKeepsFailingIsGivenUpOn stops a broken toolkit being retried
// on every tick for ever.
func TestAnAppThatKeepsFailingIsGivenUpOn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.awaiting("apc_1", "github", "ca_1")
	h.composio.triggerTypeErr = composio.ErrUnavailable

	for range autoSubscribeAttempts + 3 {
		h.service.maintain(context.Background())
	}
	if reads := h.composio.triggerLists; reads > autoSubscribeAttempts {
		t.Fatalf("read the catalog %d times, want at most %d", reads, autoSubscribeAttempts)
	}
}

// TestAutoSubscribableOrdersWhatItTakesAndNamesWhatItLeaves keeps two
// deployments of the same app committing a member to the same events, and
// keeps what was left out nameable rather than merely counted.
func TestAutoSubscribableOrdersWhatItTakesAndNamesWhatItLeaves(t *testing.T) {
	t.Parallel()
	definitions := map[string]domain.TriggerDefinition{
		"Z_NEEDS_NOTHING": {Slug: "Z_NEEDS_NOTHING", ConfigSchema: json.RawMessage(`{"type":"object"}`)},
		"A_NEEDS_NOTHING": {Slug: "A_NEEDS_NOTHING"},
		"M_NEEDS_A_REPO": {Slug: "M_NEEDS_A_REPO",
			ConfigSchema: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}},"required":["repo"]}`)},
	}
	wanted, skipped := autoSubscribable(definitions)

	if len(wanted) != 2 || wanted[0].Slug != "A_NEEDS_NOTHING" || wanted[1].Slug != "Z_NEEDS_NOTHING" {
		t.Fatalf("wanted = %v, want both configuration-free events in slug order", slugs(wanted))
	}
	if len(skipped) != 1 || skipped[0] != "M_NEEDS_A_REPO" {
		t.Fatalf("skipped = %v, want the one needing a repository", skipped)
	}
}

package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

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

	h.service.subscribeConnections(context.Background())

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

// A member who connects an app lands back in it moments later and looks for
// its events, so completing the connection wakes the subscriber rather than
// leaving it to the next sweep.
func TestCompletingAConnectionWakesTheSubscriber(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection, state := h.pending(t, "github")
	h.composio.accounts[connection.ComposioAccountID] = []composio.ConnectedAccount{account(connection, composio.StatusActive)}

	if _, err := h.service.CompleteConnection(context.Background(), state, "success", connection.ComposioAccountID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.service.subscribeWake:
	default:
		t.Fatal("completing a connection did not wake the subscriber")
	}
}

// Woken, the subscriber subscribes at once instead of at its next look.
func TestRunAutoSubscribeSubscribesAsSoonAsWoken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.upsertPerSlug = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.service.RunAutoSubscribe(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Owed only after its first look has passed, so nothing but the wake
	// can find it before autoSubscribeEvery.
	time.Sleep(50 * time.Millisecond)
	h.store.mu.Lock()
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	connection.AutoSubscribe = true
	h.store.connections[connection.ID] = connection
	h.store.mu.Unlock()
	h.service.wakeSubscriber()

	deadline := time.Now().Add(5 * time.Second)
	for {
		h.store.mu.Lock()
		subscribed := h.store.connections[connection.ID].AutoSubscribedAt != nil
		h.store.mu.Unlock()
		if subscribed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the woken subscriber did not subscribe the connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunAutoSubscribeStopsWithItsContext(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.service.RunAutoSubscribe(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscriber did not stop")
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

	h.service.subscribeConnections(context.Background())

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
		h.service.subscribeConnections(context.Background())
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

	h.service.subscribeConnections(context.Background())

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

	h.service.subscribeConnections(context.Background())

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

	h.service.subscribeConnections(context.Background())

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

	h.service.subscribeConnections(context.Background())
	if h.store.connections[connection.ID].AutoSubscribedAt != nil {
		t.Fatal("a connection nothing could be read for was marked done")
	}

	// Composio comes back, and the next pass does the work.
	h.composio.triggerTypeErr = nil
	h.composio.upsertPerSlug = true
	h.service.subscribeConnections(context.Background())
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
		h.service.subscribeConnections(context.Background())
	}
	if reads := h.composio.triggerLists; reads > autoSubscribeAttempts {
		t.Fatalf("read the catalog %d times, want at most %d", reads, autoSubscribeAttempts)
	}
}

// TestAutoSubscribeGivingUpIsLogged covers the alternative to a silent
// auto_subscribed_at that stays NULL forever: on the pass that uses up the
// connection's last allowed attempt, a warning says so, naming how many were
// tried, so an operator - or the member - is not left guessing why an app's
// events never arrived.
// TestAPassWhereEveryEventIsRefusedIsTriedAgain: one event refused is that
// event's business, but every one refused says something about the request
// or the project. The first live run met exactly that — a project wanting a
// field every subscription left out — and the pass was marked done with
// nothing subscribed. It is a failed pass now, tried again.
func TestAPassWhereEveryEventIsRefusedIsTriedAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.awaiting("apc_1", "github", "ca_1")
	h.composio.upsertErr = fmt.Errorf("%w: user_id is required to create or update a trigger", composio.ErrInvalid)

	h.service.subscribeConnections(context.Background())
	if h.store.connections[connection.ID].AutoSubscribedAt != nil {
		t.Fatal("a pass that subscribed nothing was marked done")
	}

	// Once the refusal stops, the next pass subscribes and finishes.
	h.composio.upsertErr = nil
	h.composio.upsertPerSlug = true
	h.service.subscribeConnections(context.Background())
	if h.store.connections[connection.ID].AutoSubscribedAt == nil {
		t.Fatal("the connection was not subscribed once Composio accepted it")
	}
	if len(h.store.triggers) == 0 {
		t.Fatal("nothing was subscribed")
	}
}

func TestAutoSubscribeGivingUpIsLogged(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	fake := newFakeComposio()
	fake.triggerTypeErr = composio.ErrUnavailable
	var logs bytes.Buffer
	service, err := New(Dependencies{
		Store: store, Directory: &fakeDirectory{}, Composio: fake,
		Clock:  func() time.Time { return testClock },
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}, Config{PublicBaseURL: "https://apps.oryxa.test", WebhookSecret: "whsec_test"})
	if err != nil {
		t.Fatal(err)
	}
	connected := testClock
	connection := domain.Connection{
		ID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
		ComposioAccountID: "ca_1", Status: domain.ConnectionActive, AutoSubscribe: true,
		CreatedAt: testClock, UpdatedAt: testClock, ConnectedAt: &connected,
	}
	store.connections[connection.ID] = connection

	for attempt := 1; attempt <= autoSubscribeAttempts; attempt++ {
		service.subscribeConnections(context.Background())
		if attempt < autoSubscribeAttempts && strings.Contains(logs.String(), "gave up") {
			t.Fatalf("gave up before the last attempt (attempt %d of %d): %s", attempt, autoSubscribeAttempts, logs.String())
		}
	}

	if !strings.Contains(logs.String(), "auto-subscribe gave up") {
		t.Fatalf("logs = %s, want a warning that auto-subscribe gave up", logs.String())
	}
	if store.connections[connection.ID].AutoSubscribedAt != nil {
		t.Fatal("a connection nothing could be read for was marked done")
	}
	if got := store.connections[connection.ID].AutoSubscribeAttempts; got != autoSubscribeAttempts {
		t.Fatalf("attempts = %d, want %d", got, autoSubscribeAttempts)
	}

	// It gave up: a further pass does not read the catalog again, and does
	// not repeat the warning.
	logs.Reset()
	service.subscribeConnections(context.Background())
	if reads := fake.triggerLists; reads != autoSubscribeAttempts {
		t.Fatalf("read the catalog %d times, want exactly %d (no further attempts)", reads, autoSubscribeAttempts)
	}
	if logs.Len() != 0 {
		t.Fatalf("logged again after giving up: %s", logs.String())
	}
}

// TestAutoSubscribeSkipsWhatIsAlreadyOnTheConnection covers a slug the member
// already subscribed - maybe with their own filters, maybe from an earlier
// partial pass - and one they removed: connecting again must not add a
// second, unfiltered instance beside the first, and must not bring back what
// the member took away. It still subscribes everything else.
func TestAutoSubscribeSkipsWhatIsAlreadyOnTheConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Two more configuration-free GitHub events than the harness's default
	// catalog, so there is something left to subscribe to once X and Y below
	// are skipped.
	h.composio.triggerTypes["github"] = append(h.composio.triggerTypes["github"],
		composio.TriggerType{
			Slug: "GITHUB_FORK_EVENT", Toolkit: "github", Kind: "webhook",
			Config: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		composio.TriggerType{
			Slug: "GITHUB_RELEASE_EVENT", Toolkit: "github", Kind: "webhook",
			Config: json.RawMessage(`{"type":"object","properties":{}}`),
		},
	)
	connection := h.awaiting("apc_1", "github", "ca_1")
	h.composio.upsertPerSlug = true

	// X: already live, with the member's own filter. A second, unfiltered
	// subscription beside it would be a bug, not a convenience.
	h.store.triggers["apt_x"] = domain.Trigger{
		ID: "apt_x", OrganizationID: "org_1", MemberID: connection.MemberID, ConnectionID: connection.ID,
		Toolkit: "github", Slug: "GITHUB_STAR_ADDED_EVENT", Config: json.RawMessage(`{"query":"from:boss"}`),
		ComposioTriggerID: "ti_x", Status: domain.TriggerActive,
	}
	// Y: the member removed this. It must never come back on its own.
	h.store.triggers["apt_y"] = domain.Trigger{
		ID: "apt_y", OrganizationID: "org_1", MemberID: connection.MemberID, ConnectionID: connection.ID,
		Toolkit: "github", Slug: "GITHUB_FORK_EVENT", ComposioTriggerID: "ti_y", Status: domain.TriggerRemoved,
	}

	h.service.subscribeConnections(context.Background())

	bySlug := map[string]bool{}
	for _, trigger := range h.store.triggers {
		if trigger.ConnectionID == connection.ID {
			bySlug[trigger.Slug] = true
		}
	}
	for _, upsert := range h.composio.upserts {
		if strings.HasPrefix(upsert, "GITHUB_STAR_ADDED_EVENT@") || strings.HasPrefix(upsert, "GITHUB_FORK_EVENT@") {
			t.Fatalf("resubscribed a slug already on the connection: %v", h.composio.upserts)
		}
	}
	if !bySlug["GITHUB_RELEASE_EVENT"] {
		t.Fatalf("did not subscribe to the other configuration-free event: %v", bySlug)
	}
	// X's own row is untouched: still the member's filtered instance, not
	// replaced by a fresh unfiltered one.
	if got := h.store.triggers["apt_x"]; got.ComposioTriggerID != "ti_x" || string(got.Config) != `{"query":"from:boss"}` {
		t.Fatalf("the existing filtered trigger was touched: %+v", got)
	}
	if h.store.connections[connection.ID].AutoSubscribedAt == nil {
		t.Fatal("the connection was not marked subscribed")
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

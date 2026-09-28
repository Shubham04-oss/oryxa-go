package application

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// withEndpoint gives a connection a webhook endpoint of its own.
func (h oryxaHarness) withEndpoint(t *testing.T, connection domain.Connection) hooks.Endpoint {
	t.Helper()
	created, err := h.hooks.Create(context.Background(), hooks.CreateInput{
		Toolkit: connection.Toolkit, Scope: integration.HookScopeConnection,
		OrganizationID: connection.OrganizationID, ConnectionID: connection.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.Endpoint
}

func TestRemovingAnOryxaConnectionRevokesItAtTheApp(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	connection := h.oryxaConnection("apc_oryxa", "user-1", "github", domain.ConnectionActive, integration.KindOAuth2Code)
	endpoint := h.withEndpoint(t, connection)
	h.signin.revocation = signin.Revocation{ProviderRevoked: true}

	if err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", connection.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.signin.revoked, []string{connection.ID}) {
		t.Fatalf("revoked = %v", h.signin.revoked)
	}
	if !slices.Equal(h.hooks.deleted, []string{connection.ID}) {
		t.Fatalf("endpoints deleted for = %v", h.hooks.deleted)
	}
	if _, left := h.hooks.endpoints[endpoint.ID]; left {
		t.Fatal("the connection's endpoint outlived it")
	}
	if got := h.store.connection(connection.ID); got.Status != domain.ConnectionRemoved {
		t.Fatalf("connection = %+v", got)
	}
	var removed appsv1.ConnectionRemoved
	decodeLast(t, h.store, appsv1.ConnectionRemovedDetailType, &removed)
	if removed.Broker != appsv1.BrokerOryxa || removed.Reason != appsv1.RemovedByMember ||
		removed.ProviderRevoked == nil || !*removed.ProviderRevoked {
		t.Fatalf("removed = %+v", removed)
	}
	// Composio holds nothing of it, so nothing is asked of Composio.
	if len(h.composio.deleted) != 0 || len(h.composio.deletedTriggers) != 0 {
		t.Fatalf("composio deleted %v and triggers %v", h.composio.deleted, h.composio.deletedTriggers)
	}
}

// An app that does not answer never blocks the removal: signin keeps the
// sealed credential and tries again once the connection has ended.
func TestRemovingAnOryxaConnectionWhoseAppDoesNotAnswerStillRemovesIt(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	connection := h.oryxaConnection("apc_oryxa", "user-1", "github", domain.ConnectionActive, integration.KindOAuth2Code)
	h.signin.revocation = signin.Revocation{Pending: true}

	if err := h.service.RemoveConnection(context.Background(), adminToken, "org_1", connection.ID); err != nil {
		t.Fatal(err)
	}
	var removed appsv1.ConnectionRemoved
	decodeLast(t, h.store, appsv1.ConnectionRemovedDetailType, &removed)
	if removed.Reason != appsv1.RemovedByAdmin || removed.ProviderRevoked == nil || *removed.ProviderRevoked {
		t.Fatalf("removed = %+v", removed)
	}
	if got := h.store.connection(connection.ID); got.Status != domain.ConnectionRemoved {
		t.Fatalf("connection = %+v", got)
	}
}

func TestRemovingAnOryxaConnectionIsRetriedWhenTheStoreFails(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	connection := h.oryxaConnection("apc_oryxa", "user-1", "github", domain.ConnectionActive, integration.KindAPIToken)
	h.signin.revokeErr = errBoom

	if err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", connection.ID); !errors.Is(err, errBoom) {
		t.Fatalf("error = %v", err)
	}
	if got := h.store.connection(connection.ID); got.Status != domain.ConnectionActive {
		t.Fatalf("connection = %+v, want it left for a retry", got)
	}
}

func TestAMemberLeavingTakesTheirOryxaAndComposioConnections(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	oryxa := h.oryxaConnection("apc_oryxa", "user-1", "github", domain.ConnectionActive, integration.KindAPIToken)
	composioConnection := h.composioConnection("apc_composio", "user-1", "slack")
	kept := h.oryxaConnection("apc_kept", "user-2", "github", domain.ConnectionActive, integration.KindAPIToken)

	if err := h.service.MemberRemoved(context.Background(), authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.signin.revoked, []string{oryxa.ID}) {
		t.Fatalf("revoked = %v", h.signin.revoked)
	}
	if !slices.Equal(h.composio.deleted, []string{composioConnection.ComposioAccountID}) {
		t.Fatalf("composio deleted = %v", h.composio.deleted)
	}
	for _, id := range []string{oryxa.ID, composioConnection.ID} {
		if got := h.store.connection(id); got.Status != domain.ConnectionRemoved || got.StatusReason != appsv1.RemovedMemberLeft {
			t.Fatalf("%s = %+v", id, got)
		}
	}
	if got := h.store.connection(kept.ID); got.Status != domain.ConnectionActive {
		t.Fatalf("another member's connection went too: %+v", got)
	}
	// Composio's removal stays exactly what it was: no broker, no revocation
	// field.
	for _, event := range h.store.published(appsv1.ConnectionRemovedDetailType) {
		var removed map[string]any
		if err := json.Unmarshal(event.Data, &removed); err != nil {
			t.Fatal(err)
		}
		if removed["connection_id"] == composioConnection.ID {
			if _, ok := removed["broker"]; ok {
				t.Fatalf("Composio's removal carries a broker: %v", removed)
			}
			if _, ok := removed["provider_revoked"]; ok {
				t.Fatalf("Composio's removal carries provider_revoked: %v", removed)
			}
		}
	}
}

func TestAnOrganizationDeletedTakesItsOryxaConnections(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	first := h.oryxaConnection("apc_1", "user-1", "github", domain.ConnectionActive, integration.KindAPIToken)
	second := h.oryxaConnection("apc_2", "user-2", "github", domain.ConnectionPending, "")

	if err := h.service.OrganizationDeleted(context.Background(), authv1.OrganizationDeleted{OrganizationID: "org_1"}); err != nil {
		t.Fatal(err)
	}
	for _, connection := range []domain.Connection{first, second} {
		if got := h.store.connection(connection.ID); got.Status != domain.ConnectionRemoved || got.StatusReason != appsv1.RemovedOrganizationDeleted {
			t.Fatalf("%s = %+v", connection.ID, got)
		}
	}
	if len(h.signin.revoked) != 2 {
		t.Fatalf("revoked = %v", h.signin.revoked)
	}
}

func TestOryxaUpkeepExpiresWhatAHealthCheckRejects(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	rejected := h.oryxaConnection("apc_rejected", "user-1", "github", domain.ConnectionActive, integration.KindOAuth2Code)
	passed := h.oryxaConnection("apc_passed", "user-2", "github", domain.ConnectionActive, integration.KindAPIToken)
	h.signin.checks = []signin.Check{
		{ConnectionID: rejected.ID, OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Outcome: signin.CheckRejected, Reason: signin.ExpiredRefreshRejected},
		{ConnectionID: passed.ID, OrganizationID: "org_1", MemberID: "user-2", Toolkit: "github", Outcome: signin.CheckPassed},
	}

	h.service.maintainOryxa(context.Background())

	if got := h.store.connection(rejected.ID); got.Status != domain.ConnectionExpired || got.StatusReason != "the app no longer accepts the sign-in" {
		t.Fatalf("rejected = %+v", got)
	}
	if got := h.store.connection(passed.ID); got.Status != domain.ConnectionActive {
		t.Fatalf("passed = %+v", got)
	}
	var expired appsv1.ConnectionExpired
	decodeLast(t, h.store, appsv1.ConnectionExpiredDetailType, &expired)
	if expired.ConnectionID != rejected.ID || expired.Reason != appsv1.ExpiredRefreshRejected || expired.Broker != appsv1.BrokerOryxa {
		t.Fatalf("expired = %+v", expired)
	}
	if h.signin.checkCalls == 0 || h.signin.retryCalls != 1 || h.hooks.swept != 1 {
		t.Fatalf("checks %d, revocation passes %d, sweeps %d", h.signin.checkCalls, h.signin.retryCalls, h.hooks.swept)
	}
}

func TestOryxaUpkeepFailsSigninsNobodyFinished(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	abandoned := h.oryxaConnection("apc_abandoned", "user-1", "github", domain.ConnectionPending, "")
	abandoned.CreatedAt = testClock.Add(-abandonAfter - time.Minute)
	h.store.connections[abandoned.ID] = abandoned
	fresh := h.oryxaConnection("apc_fresh", "user-2", "github", domain.ConnectionPending, "")
	composioPending := h.composioConnection("apc_composio", "user-3", "github")
	composioPending.Status, composioPending.CreatedAt = domain.ConnectionPending, testClock.Add(-time.Hour)
	h.store.connections[composioPending.ID] = composioPending

	h.service.maintainOryxa(context.Background())

	if got := h.store.connection(abandoned.ID); got.Status != domain.ConnectionFailed {
		t.Fatalf("abandoned = %+v", got)
	}
	var failed appsv1.ConnectionFailed
	decodeLast(t, h.store, appsv1.ConnectionFailedDetailType, &failed)
	if failed.ConnectionID != abandoned.ID || failed.Reason != appsv1.FailedAbandoned || failed.Broker != appsv1.BrokerOryxa {
		t.Fatalf("failed = %+v", failed)
	}
	if got := h.store.connection(fresh.ID); got.Status != domain.ConnectionPending {
		t.Fatalf("a sign-in still in its flow was failed: %+v", got)
	}
	// Composio's pending connections are its own sweep's, a day later.
	if got := h.store.connection(composioPending.ID); got.Status != domain.ConnectionPending {
		t.Fatalf("a Composio connection was failed: %+v", got)
	}
}

func TestOryxaUpkeepDoesNothingWhereItIsNotConfigured(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.service.RunOryxa(ctx) // returns at once rather than waiting on nothing
}

// Composio's key cannot act through a connection Composio does not hold, so
// every Composio path refuses an Oryxa connection, saying so, rather than
// calling Composio with an account it never had.
func TestComposioPathsRefuseAnOryxaConnection(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	connection := h.oryxaConnection("apc_oryxa", "user-1", "github", domain.ConnectionActive, integration.KindOAuth2Code)
	connection.AutoSubscribe = true
	h.store.connections[connection.ID] = connection
	h.composio.tools["GITHUB_LIST_ISSUES"] = composio.Tool{
		Slug: "GITHUB_LIST_ISSUES", Toolkit: "github", Name: "List issues", Tags: []string{"readOnlyHint"},
	}
	ctx := context.Background()

	checks := map[string]error{}
	_, checks["running a tool"] = h.service.RunTool(ctx, memberToken, "org_1", RunToolInput{Tool: "GITHUB_LIST_ISSUES"})
	_, checks["running a tool on the named connection"] = h.service.RunTool(ctx, memberToken, "org_1",
		RunToolInput{Tool: "GITHUB_LIST_ISSUES", ConnectionID: connection.ID})
	_, checks["an agent searching its tools"] = h.service.SearchTools(ctx, agentToken, "org_1", "github", "issues", 10)
	_, checks["reading the app's API"] = h.service.Proxy(ctx, memberToken, "org_1", ProxyInput{Toolkit: "github", Endpoint: "/user/repos"})
	_, checks["listing its targets"] = h.service.ConnectionTargets(ctx, memberToken, "org_1", connection.ID)
	_, checks["subscribing a trigger"] = h.service.CreateTrigger(ctx, memberToken, "org_1",
		TriggerInput{ConnectionID: connection.ID, Trigger: "GITHUB_STAR_ADDED_EVENT"})
	for name, err := range checks {
		if !errors.Is(err, domain.ErrNotConnectable) {
			t.Errorf("%s = %v, want not connectable", name, err)
		}
	}
	h.service.subscribeConnections(ctx)

	if len(h.composio.executed) != 0 || len(h.composio.proxied) != 0 || len(h.composio.upserts) != 0 {
		t.Fatalf("composio was used: executed %d, proxied %d, upserts %v",
			len(h.composio.executed), len(h.composio.proxied), h.composio.upserts)
	}
	if len(h.store.actions) != 0 {
		t.Fatalf("a refused run was recorded: %v", h.store.actions)
	}
	// Another member's Composio connection to the same app is untouched.
	h.composioConnection("apc_composio", "user-2", "github")
	if _, err := h.service.SearchTools(ctx, otherToken, "org_1", "github", "", 10); err != nil {
		t.Fatalf("a Composio member's search = %v", err)
	}
}

func TestConnectsAndKeyReplacementsAreRateLimitedPerMember(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.signin.saveErr = &signin.Failure{Reason: signin.ReasonInvalidCredentials, Field: "token", Rule: "was refused by the app"}
	key := ConnectInput{Toolkit: "github", Kind: integration.KindAPIToken, Fields: map[string]string{"token": "ghp_guess"}}
	ctx := context.Background()

	for attempt := range ConnectBudget {
		if _, err := h.service.ConnectOryxa(ctx, memberToken, "org_1", key); errors.Is(err, domain.ErrRateLimited) {
			t.Fatalf("attempt %d was limited", attempt+1)
		}
	}
	if _, err := h.service.ConnectOryxa(ctx, memberToken, "org_1", key); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("attempt %d = %v, want rate limited", ConnectBudget+1, err)
	}
	// Replacing a key spends from the same budget.
	h.oryxaConnection("apc_key", "user-1", "github", domain.ConnectionActive, integration.KindAPIToken)
	if _, err := h.service.ReplaceCredential(ctx, memberToken, "org_1", "apc_key", ReplaceCredentialInput{}); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("replacing = %v, want rate limited", err)
	}
	// Composio's connect too.
	h.composio.toolkits["slack"] = composioToolkit("slack")
	if _, _, err := h.service.Connect(ctx, memberToken, "org_1", ConnectInput{Toolkit: "slack"}); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("a Composio connect = %v, want rate limited", err)
	}
	// Another member has a budget of their own.
	if _, err := h.service.ConnectOryxa(ctx, otherToken, "org_1", key); errors.Is(err, domain.ErrRateLimited) {
		t.Fatal("another member was limited")
	}
	// The budget refills with time.
	later := NewRateLimiter(ConnectBudget, ConnectWindow, func() time.Time { return testClock })
	for range ConnectBudget {
		later.Allow("k")
	}
	if later.Allow("k") {
		t.Fatal("an empty bucket allowed a call")
	}
	later.now = func() time.Time { return testClock.Add(ConnectWindow / ConnectBudget) }
	if !later.Allow("k") {
		t.Fatal("a bucket did not refill")
	}
}

// The ingress's subscriptions answer none until Oryxa's own integrations
// have triggers, so every verified delivery is recorded and nothing is
// published.
func TestHookSubscriptionsAnswerNoneYet(t *testing.T) {
	t.Parallel()
	subscriptions := HookSubscriptions{}
	if found, err := subscriptions.ForEndpoint(context.Background(), hooks.Endpoint{ID: "aph_1"}, "GITHUB_PUSH"); err != nil || len(found) != 0 {
		t.Fatalf("for endpoint = %v, %v", found, err)
	}
	if found, err := subscriptions.ForAccount(context.Background(), "slack", "", "U1", "SLACK_MESSAGE"); err != nil || len(found) != 0 {
		t.Fatalf("for account = %v, %v", found, err)
	}
}

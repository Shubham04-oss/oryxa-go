package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// connectPipedream answers the part of Pipedream Connect connecting uses. The rest
// is left to the nil interface, and would panic if it were called.
type connectPipedream struct {
	Pipedream
	mu sync.Mutex

	tokens []PipedreamTokenRequest
	// accounts are what ListAccounts lists, once it has been asked
	// visibleFrom times: Pipedream may redirect before it lists the account.
	accounts    []PipedreamAccount
	visibleFrom int
	listed      int
	apps        []PipedreamApp
	appsListed  int
	// health is what GetAccount answers, by id; one missing is not found.
	health map[string]PipedreamAccount
	read   []string

	deleteAccountErr error
	deletedAccounts  []string
	deletedTriggers  []string
	deletedUsers     []string
}

func (fake *connectPipedream) CreateConnectToken(_ context.Context, request PipedreamTokenRequest) (PipedreamConnectToken, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.tokens = append(fake.tokens, request)
	return PipedreamConnectToken{
		Token: "ctok_1", ConnectLinkURL: "https://pipedream.com/_static/connect.html?token=ctok_1&connectLink=true",
		ExpiresAt: testClock.Add(request.ExpiresIn),
	}, nil
}

func (fake *connectPipedream) ListAccounts(_ context.Context, externalUserID, app string) ([]PipedreamAccount, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.listed++
	if fake.listed < fake.visibleFrom {
		return nil, nil
	}
	var found []PipedreamAccount
	for _, account := range fake.accounts {
		if account.ExternalUserID == externalUserID && account.App == app {
			found = append(found, account)
		}
	}
	return found, nil
}

func (fake *connectPipedream) GetAccount(_ context.Context, id string) (PipedreamAccount, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.read = append(fake.read, id)
	account, ok := fake.health[id]
	if !ok {
		return PipedreamAccount{}, fmt.Errorf("no account %s: %w", id, domain.ErrNotFound)
	}
	return account, nil
}

func (fake *connectPipedream) DeleteAccount(_ context.Context, id string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.deleteAccountErr != nil {
		return fake.deleteAccountErr
	}
	fake.deletedAccounts = append(fake.deletedAccounts, id)
	return nil
}

func (fake *connectPipedream) DeleteTrigger(_ context.Context, externalUserID, id string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.deletedTriggers = append(fake.deletedTriggers, externalUserID+"/"+id)
	return nil
}

func (fake *connectPipedream) DeleteUser(_ context.Context, externalUserID string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.deletedUsers = append(fake.deletedUsers, externalUserID)
	return nil
}

func (fake *connectPipedream) ListApps(context.Context, string) ([]PipedreamApp, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.appsListed++
	return fake.apps, nil
}

func pipedreamHarness(t *testing.T) (harness, *connectPipedream) {
	t.Helper()
	h := newHarness(t)
	fake := &connectPipedream{health: map[string]PipedreamAccount{}}
	h.service.pipedream = fake
	return h, fake
}

var memberUser = domain.ComposioUserID("org_1", "user-1")

// startPipedream connects linear through Pipedream for user-1 and answers
// the connection and the state its callback carries.
func startPipedream(t *testing.T, h harness, fake *connectPipedream) (domain.Connection, string) {
	t.Helper()
	connection, link, err := h.service.ConnectPipedream(context.Background(), memberToken, "org_1", ConnectInput{
		Toolkit: "linear", ReturnTo: "https://app.oryxa.test/apps",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "pipedream.com" || parsed.Query().Get("app") != "linear" || parsed.Query().Get("token") != "ctok_1" {
		t.Fatalf("link = %s", link)
	}
	success, err := url.Parse(fake.tokens[len(fake.tokens)-1].SuccessRedirectURI)
	if err != nil {
		t.Fatal(err)
	}
	return connection, success.Query().Get("state")
}

func TestConnectPipedreamRecordsAPendingConnectionAndAnswersTheLink(t *testing.T) {
	t.Parallel()
	h, fake := pipedreamHarness(t)
	connection, state := startPipedream(t, h, fake)

	if connection.Broker != domain.BrokerPipedream || connection.Status != domain.ConnectionPending ||
		connection.ComposioUserID != memberUser || connection.PipedreamAccountID != "" {
		t.Fatalf("connection = %+v", connection)
	}
	request := fake.tokens[0]
	if request.ExternalUserID != memberUser || request.App != "linear" || request.ExpiresIn != 900*time.Second {
		t.Fatalf("token request = %+v", request)
	}
	callback := "https://apps.oryxa.test/v1/apps/connect/pipedream/callback?state=" + url.QueryEscape(state)
	if request.SuccessRedirectURI != callback || request.ErrorRedirectURI != callback+"&status=failed" {
		t.Fatalf("redirects = %q, %q", request.SuccessRedirectURI, request.ErrorRedirectURI)
	}
	if pending, err := h.store.PendingConnection(context.Background(), hashState(state)); err != nil || pending.ID != connection.ID {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	initiated := h.store.published(appsv1.ConnectionInitiatedDetailType)
	if len(initiated) != 1 || !jsonHas(t, initiated[0], "broker", "pipedream") {
		t.Fatalf("initiated = %+v", initiated)
	}

	// One current connection per app, whatever its broker.
	if _, _, err := h.service.ConnectPipedream(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "linear"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second connect = %v, want conflict", err)
	}
	if len(fake.tokens) != 1 {
		t.Fatalf("a token was made for a refused connect: %d", len(fake.tokens))
	}
}

func TestPipedreamCallbackReadsTheAccountBack(t *testing.T) {
	t.Parallel()
	h, fake := pipedreamHarness(t)
	connection, state := startPipedream(t, h, fake)
	// A removed connection of the member's still records an account Pipedream
	// lists: it must not pass for the new one, though it is newer.
	h.store.connections["apc_old"] = domain.Connection{
		ID: "apc_old", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "linear", Broker: domain.BrokerPipedream,
		PipedreamAccountID: "apn_recorded", Status: domain.ConnectionRemoved,
	}
	fake.accounts = []PipedreamAccount{
		// Made before the connect began: an earlier attempt's.
		{ID: "apn_before", ExternalUserID: memberUser, App: "linear", Name: "old@acme.test", CreatedAt: testClock.Add(-10 * time.Minute)},
		{ID: "apn_new", ExternalUserID: memberUser, App: "linear", Name: "ada@acme.test", CreatedAt: testClock.Add(time.Minute)},
		{ID: "apn_recorded", ExternalUserID: memberUser, App: "linear", Name: "x@acme.test", CreatedAt: testClock.Add(2 * time.Minute)},
		// Another member's, and another app's.
		{ID: "apn_other", ExternalUserID: domain.ComposioUserID("org_1", "user-2"), App: "linear", CreatedAt: testClock.Add(3 * time.Minute)},
		{ID: "apn_jira", ExternalUserID: memberUser, App: "jira", CreatedAt: testClock.Add(3 * time.Minute)},
	}
	// Pipedream lists it only on the third read.
	fake.visibleFrom = 3

	completion, err := h.service.CompletePipedreamConnection(context.Background(), state, "")
	if err != nil {
		t.Fatal(err)
	}
	got := completion.Connection
	if got.ID != connection.ID || got.Status != domain.ConnectionActive || got.PipedreamAccountID != "apn_new" || got.Label != "ada@acme.test" {
		t.Fatalf("connection = %+v", got)
	}
	if completion.ReturnTo != "https://app.oryxa.test/apps" || fake.listed != 3 {
		t.Fatalf("return to %q after %d reads", completion.ReturnTo, fake.listed)
	}
	connected := h.store.published(appsv1.ConnectionConnectedDetailType)
	if len(connected) != 1 || !jsonHas(t, connected[0], "broker", "pipedream") {
		t.Fatalf("connected = %+v", connected)
	}
	// The state is spent.
	if _, err := h.service.CompletePipedreamConnection(context.Background(), state, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("replay = %v, want not found", err)
	}
}

func TestPipedreamCallbackLeavesAConnectionPendingUntilTheAccountTurnsUp(t *testing.T) {
	t.Parallel()
	h, fake := pipedreamHarness(t)
	_, state := startPipedream(t, h, fake)
	waits := 0
	h.service.wait = func(context.Context, time.Duration) error { waits++; return nil }

	completion, err := h.service.CompletePipedreamConnection(context.Background(), state, "")
	if err != nil || completion.Connection.Status != domain.ConnectionPending {
		t.Fatalf("completion = %+v, %v", completion, err)
	}
	if fake.listed != connectPolls || waits != connectPolls-1 {
		t.Fatalf("read %d times, waited %d", fake.listed, waits)
	}
	// The member can still come back to it.
	if _, err := h.store.PendingConnection(context.Background(), hashState(state)); err != nil {
		t.Fatal(err)
	}
}

func TestPipedreamCallbackRecordsAnUnfinishedConnect(t *testing.T) {
	t.Parallel()
	h, fake := pipedreamHarness(t)
	connection, state := startPipedream(t, h, fake)
	completion, err := h.service.CompletePipedreamConnection(context.Background(), state, "failed")
	if err != nil || completion.Connection.Status != domain.ConnectionFailed {
		t.Fatalf("completion = %+v, %v", completion, err)
	}
	if h.store.connections[connection.ID].Status != domain.ConnectionFailed || fake.listed != 0 {
		t.Fatalf("connection = %+v after %d reads", h.store.connections[connection.ID], fake.listed)
	}
}

func TestPipedreamCallbackFinishesOnlyPipedreamConnects(t *testing.T) {
	t.Parallel()
	h, _ := pipedreamHarness(t)
	composioConnection := domain.Connection{
		ID: "apc_c", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github", Status: domain.ConnectionPending,
	}
	if err := h.store.CreateConnection(context.Background(), composioConnection, hashState("composio-state"), domain.Event{}); err != nil {
		t.Fatal(err)
	}
	for state, want := range map[string]error{"composio-state": domain.ErrNotFound, "nobody": domain.ErrNotFound, "": domain.ErrInvalid} {
		if _, err := h.service.CompletePipedreamConnection(context.Background(), state, ""); !errors.Is(err, want) {
			t.Errorf("state %q = %v, want %v", state, err, want)
		}
	}
}

// activePipedream records an active Pipedream connection of memberID's.
func (h harness) activePipedream(id, memberID, toolkit, accountID string) domain.Connection {
	connected := testClock
	connection := domain.Connection{
		ID: id, OrganizationID: "org_1", MemberID: memberID, Toolkit: toolkit, Broker: domain.BrokerPipedream,
		ComposioUserID: domain.ComposioUserID("org_1", memberID), PipedreamAccountID: accountID,
		Status: domain.ConnectionActive, CreatedAt: testClock, UpdatedAt: testClock, ConnectedAt: &connected,
	}
	h.store.connections[id] = connection
	return connection
}

func TestRemovingAPipedreamConnectionDeletesItsTriggersThenItsAccount(t *testing.T) {
	t.Parallel()
	h, fake := pipedreamHarness(t)
	connection := h.activePipedream("apc_1", "user-1", "linear", "apn_1")
	h.store.triggers["apt_1"] = domain.Trigger{
		ID: "apt_1", OrganizationID: "org_1", MemberID: "user-1", ConnectionID: connection.ID, Toolkit: "linear",
		PipedreamTriggerID: "dc_1", Status: domain.TriggerActive,
	}

	if err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", connection.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.deletedTriggers) != 1 || fake.deletedTriggers[0] != memberUser+"/dc_1" ||
		len(fake.deletedAccounts) != 1 || fake.deletedAccounts[0] != "apn_1" {
		t.Fatalf("deleted triggers %v, accounts %v", fake.deletedTriggers, fake.deletedAccounts)
	}
	if h.store.connections[connection.ID].Status != domain.ConnectionRemoved || h.store.triggers["apt_1"].Status != domain.TriggerRemoved {
		t.Fatalf("connection %s, trigger %s", h.store.connections[connection.ID].Status, h.store.triggers["apt_1"].Status)
	}
	removed := h.store.published(appsv1.ConnectionRemovedDetailType)
	if len(removed) != 1 || !jsonHas(t, removed[0], "broker", "pipedream") {
		t.Fatalf("removed = %+v", removed)
	}
	if len(h.composio.deleted) != 0 {
		t.Fatal("Composio was asked to delete a Pipedream account")
	}

	// An account Pipedream no longer has is already gone, not a failure.
	again := h.activePipedream("apc_2", "user-1", "jira", "apn_2")
	fake.deleteAccountErr = fmt.Errorf("gone: %w", domain.ErrNotFound)
	if err := h.service.RemoveConnection(context.Background(), memberToken, "org_1", again.ID); err != nil {
		t.Fatalf("remove a gone account = %v", err)
	}
}

func TestAMemberLeavingDeletesTheirPipedreamUserOnceNothingOfTheirsIsLive(t *testing.T) {
	t.Parallel()
	h, fake := pipedreamHarness(t)
	h.activePipedream("apc_1", "user-1", "linear", "apn_1")
	h.activePipedream("apc_2", "user-1", "jira", "apn_2")
	h.activePipedream("apc_3", "user-2", "linear", "apn_3")
	ctx := context.Background()

	// Pipedream will not delete the accounts: the member stays filed there.
	fake.deleteAccountErr = errBoom
	if err := h.service.MemberRemoved(ctx, memberRemoved("user-1")); err == nil {
		t.Fatal("MemberRemoved() succeeded with the accounts still at Pipedream")
	}
	if len(fake.deletedUsers) != 0 {
		t.Fatalf("deleted users %v while their connections were live", fake.deletedUsers)
	}

	// The retry removes them, and then the member at Pipedream.
	fake.deleteAccountErr = nil
	if err := h.service.MemberRemoved(ctx, memberRemoved("user-1")); err != nil {
		t.Fatal(err)
	}
	if len(fake.deletedUsers) != 1 || fake.deletedUsers[0] != memberUser {
		t.Fatalf("deleted users %v", fake.deletedUsers)
	}
	if h.store.connections["apc_3"].Status != domain.ConnectionActive {
		t.Fatal("another member's connection was removed")
	}
}

func TestTheHealthSweepExpiresWhatPipedreamCannotUse(t *testing.T) {
	t.Parallel()
	h, fake := pipedreamHarness(t)
	for id, account := range map[string]PipedreamAccount{
		"healthy":   {Healthy: true},
		"unhealthy": {Healthy: false},
		"dead":      {Healthy: true, Dead: true},
	} {
		h.activePipedream("apc_"+id, "user-1", id, "apn_"+id)
		fake.health["apn_"+id] = account
	}
	h.activePipedream("apc_gone", "user-1", "gone", "apn_gone")
	// Many more than one page of healthy ones.
	for index := range pipedreamHealthPage + 5 {
		id := fmt.Sprintf("apc_z%03d", index)
		h.activePipedream(id, "user-2", fmt.Sprintf("app%03d", index), "apn_"+id)
		fake.health["apn_"+id] = PipedreamAccount{Healthy: true}
	}
	var paced []time.Duration
	h.service.wait = func(_ context.Context, pause time.Duration) error { paced = append(paced, pause); return nil }

	h.service.maintain(context.Background())

	for id, want := range map[string]domain.ConnectionStatus{
		"apc_healthy": domain.ConnectionActive, "apc_unhealthy": domain.ConnectionExpired,
		"apc_dead": domain.ConnectionExpired, "apc_gone": domain.ConnectionExpired, "apc_z104": domain.ConnectionActive,
	} {
		if got := h.store.connections[id].Status; got != want {
			t.Errorf("%s is %s, want %s", id, got, want)
		}
	}
	if len(fake.read) != pipedreamHealthPage+9 {
		t.Fatalf("read %d accounts", len(fake.read))
	}
	// 2,000 reads in five minutes is one every 150ms.
	if len(paced) != len(fake.read) || paced[0] != 150*time.Millisecond {
		t.Fatalf("paced %d reads, the first by %v", len(paced), paced[0])
	}
	expired := h.store.published(appsv1.ConnectionExpiredDetailType)
	if len(expired) != 3 || !jsonHas(t, expired[0], "broker", "pipedream") {
		t.Fatalf("expired = %+v", expired)
	}

	// Once a day: the next fifteen-minute pass reads nothing.
	read := len(fake.read)
	h.service.maintain(context.Background())
	if len(fake.read) != read {
		t.Fatalf("the sweep ran again within the day: %d reads", len(fake.read)-read)
	}
	h.service.now = func() time.Time { return testClock.Add(25 * time.Hour) }
	h.service.maintain(context.Background())
	if len(fake.read) == read {
		t.Fatal("the sweep did not run the next day")
	}
}

func TestTheCatalogAddsPipedreamsOwnAppsAfterComposios(t *testing.T) {
	t.Parallel()
	h, fake := pipedreamHarness(t)
	for _, slug := range []string{"github", "googlecalendar", "outlook"} {
		h.composio.toolkits[slug] = composio.Toolkit{Slug: slug, Name: slug, ComposioManagedAuthSchemes: []string{"OAUTH2"}}
	}
	fake.apps = []PipedreamApp{
		{Slug: "google_calendar", Name: "Google Calendar"}, // Composio's googlecalendar
		{Slug: "microsoft_outlook", Name: "Outlook"},       // an alias of Composio's outlook
		{Slug: "linear_app", Name: "Linear", AuthType: "oauth", Categories: []string{"Project Management"}},
		{Slug: "Not A Slug", Name: "Broken"}, // not a toolkit, so never offered
		{Slug: "hacker_news", Name: "Hacker News", AuthType: "none"},
	}
	ctx := context.Background()

	views, next, err := h.service.Catalog(ctx, CatalogQuery{})
	if err != nil || next != pipedreamCursor+"0" || len(views) != 3 || views[0].Broker != domain.BrokerComposio {
		t.Fatalf("Composio's page = %+v, %q, %v", views, next, err)
	}
	views, next, err = h.service.Catalog(ctx, CatalogQuery{Cursor: next, Limit: 1})
	if err != nil || len(views) != 1 || next != pipedreamCursor+"1" {
		t.Fatalf("Pipedream's first page = %+v, %q, %v", views, next, err)
	}
	linear := views[0]
	if linear.Toolkit != "linear_app" || linear.Broker != domain.BrokerPipedream || !linear.Connectable ||
		len(linear.Categories) != 1 || linear.Categories[0].ID != "project-management" {
		t.Fatalf("linear = %+v", linear)
	}
	views, next, err = h.service.Catalog(ctx, CatalogQuery{Cursor: next})
	if err != nil || len(views) != 1 || views[0].Toolkit != "hacker_news" || views[0].Connectable || next != "" {
		t.Fatalf("Pipedream's last page = %+v, %q, %v", views, next, err)
	}

	// A search narrows Pipedream's too; a category is matched by its id.
	views, _, _ = h.service.Catalog(ctx, CatalogQuery{Cursor: pipedreamCursor + "0", Search: "hacker"})
	if len(views) != 1 || views[0].Toolkit != "hacker_news" {
		t.Fatalf("searched = %+v", views)
	}
	views, _, _ = h.service.Catalog(ctx, CatalogQuery{Cursor: pipedreamCursor + "0", Category: "project-management"})
	if len(views) != 1 || views[0].Toolkit != "linear_app" {
		t.Fatalf("by category = %+v", views)
	}
	if _, _, err := h.service.Catalog(ctx, CatalogQuery{Cursor: pipedreamCursor + "x"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a bad cursor = %v", err)
	}

	// One app, described, and routed there.
	if view, err := h.service.CatalogToolkit(ctx, "linear_app"); err != nil || view.Broker != domain.BrokerPipedream {
		t.Fatalf("linear_app = %+v, %v", view, err)
	}
	if broker, err := h.service.BrokerFor(ctx, "linear_app"); err != nil || broker != domain.BrokerPipedream {
		t.Fatalf("BrokerFor() = %q, %v", broker, err)
	}
	if fake.appsListed != 1 {
		t.Fatalf("Pipedream's catalog was listed %d times, want once a day", fake.appsListed)
	}
	h.service.now = func() time.Time { return testClock.Add(25 * time.Hour) }
	if _, err := h.service.BrokerFor(ctx, "linear_app"); err != nil || fake.appsListed != 2 {
		t.Fatalf("the next day = %v, listed %d times", err, fake.appsListed)
	}
}

func TestCatalogKeysMatchAcrossSpelling(t *testing.T) {
	t.Parallel()
	for slug, want := range map[string]string{"google_calendar": "googlecalendar", "Google-Calendar": "googlecalendar", "github": "github"} {
		if got := catalogKey(slug); got != want {
			t.Errorf("catalogKey(%q) = %q, want %q", slug, got, want)
		}
	}
}

// jsonHas says an event's data holds field with value.
func jsonHas(t *testing.T, event domain.Event, field, value string) bool {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(event.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data[field] == value
}

func memberRemoved(memberID string) authv1.MemberRemoved {
	return authv1.MemberRemoved{OrganizationID: "org_1", UserID: memberID}
}

func TestPipedreamCallbackToleratesClockSkew(t *testing.T) {
	t.Parallel()
	h, fake := pipedreamHarness(t)
	connection, state := startPipedream(t, h, fake)
	// Pipedream's clock runs a few seconds behind ours: the member's new
	// account looks slightly older than the connection, and is still it.
	fake.accounts = []PipedreamAccount{
		{ID: "apn_skewed", ExternalUserID: memberUser, App: "linear", Name: "ada@acme.test", CreatedAt: testClock.Add(-5 * time.Second)},
	}

	completion, err := h.service.CompletePipedreamConnection(context.Background(), state, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := completion.Connection; got.ID != connection.ID || got.PipedreamAccountID != "apn_skewed" {
		t.Fatalf("connection = %+v", got)
	}
}

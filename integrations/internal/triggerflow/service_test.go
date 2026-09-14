package triggerflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/github"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/google"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/microsoftgraph"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

type triggerAuthorizer struct{ err error }

func (a triggerAuthorizer) CanCreateConnection(context.Context, string, string) error { return a.err }

type triggerConnections struct{ connection domain.Connection }

func (c triggerConnections) ConnectionForExecution(context.Context, string, string) (domain.Connection, error) {
	return c.connection, nil
}

type triggerCatalog struct{}

func (triggerCatalog) TriggerDefinition(provider, name string) (Definition, bool) {
	if provider != "slack" || name != "hook" {
		return Definition{}, false
	}
	return Definition{Provider: provider, PieceName: "piece", PieceVersion: "1", TriggerName: name, RouteMode: domain.WebhookRouteAppLevel, Event: "message", ValidateProps: func(raw json.RawMessage) error { var value map[string]any; return json.Unmarshal(raw, &value) }}, true
}

type triggerRoutes struct {
	route domain.WebhookRoute
	err   error
}

func (r triggerRoutes) ActiveWebhookRoute(context.Context, string, string, string) (domain.WebhookRoute, error) {
	return r.route, r.err
}

type triggerStore struct {
	created   domain.Trigger
	event     domain.Event
	trigger   domain.Trigger
	createErr error
	disabled  bool
}

func (s *triggerStore) CreateTriggerWithEvent(_ context.Context, trigger domain.Trigger, event domain.Event) error {
	s.created = trigger
	s.event = event
	return s.createErr
}
func (s *triggerStore) Trigger(context.Context, string, string) (domain.Trigger, error) {
	return s.trigger, nil
}
func (s *triggerStore) DisableTriggerWithEvent(context.Context, string, string, string, time.Time, domain.Event) error {
	s.disabled = true
	return nil
}

type triggerState struct{ applied []piece.StoreMutation }

func (s *triggerState) LoadTriggerState(context.Context, string, string) (map[string]any, int64, error) {
	return map[string]any{}, 0, nil
}

type graphState struct {
	values    map[string]any
	version   int64
	applied   []piece.StoreMutation
	deleted   bool
	deleteErr error
}

func (s *graphState) LoadTriggerState(context.Context, string, string) (map[string]any, int64, error) {
	result := make(map[string]any, len(s.values))
	for key, value := range s.values {
		result[key] = value
	}
	return result, s.version, nil
}
func (s *graphState) ApplyTriggerState(_ context.Context, _, _ string, expected int64, mutations []piece.StoreMutation) error {
	if expected != s.version {
		return domain.ErrConflict
	}
	if s.values == nil {
		s.values = map[string]any{}
	}
	for _, mutation := range mutations {
		key := mutation.Scope + "\x00" + mutation.Key
		if mutation.Operation == "delete" {
			delete(s.values, key)
		} else {
			s.values[key] = mutation.Value
		}
	}
	s.applied = mutations
	s.version++
	return nil
}
func (s *graphState) DeleteTriggerState(context.Context, string, string) error {
	s.values = nil
	s.deleted = true
	return s.deleteErr
}
func (s *triggerState) ApplyTriggerState(_ context.Context, _, _ string, _ int64, mutations []piece.StoreMutation) error {
	s.applied = mutations
	return nil
}
func (*triggerState) DeleteTriggerState(context.Context, string, string) error { return nil }

type triggerSecrets struct{}

func (triggerSecrets) Open(_ context.Context, binding secrets.Binding, _ domain.SealedData) ([]byte, error) {
	if binding.ResourceType != "connection" || binding.ResourceID != "con-1" {
		return nil, errors.New("wrong secret binding")
	}
	return []byte(`{"access_token":"secret"}`), nil
}

type graphSecrets struct {
	triggerSecrets
	binding secrets.Binding
}

func (s *graphSecrets) Seal(_ context.Context, binding secrets.Binding, value []byte) (domain.SealedData, error) {
	s.binding = binding
	if len(value) < 32 {
		return domain.SealedData{}, errors.New("client state was not random")
	}
	return domain.SealedData{Version: 1, Ciphertext: append([]byte(nil), value...)}, nil
}

func (s *graphSecrets) Open(ctx context.Context, binding secrets.Binding, sealed domain.SealedData) ([]byte, error) {
	if binding.ResourceType == "webhook-route" {
		return append([]byte(nil), sealed.Ciphertext...), nil
	}
	return s.triggerSecrets.Open(ctx, binding, sealed)
}

func TestMicrosoftGraphCalendarReadScopeAcceptsReadWriteGrant(t *testing.T) {
	t.Parallel()
	if missing := missingScopes("microsoft", []string{"Calendars.Read"}, []string{"Calendars.ReadWrite"}); len(missing) != 0 {
		t.Fatalf("Calendars.ReadWrite did not satisfy trigger read: %v", missing)
	}
	if missing := missingScopes("microsoft", []string{"Calendars.ReadWrite"}, []string{"Calendars.Read"}); len(missing) != 1 {
		t.Fatalf("read unexpectedly satisfied write: %v", missing)
	}
	if missing := missingScopes("microsoft", []string{"Mail.Read"}, []string{"Mail.ReadWrite"}); len(missing) != 0 {
		t.Fatalf("Mail.ReadWrite did not satisfy trigger read: %v", missing)
	}
	if missing := missingScopes("microsoft", []string{"Files.Read"}, []string{"Files.ReadWrite"}); len(missing) != 0 {
		t.Fatalf("Files.ReadWrite did not satisfy trigger read: %v", missing)
	}
}

type graphRoutes struct {
	route         domain.WebhookRoute
	created       bool
	deactivated   bool
	deactivateErr error
}

func (r *graphRoutes) ActiveWebhookRoute(context.Context, string, string, string) (domain.WebhookRoute, error) {
	return r.route, nil
}
func (r *graphRoutes) CreateWebhookRoute(_ context.Context, route domain.WebhookRoute) error {
	r.route, r.created = route, true
	return nil
}
func (r *graphRoutes) ActiveWebhookRouteForConnection(_ context.Context, org, profile, trigger, connection string) (domain.WebhookRoute, error) {
	if !r.route.Active || r.route.OrganizationID != org || r.route.ProfileID != profile || r.route.TriggerName != trigger || r.route.ConnectionID != connection {
		return domain.WebhookRoute{}, domain.ErrNotFound
	}
	return r.route, nil
}
func (r *graphRoutes) DeactivateWebhookRoute(_ context.Context, opaque string) error {
	if r.route.OpaqueRoute != opaque {
		return domain.ErrNotFound
	}
	r.route.Active = false
	r.deactivated = true
	return r.deactivateErr
}

type graphSubscriptions struct {
	createRequest microsoftgraph.SubscriptionRequest
	renewID       string
	removeID      string
	subscription  microsoftgraph.Subscription
	createErr     error
	removeErr     error
	routeCreated  *bool
}

func (s *graphSubscriptions) Create(_ context.Context, _ string, request microsoftgraph.SubscriptionRequest) (microsoftgraph.Subscription, error) {
	s.createRequest = request
	if s.routeCreated != nil && !*s.routeCreated {
		return microsoftgraph.Subscription{}, errors.New("route was not persisted before Graph validation")
	}
	if s.createErr != nil {
		return microsoftgraph.Subscription{}, s.createErr
	}
	return s.subscription, nil
}
func (s *graphSubscriptions) Renew(_ context.Context, _ string, id string, expires time.Time) (microsoftgraph.Subscription, error) {
	s.renewID = id
	result := s.subscription
	result.ID, result.ExpirationDateTime = id, expires
	return result, nil
}
func (s *graphSubscriptions) Remove(_ context.Context, _ string, id string) error {
	s.removeID = id
	return s.removeErr
}

type graphCatalog struct{}

func (graphCatalog) TriggerDefinition(provider, name string) (Definition, bool) {
	if provider != "microsoft" || name != "new-email" {
		return Definition{}, false
	}
	return Definition{Provider: "microsoft", PieceName: "@activepieces/piece-microsoft-outlook", PieceVersion: "0.5.3",
		TriggerName: name, RouteMode: domain.WebhookRoutePerConnection, Event: "change-notification",
		GraphResource: "/me/mailFolders('Inbox')/messages", GraphChangeType: "created", GraphTTL: 72 * time.Hour, RequiredScopes: []string{"Mail.Read"}}, true
}

type triggerRunner struct {
	listener string
	request  piece.TriggerInvocation
	calls    int
}

func (r *triggerRunner) RunTrigger(_ context.Context, request piece.TriggerInvocation) (piece.TriggerResult, error) {
	r.calls++
	r.request = request
	return piece.TriggerResult{Listeners: []piece.Listener{{Events: []string{"message"}, IdentifierValue: r.listener}}, StoreMutations: []piece.StoreMutation{{Operation: "put", Key: "listener", Value: "registered"}}}, nil
}

func TestCreateEnablesPieceAndPersistsTenantBoundTrigger(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	store := &triggerStore{}
	state := &triggerState{}
	runner := &triggerRunner{listener: "T123"}
	service := Service{Catalog: triggerCatalog{}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: testConnection()}, Routes: triggerRoutes{route: testRoute()}, Store: store, State: state, Secrets: triggerSecrets{}, Runner: runner, Now: func() time.Time { return now }, IDs: func(prefix string) (string, error) { return prefix + "-1", nil }, PublicURL: "https://connect.example.com"}
	trigger, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "hook", Props: json.RawMessage(`{"ignoreBots":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if trigger.ProviderIdentifier != "T123" || trigger.Status != domain.TriggerStatusActive || store.created.ID != "trg-1" {
		t.Fatalf("trigger = %+v", trigger)
	}
	if runner.request.Operation != "trigger.enable" || runner.request.WebhookURL != "https://connect.example.com/v1/webhooks/opaque" || len(state.applied) != 1 {
		t.Fatalf("runner/state = %+v/%+v", runner.request, state.applied)
	}
	encoded, _ := json.Marshal(store.event)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "access_token") {
		t.Fatalf("trigger event leaked credential: %s", encoded)
	}
}

func TestCreateRejectsAnotherMembersConnectionBeforeRuntime(t *testing.T) {
	t.Parallel()
	connection := testConnection()
	connection.MemberID = "another-member"
	runner := &triggerRunner{listener: "T123"}
	service := Service{Catalog: triggerCatalog{}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: connection}, Routes: triggerRoutes{route: testRoute()}, Runner: runner}
	_, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "hook", Props: json.RawMessage(`{}`)})
	if err == nil || runner.calls != 0 {
		t.Fatalf("error/runtime calls = %v/%d", err, runner.calls)
	}
}

func TestCreateRejectsListenerForDifferentProviderTenant(t *testing.T) {
	t.Parallel()
	runner := &triggerRunner{listener: "T999"}
	store := &triggerStore{}
	service := Service{Catalog: triggerCatalog{}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: testConnection()}, Routes: triggerRoutes{route: testRoute()}, Store: store, State: &triggerState{}, Secrets: triggerSecrets{}, Runner: runner, IDs: func(string) (string, error) { return "id", nil }, PublicURL: "https://connect.example.com"}
	_, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "hook", Props: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrIdentityMismatch) || store.created.ID != "" {
		t.Fatalf("error/created = %v/%+v", err, store.created)
	}
}

func testConnection() domain.Connection {
	return domain.Connection{ID: "con-1", OrganizationID: "org-1", MemberID: "member-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", Identity: domain.ProviderIdentity{TenantID: "T123"}, Status: domain.ConnectionStatusActive}
}
func testRoute() domain.WebhookRoute {
	return domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", TriggerName: "hook", Mode: domain.WebhookRouteAppLevel, Active: true}
}

func testMicrosoftConnection() domain.Connection {
	connection := testConnection()
	connection.Provider = "microsoft"
	connection.PieceName = "@activepieces/piece-microsoft-outlook"
	connection.PieceVersion = "0.5.3"
	connection.GrantedScopes = []string{"Mail.Read"}
	return connection
}

func TestMicrosoftGraphCreatePersistsRouteBeforeSubscriptionAndCompensates(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{}
	state := &graphState{}
	secrets := &graphSecrets{}
	graph := &graphSubscriptions{subscription: microsoftgraph.Subscription{ID: "sub-1", ExpirationDateTime: now.Add(72 * time.Hour)}, routeCreated: &routes.created}
	store := &triggerStore{}
	service := Service{Catalog: graphCatalog{}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: testMicrosoftConnection()},
		Routes: routes, Store: store, State: state, Secrets: secrets, MicrosoftGraph: graph, Now: func() time.Time { return now },
		IDs: func(prefix string) (string, error) { return prefix + "-1", nil }, Random: bytes.NewReader(bytes.Repeat([]byte{7}, 64)), PublicURL: "https://connect.example.com"}
	trigger, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "new-email"})
	if err != nil {
		t.Fatal(err)
	}
	if trigger.ProviderIdentifier != "con-1" || !routes.created || routes.route.ConnectionID != "con-1" || routes.route.Mode != domain.WebhookRoutePerConnection || routes.route.OpaqueRoute == "" {
		t.Fatalf("trigger/route = %+v/%+v", trigger, routes.route)
	}
	if secrets.binding.ResourceType != "webhook-route" || secrets.binding.ResourceID != routes.route.OpaqueRoute ||
		graph.createRequest.NotificationURL != "https://connect.example.com/v1/webhooks/"+routes.route.OpaqueRoute ||
		graph.createRequest.ClientState == "" || graph.createRequest.Resource != "/me/mailFolders('Inbox')/messages" || graph.createRequest.ChangeType != "created" {
		t.Fatalf("secret/subscription = %+v/%+v", secrets.binding, graph.createRequest)
	}
	if state.values["\x00microsoft_graph_subscription_id"] != "sub-1" || state.values["\x00microsoft_graph_subscription_expires_at"] == nil {
		t.Fatalf("subscription state = %#v", state.values)
	}

	store.createErr = errors.New("postgres unavailable")
	routes = &graphRoutes{}
	state = &graphState{}
	graph = &graphSubscriptions{subscription: microsoftgraph.Subscription{ID: "sub-2", ExpirationDateTime: now.Add(72 * time.Hour)}, routeCreated: &routes.created}
	service.Routes, service.Store, service.State, service.MicrosoftGraph = routes, store, state, graph
	service.Random = bytes.NewReader(bytes.Repeat([]byte{8}, 64))
	_, err = service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "new-email"})
	if err == nil || graph.removeID != "sub-2" || !routes.deactivated || !state.deleted {
		t.Fatalf("compensation error/remove/route/state = %v/%q/%v/%v", err, graph.removeID, routes.deactivated, state.deleted)
	}
}

func TestMicrosoftGraphCreateSurfacesEveryCompensationFailure(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	primary := errors.New("trigger persistence failed")
	removeFailure := errors.New("Graph delete unavailable")
	routeFailure := errors.New("Dynamo route update unavailable")
	stateFailure := errors.New("Dynamo state delete unavailable")
	store := &triggerStore{createErr: primary}
	routes := &graphRoutes{deactivateErr: routeFailure}
	state := &graphState{deleteErr: stateFailure}
	secrets := &graphSecrets{}
	graph := &graphSubscriptions{subscription: microsoftgraph.Subscription{ID: "sub-1", ExpirationDateTime: now.Add(72 * time.Hour)}, routeCreated: &routes.created, removeErr: removeFailure}
	service := Service{Catalog: graphCatalog{}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: testMicrosoftConnection()},
		Routes: routes, Store: store, State: state, Secrets: secrets, MicrosoftGraph: graph, Now: func() time.Time { return now },
		IDs: func(prefix string) (string, error) { return prefix + "-1", nil }, Random: bytes.NewReader(bytes.Repeat([]byte{9}, 64)), PublicURL: "https://connect.example.com"}
	_, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "new-email"})
	if !errors.Is(err, primary) || !errors.Is(err, removeFailure) || !errors.Is(err, routeFailure) || !errors.Is(err, stateFailure) {
		t.Fatalf("compensation errors = %v", err)
	}
	if graph.removeID != "sub-1" || !routes.deactivated || !state.deleted {
		t.Fatalf("cleanup did not attempt every resource: graph=%q route=%v state=%v", graph.removeID, routes.deactivated, state.deleted)
	}
}

func TestMicrosoftGraphRenewAndDisableUseDurableState(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	trigger := domain.Trigger{ID: "trg-1", OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", ProfileID: "app-1", Provider: "microsoft", PieceName: "@activepieces/piece-microsoft-outlook", PieceVersion: "0.5.3", TriggerName: "new-email", Status: domain.TriggerStatusActive}
	store := &triggerStore{trigger: trigger}
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1", TriggerName: "new-email", Mode: domain.WebhookRoutePerConnection, PieceName: trigger.PieceName, PieceVersion: trigger.PieceVersion, Active: true}}
	state := &graphState{values: map[string]any{"\x00microsoft_graph_subscription_id": "sub-1", "\x00microsoft_graph_subscription_expires_at": now.Add(23 * time.Hour).Format(time.RFC3339Nano)}}
	graph := &graphSubscriptions{}
	service := Service{Catalog: graphCatalog{}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: testMicrosoftConnection()}, Routes: routes, Store: store, State: state,
		Secrets: &graphSecrets{}, MicrosoftGraph: graph, Now: func() time.Time { return now }, IDs: func(prefix string) (string, error) { return prefix + "-1", nil }, PublicURL: "https://connect.example.com"}
	if err := service.Renew(context.Background(), "org-1", "trg-1"); err != nil || graph.renewID != "sub-1" || state.values["\x00microsoft_graph_subscription_expires_at"] == nil {
		t.Fatalf("renew/id/state = %v/%q/%#v", err, graph.renewID, state.values)
	}
	if err := service.Disable(context.Background(), "org-1", "member-1", "trg-1"); err != nil || graph.removeID != "sub-1" || !routes.deactivated || !store.disabled {
		t.Fatalf("disable/remove/route/store = %v/%q/%v/%v", err, graph.removeID, routes.deactivated, store.disabled)
	}
}

func TestGitHubRepoScopeSatisfiesRepoHook(t *testing.T) {
	t.Parallel()
	if missing := missingScopes("github", []string{"admin:repo_hook"}, []string{"repo"}); len(missing) != 0 {
		t.Fatalf("repo did not satisfy admin:repo_hook: %v", missing)
	}
	if missing := missingScopes("github", []string{"admin:org_hook"}, []string{"repo", "admin:repo_hook"}); len(missing) != 1 {
		t.Fatalf("repo unexpectedly satisfied admin:org_hook: %v", missing)
	}
}

type githubCatalog struct{ name string }

func (c githubCatalog) TriggerDefinition(provider, name string) (Definition, bool) {
	if provider != "github" || (name != "new-issue" && name != "new-pull-request") {
		return Definition{}, false
	}
	if c.name != "" && name != c.name {
		return Definition{}, false
	}
	event := "issues"
	if name == "new-pull-request" {
		event = "pull_request"
	}
	return Definition{Provider: "github", PieceName: "@activepieces/piece-github", PieceVersion: "0.8.11",
		TriggerName: name, RouteMode: domain.WebhookRoutePerConnection, Event: event, GitHubHookEvent: event,
		ValidateProps: func(raw json.RawMessage) error {
			var props githubTriggerTarget
			return json.Unmarshal(raw, &props)
		}}, true
}

type githubHooks struct {
	createRequest github.HookRequest
	removed       github.HookState
	hook          github.Hook
	createErr     error
	removeErr     error
	routeCreated  *bool
}

func (s *githubHooks) Create(_ context.Context, _ string, request github.HookRequest) (github.Hook, error) {
	s.createRequest = request
	if s.routeCreated != nil && !*s.routeCreated {
		return github.Hook{}, errors.New("route was not persisted before GitHub hook create")
	}
	if s.createErr != nil {
		return github.Hook{}, s.createErr
	}
	return s.hook, nil
}
func (s *githubHooks) Remove(_ context.Context, _ string, state github.HookState) error {
	s.removed = state
	return s.removeErr
}

func testGitHubConnection() domain.Connection {
	connection := testConnection()
	connection.Provider = "github"
	connection.PieceName = "@activepieces/piece-github"
	connection.PieceVersion = "0.8.11"
	connection.GrantedScopes = []string{"repo", "read:user"}
	return connection
}

func TestGitHubCreatePersistsRepoHookAndCompensates(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{}
	state := &graphState{}
	secrets := &graphSecrets{}
	hooks := &githubHooks{hook: github.Hook{ID: "12345", URL: "https://api.github.com/repos/oryxa/api/hooks/12345"}, routeCreated: &routes.created}
	store := &triggerStore{}
	service := Service{Catalog: githubCatalog{}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: testGitHubConnection()},
		Routes: routes, Store: store, State: state, Secrets: secrets, GitHub: hooks, Now: func() time.Time { return now },
		IDs: func(prefix string) (string, error) { return prefix + "-1", nil }, Random: bytes.NewReader(bytes.Repeat([]byte{7}, 64)), PublicURL: "https://connect.example.com"}
	trigger, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1",
		TriggerName: "new-issue", Props: json.RawMessage(`{"scope":"repository","owner":"oryxa","repo":"api"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if trigger.ProviderIdentifier != "con-1" || !routes.created || routes.route.ConnectionID != "con-1" ||
		hooks.createRequest.Scope != github.HookScopeRepository || hooks.createRequest.Events[0] != github.EventIssues ||
		hooks.createRequest.URL != "https://connect.example.com/v1/webhooks/"+routes.route.OpaqueRoute {
		t.Fatalf("trigger/route/hook = %+v/%+v/%+v", trigger, routes.route, hooks.createRequest)
	}
	if state.values["\x00github_hook_id"] != "12345" || state.values["\x00github_hook_owner"] != "oryxa" {
		t.Fatalf("hook state = %#v", state.values)
	}

	store.createErr = errors.New("postgres unavailable")
	routes = &graphRoutes{}
	state = &graphState{}
	hooks = &githubHooks{hook: github.Hook{ID: "99", URL: "https://api.github.com/repos/oryxa/api/hooks/99"}, routeCreated: &routes.created}
	service.Routes, service.Store, service.State, service.GitHub = routes, store, state, hooks
	service.Random = bytes.NewReader(bytes.Repeat([]byte{8}, 64))
	_, err = service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1",
		TriggerName: "new-issue", Props: json.RawMessage(`{"scope":"repository","owner":"oryxa","repo":"api"}`)})
	if err == nil || hooks.removed.ID != "99" || !routes.deactivated || !state.deleted {
		t.Fatalf("compensation error/remove/route/state = %v/%q/%v/%v", err, hooks.removed.ID, routes.deactivated, state.deleted)
	}
}

func TestGitHubCreateRejectsOrganizationScopeWithoutOrgHook(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	hooks := &githubHooks{hook: github.Hook{ID: "1"}}
	service := Service{Catalog: githubCatalog{}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: testGitHubConnection()},
		Routes: &graphRoutes{}, Store: &triggerStore{}, State: &graphState{}, Secrets: &graphSecrets{}, GitHub: hooks,
		Now: func() time.Time { return now }, IDs: func(prefix string) (string, error) { return prefix + "-1", nil },
		Random: bytes.NewReader(bytes.Repeat([]byte{9}, 64)), PublicURL: "https://connect.example.com"}
	_, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1",
		TriggerName: "new-issue", Props: json.RawMessage(`{"scope":"organization","owner":"oryxa"}`)})
	if !errors.Is(err, ErrRequiredScope) || hooks.createRequest.Owner != "" {
		t.Fatalf("error/create = %v/%+v", err, hooks.createRequest)
	}
}

func TestGitHubCreateOrganizationHookWhenOrgScopeGranted(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	connection := testGitHubConnection()
	connection.GrantedScopes = []string{"repo", "admin:org_hook", "read:user"}
	routes := &graphRoutes{}
	hooks := &githubHooks{hook: github.Hook{ID: "77", URL: "https://api.github.com/orgs/oryxa/hooks/77"}, routeCreated: &routes.created}
	service := Service{Catalog: githubCatalog{name: "new-pull-request"}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: connection},
		Routes: routes, Store: &triggerStore{}, State: &graphState{}, Secrets: &graphSecrets{}, GitHub: hooks,
		Now: func() time.Time { return now }, IDs: func(prefix string) (string, error) { return prefix + "-1", nil },
		Random: bytes.NewReader(bytes.Repeat([]byte{3}, 64)), PublicURL: "https://connect.example.com"}
	trigger, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1",
		TriggerName: "new-pull-request", Props: json.RawMessage(`{"scope":"organization","owner":"oryxa"}`)})
	if err != nil || trigger.TriggerName != "new-pull-request" || hooks.createRequest.Scope != github.HookScopeOrganization ||
		hooks.createRequest.Events[0] != github.EventPullRequest || hooks.createRequest.Repo != "" {
		t.Fatalf("trigger/error/hook = %+v/%v/%+v", trigger, err, hooks.createRequest)
	}
}

func TestGitHubDisableDeletesHookAndDeactivatesRoute(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	trigger := domain.Trigger{ID: "trg-1", OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", ProfileID: "app-1",
		Provider: "github", PieceName: "@activepieces/piece-github", PieceVersion: "0.8.11", TriggerName: "new-issue", Status: domain.TriggerStatusActive}
	store := &triggerStore{trigger: trigger}
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1",
		TriggerName: "new-issue", Mode: domain.WebhookRoutePerConnection, PieceName: trigger.PieceName, PieceVersion: trigger.PieceVersion, Active: true}}
	state := &graphState{values: map[string]any{"\x00github_hook_id": "12345", "\x00github_hook_scope": "repository", "\x00github_hook_owner": "oryxa", "\x00github_hook_repo": "api"}}
	hooks := &githubHooks{}
	service := Service{Catalog: githubCatalog{}, Authorizer: triggerAuthorizer{}, Connections: triggerConnections{connection: testGitHubConnection()},
		Routes: routes, Store: store, State: state, Secrets: &graphSecrets{}, GitHub: hooks, Now: func() time.Time { return now },
		IDs: func(prefix string) (string, error) { return prefix + "-1", nil }, PublicURL: "https://connect.example.com"}
	if err := service.Disable(context.Background(), "org-1", "member-1", "trg-1"); err != nil || hooks.removed.ID != "12345" || !routes.deactivated || !store.disabled {
		t.Fatalf("disable/remove/route/store = %v/%q/%v/%v", err, hooks.removed.ID, routes.deactivated, store.disabled)
	}
}

func TestGoogleCalendarReadScopeAcceptsEventsGrant(t *testing.T) {
	t.Parallel()
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/calendar.readonly"},
		[]string{"https://www.googleapis.com/auth/calendar.events"}); len(missing) != 0 {
		t.Fatalf("calendar.events did not satisfy calendar.readonly: %v", missing)
	}
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/gmail.metadata"},
		[]string{"https://www.googleapis.com/auth/gmail.send"}); len(missing) != 1 {
		t.Fatalf("gmail.send unexpectedly satisfied gmail.metadata: %v", missing)
	}
	if missing := missingScopes("google", []string{"https://www.googleapis.com/auth/gmail.metadata"},
		[]string{"https://www.googleapis.com/auth/gmail.modify"}); len(missing) != 0 {
		t.Fatalf("gmail.modify did not satisfy gmail.metadata: %v", missing)
	}
}

type googleCatalog struct{ name string }

func (c googleCatalog) TriggerDefinition(provider, name string) (Definition, bool) {
	if provider != "google" || (name != "new-email" && name != "calendar-event-changed") {
		return Definition{}, false
	}
	if c.name != "" && name != c.name {
		return Definition{}, false
	}
	if name == "new-email" {
		return Definition{Provider: "google", PieceName: "@activepieces/piece-gmail", PieceVersion: "0.15.0",
			TriggerName: name, RouteMode: domain.WebhookRoutePerConnection, Event: "mailbox-change",
			GoogleWatch: google.WatchKindGmail, GoogleTTL: 7 * 24 * time.Hour,
			RequiredScopes: []string{"https://www.googleapis.com/auth/gmail.metadata"}}, true
	}
	return Definition{Provider: "google", PieceName: "@activepieces/piece-google-calendar", PieceVersion: "0.10.3",
		TriggerName: name, RouteMode: domain.WebhookRoutePerConnection, Event: "channel-notification",
		GoogleWatch: google.WatchKindCalendar, GoogleTTL: 7 * 24 * time.Hour,
		RequiredScopes: []string{"https://www.googleapis.com/auth/calendar.readonly"}}, true
}

type googleWatches struct {
	calendarRequest google.CalendarWatchRequest
	gmailRequest    google.GmailWatchRequest
	channel         google.Channel
	watch           google.GmailWatch
	stopped         google.WatchState
	gmailStopped    bool
	createErr       error
	stopErr         error
	gmailErr        error
	gmailStopErr    error
	routeCreated    *bool
}

func (s *googleWatches) WatchCalendar(_ context.Context, _ string, request google.CalendarWatchRequest) (google.Channel, error) {
	s.calendarRequest = request
	if s.routeCreated != nil && !*s.routeCreated {
		return google.Channel{}, errors.New("route was not persisted before Google Calendar watch")
	}
	if s.createErr != nil {
		return google.Channel{}, s.createErr
	}
	channel := s.channel
	if channel.ID == "" {
		channel.ID = request.ChannelID
	}
	if channel.ResourceID == "" {
		channel.ResourceID = "resource-1"
	}
	if channel.CalendarID == "" {
		channel.CalendarID = request.CalendarID
	}
	if channel.Expiration.IsZero() {
		channel.Expiration = request.Expiration
	}
	return channel, nil
}
func (s *googleWatches) StopChannel(_ context.Context, _ string, state google.WatchState) error {
	s.stopped = state
	return s.stopErr
}
func (s *googleWatches) WatchGmail(_ context.Context, _ string, request google.GmailWatchRequest) (google.GmailWatch, error) {
	s.gmailRequest = request
	if s.gmailErr != nil {
		return google.GmailWatch{}, s.gmailErr
	}
	watch := s.watch
	if watch.HistoryID == "" {
		watch.HistoryID = "12345"
	}
	if watch.Expiration.IsZero() {
		watch.Expiration = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	}
	return watch, nil
}
func (s *googleWatches) StopGmail(context.Context, string) error {
	s.gmailStopped = true
	return s.gmailStopErr
}

type gmailCensus struct {
	count int
	err   error
}

func (c gmailCensus) ActiveGoogleGmailTriggerCount(context.Context, string) (int, error) {
	return c.count, c.err
}

func testGoogleConnection() domain.Connection {
	connection := testConnection()
	connection.Provider = "google"
	connection.PieceName = "@activepieces/piece-gmail"
	connection.PieceVersion = "0.15.0"
	connection.Identity.AccountID = "member@example.com"
	connection.GrantedScopes = []string{"https://www.googleapis.com/auth/calendar.events", "https://www.googleapis.com/auth/gmail.metadata"}
	return connection
}

func TestGoogleCalendarCreatePersistsRouteBeforeWatchAndCompensates(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	routes := &graphRoutes{}
	state := &graphState{}
	secrets := &graphSecrets{}
	watches := &googleWatches{routeCreated: &routes.created}
	store := &triggerStore{}
	service := Service{Catalog: googleCatalog{name: "calendar-event-changed"}, Authorizer: triggerAuthorizer{},
		Connections: triggerConnections{connection: testGoogleConnection()}, Routes: routes, Store: store, State: state,
		Secrets: secrets, Google: watches, Now: func() time.Time { return now },
		IDs:    func(prefix string) (string, error) { return prefix + "-1", nil },
		Random: bytes.NewReader(bytes.Repeat([]byte{7}, 128)), PublicURL: "https://connect.example.com"}
	trigger, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "calendar-event-changed"})
	if err != nil {
		t.Fatal(err)
	}
	if trigger.ProviderIdentifier != "con-1" || !routes.created || routes.route.ConnectionID != "con-1" ||
		watches.calendarRequest.CalendarID != "primary" ||
		watches.calendarRequest.Address != "https://connect.example.com/v1/webhooks/"+routes.route.OpaqueRoute ||
		len(watches.calendarRequest.Token) < 32 {
		t.Fatalf("trigger/route/watch = %+v/%+v/%+v", trigger, routes.route, watches.calendarRequest)
	}
	if state.values["\x00google_watch_kind"] != google.WatchKindCalendar || state.values["\x00google_channel_id"] == nil {
		t.Fatalf("calendar watch state = %#v", state.values)
	}

	store.createErr = errors.New("postgres unavailable")
	routes = &graphRoutes{}
	state = &graphState{}
	watches = &googleWatches{routeCreated: &routes.created}
	service.Routes, service.Store, service.State, service.Google = routes, store, state, watches
	service.Random = bytes.NewReader(bytes.Repeat([]byte{8}, 128))
	_, err = service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "calendar-event-changed"})
	if err == nil || watches.stopped.ChannelID == "" || !routes.deactivated || !state.deleted {
		t.Fatalf("compensation error/stop/route/state = %v/%+v/%v/%v", err, watches.stopped, routes.deactivated, state.deleted)
	}
}

func TestGoogleGmailCreateRequiresTopicAndStopsOrphanWatch(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	watches := &googleWatches{}
	service := Service{Catalog: googleCatalog{name: "new-email"}, Authorizer: triggerAuthorizer{},
		Connections: triggerConnections{connection: testGoogleConnection()}, Store: &triggerStore{}, State: &graphState{},
		Secrets: &graphSecrets{}, Google: watches, Now: func() time.Time { return now },
		IDs: func(prefix string) (string, error) { return prefix + "-1", nil }}
	_, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "new-email"})
	if !errors.Is(err, ErrRouteUnavailable) || watches.gmailRequest.TopicName != "" {
		t.Fatalf("missing topic error/watch = %v/%+v", err, watches.gmailRequest)
	}

	store := &triggerStore{}
	state := &graphState{}
	service.GmailPubSubTopic = "projects/oryxa/topics/gmail-watch"
	service.Store, service.State = store, state
	trigger, err := service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "new-email"})
	if err != nil || trigger.TriggerName != "new-email" || watches.gmailRequest.TopicName != "projects/oryxa/topics/gmail-watch" ||
		state.values["\x00google_watch_kind"] != google.WatchKindGmail {
		t.Fatalf("gmail create = %+v/%v/%+v/%#v", trigger, err, watches.gmailRequest, state.values)
	}

	store.createErr = errors.New("postgres unavailable")
	state = &graphState{}
	watches = &googleWatches{}
	service.Store, service.State, service.Google, service.GmailCensus = store, state, watches, gmailCensus{}
	_, err = service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "new-email"})
	if err == nil || !watches.gmailStopped || !state.deleted {
		t.Fatalf("orphan gmail compensation = %v stopped:%v deleted:%v", err, watches.gmailStopped, state.deleted)
	}

	watches = &googleWatches{}
	state = &graphState{}
	service.Google, service.State, service.GmailCensus = watches, state, gmailCensus{count: 2}
	_, err = service.Create(context.Background(), CreateRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", TriggerName: "new-email"})
	if err == nil || watches.gmailStopped {
		t.Fatalf("shared mailbox compensation stopped Gmail: %v/%v", err, watches.gmailStopped)
	}
}

func TestGoogleCalendarRenewAndDisableUseDurableState(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	trigger := domain.Trigger{ID: "trg-1", OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", ProfileID: "app-1",
		Provider: "google", PieceName: "@activepieces/piece-google-calendar", PieceVersion: "0.10.3",
		TriggerName: "calendar-event-changed", Status: domain.TriggerStatusActive}
	store := &triggerStore{trigger: trigger}
	token := []byte("abcdefghijklmnopqrstuvwxyz012345")
	routes := &graphRoutes{route: domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1",
		TriggerName: "calendar-event-changed", Mode: domain.WebhookRoutePerConnection, PieceName: trigger.PieceName,
		PieceVersion: trigger.PieceVersion, Active: true, SigningSecret: domain.SealedData{Version: 1, Ciphertext: token}}}
	state := &graphState{values: map[string]any{
		"\x00google_watch_kind": google.WatchKindCalendar, "\x00google_channel_id": "c-old",
		"\x00google_resource_id": "resource-old", "\x00google_calendar_id": "primary",
		"\x00google_watch_expires_at": now.Add(23 * time.Hour).Format(time.RFC3339Nano),
	}}
	watches := &googleWatches{}
	service := Service{Catalog: googleCatalog{name: "calendar-event-changed"}, Authorizer: triggerAuthorizer{},
		Connections: triggerConnections{connection: testGoogleConnection()}, Routes: routes, Store: store, State: state,
		Secrets: &graphSecrets{}, Google: watches, Now: func() time.Time { return now },
		IDs:    func(prefix string) (string, error) { return prefix + "-1", nil },
		Random: bytes.NewReader(bytes.Repeat([]byte{4}, 64)), PublicURL: "https://connect.example.com"}
	if err := service.Renew(context.Background(), "org-1", "trg-1"); err != nil || watches.calendarRequest.Token != string(token) ||
		watches.stopped.ChannelID != "c-old" || state.values["\x00google_channel_id"] == "c-old" {
		t.Fatalf("renew/request/stop/state = %v/%+v/%+v/%#v", err, watches.calendarRequest, watches.stopped, state.values)
	}
	if err := service.Disable(context.Background(), "org-1", "member-1", "trg-1"); err != nil || watches.stopped.ChannelID == "" ||
		!routes.deactivated || !store.disabled {
		t.Fatalf("disable/stop/route/store = %v/%+v/%v/%v", err, watches.stopped, routes.deactivated, store.disabled)
	}
}

func TestGoogleGmailDisableStopsOnlyTheLastMailboxWatch(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	trigger := domain.Trigger{ID: "trg-1", OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", ProfileID: "app-1",
		Provider: "google", PieceName: "@activepieces/piece-gmail", PieceVersion: "0.15.0", TriggerName: "new-email", Status: domain.TriggerStatusActive}
	store := &triggerStore{trigger: trigger}
	state := &graphState{values: map[string]any{
		"\x00google_watch_kind": google.WatchKindGmail, "\x00google_history_id": "12345",
		"\x00google_watch_expires_at": now.Add(time.Hour).Format(time.RFC3339Nano),
	}}
	watches := &googleWatches{}
	service := Service{Catalog: googleCatalog{name: "new-email"}, Authorizer: triggerAuthorizer{},
		Connections: triggerConnections{connection: testGoogleConnection()}, Store: store, State: state,
		Secrets: &graphSecrets{}, Google: watches, GmailCensus: gmailCensus{count: 2}, Now: func() time.Time { return now },
		IDs: func(prefix string) (string, error) { return prefix + "-1", nil }}
	if err := service.Disable(context.Background(), "org-1", "member-1", "trg-1"); err != nil || watches.gmailStopped || !store.disabled {
		t.Fatalf("shared mailbox disable = %v stopped:%v disabled:%v", err, watches.gmailStopped, store.disabled)
	}

	store.disabled = false
	watches = &googleWatches{}
	service.Google, service.GmailCensus = watches, gmailCensus{count: 1}
	if err := service.Disable(context.Background(), "org-1", "member-1", "trg-1"); err != nil || !watches.gmailStopped || !store.disabled {
		t.Fatalf("last mailbox disable = %v stopped:%v disabled:%v", err, watches.gmailStopped, store.disabled)
	}
}

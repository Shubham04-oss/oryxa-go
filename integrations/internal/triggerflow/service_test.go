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

func TestMicrosoftGraphCalendarReadScopeAcceptsReadWriteGrant(t *testing.T) {
	t.Parallel()
	if missing := missingScopes("microsoft", []string{"Calendars.Read"}, []string{"Calendars.ReadWrite"}); len(missing) != 0 {
		t.Fatalf("Calendars.ReadWrite did not satisfy trigger read: %v", missing)
	}
	if missing := missingScopes("microsoft", []string{"Calendars.ReadWrite"}, []string{"Calendars.Read"}); len(missing) != 1 {
		t.Fatalf("read unexpectedly satisfied write: %v", missing)
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

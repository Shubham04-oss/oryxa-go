package triggerflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

type triggerAuthorizer struct{ err error }

func (a triggerAuthorizer) CanCreateConnection(context.Context, string, string) error { return a.err }

type triggerConnections struct{ connection domain.Connection }

func (c triggerConnections) ConnectionForExecution(context.Context, string, string) (domain.Connection, error) {
	return c.connection, nil
}

type triggerCatalog struct{}

func (triggerCatalog) TriggerDefinition(pieceName, version, name string) (Definition, bool) {
	if pieceName != "piece" || version != "1" || name != "hook" {
		return Definition{}, false
	}
	return Definition{Provider: "slack", PieceName: pieceName, PieceVersion: version, TriggerName: name, RouteMode: domain.WebhookRouteAppLevel, Event: "message", ValidateProps: func(raw json.RawMessage) error { var value map[string]any; return json.Unmarshal(raw, &value) }}, true
}

type triggerRoutes struct {
	route domain.WebhookRoute
	err   error
}

func (r triggerRoutes) ActiveWebhookRoute(context.Context, string, string, string) (domain.WebhookRoute, error) {
	return r.route, r.err
}

type triggerStore struct {
	created domain.Trigger
	event   domain.Event
	trigger domain.Trigger
}

func (s *triggerStore) CreateTriggerWithEvent(_ context.Context, trigger domain.Trigger, event domain.Event) error {
	s.created = trigger
	s.event = event
	return nil
}
func (s *triggerStore) Trigger(context.Context, string, string) (domain.Trigger, error) {
	return s.trigger, nil
}
func (s *triggerStore) DisableTriggerWithEvent(context.Context, string, string, string, time.Time, domain.Event) error {
	return nil
}

type triggerState struct{ applied []piece.StoreMutation }

func (s *triggerState) LoadTriggerState(context.Context, string, string) (map[string]any, int64, error) {
	return map[string]any{}, 0, nil
}
func (s *triggerState) ApplyTriggerState(_ context.Context, _, _ string, _ int64, mutations []piece.StoreMutation) error {
	s.applied = mutations
	return nil
}

type triggerSecrets struct{}

func (triggerSecrets) Open(_ context.Context, binding secrets.Binding, _ domain.SealedData) ([]byte, error) {
	if binding.ResourceType != "connection" || binding.ResourceID != "con-1" {
		return nil, errors.New("wrong secret binding")
	}
	return []byte(`{"access_token":"secret"}`), nil
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

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

type executionStore struct {
	triggers       []domain.Trigger
	connection     domain.Connection
	claimed        bool
	existingStatus domain.ExecutionStatus
	claimCalls     int
	completed      []domain.Event
	failed         []string
}

func (s *executionStore) RunnableTriggers(_ context.Context, org, profile, pieceName, version, name, connection, identifier string) ([]domain.Trigger, error) {
	var result []domain.Trigger
	for _, trigger := range s.triggers {
		if trigger.OrganizationID == org && trigger.ProfileID == profile && trigger.PieceName == pieceName && trigger.PieceVersion == version && trigger.TriggerName == name &&
			(connection == "" || trigger.ConnectionID == connection) && (identifier == "" || trigger.ProviderIdentifier == identifier) {
			result = append(result, trigger)
		}
	}
	return result, nil
}
func (s *executionStore) ConnectionForExecution(context.Context, string, string) (domain.Connection, error) {
	return s.connection, nil
}
func (s *executionStore) ClaimTriggerExecution(_ context.Context, id, org, trigger, invocation string, now, _ time.Time) (domain.TriggerExecution, bool, error) {
	s.claimCalls++
	if !s.claimed {
		return domain.TriggerExecution{ID: id, OrganizationID: org, TriggerID: trigger, InvocationID: invocation, Status: s.existingStatus}, false, nil
	}
	return domain.TriggerExecution{ID: id, OrganizationID: org, TriggerID: trigger, InvocationID: invocation, Attempt: 1, StartedAt: now}, true, nil
}
func (s *executionStore) CompleteTriggerExecutionWithEvent(_ context.Context, _ domain.TriggerExecution, _ time.Time, event domain.Event) error {
	s.completed = append(s.completed, event)
	return nil
}
func (s *executionStore) FailTriggerExecution(_ context.Context, _ domain.TriggerExecution, _ time.Time, code string) error {
	s.failed = append(s.failed, code)
	return nil
}

type executionState struct{ mutations []piece.StoreMutation }

func (s *executionState) LoadTriggerState(context.Context, string, string) (map[string]any, int64, error) {
	return map[string]any{"cursor": "1"}, 2, nil
}
func (s *executionState) ApplyTriggerState(_ context.Context, _, _ string, version int64, mutations []piece.StoreMutation) error {
	if version != 2 {
		return errors.New("wrong version")
	}
	s.mutations = mutations
	return nil
}

type executionSecrets struct{}

func (executionSecrets) Open(_ context.Context, binding secrets.Binding, _ domain.SealedData) ([]byte, error) {
	if binding.OrganizationID != "org-1" || binding.ResourceID != "con-1" {
		return nil, errors.New("wrong binding")
	}
	return []byte(`{"access_token":"xoxb-secret","data":{"team":{"id":"T123"}}}`), nil
}

type executionRunner struct {
	calls      int
	invocation piece.TriggerInvocation
	output     json.RawMessage
}

func (r *executionRunner) RunTrigger(_ context.Context, invocation piece.TriggerInvocation) (piece.TriggerResult, error) {
	r.calls++
	r.invocation = invocation
	return piece.TriggerResult{Output: r.output, StoreMutations: []piece.StoreMutation{{Operation: "put", Key: "cursor", Value: "2"}}}, nil
}

func TestProcessWebhookRoutesOneWorkspaceAndPublishesDomainMessage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	trigger := domain.Trigger{ID: "trg-1", OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "con-1", ProfileID: "app-1", Provider: "slack", PieceName: "@activepieces/piece-slack", PieceVersion: "0.17.10", TriggerName: "new-message", ProviderIdentifier: "T123", Props: json.RawMessage(`{"ignoreBots":true}`), Status: domain.TriggerStatusActive}
	store := &executionStore{triggers: []domain.Trigger{trigger, {ID: "other-workspace", OrganizationID: "org-1", ProfileID: "app-1", PieceName: trigger.PieceName, PieceVersion: trigger.PieceVersion, TriggerName: trigger.TriggerName, ProviderIdentifier: "T999", Status: domain.TriggerStatusActive}}, claimed: true,
		connection: domain.Connection{ID: "con-1", OrganizationID: "org-1", ProfileID: "app-1", Provider: "slack", PieceName: trigger.PieceName, PieceVersion: trigger.PieceVersion, Identity: domain.ProviderIdentity{TenantID: "T123"}, Status: domain.ConnectionStatusActive}}
	state := &executionState{}
	runner := &executionRunner{output: json.RawMessage(`[{"channel":"C123","user":"U123","text":"hello","ts":"1.23"}]`)}
	service := Service{Store: store, State: state, Secrets: executionSecrets{}, Runner: runner, Mapper: DomainMapper{}, Now: func() time.Time { return now }, IDs: func(prefix string) (string, error) { return prefix + "-1", nil }}
	envelope := webhooks.Envelope{InvocationID: "whi-1", OrganizationID: "org-1", ProfileID: "app-1", Provider: "slack", PieceName: trigger.PieceName, PieceVersion: trigger.PieceVersion, TriggerName: trigger.TriggerName, IdentifierValue: "T123", Method: "POST", RawBody: []byte(`{"secret":"provider-only"}`), ReceivedAt: now.Add(-time.Second)}
	if err := service.ProcessWebhook(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 || runner.invocation.InitialStore["cursor"] != "1" || len(state.mutations) != 1 {
		t.Fatalf("runner/state = %d/%+v/%+v", runner.calls, runner.invocation.InitialStore, state.mutations)
	}
	if len(store.completed) != 1 || store.completed[0].DetailType != integrationevents.SlackMessageReceivedDetailType {
		t.Fatalf("completed events = %+v", store.completed)
	}
	data := store.completed[0].Data.(integrationevents.SlackMessageReceived)
	if data.WorkspaceID != "T123" || len(data.Messages) != 1 || data.Messages[0].Text != "hello" {
		t.Fatalf("domain event = %+v", data)
	}
	encoded, _ := json.Marshal(data)
	for _, forbidden := range []string{"xoxb-secret", "provider-only", "access_token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("event leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestProcessWebhookRedeliverySkipsSucceededExecution(t *testing.T) {
	t.Parallel()
	store := &executionStore{triggers: []domain.Trigger{{ID: "trg-1", OrganizationID: "org-1", ConnectionID: "con-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", TriggerName: "hook", ProviderIdentifier: "tenant", Status: domain.TriggerStatusActive}}, claimed: false, existingStatus: domain.ExecutionStatusSucceeded}
	runner := &executionRunner{}
	service := Service{Store: store, State: &executionState{}, Secrets: executionSecrets{}, Runner: runner, Mapper: DomainMapper{}, IDs: func(string) (string, error) { return "exe", nil }}
	err := service.ProcessWebhook(context.Background(), webhooks.Envelope{InvocationID: "delivery", OrganizationID: "org-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", TriggerName: "hook", IdentifierValue: "tenant"})
	if err != nil || runner.calls != 0 || store.claimCalls != 1 {
		t.Fatalf("error/runner/claims = %v/%d/%d", err, runner.calls, store.claimCalls)
	}
}

func TestProcessWebhookKeepsConcurrentExecutionOnQueue(t *testing.T) {
	t.Parallel()
	store := &executionStore{triggers: []domain.Trigger{{ID: "trg-1", OrganizationID: "org-1", ConnectionID: "con-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", TriggerName: "hook", ProviderIdentifier: "tenant", Status: domain.TriggerStatusActive}}, existingStatus: domain.ExecutionStatusProcessing}
	runner := &executionRunner{}
	service := Service{Store: store, State: &executionState{}, Secrets: executionSecrets{}, Runner: runner, Mapper: DomainMapper{}, IDs: func(string) (string, error) { return "id", nil }}
	err := service.ProcessWebhook(context.Background(), webhooks.Envelope{InvocationID: "delivery", OrganizationID: "org-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", TriggerName: "hook", IdentifierValue: "tenant"})
	if !errors.Is(err, ErrExecutionInProgress) || runner.calls != 0 {
		t.Fatalf("error/runner = %v/%d", err, runner.calls)
	}
}

func TestProcessWebhookRejectsConnectionTenantMismatchBeforeRunner(t *testing.T) {
	t.Parallel()
	trigger := domain.Trigger{ID: "trg-1", OrganizationID: "org-1", ConnectionID: "con-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", TriggerName: "hook", ProviderIdentifier: "T123", Status: domain.TriggerStatusActive}
	store := &executionStore{triggers: []domain.Trigger{trigger}, claimed: true, connection: domain.Connection{ID: "con-1", OrganizationID: "org-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", Identity: domain.ProviderIdentity{TenantID: "T999"}, Status: domain.ConnectionStatusActive}}
	runner := &executionRunner{}
	service := Service{Store: store, State: &executionState{}, Secrets: executionSecrets{}, Runner: runner, Mapper: DomainMapper{}, IDs: func(string) (string, error) { return "id", nil }}
	err := service.ProcessWebhook(context.Background(), webhooks.Envelope{InvocationID: "delivery", OrganizationID: "org-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", TriggerName: "hook", IdentifierValue: "T123"})
	if !errors.Is(err, ErrIsolationMismatch) || runner.calls != 0 || len(store.failed) != 1 || store.failed[0] != "isolation_mismatch" {
		t.Fatalf("error/runner/failed = %v/%d/%+v", err, runner.calls, store.failed)
	}
}

package actionflow

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
)

func TestRunMapsSafeSlackResultAndReplaysDurably(t *testing.T) {
	store := &actionStore{executions: map[string]domain.ActionExecution{}}
	runner := &actionRunner{output: json.RawMessage(`{"ok":true,"channel":"C123","ts":"171.2","message":{"text":"private content","thread_ts":"170.1"},"token":"must-not-leak"}`)}
	service := testService(store, runner)
	request := RunRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "connection-1",
		ActionName: "post-message", IdempotencyKey: "request-123", Props: json.RawMessage(`{"channel":"C123","text":"private content"}`)}

	result, err := service.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || runner.calls != 1 || !strings.Contains(string(result.Result), `"message_timestamp":"171.2"`) {
		t.Fatalf("result/calls = %+v/%d", result, runner.calls)
	}
	if runner.invocation.PieceName != "action-piece" || runner.invocation.PieceVersion != "2" {
		t.Fatalf("action ran with connection adapter instead of catalog piece: %+v", runner.invocation)
	}
	if len(store.events) != 1 {
		t.Fatalf("events = %+v", store.events)
	}
	for _, forbidden := range []string{"private content", "must-not-leak", "token"} {
		if strings.Contains(string(result.Result), forbidden) {
			t.Fatalf("safe result leaked %q: %s", forbidden, result.Result)
		}
	}
	if store.events[0].DetailType != integrationevents.SlackMessageSentDetailType {
		t.Fatalf("event = %+v", store.events[0])
	}

	replay, err := service.Run(context.Background(), request)
	if err != nil || !replay.Replayed || replay.ExecutionID != result.ExecutionID || runner.calls != 1 {
		t.Fatalf("replay/calls = %+v/%d/%v", replay, runner.calls, err)
	}
}

func TestRunRejectsCrossMemberScopeAndIdempotencyChanges(t *testing.T) {
	store := &actionStore{executions: map[string]domain.ActionExecution{}}
	runner := &actionRunner{output: json.RawMessage(`{"ok":true,"channel":"C123","ts":"171.2"}`)}
	service := testService(store, runner)

	wrongMember := RunRequest{OrganizationID: "org-1", MemberID: "member-2", ConnectionID: "connection-1",
		ActionName: "post-message", IdempotencyKey: "request-123", Props: json.RawMessage(`{"channel":"C123","text":"hello"}`)}
	if _, err := service.Run(context.Background(), wrongMember); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong member error = %v", err)
	}

	service.Connections = actionConnections{connection: domain.Connection{ID: "connection-1", OrganizationID: "org-1", MemberID: "member-1",
		Provider: "slack", PieceName: "piece", PieceVersion: "1", Status: domain.ConnectionStatusActive}}
	request := wrongMember
	request.MemberID = "member-1"
	if _, err := service.Run(context.Background(), request); !errors.Is(err, ErrRequiredScope) {
		t.Fatalf("scope error = %v", err)
	}

	service = testService(store, runner)
	if _, err := service.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Props = json.RawMessage(`{"channel":"C123","text":"different"}`)
	if _, err := service.Run(context.Background(), request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency error = %v", err)
	}
}

func TestFailedWriteIsNotAutomaticallyRetried(t *testing.T) {
	store := &actionStore{executions: map[string]domain.ActionExecution{}}
	runner := &actionRunner{err: errors.New("provider timeout")}
	service := testService(store, runner)
	request := RunRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "connection-1",
		ActionName: "post-message", IdempotencyKey: "request-123", Props: json.RawMessage(`{"channel":"C123","text":"hello"}`)}
	if _, err := service.Run(context.Background(), request); !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("first error = %v", err)
	}
	if _, err := service.Run(context.Background(), request); !errors.Is(err, ErrActionPreviouslyFailed) {
		t.Fatalf("replay error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
}

func testService(store *actionStore, runner *actionRunner) Service {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	return Service{
		Catalog: actionCatalog{}, Authorizer: allowActions{},
		Connections: actionConnections{connection: domain.Connection{ID: "connection-1", OrganizationID: "org-1", MemberID: "member-1",
			Provider: "slack", PieceName: "auth-piece", PieceVersion: "1", GrantedScopes: []string{"chat:write"}, Status: domain.ConnectionStatusActive}},
		Store: store, Credentials: staticCredentials{}, Runner: runner, Mapper: DomainMapper{},
		Now: func() time.Time { return now }, IDs: func(prefix string) (string, error) { return prefix + "-1", nil },
	}
}

type allowActions struct{}

func (allowActions) CanCreateConnection(context.Context, string, string) error { return nil }

type actionConnections struct{ connection domain.Connection }

func (c actionConnections) ConnectionForExecution(context.Context, string, string) (domain.Connection, error) {
	return c.connection, nil
}

type actionCatalog struct{}

func (actionCatalog) ActionDefinition(provider, name string) (Definition, bool) {
	if provider != "slack" || name != "post-message" {
		return Definition{}, false
	}
	return Definition{Provider: "slack", PieceName: "action-piece", PieceVersion: "2",
		ActionName: name, RunnerActionName: "slack_post_message", RequiredScopes: []string{"chat:write"},
		ValidateProps: func(json.RawMessage) error { return nil }}, true
}

type staticCredentials struct{}

func (staticCredentials) ForExecution(context.Context, domain.Connection) ([]byte, error) {
	return []byte(`{"access_token":"secret"}`), nil
}

type actionRunner struct {
	output     json.RawMessage
	err        error
	calls      int
	invocation piece.ActionInvocation
}

func (r *actionRunner) RunAction(_ context.Context, invocation piece.ActionInvocation) (piece.ActionResult, error) {
	r.calls++
	r.invocation = invocation
	return piece.ActionResult{Output: r.output}, r.err
}

type actionStore struct {
	executions map[string]domain.ActionExecution
	events     []domain.Event
}

func actionKey(value domain.ActionExecution) string {
	return value.OrganizationID + "/" + value.ConnectionID + "/" + value.ActionName + "/" + value.IdempotencyKeyHash
}
func (s *actionStore) ClaimActionExecution(_ context.Context, value domain.ActionExecution) (domain.ActionExecution, bool, error) {
	key := actionKey(value)
	if existing, ok := s.executions[key]; ok {
		return existing, false, nil
	}
	s.executions[key] = value
	return value, true, nil
}
func (s *actionStore) CompleteActionExecutionWithEvent(_ context.Context, value domain.ActionExecution, result json.RawMessage, at time.Time, event domain.Event) error {
	value.Status = domain.ExecutionStatusSucceeded
	value.SafeResult = append(json.RawMessage(nil), result...)
	value.CompletedAt = &at
	s.executions[actionKey(value)] = value
	if event.EventID != "" {
		s.events = append(s.events, event)
	}
	return nil
}
func (s *actionStore) FailActionExecution(_ context.Context, value domain.ActionExecution, at time.Time, _ string) error {
	value.Status = domain.ExecutionStatusFailed
	value.CompletedAt = &at
	s.executions[actionKey(value)] = value
	return nil
}

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
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/filebroker"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/filesclient"
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
	_, err := service.Run(context.Background(), request)
	var failed ExecutionFailed
	if !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("first error = %v", err)
	}
	if errors.As(err, &failed) {
		t.Fatalf("opaque runner error became a public execution failure: %+v", failed)
	}
	if _, err := service.Run(context.Background(), request); !errors.Is(err, ErrActionPreviouslyFailed) {
		t.Fatalf("replay error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
}

func TestRunSurfacesValidationAndStableRunnerMessages(t *testing.T) {
	store := &actionStore{executions: map[string]domain.ActionExecution{}}
	service := testService(store, &actionRunner{})
	service.Catalog = validatingCatalog{}
	request := RunRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "connection-1",
		ActionName: "post-message", IdempotencyKey: "request-123", Props: json.RawMessage(`{"channel":"C123","text":"hello"}`)}
	_, err := service.Run(context.Background(), request)
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "invalid Slack post-message properties") {
		t.Fatalf("validation error = %v", err)
	}

	store = &actionStore{executions: map[string]domain.ActionExecution{}}
	runner := &actionRunner{err: piece.StableRunnerError("file_too_large", "file exceeds the size limit")}
	service = testService(store, runner)
	_, err = service.Run(context.Background(), request)
	var failed ExecutionFailed
	if !errors.As(err, &failed) || failed.Code != "file_too_large" || failed.Message != "file exceeds the size limit" {
		t.Fatalf("runner error = %+v / %v", failed, err)
	}
	if len(store.failedCodes) != 1 || store.failedCodes[0] != "file_too_large" {
		t.Fatalf("failed codes = %+v", store.failedCodes)
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

func TestRunRejectsFileActionsWhenBrokerIsMissing(t *testing.T) {
	store := &actionStore{executions: map[string]domain.ActionExecution{}}
	runner := &actionRunner{output: json.RawMessage(`{"ok":true}`)}
	service := testService(store, runner)
	service.Catalog = filesActionCatalog{}
	service.Connections = actionConnections{connection: domain.Connection{ID: "connection-1", OrganizationID: "org-1", MemberID: "member-1",
		Provider: "slack", PieceName: "piece", PieceVersion: "1", GrantedScopes: []string{"files:write"}, Status: domain.ConnectionStatusActive}}
	request := RunRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "connection-1",
		ActionName: "upload-file", IdempotencyKey: "request-file", Props: json.RawMessage(`{"file":{"file_id":"file_abc"}}`),
		Authorization: "Bearer user-token"}
	if _, err := service.Run(context.Background(), request); !errors.Is(err, ErrFilesUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
}

func TestRunAttachesFileBrokerAndCleansSession(t *testing.T) {
	store := &actionStore{executions: map[string]domain.ActionExecution{}}
	runner := &actionRunner{output: json.RawMessage(`{"ok":true,"files":[{"id":"F123"}]}`)}
	sessions := filebroker.NewMemory()
	service := testService(store, runner)
	service.Catalog = filesActionCatalog{}
	service.Files = stubFiles{}
	service.FileSessions = sessions
	service.FileBrokerURL = "https://connect.oryxa.test/v1/internal/piece-files"
	service.Connections = actionConnections{connection: domain.Connection{ID: "connection-1", OrganizationID: "org-1", MemberID: "member-1",
		Provider: "slack", PieceName: "piece", PieceVersion: "1", GrantedScopes: []string{"files:write"}, Status: domain.ConnectionStatusActive}}
	result, err := service.Run(context.Background(), RunRequest{
		OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "connection-1",
		ActionName: "upload-file", IdempotencyKey: "request-file", Props: json.RawMessage(`{"file":{"file_id":"file_abc"}}`),
		Authorization: "Bearer user-token",
	})
	if err != nil || result.Status != ExecutionSucceeded {
		t.Fatalf("result/error = %+v / %v", result, err)
	}
	if runner.invocation.FilesBroker == nil || runner.invocation.FilesBroker.URL != service.FileBrokerURL ||
		runner.invocation.FilesBroker.Token == "" {
		t.Fatalf("files broker = %+v", runner.invocation.FilesBroker)
	}
	if _, getErr := sessions.Get(context.Background(), runner.invocation.FilesBroker.Token); getErr == nil {
		t.Fatal("file broker session was not deleted after the run")
	}
}

func TestRunStoresGitHubFileContentAndStripsBytes(t *testing.T) {
	store := &actionStore{executions: map[string]domain.ActionExecution{}}
	runner := &actionRunner{output: json.RawMessage(`{"name":"readme.md","path":"docs/readme.md","sha":"abc1234","size":5,"html_url":"https://github.com/oryxa/api/blob/main/docs/readme.md","type":"file","encoding":"base64","content":"aGVsbG8=\n","download_url":"secret-url"}`)}
	files := &capturingFiles{}
	service := testService(store, runner)
	service.Catalog = githubFileCatalog{}
	service.Files = files
	service.Connections = actionConnections{connection: domain.Connection{ID: "connection-1", OrganizationID: "org-1", MemberID: "member-1",
		Provider: "github", PieceName: "piece", PieceVersion: "1", GrantedScopes: []string{"repo"}, Status: domain.ConnectionStatusActive}}
	result, err := service.Run(context.Background(), RunRequest{
		OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "connection-1",
		ActionName: "get-file-content", IdempotencyKey: "request-github-file",
		Props: json.RawMessage(`{"owner":"oryxa","repo":"api","path":"docs/readme.md"}`), Authorization: "Bearer user-token",
	})
	if err != nil || result.Status != ExecutionSucceeded {
		t.Fatalf("result/error = %+v / %v", result, err)
	}
	if len(files.created) != 1 || string(files.created[0].Content) != "hello" || files.created[0].Purpose != filesclient.PurposePiece {
		t.Fatalf("stored files = %+v", files.created)
	}
	if strings.Contains(string(result.Result), "aGVsbG8") || strings.Contains(string(result.Result), "secret-url") ||
		!strings.Contains(string(result.Result), `"file_id":"file_stored"`) {
		t.Fatalf("safe result = %s", result.Result)
	}
	if runner.invocation.FilesBroker != nil {
		t.Fatalf("get-file-content opened a piece file broker: %+v", runner.invocation.FilesBroker)
	}
}

func TestRunRejectsGitHubFileContentWhenFilesAreMissing(t *testing.T) {
	store := &actionStore{executions: map[string]domain.ActionExecution{}}
	runner := &actionRunner{output: json.RawMessage(`{"name":"readme.md","path":"docs/readme.md","type":"file","encoding":"base64","content":"aGVsbG8="}`)}
	service := testService(store, runner)
	service.Catalog = githubFileCatalog{}
	service.Connections = actionConnections{connection: domain.Connection{ID: "connection-1", OrganizationID: "org-1", MemberID: "member-1",
		Provider: "github", PieceName: "piece", PieceVersion: "1", GrantedScopes: []string{"repo"}, Status: domain.ConnectionStatusActive}}
	if _, err := service.Run(context.Background(), RunRequest{
		OrganizationID: "org-1", MemberID: "member-1", ConnectionID: "connection-1",
		ActionName: "get-file-content", IdempotencyKey: "request-github-file",
		Props: json.RawMessage(`{"owner":"oryxa","repo":"api","path":"docs/readme.md"}`), Authorization: "Bearer user-token",
	}); !errors.Is(err, ErrFilesUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

type capturingFiles struct {
	created []filesclient.CreateInput
}

func (c *capturingFiles) Create(_ context.Context, _ filesclient.Credentials, _ string, input filesclient.CreateInput) (filesclient.Object, error) {
	c.created = append(c.created, input)
	return filesclient.Object{ID: "file_stored", Filename: input.Filename}, nil
}

func (c *capturingFiles) Content(context.Context, filesclient.Credentials, string, string) (filesclient.Object, []byte, error) {
	return filesclient.Object{}, nil, errors.New("unused")
}

type githubFileCatalog struct{}

func (githubFileCatalog) ActionDefinition(provider, name string) (Definition, bool) {
	if provider != "github" || name != "get-file-content" {
		return Definition{}, false
	}
	return Definition{Provider: "github", PieceName: "action-piece", PieceVersion: "2",
		ActionName: name, RunnerActionName: "get_file_content", RequiredScopes: []string{"repo"},
		ValidateProps: func(json.RawMessage) error { return nil }}, true
}

type stubFiles struct{}

func (stubFiles) Create(context.Context, filesclient.Credentials, string, filesclient.CreateInput) (filesclient.Object, error) {
	return filesclient.Object{}, errors.New("unused")
}
func (stubFiles) Content(context.Context, filesclient.Credentials, string, string) (filesclient.Object, []byte, error) {
	return filesclient.Object{}, nil, errors.New("unused")
}

type filesActionCatalog struct{}

func (filesActionCatalog) ActionDefinition(provider, name string) (Definition, bool) {
	if provider != "slack" || name != "upload-file" {
		return Definition{}, false
	}
	return Definition{Provider: "slack", PieceName: "action-piece", PieceVersion: "2",
		ActionName: name, RunnerActionName: "uploadFile", RequiredScopes: []string{"files:write"}, NeedsFiles: true,
		ValidateProps: func(json.RawMessage) error { return nil }}, true
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

type validatingCatalog struct{}

func (validatingCatalog) ActionDefinition(provider, name string) (Definition, bool) {
	if provider != "slack" || name != "post-message" {
		return Definition{}, false
	}
	return Definition{Provider: "slack", PieceName: "action-piece", PieceVersion: "2",
		ActionName: name, RunnerActionName: "slack_post_message", RequiredScopes: []string{"chat:write"},
		ValidateProps: func(json.RawMessage) error { return errors.New("invalid Slack post-message properties") }}, true
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
	executions  map[string]domain.ActionExecution
	events      []domain.Event
	failedCodes []string
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
func (s *actionStore) FailActionExecution(_ context.Context, value domain.ActionExecution, at time.Time, code string) error {
	value.Status = domain.ExecutionStatusFailed
	value.CompletedAt = &at
	s.executions[actionKey(value)] = value
	s.failedCodes = append(s.failedCodes, code)
	return nil
}

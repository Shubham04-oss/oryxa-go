package actionflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
)

var (
	ErrInvalidInput           = errors.New("invalid action input")
	ErrForbidden              = errors.New("action permission denied")
	ErrOperationDisabled      = errors.New("integration action is not enabled")
	ErrRequiredScope          = errors.New("integration action requires additional scope")
	ErrIdempotencyConflict    = errors.New("idempotency key was already used with different input")
	ErrActionInProgress       = errors.New("integration action is already in progress")
	ErrActionPreviouslyFailed = errors.New("integration action previously failed; use a new idempotency key for an explicit retry")
	ErrPieceOutputInvalid     = errors.New("piece action output is invalid")
	ErrExecutionFailed        = errors.New("integration action execution failed")
)

type Definition struct {
	Provider         string
	PieceName        string
	PieceVersion     string
	ActionName       string
	RunnerActionName string
	RequiredScopes   []string
	ValidateProps    func(json.RawMessage) error
}

type Catalog interface {
	ActionDefinition(string, string) (Definition, bool)
}

type Authorizer interface {
	CanCreateConnection(context.Context, string, string) error
}

type Connections interface {
	ConnectionForExecution(context.Context, string, string) (domain.Connection, error)
}

type Store interface {
	ClaimActionExecution(context.Context, domain.ActionExecution) (domain.ActionExecution, bool, error)
	CompleteActionExecutionWithEvent(context.Context, domain.ActionExecution, json.RawMessage, time.Time, domain.Event) error
	FailActionExecution(context.Context, domain.ActionExecution, time.Time, string) error
}

type Credentials interface {
	ForExecution(context.Context, domain.Connection) ([]byte, error)
}

type Mapper interface {
	Map(MapRequest) (MappedResult, error)
}

type MapRequest struct {
	Execution  domain.ActionExecution
	Connection domain.Connection
	ActionName string
	Props      json.RawMessage
	Output     json.RawMessage
	OccurredAt time.Time
}

type MappedResult struct {
	SafeResult json.RawMessage
	DetailType string
	Data       any
}

type RunRequest struct {
	OrganizationID string
	MemberID       string
	ConnectionID   string
	ActionName     string
	IdempotencyKey string
	Props          json.RawMessage
}

type RunResult struct {
	ExecutionID string          `json:"execution_id"`
	Status      ExecutionStatus `json:"status"`
	Result      json.RawMessage `json:"result"`
	Replayed    bool            `json:"replayed"`
}

type ExecutionStatus string

const ExecutionSucceeded ExecutionStatus = "succeeded"

type Service struct {
	Catalog     Catalog
	Authorizer  Authorizer
	Connections Connections
	Store       Store
	Credentials Credentials
	Runner      piece.ActionRunner
	Mapper      Mapper
	Now         func() time.Time
	IDs         func(string) (string, error)
	PublicURL   string
	MaxOutput   int
}

func (s Service) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if request.OrganizationID == "" || request.MemberID == "" || request.ConnectionID == "" || request.ActionName == "" || !validIdempotencyKey(request.IdempotencyKey) {
		return RunResult{}, ErrInvalidInput
	}
	if err := s.Authorizer.CanCreateConnection(ctx, request.OrganizationID, request.MemberID); err != nil {
		return RunResult{}, fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	connection, err := s.Connections.ConnectionForExecution(ctx, request.OrganizationID, request.ConnectionID)
	if err != nil {
		return RunResult{}, err
	}
	if connection.OrganizationID != request.OrganizationID || connection.MemberID != request.MemberID || connection.Status != domain.ConnectionStatusActive {
		return RunResult{}, ErrForbidden
	}
	definition, ok := s.Catalog.ActionDefinition(connection.Provider, request.ActionName)
	if !ok || definition.Provider != connection.Provider {
		return RunResult{}, ErrOperationDisabled
	}
	props, err := canonicalObject(request.Props)
	if err != nil {
		return RunResult{}, err
	}
	if definition.ValidateProps == nil || definition.ValidateProps(props) != nil {
		return RunResult{}, ErrInvalidInput
	}
	if missing := missingScopes(connection.Provider, definition.RequiredScopes, connection.GrantedScopes); len(missing) > 0 {
		return RunResult{}, fmt.Errorf("%w: %s", ErrRequiredScope, strings.Join(missing, ","))
	}
	executionID, err := s.newID("act")
	if err != nil {
		return RunResult{}, err
	}
	now := s.now()
	execution := domain.ActionExecution{
		ID: executionID, OrganizationID: request.OrganizationID, MemberID: request.MemberID,
		ConnectionID: connection.ID, Provider: connection.Provider,
		// A connection is provider-level. The catalog selects the exact reviewed
		// piece for this operation (for example Gmail, Calendar, or Drive) while
		// all of them consume the same Google OAuth grant.
		PieceName: definition.PieceName, PieceVersion: definition.PieceVersion,
		ActionName: definition.ActionName, IdempotencyKeyHash: digest(request.IdempotencyKey),
		InputHash: digest(string(props)), Status: domain.ExecutionStatusProcessing,
		Attempt: 1, StartedAt: now,
	}
	execution, claimed, err := s.Store.ClaimActionExecution(ctx, execution)
	if err != nil {
		return RunResult{}, err
	}
	if !claimed {
		if execution.InputHash != digest(string(props)) || execution.MemberID != request.MemberID {
			return RunResult{}, ErrIdempotencyConflict
		}
		switch execution.Status {
		case domain.ExecutionStatusSucceeded:
			return RunResult{ExecutionID: execution.ID, Status: ExecutionSucceeded,
				Result: append(json.RawMessage(nil), execution.SafeResult...), Replayed: true}, nil
		case domain.ExecutionStatusFailed:
			return RunResult{}, ErrActionPreviouslyFailed
		default:
			return RunResult{}, ErrActionInProgress
		}
	}
	fail := func(code string, cause error) (RunResult, error) {
		markErr := s.Store.FailActionExecution(ctx, execution, s.now(), code)
		return RunResult{}, errors.Join(cause, markErr)
	}
	auth, err := s.Credentials.ForExecution(ctx, connection)
	if err != nil {
		return fail("credential_unavailable", err)
	}
	defer wipe(auth)
	if !json.Valid(auth) {
		return fail("credential_invalid", errors.New("connection credential is invalid"))
	}
	output, err := s.Runner.RunAction(ctx, piece.ActionInvocation{
		InvocationID: execution.ID, OrganizationID: execution.OrganizationID,
		PieceName: execution.PieceName, PieceVersion: execution.PieceVersion,
		ActionName: definition.RunnerActionName, Auth: json.RawMessage(auth),
		Props: props, PublicURL: s.PublicURL,
	})
	if err != nil {
		return fail("piece_failed", fmt.Errorf("%w: %v", ErrExecutionFailed, err))
	}
	limit := s.MaxOutput
	if limit == 0 {
		limit = 1024 * 1024
	}
	if len(output.Output) > limit || !json.Valid(output.Output) {
		return fail("output_invalid", ErrPieceOutputInvalid)
	}
	mapped, err := s.Mapper.Map(MapRequest{Execution: execution, Connection: connection,
		ActionName: definition.ActionName, Props: props, Output: output.Output, OccurredAt: s.now()})
	if err != nil || !json.Valid(mapped.SafeResult) {
		return fail("output_mapping_failed", errors.Join(ErrPieceOutputInvalid, err))
	}
	var event domain.Event
	if mapped.DetailType != "" {
		eventID, err := s.newID("evt")
		if err != nil {
			return fail("id_unavailable", err)
		}
		event = domain.Event{EventID: eventID, Source: integrationevents.Source,
			DetailType: mapped.DetailType, Version: 1, OrganizationID: execution.OrganizationID,
			OccurredAt: s.now(), Data: mapped.Data}
	}
	if err := s.Store.CompleteActionExecutionWithEvent(ctx, execution, mapped.SafeResult, s.now(), event); err != nil {
		return fail("completion_failed", err)
	}
	return RunResult{ExecutionID: execution.ID, Status: ExecutionSucceeded,
		Result: append(json.RawMessage(nil), mapped.SafeResult...)}, nil
}

func canonicalObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, ErrInvalidInput
	}
	result, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return result, nil
}

func validIdempotencyKey(value string) bool {
	if len(value) < 8 || len(value) > 200 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func missingScopes(provider string, required, actual []string) []string {
	set := make(map[string]struct{}, len(actual))
	for _, value := range actual {
		set[value] = struct{}{}
	}
	var missing []string
	for _, value := range required {
		_, granted := set[value]
		if provider == "microsoft" && value == "Calendars.Read" {
			_, implied := set["Calendars.ReadWrite"]
			granted = granted || implied
		}
		if !granted {
			missing = append(missing, value)
		}
	}
	slices.Sort(missing)
	return missing
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) newID(prefix string) (string, error) {
	if s.IDs == nil {
		return "", errors.New("ID generator is required")
	}
	return s.IDs(prefix)
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

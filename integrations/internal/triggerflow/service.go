package triggerflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

var (
	ErrInvalidInput      = errors.New("invalid trigger input")
	ErrOperationDisabled = errors.New("trigger operation is not enabled")
	ErrRouteUnavailable  = errors.New("trigger webhook route is not configured")
	ErrIdentityMismatch  = errors.New("provider listener identity mismatch")
)

type Definition struct {
	Provider        string
	PieceName       string
	PieceVersion    string
	TriggerName     string
	RouteMode       domain.WebhookRouteMode
	Event           string
	TimestampHeader string
	TimestampWindow time.Duration
	MaxBodyBytes    int64
	ValidateProps   func(json.RawMessage) error
}

type Catalog interface {
	TriggerDefinition(string, string) (Definition, bool)
}

type Authorizer interface {
	CanCreateConnection(context.Context, string, string) error
}

type Connections interface {
	ConnectionForExecution(context.Context, string, string) (domain.Connection, error)
}

type Routes interface {
	ActiveWebhookRoute(context.Context, string, string, string) (domain.WebhookRoute, error)
}

type Store interface {
	CreateTriggerWithEvent(context.Context, domain.Trigger, domain.Event) error
	Trigger(context.Context, string, string) (domain.Trigger, error)
	DisableTriggerWithEvent(context.Context, string, string, string, time.Time, domain.Event) error
}

type RuntimeState interface {
	LoadTriggerState(context.Context, string, string) (map[string]any, int64, error)
	ApplyTriggerState(context.Context, string, string, int64, []piece.StoreMutation) error
}

type SecretBox interface {
	Open(context.Context, secrets.Binding, domain.SealedData) ([]byte, error)
}

type CredentialSource interface {
	ForExecution(context.Context, domain.Connection) ([]byte, error)
}

type CreateRequest struct {
	OrganizationID string
	MemberID       string
	ConnectionID   string
	TriggerName    string
	Props          json.RawMessage
}

type Service struct {
	Catalog     Catalog
	Authorizer  Authorizer
	Connections Connections
	Routes      Routes
	Store       Store
	State       RuntimeState
	Secrets     SecretBox
	Credentials CredentialSource
	Runner      piece.TriggerRunner
	Now         func() time.Time
	IDs         func(string) (string, error)
	PublicURL   string
}

func (s Service) Create(ctx context.Context, request CreateRequest) (domain.Trigger, error) {
	if request.OrganizationID == "" || request.MemberID == "" || request.ConnectionID == "" || request.TriggerName == "" {
		return domain.Trigger{}, ErrInvalidInput
	}
	if len(request.Props) == 0 {
		request.Props = json.RawMessage(`{}`)
	}
	if len(request.Props) > 32*1024 || !json.Valid(request.Props) {
		return domain.Trigger{}, ErrInvalidInput
	}
	if err := s.Authorizer.CanCreateConnection(ctx, request.OrganizationID, request.MemberID); err != nil {
		return domain.Trigger{}, fmt.Errorf("%w: %v", oauthflow.ErrForbidden, err)
	}
	connection, err := s.Connections.ConnectionForExecution(ctx, request.OrganizationID, request.ConnectionID)
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("load trigger connection: %w", err)
	}
	if connection.MemberID != request.MemberID {
		return domain.Trigger{}, oauthflow.ErrForbidden
	}
	definition, ok := s.Catalog.TriggerDefinition(connection.Provider, request.TriggerName)
	if !ok || definition.Provider != connection.Provider {
		return domain.Trigger{}, ErrOperationDisabled
	}
	if definition.ValidateProps != nil {
		if err := definition.ValidateProps(request.Props); err != nil {
			return domain.Trigger{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
	}
	route, err := s.Routes.ActiveWebhookRoute(ctx, request.OrganizationID, connection.ProfileID, request.TriggerName)
	if err != nil || !route.Active || route.Mode != definition.RouteMode || route.PieceName != definition.PieceName || route.PieceVersion != definition.PieceVersion {
		return domain.Trigger{}, ErrRouteUnavailable
	}
	providerIdentifier := connection.Identity.TenantID
	if definition.RouteMode == domain.WebhookRoutePerConnection {
		providerIdentifier = connection.ID
	}
	if providerIdentifier == "" {
		return domain.Trigger{}, ErrIdentityMismatch
	}
	triggerID, err := s.newID("trg")
	if err != nil {
		return domain.Trigger{}, err
	}
	auth, err := s.openConnection(ctx, connection)
	if err != nil {
		return domain.Trigger{}, err
	}
	defer wipe(auth)
	initial, stateVersion, err := s.State.LoadTriggerState(ctx, request.OrganizationID, triggerID)
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("load trigger state: %w", err)
	}
	enabled, err := s.Runner.RunTrigger(ctx, piece.TriggerInvocation{
		InvocationID: triggerID + "-enable", OrganizationID: request.OrganizationID,
		PieceName: definition.PieceName, PieceVersion: definition.PieceVersion,
		Operation: "trigger.enable", TriggerName: request.TriggerName,
		Auth: auth, Props: request.Props, WebhookURL: s.PublicURL + "/v1/webhooks/" + route.OpaqueRoute,
		PublicURL: s.PublicURL, InitialStore: initial,
	})
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("enable piece trigger: %w", err)
	}
	if definition.RouteMode == domain.WebhookRouteAppLevel && !hasListener(enabled.Listeners, definition.Event, providerIdentifier) {
		return domain.Trigger{}, ErrIdentityMismatch
	}
	if err := s.State.ApplyTriggerState(ctx, request.OrganizationID, triggerID, stateVersion, enabled.StoreMutations); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist trigger runtime state: %w", err)
	}
	now := s.now()
	trigger := domain.Trigger{
		ID: triggerID, OrganizationID: request.OrganizationID, MemberID: request.MemberID,
		ConnectionID: connection.ID, ProfileID: connection.ProfileID, Provider: connection.Provider,
		PieceName: definition.PieceName, PieceVersion: definition.PieceVersion,
		TriggerName: request.TriggerName, ProviderIdentifier: providerIdentifier,
		Props: append(json.RawMessage(nil), request.Props...), Status: domain.TriggerStatusActive,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	eventID, err := s.newID("evt")
	if err != nil {
		return domain.Trigger{}, err
	}
	event := domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.TriggerEnabledDetailType, Version: 1,
		OrganizationID: request.OrganizationID, OccurredAt: now,
		Data: integrationevents.TriggerEnabled{TriggerID: trigger.ID, ConnectionID: connection.ID,
			ProfileID: connection.ProfileID, Provider: connection.Provider,
			TriggerName: trigger.TriggerName, EnabledBy: request.MemberID, EnabledAt: now}}
	if err := s.Store.CreateTriggerWithEvent(ctx, trigger, event); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist trigger: %w", err)
	}
	return trigger, nil
}

func (s Service) Disable(ctx context.Context, organizationID, memberID, triggerID string) error {
	if organizationID == "" || memberID == "" || triggerID == "" {
		return ErrInvalidInput
	}
	if err := s.Authorizer.CanCreateConnection(ctx, organizationID, memberID); err != nil {
		return fmt.Errorf("%w: %v", oauthflow.ErrForbidden, err)
	}
	trigger, err := s.Store.Trigger(ctx, organizationID, triggerID)
	if err != nil {
		return err
	}
	if trigger.MemberID != memberID || trigger.Status != domain.TriggerStatusActive {
		return oauthflow.ErrForbidden
	}
	connection, err := s.Connections.ConnectionForExecution(ctx, organizationID, trigger.ConnectionID)
	if err != nil {
		return err
	}
	auth, err := s.openConnection(ctx, connection)
	if err != nil {
		return err
	}
	defer wipe(auth)
	initial, stateVersion, err := s.State.LoadTriggerState(ctx, organizationID, trigger.ID)
	if err != nil {
		return err
	}
	result, err := s.Runner.RunTrigger(ctx, piece.TriggerInvocation{
		InvocationID: trigger.ID + "-disable", OrganizationID: organizationID,
		PieceName: trigger.PieceName, PieceVersion: trigger.PieceVersion,
		Operation: "trigger.disable", TriggerName: trigger.TriggerName,
		Auth: auth, Props: trigger.Props, PublicURL: s.PublicURL, InitialStore: initial,
	})
	if err != nil {
		return fmt.Errorf("disable piece trigger: %w", err)
	}
	if err := s.State.ApplyTriggerState(ctx, organizationID, trigger.ID, stateVersion, result.StoreMutations); err != nil {
		return err
	}
	now := s.now()
	eventID, err := s.newID("evt")
	if err != nil {
		return err
	}
	return s.Store.DisableTriggerWithEvent(ctx, organizationID, memberID, triggerID, now, domain.Event{
		EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.TriggerDisabledDetailType, Version: 1,
		OrganizationID: organizationID, OccurredAt: now,
		Data: integrationevents.TriggerDisabled{TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID,
			Provider: trigger.Provider, DisabledBy: memberID, DisabledAt: now},
	})
}

func (s Service) openConnection(ctx context.Context, connection domain.Connection) (json.RawMessage, error) {
	var plain []byte
	var err error
	if s.Credentials != nil {
		plain, err = s.Credentials.ForExecution(ctx, connection)
	} else {
		plain, err = s.Secrets.Open(ctx, secrets.Binding{OrganizationID: connection.OrganizationID,
			ResourceType: "connection", ResourceID: connection.ID}, connection.EncryptedCredentials)
	}
	if err != nil {
		return nil, fmt.Errorf("open connection credential: %w", err)
	}
	if !json.Valid(plain) {
		wipe(plain)
		return nil, errors.New("connection credential is invalid")
	}
	return json.RawMessage(plain), nil
}

func hasListener(listeners []piece.Listener, event, identifier string) bool {
	for _, listener := range listeners {
		if listener.IdentifierValue != identifier {
			continue
		}
		for _, value := range listener.Events {
			if value == event {
				return true
			}
		}
	}
	return false
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

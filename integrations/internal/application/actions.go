package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

// actionMemory is how long an action is remembered, so a caller repeating one
// under the same idempotency key gets the first outcome rather than running it
// again.
const actionMemory = 30 * 24 * time.Hour

type RunActionInput struct {
	// ConnectionID is which of the member's connections to act with. Without
	// one, Provider says which provider to find it for, and the member must
	// have exactly one connection to it.
	ConnectionID   string
	Provider       string
	Action         string
	Input          json.RawMessage
	IdempotencyKey string
}

// Catalog is every provider read aloud: what a subscription may watch, what it
// delivers, what may be done with a connection, and the skill that says how.
// It is the same for everyone, so it takes no credentials.
func (s *Service) Catalog() []providers.Description {
	return s.providers.Describe()
}

// RunAction carries out one action with a connection, as the member asking.
// Connections decides whether that member may use the connection when it
// issues the credential. An action the connection already ran under the same
// idempotency key is not run again: its first outcome answers.
func (s *Service) RunAction(ctx context.Context, credentials authclient.Credentials, organizationID string, input RunActionInput) (domain.Action, error) {
	membership, err := s.member(ctx, credentials, organizationID)
	if err != nil {
		return domain.Action{}, err
	}
	accessToken := credentials.AccessToken()
	if accessToken == "" {
		return domain.Action{}, fmt.Errorf("a member's access token is required: %w", domain.ErrUnauthorized)
	}
	connection, err := s.connectionFor(ctx, organizationID, accessToken, input)
	if err != nil {
		return domain.Action{}, err
	}
	connectionID := connection.ID
	switch connection.Status {
	case "active":
	case "needs_reconnect":
		return domain.Action{}, domain.ErrNeedsReconnect
	default:
		return domain.Action{}, fmt.Errorf("the connection is %s: %w", connection.Status, domain.ErrConflict)
	}
	provider, err := s.provider(connection.Provider)
	if err != nil {
		return domain.Action{}, err
	}
	action, ok := provider.Action(strings.TrimSpace(input.Action))
	if !ok {
		return domain.Action{}, fmt.Errorf("%s has no action %q: %w", provider.Name, strings.TrimSpace(input.Action), domain.ErrInvalid)
	}
	key, err := s.idempotencyKey(input.IdempotencyKey, action)
	if err != nil {
		return domain.Action{}, err
	}

	id, err := newID("act")
	if err != nil {
		return domain.Action{}, err
	}
	now := s.now()
	started, fresh, err := s.store.StartAction(ctx, domain.Action{
		ID: id, OrganizationID: organizationID, ConnectionID: connectionID, AppID: connection.AppID, Provider: provider.Name,
		Name: action.Name, Scope: connection.Scope, TeamID: connection.TeamID, MemberID: connection.MemberID,
		ProviderAccount: connection.ProviderAccount, RequestedBy: membership.UserID, Status: domain.ActionRunning,
		StartedAt: now, IdempotencyKey: key,
	})
	if err != nil {
		return domain.Action{}, err
	}
	if !fresh {
		// The caller already ran this action under this key: what it did then
		// is what it did, whether that was carrying it out or failing.
		return started, nil
	}

	credential, err := s.connections.Token(ctx, connectionsclient.TokenRequest{
		OrganizationID: organizationID, ConnectionID: connectionID, Purpose: domain.PurposeAction, AccessToken: accessToken,
	})
	if err != nil {
		return s.actionFailed(ctx, started, err)
	}
	result, err := action.Run(ctx, s.http, providers.ActionRequest{Credential: credential, Input: input.Input, Now: s.now()})
	if err != nil {
		return s.actionFailed(ctx, started, err)
	}
	return s.actionCompleted(ctx, started, result)
}

// connectionFor is the connection an action runs with: the one the caller
// named, or, when it named only a provider, the member's one connection to it.
// A member with several is asked which, since acting with the wrong account is
// worse than asking.
func (s *Service) connectionFor(ctx context.Context, organizationID, accessToken string, input RunActionInput) (domain.Connection, error) {
	if connectionID := strings.TrimSpace(input.ConnectionID); connectionID != "" {
		connection, err := s.connections.Connection(ctx, organizationID, connectionID, accessToken)
		if err != nil {
			return domain.Connection{}, concealed(err)
		}
		return connection, nil
	}
	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		return domain.Connection{}, fmt.Errorf("connection_id is required: %w", domain.ErrInvalid)
	}
	connections, err := s.connections.Connections(ctx, organizationID, accessToken)
	if err != nil {
		return domain.Connection{}, err
	}
	var candidates []domain.Connection
	for _, connection := range connections {
		if connection.Provider == provider && connection.Status == "active" {
			candidates = append(candidates, connection)
		}
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return domain.Connection{}, fmt.Errorf("the member has no active %s connection: %w", provider, domain.ErrNotFound)
	default:
		return domain.Connection{}, fmt.Errorf("the member has %d %s connections, so connection_id says which: %w",
			len(candidates), provider, domain.ErrInvalid)
	}
}

// ProviderConnections are the member's connections to one provider, so a
// caller offering actions can say which account each would act as.
func (s *Service) ProviderConnections(ctx context.Context, credentials authclient.Credentials, organizationID, provider string) ([]domain.Connection, error) {
	if _, err := s.member(ctx, credentials, organizationID); err != nil {
		return nil, err
	}
	accessToken := credentials.AccessToken()
	if accessToken == "" {
		return nil, fmt.Errorf("a member's access token is required: %w", domain.ErrUnauthorized)
	}
	connections, err := s.connections.Connections(ctx, organizationID, accessToken)
	if err != nil {
		return nil, err
	}
	found := []domain.Connection{}
	for _, connection := range connections {
		if provider == "" || connection.Provider == provider {
			found = append(found, connection)
		}
	}
	return found, nil
}

// idempotencyKey is the key an action is remembered under. An action that
// changes something at the provider must carry one, so a caller that asks
// again after a timeout cannot post twice; one that only reads is remembered
// under a key of its own.
func (s *Service) idempotencyKey(given string, action providers.Action) (string, error) {
	if strings.TrimSpace(given) != "" {
		return domain.CheckIdempotencyKey(given)
	}
	if action.Writes {
		return "", fmt.Errorf("%s changes %s, so idempotency_key is required: %w", action.Name, "something at the provider", domain.ErrInvalid)
	}
	return newSecret()
}

func (s *Service) actionCompleted(ctx context.Context, action domain.Action, result providers.ActionResult) (domain.Action, error) {
	at := s.now()
	action.Status, action.EndedAt = domain.ActionCompleted, &at
	kept, omitted := bounded(result.Result)
	action.Result = kept
	if result.Resource.Type != "" || result.Resource.ID != "" {
		action.Resource = &domain.ActionResource{
			Type: clip(result.Resource.Type, maxResourceName), ID: clip(result.Resource.ID, maxResourceID),
			Name: clip(result.Resource.Name, maxResourceName), URL: clip(result.Resource.URL, maxResourceURL),
		}
	}
	event, err := s.completedEvent(action, at, omitted)
	if err != nil {
		return domain.Action{}, err
	}
	if err := s.store.FinishAction(ctx, action, event); err != nil {
		return domain.Action{}, err
	}
	return action, nil
}

// actionFailed records why an action did not happen and answers the failure,
// so a caller sees the same reason the event carries.
func (s *Service) actionFailed(ctx context.Context, action domain.Action, failure error) (domain.Action, error) {
	at := s.now()
	action.Status, action.EndedAt, action.Error = domain.ActionFailed, &at, actionError(failure)
	event, err := s.failedActionEvent(action, at)
	if err != nil {
		return domain.Action{}, err
	}
	if err := s.store.FinishAction(ctx, action, event); err != nil {
		s.logger.ErrorContext(ctx, "record a failed action", "action_id", action.ID, "error", err)
	}
	return domain.Action{}, failure
}

func (s *Service) completedEvent(action domain.Action, at time.Time, omitted bool) (domain.Event, error) {
	return s.event(integrationsv1.ActionCompletedDetailType, integrationsv1.ActionCompleted{
		ActionID: action.ID, OrganizationID: action.OrganizationID, ConnectionID: action.ConnectionID, AppID: action.AppID,
		Provider: action.Provider, Action: action.Provider + "." + action.Name, Scope: action.Scope, TeamID: action.TeamID,
		MemberID: action.MemberID, ProviderAccount: integrationsv1.ProviderAccount(action.ProviderAccount),
		Service: s.serviceName, RequestedBy: action.RequestedBy, Resource: eventResource(action.Resource),
		Result: action.Result, ResultOmitted: omitted, StartedAt: action.StartedAt, CompletedAt: at,
	}, at)
}

func (s *Service) failedActionEvent(action domain.Action, at time.Time) (domain.Event, error) {
	return s.event(integrationsv1.ActionFailedDetailType, integrationsv1.ActionFailed{
		ActionID: action.ID, OrganizationID: action.OrganizationID, ConnectionID: action.ConnectionID, AppID: action.AppID,
		Provider: action.Provider, Action: action.Provider + "." + action.Name, Scope: action.Scope, TeamID: action.TeamID,
		MemberID: action.MemberID, ProviderAccount: integrationsv1.ProviderAccount(action.ProviderAccount),
		Service: s.serviceName, RequestedBy: action.RequestedBy, Resource: eventResource(action.Resource),
		Error: action.Error, Retryable: retryable(action.Error), StartedAt: action.StartedAt, FailedAt: at,
	}, at)
}

func eventResource(resource *domain.ActionResource) *integrationsv1.Resource {
	if resource == nil {
		return nil
	}
	return &integrationsv1.Resource{Type: resource.Type, ID: resource.ID, Name: resource.Name, URL: resource.URL}
}

// bounded keeps what fits in an event; anything larger is left out and
// flagged, as a provider's payload is.
func bounded(result json.RawMessage) (json.RawMessage, bool) {
	if len(result) > integrationsv1.MaxPayloadBytes || (len(result) > 0 && !json.Valid(result)) {
		return nil, true
	}
	return result, false
}

// actionError names why an action failed, in the words the contract uses, so
// a caller can decide what to do without reading a provider's answer.
func actionError(err error) string {
	switch {
	case errors.Is(err, domain.ErrNeedsReconnect):
		return integrationsv1.ActionErrorNeedsReconnect
	case errors.Is(err, domain.ErrCredentialRejected):
		return integrationsv1.ActionErrorCredentialRejected
	case errors.Is(err, domain.ErrNotFound):
		return integrationsv1.ActionErrorNotFound
	case errors.Is(err, domain.ErrForbidden):
		return integrationsv1.ActionErrorForbidden
	case errors.Is(err, domain.ErrInvalid):
		return integrationsv1.ActionErrorInvalidRequest
	case errors.Is(err, domain.ErrProviderUnavailable):
		return integrationsv1.ActionErrorProviderUnavailable
	default:
		return integrationsv1.ActionErrorProviderError
	}
}

// retryable says whether running the same action again, unchanged, could
// succeed.
func retryable(reason string) bool {
	switch reason {
	case integrationsv1.ActionErrorProviderUnavailable, integrationsv1.ActionErrorRateLimited:
		return true
	}
	return false
}

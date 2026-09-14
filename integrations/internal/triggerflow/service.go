package triggerflow

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/github"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/google"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/microsoftgraph"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

var (
	ErrInvalidInput      = errors.New("invalid trigger input")
	ErrOperationDisabled = errors.New("trigger operation is not enabled")
	ErrRouteUnavailable  = errors.New("trigger webhook route is not configured")
	ErrIdentityMismatch  = errors.New("provider listener identity mismatch")
	ErrRequiredScope     = errors.New("trigger requires additional provider scope")
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
	RequiredScopes  []string
	// Microsoft Graph fields are set only for a reviewed per-connection
	// subscription trigger. They are not provider input.
	GraphResource   string
	GraphChangeType string
	GraphTTL        time.Duration
	// GitHubHookEvent is the native GitHub webhook event name for a reviewed
	// per-connection hook trigger. It is not provider input.
	GitHubHookEvent string
	// GoogleWatch is "calendar" or "gmail" for a reviewed native Google
	// inbound trigger. It is not provider input.
	GoogleWatch string
	GoogleTTL   time.Duration
	// AppWebhookFamily is the shared app-level route key when several piece
	// triggers share one provider Events URL (Slack). Empty means the route
	// is keyed by TriggerName.
	AppWebhookFamily string
	// RunnerTriggerName is the pinned piece trigger name when it differs from
	// the public catalog name. Empty means TriggerName.
	RunnerTriggerName string
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

// PerConnectionRoutes is kept separate from Routes so Slack's existing
// app-level lifecycle does not acquire a per-connection route dependency.
type PerConnectionRoutes interface {
	CreateWebhookRoute(context.Context, domain.WebhookRoute) error
	ActiveWebhookRouteForConnection(context.Context, string, string, string, string) (domain.WebhookRoute, error)
	DeactivateWebhookRoute(context.Context, string) error
}

type Store interface {
	CreateTriggerWithEvent(context.Context, domain.Trigger, domain.Event) error
	Trigger(context.Context, string, string) (domain.Trigger, error)
	DisableTriggerWithEvent(context.Context, string, string, string, time.Time, domain.Event) error
}

type RuntimeState interface {
	LoadTriggerState(context.Context, string, string) (map[string]any, int64, error)
	ApplyTriggerState(context.Context, string, string, int64, []piece.StoreMutation) error
	DeleteTriggerState(context.Context, string, string) error
}

type SecretBox interface {
	Open(context.Context, secrets.Binding, domain.SealedData) ([]byte, error)
}

type SecretSealer interface {
	Seal(context.Context, secrets.Binding, []byte) (domain.SealedData, error)
}

type CredentialSource interface {
	ForExecution(context.Context, domain.Connection) ([]byte, error)
}

// MicrosoftGraphSubscriptions is intentionally the small provider boundary
// needed by the control plane. The concrete client remains independently
// testable against Graph's HTTP contract.
type MicrosoftGraphSubscriptions interface {
	Create(context.Context, string, microsoftgraph.SubscriptionRequest) (microsoftgraph.Subscription, error)
	Renew(context.Context, string, string, time.Time) (microsoftgraph.Subscription, error)
	Remove(context.Context, string, string) error
}

type GitHubHooks interface {
	Create(context.Context, string, github.HookRequest) (github.Hook, error)
	Remove(context.Context, string, github.HookState) error
}

type GoogleWatches interface {
	WatchCalendar(context.Context, string, google.CalendarWatchRequest) (google.Channel, error)
	WatchDrive(context.Context, string, google.DriveWatchRequest) (google.Channel, error)
	StopChannel(context.Context, string, google.WatchState) error
	WatchGmail(context.Context, string, google.GmailWatchRequest) (google.GmailWatch, error)
	StopGmail(context.Context, string) error
}

type GoogleGmailCensus interface {
	ActiveGoogleGmailTriggerCount(context.Context, string) (int, error)
}

type CreateRequest struct {
	OrganizationID string
	MemberID       string
	ConnectionID   string
	TriggerName    string
	Props          json.RawMessage
}

type Service struct {
	Catalog          Catalog
	Authorizer       Authorizer
	Connections      Connections
	Routes           Routes
	Store            Store
	State            RuntimeState
	Secrets          SecretBox
	Credentials      CredentialSource
	Runner           piece.TriggerRunner
	MicrosoftGraph   MicrosoftGraphSubscriptions
	GitHub           GitHubHooks
	Google           GoogleWatches
	GmailCensus      GoogleGmailCensus
	GmailPubSubTopic string
	Now              func() time.Time
	IDs              func(string) (string, error)
	Random           io.Reader
	PublicURL        string
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
	if isMicrosoftGraphTrigger(connection, definition) && !isActiveMicrosoftConnection(connection) {
		return domain.Trigger{}, oauthflow.ErrForbidden
	}
	if isGitHubTrigger(connection, definition) && !isActiveGitHubConnection(connection) {
		return domain.Trigger{}, oauthflow.ErrForbidden
	}
	if isGoogleTrigger(connection, definition) && !isActiveGoogleConnection(connection) {
		return domain.Trigger{}, oauthflow.ErrForbidden
	}
	if missing := missingScopes(connection.Provider, definition.RequiredScopes, connection.GrantedScopes); len(missing) > 0 {
		return domain.Trigger{}, fmt.Errorf("%w: %s", ErrRequiredScope, strings.Join(missing, ","))
	}
	if definition.ValidateProps != nil {
		if err := definition.ValidateProps(request.Props); err != nil {
			return domain.Trigger{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
	}
	var route domain.WebhookRoute
	if !isMicrosoftGraphTrigger(connection, definition) && !isGitHubTrigger(connection, definition) && !isGoogleTrigger(connection, definition) {
		route, err = s.activeAppLevelRoute(ctx, request.OrganizationID, connection.ProfileID, definition, request.TriggerName)
		if err != nil || !route.Active || route.Mode != definition.RouteMode || route.PieceName != definition.PieceName || route.PieceVersion != definition.PieceVersion {
			return domain.Trigger{}, ErrRouteUnavailable
		}
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
	if isMicrosoftGraphTrigger(connection, definition) {
		return s.createMicrosoftGraphTrigger(ctx, request, connection, definition, triggerID)
	}
	if isGitHubTrigger(connection, definition) {
		return s.createGitHubTrigger(ctx, request, connection, definition, triggerID)
	}
	if isGoogleTrigger(connection, definition) {
		return s.createGoogleTrigger(ctx, request, connection, definition, triggerID)
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
		Operation: "trigger.enable", TriggerName: runnerTriggerName(definition, request.TriggerName),
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

func (s Service) createMicrosoftGraphTrigger(ctx context.Context, request CreateRequest, connection domain.Connection, definition Definition, triggerID string) (trigger domain.Trigger, resultErr error) {
	routes, ok := s.Routes.(PerConnectionRoutes)
	if !ok || s.MicrosoftGraph == nil || s.PublicURL == "" {
		return domain.Trigger{}, ErrRouteUnavailable
	}
	sealer, ok := s.Secrets.(SecretSealer)
	if !ok {
		return domain.Trigger{}, errors.New("webhook secret sealing is not configured")
	}
	opaqueRoute, err := randomOpaqueRoute(s.Random)
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("generate opaque webhook route: %w", err)
	}
	clientState, err := microsoftgraph.NewClientState(s.Random)
	if err != nil {
		return domain.Trigger{}, err
	}
	sealed, err := sealer.Seal(ctx, secrets.Binding{OrganizationID: request.OrganizationID,
		ResourceType: "webhook-route", ResourceID: opaqueRoute}, []byte(clientState))
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("encrypt Microsoft Graph client state: %w", err)
	}
	route := domain.WebhookRoute{
		OpaqueRoute: opaqueRoute, OrganizationID: request.OrganizationID, ProfileID: connection.ProfileID,
		ConnectionID: connection.ID, Provider: connection.Provider, PieceName: definition.PieceName,
		PieceVersion: definition.PieceVersion, TriggerName: request.TriggerName,
		Mode: domain.WebhookRoutePerConnection, SigningSecret: sealed, MaxBodyBytes: definition.MaxBodyBytes,
		Active: true, CredentialRevision: 1,
	}
	// The route must exist before Graph calls it with validationToken during the
	// synchronous subscription handshake.
	if err := routes.CreateWebhookRoute(ctx, route); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist Microsoft Graph webhook route: %w", err)
	}
	compensateRoute := true
	statePersisted := false
	var subscriptionID string
	var graphAccessToken string
	defer func() {
		if !compensateRoute {
			return
		}
		// Every cleanup is attempted even after another cleanup fails. The caller
		// receives the complete failure set, rather than a successful-looking
		// create response that masks an orphaned Graph subscription or ingress.
		var cleanupErrs []error
		cleanupCtx := context.WithoutCancel(ctx)
		if subscriptionID != "" && graphAccessToken != "" {
			if err := s.MicrosoftGraph.Remove(cleanupCtx, graphAccessToken, subscriptionID); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate Microsoft Graph subscription %s: %w", subscriptionID, err))
			}
		}
		if statePersisted {
			if err := s.State.DeleteTriggerState(cleanupCtx, request.OrganizationID, triggerID); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate Microsoft Graph trigger state: %w", err))
			}
		}
		if err := routes.DeactivateWebhookRoute(cleanupCtx, opaqueRoute); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate Microsoft Graph webhook route: %w", err))
		}
		if len(cleanupErrs) > 0 {
			resultErr = errors.Join(resultErr, errors.Join(cleanupErrs...))
		}
	}()
	auth, err := s.openConnection(ctx, connection)
	if err != nil {
		return domain.Trigger{}, err
	}
	defer wipe(auth)
	graphAccessToken, err = accessToken(auth)
	if err != nil {
		return domain.Trigger{}, err
	}
	expiresAt := s.now().Add(graphTTL(definition))
	subscription, err := s.MicrosoftGraph.Create(ctx, graphAccessToken, microsoftgraph.SubscriptionRequest{
		ChangeType: definition.GraphChangeType, Resource: definition.GraphResource,
		NotificationURL: s.PublicURL + "/v1/webhooks/" + opaqueRoute,
		ClientState:     clientState, ExpirationDateTime: expiresAt,
	})
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("create Microsoft Graph subscription: %w", err)
	}
	subscriptionID = subscription.ID
	initial, stateVersion, err := s.State.LoadTriggerState(ctx, request.OrganizationID, triggerID)
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("load trigger state: %w", err)
	}
	_ = initial // Graph state starts empty, but load establishes the expected version.
	if err := s.State.ApplyTriggerState(ctx, request.OrganizationID, triggerID, stateVersion, subscription.State().StoreMutations()); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist Microsoft Graph subscription state: %w", err)
	}
	statePersisted = true
	now := s.now()
	trigger = domain.Trigger{ID: triggerID, OrganizationID: request.OrganizationID, MemberID: request.MemberID,
		ConnectionID: connection.ID, ProfileID: connection.ProfileID, Provider: connection.Provider,
		PieceName: definition.PieceName, PieceVersion: definition.PieceVersion, TriggerName: request.TriggerName,
		ProviderIdentifier: connection.ID, Props: append(json.RawMessage(nil), request.Props...),
		Status: domain.TriggerStatusActive, CreatedAt: now, UpdatedAt: now, Version: 1}
	eventID, err := s.newID("evt")
	if err != nil {
		return domain.Trigger{}, err
	}
	event := domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.TriggerEnabledDetailType, Version: 1, OrganizationID: request.OrganizationID,
		OccurredAt: now, Data: integrationevents.TriggerEnabled{TriggerID: trigger.ID, ConnectionID: connection.ID,
			ProfileID: connection.ProfileID, Provider: connection.Provider, TriggerName: trigger.TriggerName,
			EnabledBy: request.MemberID, EnabledAt: now}}
	if err := s.Store.CreateTriggerWithEvent(ctx, trigger, event); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist trigger: %w", err)
	}
	compensateRoute = false
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
	if trigger.MemberID != memberID {
		return oauthflow.ErrForbidden
	}
	if trigger.Status == domain.TriggerStatusDisabled {
		return nil
	}
	if trigger.Status != domain.TriggerStatusActive {
		return oauthflow.ErrForbidden
	}
	if trigger.Provider == "microsoft" {
		return s.disableMicrosoftGraphTrigger(ctx, organizationID, memberID, trigger)
	}
	if trigger.Provider == "github" {
		return s.disableGitHubTrigger(ctx, organizationID, memberID, trigger)
	}
	if trigger.Provider == "google" {
		return s.disableGoogleTrigger(ctx, organizationID, memberID, trigger)
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
	disableName := trigger.TriggerName
	if s.Catalog != nil {
		if definition, ok := s.Catalog.TriggerDefinition(trigger.Provider, trigger.TriggerName); ok {
			disableName = runnerTriggerName(definition, trigger.TriggerName)
		}
	}
	result, err := s.Runner.RunTrigger(ctx, piece.TriggerInvocation{
		InvocationID: trigger.ID + "-disable", OrganizationID: organizationID,
		PieceName: trigger.PieceName, PieceVersion: trigger.PieceVersion,
		Operation: "trigger.disable", TriggerName: disableName,
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

// Renew is a scheduler-safe operation: no end-user authorization is accepted,
// and all authority is recovered from the durable active trigger and its
// member-owned connection.
func (s Service) Renew(ctx context.Context, organizationID, triggerID string) error {
	if organizationID == "" || triggerID == "" {
		return ErrInvalidInput
	}
	trigger, err := s.Store.Trigger(ctx, organizationID, triggerID)
	if err != nil {
		return err
	}
	if trigger.Status != domain.TriggerStatusActive || (trigger.Provider != "microsoft" && trigger.Provider != "google") {
		return ErrOperationDisabled
	}
	connection, err := s.Connections.ConnectionForExecution(ctx, organizationID, trigger.ConnectionID)
	if err != nil || connection.MemberID != trigger.MemberID {
		return oauthflow.ErrForbidden
	}
	if trigger.Provider == "google" {
		if !isActiveGoogleConnection(connection) {
			return oauthflow.ErrForbidden
		}
		definition, ok := s.Catalog.TriggerDefinition(connection.Provider, trigger.TriggerName)
		if !ok || !isGoogleTrigger(connection, definition) {
			return ErrOperationDisabled
		}
		if missing := missingScopes(connection.Provider, definition.RequiredScopes, connection.GrantedScopes); len(missing) > 0 {
			return fmt.Errorf("%w: %s", ErrRequiredScope, strings.Join(missing, ","))
		}
		return s.renewGoogleWatch(ctx, trigger, connection, definition)
	}
	if !isActiveMicrosoftConnection(connection) {
		return oauthflow.ErrForbidden
	}
	definition, ok := s.Catalog.TriggerDefinition(connection.Provider, trigger.TriggerName)
	if !ok || !isMicrosoftGraphTrigger(connection, definition) || s.MicrosoftGraph == nil {
		return ErrOperationDisabled
	}
	if missing := missingScopes(connection.Provider, definition.RequiredScopes, connection.GrantedScopes); len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrRequiredScope, strings.Join(missing, ","))
	}
	routes, ok := s.Routes.(PerConnectionRoutes)
	if !ok {
		return ErrRouteUnavailable
	}
	route, err := routes.ActiveWebhookRouteForConnection(ctx, organizationID, connection.ProfileID, trigger.TriggerName, connection.ID)
	if err != nil || route.PieceName != trigger.PieceName || route.PieceVersion != trigger.PieceVersion {
		return ErrRouteUnavailable
	}
	state, version, err := s.State.LoadTriggerState(ctx, organizationID, trigger.ID)
	if err != nil {
		return err
	}
	subscription, err := microsoftSubscriptionState(state)
	if err != nil {
		return fmt.Errorf("load Microsoft Graph subscription state: %w", err)
	}
	// Scheduler invocations can be safely frequent. Graph is only contacted in
	// the final 24 hours of the conservative three-day subscription window.
	if subscription.ExpiresAt.After(s.now().Add(24 * time.Hour)) {
		return nil
	}
	auth, err := s.openConnection(ctx, connection)
	if err != nil {
		return err
	}
	defer wipe(auth)
	accessToken, err := accessToken(auth)
	if err != nil {
		return err
	}
	renewed, err := s.MicrosoftGraph.Renew(ctx, accessToken, subscription.ID, s.now().Add(graphTTL(definition)))
	if err != nil {
		return fmt.Errorf("renew Microsoft Graph subscription: %w", err)
	}
	if err := s.State.ApplyTriggerState(ctx, organizationID, trigger.ID, version, renewed.State().StoreMutations()); err != nil {
		return fmt.Errorf("persist renewed Microsoft Graph subscription state: %w", err)
	}
	return nil
}

func (s Service) disableMicrosoftGraphTrigger(ctx context.Context, organizationID, memberID string, trigger domain.Trigger) error {
	if s.MicrosoftGraph == nil {
		return ErrOperationDisabled
	}
	routes, ok := s.Routes.(PerConnectionRoutes)
	if !ok {
		return ErrRouteUnavailable
	}
	connection, err := s.Connections.ConnectionForExecution(ctx, organizationID, trigger.ConnectionID)
	if err != nil || connection.MemberID != memberID || !isActiveMicrosoftConnection(connection) {
		return oauthflow.ErrForbidden
	}
	route, err := routes.ActiveWebhookRouteForConnection(ctx, organizationID, connection.ProfileID, trigger.TriggerName, connection.ID)
	routeActive := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return ErrRouteUnavailable
	}
	state, _, err := s.State.LoadTriggerState(ctx, organizationID, trigger.ID)
	if err != nil {
		return err
	}
	subscription, err := microsoftSubscriptionState(state)
	if err != nil {
		return fmt.Errorf("load Microsoft Graph subscription state: %w", err)
	}
	auth, err := s.openConnection(ctx, connection)
	if err != nil {
		return err
	}
	defer wipe(auth)
	accessToken, err := accessToken(auth)
	if err != nil {
		return err
	}
	if err := s.MicrosoftGraph.Remove(ctx, accessToken, subscription.ID); err != nil {
		return fmt.Errorf("remove Microsoft Graph subscription: %w", err)
	}
	if routeActive {
		if err := routes.DeactivateWebhookRoute(ctx, route.OpaqueRoute); err != nil {
			return fmt.Errorf("deactivate Microsoft Graph webhook route: %w", err)
		}
	}
	now := s.now()
	eventID, err := s.newID("evt")
	if err != nil {
		return err
	}
	return s.Store.DisableTriggerWithEvent(ctx, organizationID, memberID, trigger.ID, now, domain.Event{
		EventID: eventID, Source: integrationevents.Source, DetailType: integrationevents.TriggerDisabledDetailType,
		Version: 1, OrganizationID: organizationID, OccurredAt: now,
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

func runnerTriggerName(definition Definition, fallback string) string {
	if definition.RunnerTriggerName != "" {
		return definition.RunnerTriggerName
	}
	if definition.TriggerName != "" {
		return definition.TriggerName
	}
	return fallback
}

func (s Service) activeAppLevelRoute(ctx context.Context, organizationID, profileID string, definition Definition, requested string) (domain.WebhookRoute, error) {
	names := []string{requested}
	if definition.AppWebhookFamily != "" {
		names = []string{definition.AppWebhookFamily, requested}
	}
	seen := map[string]struct{}{}
	var last error
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		route, err := s.Routes.ActiveWebhookRoute(ctx, organizationID, profileID, name)
		if err != nil {
			last = err
			continue
		}
		return route, nil
	}
	if last != nil {
		return domain.WebhookRoute{}, last
	}
	return domain.WebhookRoute{}, ErrRouteUnavailable
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

func isGitHubTrigger(connection domain.Connection, definition Definition) bool {
	return connection.Provider == "github" && definition.Provider == "github" &&
		definition.RouteMode == domain.WebhookRoutePerConnection && definition.GitHubHookEvent != ""
}

func isActiveGitHubConnection(connection domain.Connection) bool {
	return connection.Provider == "github" && connection.Status == domain.ConnectionStatusActive
}

func isMicrosoftGraphTrigger(connection domain.Connection, definition Definition) bool {
	return connection.Provider == "microsoft" && definition.Provider == "microsoft" &&
		definition.RouteMode == domain.WebhookRoutePerConnection && definition.GraphResource != "" &&
		definition.GraphChangeType != ""
}

func isActiveMicrosoftConnection(connection domain.Connection) bool {
	return connection.Provider == "microsoft" && connection.Status == domain.ConnectionStatusActive
}

func graphTTL(definition Definition) time.Duration {
	if definition.GraphTTL > time.Minute {
		return definition.GraphTTL
	}
	return 24 * time.Hour
}

func randomOpaqueRoute(source io.Reader) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	value := make([]byte, 32)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func accessToken(raw json.RawMessage) (string, error) {
	var grant struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(raw, &grant) != nil || strings.TrimSpace(grant.AccessToken) == "" {
		return "", errors.New("connection credential has no access token")
	}
	return grant.AccessToken, nil
}

func microsoftSubscriptionState(store map[string]any) (microsoftgraph.SubscriptionState, error) {
	// Dynamo scopes all runner state keys, including empty scope, as "\x00key".
	// Graph state is written directly by this service, so normalize it before
	// passing it to the provider's deliberately scope-agnostic parser.
	normalized := make(map[string]any, len(store))
	for key, value := range store {
		normalized[strings.TrimPrefix(key, "\x00")] = value
	}
	return microsoftgraph.SubscriptionStateFromStore(normalized)
}

func missingScopes(provider string, required, actual []string) []string {
	granted := make(map[string]struct{}, len(actual))
	for _, scope := range actual {
		granted[scope] = struct{}{}
	}
	var missing []string
	for _, scope := range required {
		_, allowed := granted[scope]
		if provider == "microsoft" {
			switch scope {
			case "Calendars.Read":
				_, implied := granted["Calendars.ReadWrite"]
				allowed = allowed || implied
			case "Mail.Read":
				_, implied := granted["Mail.ReadWrite"]
				allowed = allowed || implied
			case "Files.Read":
				_, implied := granted["Files.ReadWrite"]
				allowed = allowed || implied
			}
		}
		if provider == "github" && scope == "admin:repo_hook" {
			_, implied := granted["repo"]
			allowed = allowed || implied
		}
		if provider == "google" {
			allowed = allowed || googleScopeImplied(scope, granted)
		}
		if !allowed {
			missing = append(missing, scope)
		}
	}
	return missing
}

func googleScopeImplied(required string, granted map[string]struct{}) bool {
	has := func(scope string) bool {
		_, ok := granted[scope]
		return ok
	}
	switch required {
	case "https://www.googleapis.com/auth/calendar.readonly":
		return has("https://www.googleapis.com/auth/calendar.events")
	case "https://www.googleapis.com/auth/gmail.metadata":
		return has("https://www.googleapis.com/auth/gmail.readonly") || has("https://www.googleapis.com/auth/gmail.modify")
	case "https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/gmail.compose":
		return has("https://www.googleapis.com/auth/gmail.modify")
	case "https://www.googleapis.com/auth/gmail.send":
		return has("https://www.googleapis.com/auth/gmail.modify") || has("https://www.googleapis.com/auth/gmail.compose")
	case "https://www.googleapis.com/auth/drive.metadata.readonly":
		return has("https://www.googleapis.com/auth/drive") || has("https://www.googleapis.com/auth/drive.readonly") ||
			has("https://www.googleapis.com/auth/drive.metadata")
	default:
		return false
	}
}

package triggerflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/github"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

type githubTriggerTarget struct {
	Scope string `json:"scope"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

func (s Service) createGitHubTrigger(ctx context.Context, request CreateRequest, connection domain.Connection, definition Definition, triggerID string) (trigger domain.Trigger, resultErr error) {
	target, err := parseGitHubTriggerTarget(request.Props)
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if missing := missingScopes(connection.Provider, githubRequiredScopes(target.Scope), connection.GrantedScopes); len(missing) > 0 {
		return domain.Trigger{}, fmt.Errorf("%w: %s", ErrRequiredScope, strings.Join(missing, ","))
	}
	routes, ok := s.Routes.(PerConnectionRoutes)
	if !ok || s.GitHub == nil || s.PublicURL == "" {
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
	hookSecret, err := github.NewHookSecret(s.Random)
	if err != nil {
		return domain.Trigger{}, err
	}
	sealed, err := sealer.Seal(ctx, secrets.Binding{OrganizationID: request.OrganizationID,
		ResourceType: "webhook-route", ResourceID: opaqueRoute}, []byte(hookSecret))
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("encrypt GitHub webhook secret: %w", err)
	}
	route := domain.WebhookRoute{
		OpaqueRoute: opaqueRoute, OrganizationID: request.OrganizationID, ProfileID: connection.ProfileID,
		ConnectionID: connection.ID, Provider: connection.Provider, PieceName: definition.PieceName,
		PieceVersion: definition.PieceVersion, TriggerName: request.TriggerName,
		Mode: domain.WebhookRoutePerConnection, SigningSecret: sealed, MaxBodyBytes: definition.MaxBodyBytes,
		Active: true, CredentialRevision: 1,
	}
	if err := routes.CreateWebhookRoute(ctx, route); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist GitHub webhook route: %w", err)
	}
	compensateRoute := true
	statePersisted := false
	var hookState github.HookState
	var githubAccessToken string
	defer func() {
		if !compensateRoute {
			return
		}
		var cleanupErrs []error
		cleanupCtx := context.WithoutCancel(ctx)
		if hookState.ID != "" && githubAccessToken != "" {
			if err := s.GitHub.Remove(cleanupCtx, githubAccessToken, hookState); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate GitHub webhook %s: %w", hookState.ID, err))
			}
		}
		if statePersisted {
			if err := s.State.DeleteTriggerState(cleanupCtx, request.OrganizationID, triggerID); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate GitHub trigger state: %w", err))
			}
		}
		if err := routes.DeactivateWebhookRoute(cleanupCtx, opaqueRoute); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate GitHub webhook route: %w", err))
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
	githubAccessToken, err = accessToken(auth)
	if err != nil {
		return domain.Trigger{}, err
	}
	hook, err := s.GitHub.Create(ctx, githubAccessToken, github.HookRequest{
		Scope: target.Scope, Owner: target.Owner, Repo: target.Repo,
		Events: []string{definition.GitHubHookEvent},
		URL:    s.PublicURL + "/v1/webhooks/" + opaqueRoute, Secret: hookSecret,
	})
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("create GitHub webhook: %w", err)
	}
	hookState = github.HookState{ID: hook.ID, URL: hook.URL, Scope: target.Scope, Owner: target.Owner, Repo: target.Repo}
	initial, stateVersion, err := s.State.LoadTriggerState(ctx, request.OrganizationID, triggerID)
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("load trigger state: %w", err)
	}
	_ = initial
	if err := s.State.ApplyTriggerState(ctx, request.OrganizationID, triggerID, stateVersion, hookState.StoreMutations()); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist GitHub webhook state: %w", err)
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

func (s Service) disableGitHubTrigger(ctx context.Context, organizationID, memberID string, trigger domain.Trigger) error {
	if s.GitHub == nil {
		return ErrOperationDisabled
	}
	routes, ok := s.Routes.(PerConnectionRoutes)
	if !ok {
		return ErrRouteUnavailable
	}
	connection, err := s.Connections.ConnectionForExecution(ctx, organizationID, trigger.ConnectionID)
	if err != nil || connection.MemberID != memberID || !isActiveGitHubConnection(connection) {
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
	hook, err := githubHookState(state)
	if err != nil {
		return fmt.Errorf("load GitHub webhook state: %w", err)
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
	if err := s.GitHub.Remove(ctx, accessToken, hook); err != nil {
		return fmt.Errorf("remove GitHub webhook: %w", err)
	}
	if routeActive {
		if err := routes.DeactivateWebhookRoute(ctx, route.OpaqueRoute); err != nil {
			return fmt.Errorf("deactivate GitHub webhook route: %w", err)
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

func parseGitHubTriggerTarget(raw json.RawMessage) (githubTriggerTarget, error) {
	var target githubTriggerTarget
	if json.Unmarshal(raw, &target) != nil {
		return githubTriggerTarget{}, errors.New("invalid GitHub trigger properties")
	}
	switch target.Scope {
	case github.HookScopeRepository:
		if target.Owner == "" || target.Repo == "" {
			return githubTriggerTarget{}, errors.New("invalid GitHub trigger properties")
		}
	case github.HookScopeOrganization:
		if target.Owner == "" || target.Repo != "" {
			return githubTriggerTarget{}, errors.New("invalid GitHub trigger properties")
		}
	default:
		return githubTriggerTarget{}, errors.New("invalid GitHub trigger properties")
	}
	return target, nil
}

func githubRequiredScopes(scope string) []string {
	if scope == github.HookScopeOrganization {
		return []string{"admin:org_hook"}
	}
	return []string{"admin:repo_hook"}
}

func githubHookState(store map[string]any) (github.HookState, error) {
	normalized := make(map[string]any, len(store))
	for key, value := range store {
		normalized[strings.TrimPrefix(key, "\x00")] = value
	}
	return github.HookStateFromStore(normalized)
}

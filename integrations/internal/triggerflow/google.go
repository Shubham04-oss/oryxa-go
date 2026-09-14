package triggerflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/google"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

func (s Service) createGoogleTrigger(ctx context.Context, request CreateRequest, connection domain.Connection, definition Definition, triggerID string) (domain.Trigger, error) {
	if definition.GoogleWatch == google.WatchKindGmail {
		return s.createGoogleGmailTrigger(ctx, request, connection, definition, triggerID)
	}
	return s.createGoogleCalendarTrigger(ctx, request, connection, definition, triggerID)
}

func (s Service) createGoogleCalendarTrigger(ctx context.Context, request CreateRequest, connection domain.Connection, definition Definition, triggerID string) (trigger domain.Trigger, resultErr error) {
	routes, ok := s.Routes.(PerConnectionRoutes)
	if !ok || s.Google == nil || s.PublicURL == "" {
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
	channelID, err := google.NewChannelID(s.Random)
	if err != nil {
		return domain.Trigger{}, err
	}
	token, err := google.NewChannelToken(s.Random)
	if err != nil {
		return domain.Trigger{}, err
	}
	sealed, err := sealer.Seal(ctx, secrets.Binding{OrganizationID: request.OrganizationID,
		ResourceType: "webhook-route", ResourceID: opaqueRoute}, []byte(token))
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("encrypt Google Calendar channel token: %w", err)
	}
	route := domain.WebhookRoute{
		OpaqueRoute: opaqueRoute, OrganizationID: request.OrganizationID, ProfileID: connection.ProfileID,
		ConnectionID: connection.ID, Provider: connection.Provider, PieceName: definition.PieceName,
		PieceVersion: definition.PieceVersion, TriggerName: request.TriggerName,
		Mode: domain.WebhookRoutePerConnection, SigningSecret: sealed, MaxBodyBytes: definition.MaxBodyBytes,
		Active: true, CredentialRevision: 1,
	}
	if err := routes.CreateWebhookRoute(ctx, route); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist Google Calendar webhook route: %w", err)
	}
	compensateRoute := true
	statePersisted := false
	var watchState google.WatchState
	var googleAccessToken string
	defer func() {
		if !compensateRoute {
			return
		}
		var cleanupErrs []error
		cleanupCtx := context.WithoutCancel(ctx)
		if watchState.ChannelID != "" && googleAccessToken != "" {
			if err := s.Google.StopChannel(cleanupCtx, googleAccessToken, watchState); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate Google Calendar channel %s: %w", watchState.ChannelID, err))
			}
		}
		if statePersisted {
			if err := s.State.DeleteTriggerState(cleanupCtx, request.OrganizationID, triggerID); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate Google Calendar trigger state: %w", err))
			}
		}
		if err := routes.DeactivateWebhookRoute(cleanupCtx, opaqueRoute); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate Google Calendar webhook route: %w", err))
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
	googleAccessToken, err = accessToken(auth)
	if err != nil {
		return domain.Trigger{}, err
	}
	expiresAt := s.now().Add(googleTTL(definition))
	channel, err := s.Google.WatchCalendar(ctx, googleAccessToken, google.CalendarWatchRequest{
		CalendarID: "primary", ChannelID: channelID, Token: token, Expiration: expiresAt,
		Address: s.PublicURL + "/v1/webhooks/" + opaqueRoute,
	})
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("create Google Calendar watch: %w", err)
	}
	watchState = google.WatchState{Kind: google.WatchKindCalendar, ChannelID: channel.ID,
		ResourceID: channel.ResourceID, CalendarID: channel.CalendarID, ExpiresAt: channel.Expiration.UTC()}
	if watchState.ExpiresAt.IsZero() {
		watchState.ExpiresAt = expiresAt.UTC()
	}
	if _, stateVersion, err := s.State.LoadTriggerState(ctx, request.OrganizationID, triggerID); err != nil {
		return domain.Trigger{}, fmt.Errorf("load Google Calendar trigger state: %w", err)
	} else if err := s.State.ApplyTriggerState(ctx, request.OrganizationID, triggerID, stateVersion, watchState.StoreMutations()); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist Google Calendar watch state: %w", err)
	}
	statePersisted = true
	trigger, err = s.persistEnabledTrigger(ctx, request, connection, definition, triggerID)
	if err != nil {
		return domain.Trigger{}, err
	}
	compensateRoute = false
	return trigger, nil
}

func (s Service) createGoogleGmailTrigger(ctx context.Context, request CreateRequest, connection domain.Connection, definition Definition, triggerID string) (trigger domain.Trigger, resultErr error) {
	if s.Google == nil || !google.ValidPubSubTopic(s.GmailPubSubTopic) {
		return domain.Trigger{}, ErrRouteUnavailable
	}
	auth, err := s.openConnection(ctx, connection)
	if err != nil {
		return domain.Trigger{}, err
	}
	defer wipe(auth)
	token, err := accessToken(auth)
	if err != nil {
		return domain.Trigger{}, err
	}
	compensate := true
	statePersisted := false
	watchCreated := false
	defer func() {
		if !compensate {
			return
		}
		var cleanupErrs []error
		cleanupCtx := context.WithoutCancel(ctx)
		if watchCreated && s.shouldStopOrphanGmailWatch(cleanupCtx, connection) {
			if err := s.Google.StopGmail(cleanupCtx, token); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate Gmail watch: %w", err))
			}
		}
		if statePersisted {
			if err := s.State.DeleteTriggerState(cleanupCtx, request.OrganizationID, triggerID); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("compensate Gmail watch state: %w", err))
			}
		}
		if len(cleanupErrs) > 0 {
			resultErr = errors.Join(resultErr, errors.Join(cleanupErrs...))
		}
	}()
	watch, err := s.Google.WatchGmail(ctx, token, google.GmailWatchRequest{TopicName: s.GmailPubSubTopic})
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("create Gmail watch: %w", err)
	}
	watchCreated = true
	state := google.WatchState{Kind: google.WatchKindGmail, HistoryID: watch.HistoryID, ExpiresAt: watch.Expiration.UTC()}
	if state.ExpiresAt.IsZero() {
		state.ExpiresAt = s.now().Add(googleTTL(definition)).UTC()
	}
	if _, stateVersion, err := s.State.LoadTriggerState(ctx, request.OrganizationID, triggerID); err != nil {
		return domain.Trigger{}, fmt.Errorf("load Gmail trigger state: %w", err)
	} else if err := s.State.ApplyTriggerState(ctx, request.OrganizationID, triggerID, stateVersion, state.StoreMutations()); err != nil {
		return domain.Trigger{}, fmt.Errorf("persist Gmail watch state: %w", err)
	}
	statePersisted = true
	trigger, err = s.persistEnabledTrigger(ctx, request, connection, definition, triggerID)
	if err != nil {
		return domain.Trigger{}, err
	}
	compensate = false
	return trigger, nil
}

func (s Service) persistEnabledTrigger(ctx context.Context, request CreateRequest, connection domain.Connection, definition Definition, triggerID string) (domain.Trigger, error) {
	now := s.now()
	trigger := domain.Trigger{ID: triggerID, OrganizationID: request.OrganizationID, MemberID: request.MemberID,
		ConnectionID: connection.ID, ProfileID: connection.ProfileID, Provider: connection.Provider,
		PieceName: definition.PieceName, PieceVersion: definition.PieceVersion, TriggerName: request.TriggerName,
		ProviderIdentifier: connection.ID, Props: append([]byte(nil), request.Props...),
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
	return trigger, nil
}

func (s Service) disableGoogleTrigger(ctx context.Context, organizationID, memberID string, trigger domain.Trigger) error {
	if s.Google == nil {
		return ErrOperationDisabled
	}
	connection, err := s.Connections.ConnectionForExecution(ctx, organizationID, trigger.ConnectionID)
	if err != nil || connection.MemberID != memberID || !isActiveGoogleConnection(connection) {
		return oauthflow.ErrForbidden
	}
	state, _, err := s.State.LoadTriggerState(ctx, organizationID, trigger.ID)
	if err != nil {
		return err
	}
	watch, err := googleWatchState(state)
	if err != nil {
		return fmt.Errorf("load Google watch state: %w", err)
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
	if watch.Kind == google.WatchKindCalendar {
		if err := s.Google.StopChannel(ctx, accessToken, watch); err != nil {
			return fmt.Errorf("remove Google Calendar channel: %w", err)
		}
		if routes, ok := s.Routes.(PerConnectionRoutes); ok {
			route, routeErr := routes.ActiveWebhookRouteForConnection(ctx, organizationID, connection.ProfileID, trigger.TriggerName, connection.ID)
			if routeErr == nil {
				if err := routes.DeactivateWebhookRoute(ctx, route.OpaqueRoute); err != nil {
					return fmt.Errorf("deactivate Google Calendar webhook route: %w", err)
				}
			} else if !errors.Is(routeErr, domain.ErrNotFound) {
				return ErrRouteUnavailable
			}
		}
	}
	if watch.Kind == google.WatchKindGmail && s.shouldStopGmailWatch(ctx, connection) {
		if err := s.Google.StopGmail(ctx, accessToken); err != nil {
			return fmt.Errorf("remove Gmail watch: %w", err)
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

func (s Service) shouldStopGmailWatch(ctx context.Context, connection domain.Connection) bool {
	if s.GmailCensus == nil {
		return true
	}
	count, err := s.GmailCensus.ActiveGoogleGmailTriggerCount(ctx, google.NormalizeMailboxEmail(connection.Identity.AccountID))
	if err != nil {
		return true
	}
	return count <= 1
}

func (s Service) shouldStopOrphanGmailWatch(ctx context.Context, connection domain.Connection) bool {
	if s.GmailCensus == nil {
		return true
	}
	count, err := s.GmailCensus.ActiveGoogleGmailTriggerCount(ctx, google.NormalizeMailboxEmail(connection.Identity.AccountID))
	if err != nil {
		return true
	}
	return count == 0
}

func (s Service) renewGoogleWatch(ctx context.Context, trigger domain.Trigger, connection domain.Connection, definition Definition) error {
	if s.Google == nil {
		return ErrOperationDisabled
	}
	state, version, err := s.State.LoadTriggerState(ctx, trigger.OrganizationID, trigger.ID)
	if err != nil {
		return err
	}
	watch, err := googleWatchState(state)
	if err != nil {
		return fmt.Errorf("load Google watch state: %w", err)
	}
	if watch.ExpiresAt.After(s.now().Add(24 * time.Hour)) {
		return nil
	}
	auth, err := s.openConnection(ctx, connection)
	if err != nil {
		return err
	}
	defer wipe(auth)
	token, err := accessToken(auth)
	if err != nil {
		return err
	}
	expiresAt := s.now().Add(googleTTL(definition))
	var next google.WatchState
	if watch.Kind == google.WatchKindGmail {
		if s.GmailPubSubTopic == "" {
			return ErrRouteUnavailable
		}
		renewed, err := s.Google.WatchGmail(ctx, token, google.GmailWatchRequest{TopicName: s.GmailPubSubTopic})
		if err != nil {
			return fmt.Errorf("renew Gmail watch: %w", err)
		}
		next = google.WatchState{Kind: google.WatchKindGmail, HistoryID: renewed.HistoryID, ExpiresAt: renewed.Expiration.UTC()}
		if next.ExpiresAt.IsZero() {
			next.ExpiresAt = expiresAt.UTC()
		}
	} else {
		routes, ok := s.Routes.(PerConnectionRoutes)
		if !ok {
			return ErrRouteUnavailable
		}
		route, err := routes.ActiveWebhookRouteForConnection(ctx, trigger.OrganizationID, connection.ProfileID, trigger.TriggerName, connection.ID)
		if err != nil {
			return ErrRouteUnavailable
		}
		channelID, err := google.NewChannelID(s.Random)
		if err != nil {
			return err
		}
		channelToken, err := s.Secrets.Open(ctx, secrets.Binding{OrganizationID: trigger.OrganizationID,
			ResourceType: "webhook-route", ResourceID: route.OpaqueRoute}, route.SigningSecret)
		if err != nil {
			return fmt.Errorf("open Google Calendar channel token: %w", err)
		}
		defer wipe(channelToken)
		channel, err := s.Google.WatchCalendar(ctx, token, google.CalendarWatchRequest{
			CalendarID: watch.CalendarID, ChannelID: channelID, Token: string(channelToken), Expiration: expiresAt,
			Address: s.PublicURL + "/v1/webhooks/" + route.OpaqueRoute,
		})
		if err != nil {
			return fmt.Errorf("renew Google Calendar watch: %w", err)
		}
		next = google.WatchState{Kind: google.WatchKindCalendar, ChannelID: channel.ID,
			ResourceID: channel.ResourceID, CalendarID: channel.CalendarID, ExpiresAt: channel.Expiration.UTC()}
		if next.ExpiresAt.IsZero() {
			next.ExpiresAt = expiresAt.UTC()
		}
		_ = s.Google.StopChannel(ctx, token, watch)
	}
	if err := s.State.ApplyTriggerState(ctx, trigger.OrganizationID, trigger.ID, version, next.StoreMutations()); err != nil {
		return fmt.Errorf("persist renewed Google watch state: %w", err)
	}
	return nil
}

func googleWatchState(store map[string]any) (google.WatchState, error) {
	normalized := make(map[string]any, len(store))
	for key, value := range store {
		normalized[strings.TrimPrefix(key, "\x00")] = value
	}
	return google.WatchStateFromStore(normalized)
}

func googleTTL(definition Definition) time.Duration {
	if definition.GoogleTTL > time.Minute {
		return definition.GoogleTTL
	}
	return 7 * 24 * time.Hour
}

func isGoogleTrigger(connection domain.Connection, definition Definition) bool {
	return connection.Provider == "google" && definition.Provider == "google" &&
		definition.RouteMode == domain.WebhookRoutePerConnection &&
		(definition.GoogleWatch == google.WatchKindCalendar || definition.GoogleWatch == google.WatchKindGmail)
}

func isActiveGoogleConnection(connection domain.Connection) bool {
	return connection.Provider == "google" && connection.Status == domain.ConnectionStatusActive
}

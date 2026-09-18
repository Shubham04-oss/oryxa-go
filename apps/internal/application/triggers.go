package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// TriggerTypeView is an event an app can deliver, as a member chooses one.
type TriggerTypeView struct {
	Trigger      string `json:"trigger"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions,omitempty"`
	Toolkit      string `json:"toolkit"`
	// Kind is webhook when the app pushes the event, poll when Composio polls.
	Kind         string          `json:"kind"`
	ConfigSchema json.RawMessage `json:"config_schema,omitempty"`
	// ManualSetup says a webhook URL has to be registered with the app by
	// hand before the trigger delivers anything.
	ManualSetup bool `json:"manual_setup,omitempty"`
}

// TriggerInput subscribes a member's connection to an event.
type TriggerInput struct {
	ConnectionID string
	Trigger      string
	Config       json.RawMessage
}

// CreateTrigger subscribes one of the caller's own active connections to an
// event its app delivers. Asking again for the same subscription answers the
// one that exists.
//
// Every subscription is checked against the trigger's own definition in
// Composio's catalog first, because the ways a subscription can be accepted
// and then never deliver anything are all invisible afterwards: an event
// belonging to another app, a config missing a field the trigger needs, an
// app whose webhook a member still has to register by hand. A member finds
// out here rather than by waiting for something that never arrives.
func (s *Service) CreateTrigger(ctx context.Context, credentials authclient.Credentials, organizationID string, input TriggerInput) (domain.Trigger, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Trigger{}, err
	}
	if caller.Agent {
		return domain.Trigger{}, fmt.Errorf("an agent cannot subscribe to an app's events: %w", domain.ErrForbidden)
	}
	slug, err := domain.CheckTool(input.Trigger)
	if err != nil {
		return domain.Trigger{}, fmt.Errorf("a trigger is an uppercase slug such as GITHUB_COMMIT_EVENT: %w", domain.ErrInvalid)
	}
	config := input.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(config, &object) != nil {
		return domain.Trigger{}, fmt.Errorf("a trigger's config is a JSON object: %w", domain.ErrInvalid)
	}
	connection, err := s.store.Connection(ctx, organizationID, strings.TrimSpace(input.ConnectionID))
	if err != nil {
		return domain.Trigger{}, err
	}
	if connection.MemberID != caller.UserID {
		return domain.Trigger{}, domain.ErrNotFound
	}
	switch connection.Status {
	case domain.ConnectionActive:
	case domain.ConnectionExpired:
		return domain.Trigger{}, fmt.Errorf("the connection expired: %w", domain.ErrNeedsReconnect)
	default:
		return domain.Trigger{}, fmt.Errorf("the connection is %s: %w", connection.Status, domain.ErrNotConnected)
	}

	// The connection's own app decides which events it can deliver, so a
	// trigger belonging to another app is not found rather than refused:
	// this connection genuinely has no such event.
	definition, err := s.triggers.Definition(ctx, connection.Toolkit, slug)
	if err != nil {
		return domain.Trigger{}, err
	}
	if err := domain.CheckTriggerConfig(definition.ConfigSchema, config); err != nil {
		return domain.Trigger{}, err
	}

	return s.subscribe(ctx, connection, definition, config)
}

// subscribe records one subscription against a connection, which is the same
// work whether a member asked for it or connecting the app did. It acts as
// the connection's own member: the caller has already decided that is
// allowed.
func (s *Service) subscribe(ctx context.Context, connection domain.Connection, definition domain.TriggerDefinition, config json.RawMessage) (domain.Trigger, error) {
	composioID, err := s.composio.UpsertTrigger(ctx, definition.Slug, composio.TriggerSubscription{
		ConnectedAccountID: connection.ComposioAccountID, UserID: connection.ComposioUserID, Config: config,
	})
	if err != nil {
		return domain.Trigger{}, composioError("subscribe to "+definition.Slug, err)
	}
	id, err := newID("apt")
	if err != nil {
		return domain.Trigger{}, err
	}
	now := s.now().UTC()
	// The event carries how it delivers, since a subscription that polls or
	// still needs its webhook registered explains a silence nothing else
	// would.
	event, err := s.event(appsv1.TriggerSubscribedDetailType, appsv1.TriggerSubscribed{
		TriggerID: id, ConnectionID: connection.ID, OrganizationID: connection.OrganizationID,
		MemberID: connection.MemberID, Toolkit: connection.Toolkit, Trigger: definition.Slug,
		Kind: definition.Kind, ManualSetup: definition.ManualSetup, SubscribedAt: now,
	})
	if err != nil {
		return domain.Trigger{}, err
	}
	trigger, created, err := s.store.CreateTrigger(ctx, domain.Trigger{
		ID: id, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, Slug: definition.Slug, Config: config,
		ComposioTriggerID: composioID, Status: domain.TriggerActive, Kind: definition.Kind,
		ManualSetup: definition.ManualSetup, CreatedAt: now, UpdatedAt: now,
	}, event)
	if err != nil {
		return domain.Trigger{}, err
	}
	if created {
		s.logger.InfoContext(ctx, "trigger subscribed", "trigger_id", trigger.ID,
			"organization_id", connection.OrganizationID, "trigger", definition.Slug,
			"kind", definition.Kind, "manual_setup", definition.ManualSetup)
	}
	return trigger, nil
}

// TriggerSetup is what a member still has to do, and what to expect, of a
// subscription. It is advice, not a refusal: the subscription exists either
// way. The catalog carries each trigger's own instructions for the doing of
// it, at GET /v1/apps/catalog/{toolkit}/triggers.
func TriggerSetup(trigger domain.Trigger) string {
	switch {
	case trigger.ManualSetup:
		return "this trigger delivers nothing until the webhook Composio issues is registered in the app itself; the catalog carries the app's own instructions"
	case trigger.Kind == domain.TriggerKindPoll:
		return "this app does not push events, so Composio polls for them and one arrives on its polling interval rather than at once"
	default:
		return ""
	}
}

// Triggers lists the caller's live triggers, optionally on one connection.
func (s *Service) Triggers(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string) ([]domain.Trigger, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	return s.store.Triggers(ctx, organizationID, TriggerQuery{MemberID: caller.UserID, ConnectionID: connectionID})
}

// RemoveTrigger unsubscribes a trigger, for the member it belongs to or an
// org admin.
func (s *Service) RemoveTrigger(ctx context.Context, credentials authclient.Credentials, organizationID, id string) error {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	if caller.Agent {
		return fmt.Errorf("an agent cannot remove a trigger: %w", domain.ErrForbidden)
	}
	trigger, err := s.store.Trigger(ctx, organizationID, id)
	if err != nil {
		return err
	}
	if trigger.MemberID != caller.UserID && !caller.Role.IsOrgAdmin() {
		return domain.ErrNotFound
	}
	if trigger.Status == domain.TriggerRemoved {
		return nil
	}
	if err := s.composio.DeleteTrigger(ctx, trigger.ComposioTriggerID); err != nil && !errors.Is(err, composio.ErrNotFound) {
		return composioError("unsubscribe the trigger", err)
	}
	now := s.now().UTC()
	event, err := s.event(appsv1.TriggerUnsubscribedDetailType, appsv1.TriggerUnsubscribed{
		TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID, OrganizationID: trigger.OrganizationID,
		MemberID: trigger.MemberID, Toolkit: trigger.Toolkit, Trigger: trigger.Slug,
		ByAdmin: trigger.MemberID != caller.UserID, UnsubscribedAt: now,
	})
	if err != nil {
		return err
	}
	_, err = s.store.RemoveTrigger(ctx, trigger.ID, now, event)
	return err
}

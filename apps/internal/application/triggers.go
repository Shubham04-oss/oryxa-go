package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

	composioID, err := s.composio.UpsertTrigger(ctx, slug, connection.ComposioAccountID, config)
	if err != nil {
		return domain.Trigger{}, composioError("subscribe to "+slug, err)
	}
	id, err := newID("apt")
	if err != nil {
		return domain.Trigger{}, err
	}
	now := s.now().UTC()
	trigger, created, err := s.store.CreateTrigger(ctx, domain.Trigger{
		ID: id, OrganizationID: organizationID, MemberID: caller.UserID, ConnectionID: connection.ID,
		Toolkit: connection.Toolkit, Slug: slug, Config: config, ComposioTriggerID: composioID,
		Status: domain.TriggerActive, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return domain.Trigger{}, err
	}
	if created {
		s.logger.InfoContext(ctx, "trigger subscribed",
			"trigger_id", trigger.ID, "organization_id", organizationID, "trigger", slug)
	}
	return trigger, nil
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
	_, err = s.store.RemoveTrigger(ctx, trigger.ID, s.now().UTC())
	return err
}

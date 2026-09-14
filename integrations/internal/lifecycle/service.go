package lifecycle

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// Store is the durable connection and OAuth-app surface auth lifecycle events
// need. It does not check organization membership: the member may already be
// gone, and the organization may already be deleted.
type Store interface {
	OpenConnections(context.Context, string, string) ([]domain.Connection, error)
	DisconnectOpenConnectionWithEvent(context.Context, string, string, string, time.Time, domain.Event) error
	ActiveOAuthApps(context.Context, string) ([]domain.OAuthAppProfile, error)
	DisableOAuthAppWithEvent(context.Context, string, string, time.Time, domain.Event) error
}

type Service struct {
	Store Store
	IDs   func(string) (string, error)
	Now   func() time.Time
}

func (s Service) OrganizationDeleted(ctx context.Context, data autheventsv1.OrganizationDeleted) error {
	if data.OrganizationID == "" {
		return errors.New("organization event has no organization_id")
	}
	now := data.DeletedAt.UTC()
	if now.IsZero() {
		now = s.now()
	}
	if err := s.disconnectConnections(ctx, data.OrganizationID, "", data.DeletedBy, now); err != nil {
		return err
	}
	return s.disableOAuthApps(ctx, data.OrganizationID, data.DeletedBy, now)
}

func (s Service) MemberRemoved(ctx context.Context, data autheventsv1.MemberRemoved) error {
	if data.OrganizationID == "" || data.UserID == "" {
		return errors.New("member event has no organization_id or user_id")
	}
	if data.Reason == autheventsv1.MemberRemovedReasonOrganizationDeleted {
		return nil
	}
	now := data.RemovedAt.UTC()
	if now.IsZero() {
		now = s.now()
	}
	return s.disconnectConnections(ctx, data.OrganizationID, data.UserID, data.RemovedBy, now)
}

func (s Service) disconnectConnections(ctx context.Context, organizationID, memberID, actorID string, now time.Time) error {
	connections, err := s.Store.OpenConnections(ctx, organizationID, memberID)
	if err != nil {
		return fmt.Errorf("list open connections: %w", err)
	}
	for _, connection := range connections {
		eventID, err := s.newID("evt")
		if err != nil {
			return err
		}
		event := domain.Event{EventID: eventID, Source: integrationevents.Source,
			DetailType: integrationevents.ConnectionDisconnectedDetailType, Version: 1,
			OrganizationID: organizationID, OccurredAt: now,
			Data: integrationevents.ConnectionDisconnected{ConnectionID: connection.ID,
				Provider: connection.Provider, DisconnectedBy: actorID, DisconnectedAt: now}}
		if err := s.Store.DisconnectOpenConnectionWithEvent(ctx, organizationID, connection.MemberID, connection.ID, now, event); err != nil {
			return fmt.Errorf("disconnect connection %s: %w", connection.ID, err)
		}
	}
	return nil
}

func (s Service) disableOAuthApps(ctx context.Context, organizationID, actorID string, now time.Time) error {
	apps, err := s.Store.ActiveOAuthApps(ctx, organizationID)
	if err != nil {
		return fmt.Errorf("list oauth apps: %w", err)
	}
	for _, app := range apps {
		eventID, err := s.newID("evt")
		if err != nil {
			return err
		}
		event := domain.Event{EventID: eventID, Source: integrationevents.Source,
			DetailType: integrationevents.OAuthAppDisabledDetailType, Version: 1,
			OrganizationID: organizationID, OccurredAt: now,
			Data: integrationevents.OAuthAppDisabled{ProfileID: app.ID, Provider: app.Provider,
				DisabledBy: actorID, DisabledAt: now}}
		if err := s.Store.DisableOAuthAppWithEvent(ctx, organizationID, app.ID, now, event); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("disable oauth app %s: %w", app.ID, err)
		}
	}
	return nil
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) newID(prefix string) (string, error) {
	if s.IDs != nil {
		return s.IDs(prefix)
	}
	value := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", fmt.Errorf("generate %s ID: %w", prefix, err)
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(value), nil
}

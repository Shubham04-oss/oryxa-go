package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// UpdateConnectionInput is PATCH …/connections/{id}. Nil fields stay as they
// are; Default may only be true.
type UpdateConnectionInput struct {
	Label   *string
	Default *bool
}

// UpdateConnection renames a connection, or makes it its app's default in its
// scope.
func (s *Service) UpdateConnection(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string, input UpdateConnectionInput) (domain.Connection, error) {
	if input.Label == nil && input.Default == nil {
		return domain.Connection{}, fmt.Errorf("label or default is required: %w", domain.ErrInvalid)
	}
	if input.Default != nil && !*input.Default {
		return domain.Connection{}, fmt.Errorf("default can only be set to true; make another connection the default instead: %w", domain.ErrInvalid)
	}
	change := domain.ConnectionChange{MakeDefault: input.Default != nil}
	if input.Label != nil {
		label, err := domain.Label(*input.Label)
		if err != nil {
			return domain.Connection{}, err
		}
		change.Label = &label
	}
	membership, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Connection{}, err
	}
	connection, err := s.manageableConnection(ctx, credentials, membership, connectionID)
	if err != nil {
		return domain.Connection{}, err
	}
	return s.store.UpdateConnection(ctx, organizationID, connection.ID, change, s.clock())
}

// InternalConnection returns a connection's status, scope, owner, granted
// scopes and provider account to a service. When the service forwards a
// member, only a connection that member can see is returned.
func (s *Service) InternalConnection(ctx context.Context, actor Actor, organizationID, connectionID string) (domain.Connection, error) {
	if actor.Service == "" {
		return domain.Connection{}, domain.ErrForbidden
	}
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" {
		return domain.Connection{}, fmt.Errorf("organization_id is required: %w", domain.ErrInvalid)
	}
	if strings.TrimSpace(connectionID) == "" {
		return domain.Connection{}, domain.ErrNotFound
	}
	if !actor.Credentials.Present() {
		return s.store.Connection(ctx, organizationID, connectionID)
	}
	membership, err := s.caller(ctx, actor.Credentials, organizationID)
	if err != nil {
		return domain.Connection{}, err
	}
	connection, _, err := s.visibleConnection(ctx, actor.Credentials, membership, connectionID)
	return connection, err
}

// AddHold keeps a connection's credentials after it is disconnected, until
// the holder releases the hold or domain.MaxHold passes. Only services hold
// connections: they register a hold when they create remote resources with
// one.
func (s *Service) AddHold(ctx context.Context, actor Actor, organizationID, connectionID, holder string) error {
	organizationID, err := s.holdTarget(actor, organizationID, connectionID, holder)
	if err != nil {
		return err
	}
	return s.store.AddHold(ctx, organizationID, connectionID, holder, s.clock())
}

// ReleaseHold releases a hold. Releasing one that does not exist changes
// nothing.
func (s *Service) ReleaseHold(ctx context.Context, actor Actor, organizationID, connectionID, holder string) error {
	organizationID, err := s.holdTarget(actor, organizationID, connectionID, holder)
	if err != nil {
		return err
	}
	return s.store.ReleaseHold(ctx, organizationID, connectionID, holder)
}

func (s *Service) holdTarget(actor Actor, organizationID, connectionID, holder string) (string, error) {
	if actor.Service == "" {
		return "", domain.ErrForbidden
	}
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" {
		return "", fmt.Errorf("organization_id is required: %w", domain.ErrInvalid)
	}
	if strings.TrimSpace(connectionID) == "" {
		return "", domain.ErrNotFound
	}
	if err := domain.ValidHolder(holder); err != nil {
		return "", err
	}
	return organizationID, nil
}

// Report records that the provider rejected a connection's credential for a
// service, so the heartbeat checks it at once.
func (s *Service) Report(ctx context.Context, actor Actor, organizationID, connectionID string) error {
	if actor.Service == "" {
		return domain.ErrForbidden
	}
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" {
		return fmt.Errorf("organization_id is required: %w", domain.ErrInvalid)
	}
	if strings.TrimSpace(connectionID) == "" {
		return domain.ErrNotFound
	}
	return s.store.ReportConnection(ctx, organizationID, connectionID, s.clock())
}

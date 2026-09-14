package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// ConnectionFilter narrows a connection listing. A TeamID alone lists that
// team's connections.
type ConnectionFilter struct {
	AppID  string
	Scope  domain.Scope
	TeamID string
}

// ListConnections lists the live connections the caller can see. Without a
// scope that is the caller's own member connections and the organization's,
// and an org admin also sees every member's. Team connections are listed one
// team at a time, after teams confirms the caller can see the team.
func (s *Service) ListConnections(ctx context.Context, credentials authclient.Credentials, organizationID string, filter ConnectionFilter) ([]domain.Connection, error) {
	membership, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	query := domain.ConnectionQuery{
		AppID: strings.TrimSpace(filter.AppID), Scope: filter.Scope, TeamID: strings.TrimSpace(filter.TeamID),
		MemberID: membership.UserID, Admin: membership.Role.IsOrgAdmin(),
	}
	if query.TeamID != "" && query.Scope == "" {
		query.Scope = domain.ScopeTeam
	}
	switch {
	case query.Scope != "" && !query.Scope.Valid():
		return nil, fmt.Errorf("scope is not allowed: %w", domain.ErrInvalid)
	case query.Scope == domain.ScopeTeam && query.TeamID == "":
		return nil, fmt.Errorf("team connections are listed by team_id: %w", domain.ErrInvalid)
	case query.Scope != domain.ScopeTeam && query.TeamID != "":
		return nil, fmt.Errorf("team_id lists only team connections: %w", domain.ErrInvalid)
	}
	if query.Scope == domain.ScopeTeam {
		if _, err := s.teamAccess(ctx, credentials, organizationID, query.TeamID); err != nil {
			return nil, err
		}
	}
	return s.store.Connections(ctx, organizationID, query)
}

// GetConnection returns a connection the caller can see, in any status.
func (s *Service) GetConnection(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string) (domain.Connection, error) {
	membership, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Connection{}, err
	}
	connection, _, err := s.visibleConnection(ctx, credentials, membership, connectionID)
	return connection, err
}

// Disconnect ends a connection for good. Disconnecting one that is already
// disconnected changes nothing. Its credentials stay until the holds on it
// are released.
func (s *Service) Disconnect(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string) error {
	membership, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	connection, caller, err := s.visibleConnection(ctx, credentials, membership, connectionID)
	if err != nil {
		return err
	}
	if err := domain.MayDisconnect(caller, connection); err != nil {
		return err
	}
	if connection.Status == domain.StatusDisconnected {
		return nil
	}
	now := s.clock()
	_, err = s.store.DisconnectConnection(ctx, organizationID, connection.ID, domain.DisconnectRequested, now,
		s.disconnectedEvent(membership.UserID, now))
	return err
}

// visibleConnection loads a connection in any status that the caller can see,
// with what teams answered about its team.
func (s *Service) visibleConnection(ctx context.Context, credentials authclient.Credentials, membership domain.OrgMembership, connectionID string) (domain.Connection, domain.Caller, error) {
	connectionID = strings.TrimSpace(connectionID)
	if connectionID == "" {
		return domain.Connection{}, domain.Caller{}, domain.ErrNotFound
	}
	connection, err := s.store.Connection(ctx, membership.OrganizationID, connectionID)
	if err != nil {
		return domain.Connection{}, domain.Caller{}, err
	}
	caller, err := s.callerAt(ctx, credentials, membership, connection.Scope, connection.TeamID)
	if err != nil {
		return domain.Connection{}, domain.Caller{}, err
	}
	if err := domain.MaySee(caller, connection); err != nil {
		return domain.Connection{}, domain.Caller{}, err
	}
	return connection, caller, nil
}

package postgres

import (
	"context"
	"errors"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CanManageOAuthApps(ctx context.Context, organizationID, actorID string) error {
	role, err := s.membershipRole(ctx, organizationID, actorID)
	if err != nil {
		return err
	}
	if role != "owner" && role != "admin" {
		return oauthflow.ErrForbidden
	}
	return nil
}

func (s *Store) CanCreateConnection(ctx context.Context, organizationID, memberID string) error {
	_, err := s.membershipRole(ctx, organizationID, memberID)
	return err
}

func (s *Store) membershipRole(ctx context.Context, organizationID, userID string) (string, error) {
	if organizationID == "" || userID == "" {
		return "", oauthflow.ErrForbidden
	}
	var role string
	err := s.pool.QueryRow(ctx, `
		SELECT role FROM auth_memberships
		WHERE organization_id = $1 AND user_id = $2`, organizationID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", oauthflow.ErrForbidden
	}
	if err != nil {
		return "", err
	}
	if role == "" {
		return "", domain.ErrNotFound
	}
	return role, nil
}

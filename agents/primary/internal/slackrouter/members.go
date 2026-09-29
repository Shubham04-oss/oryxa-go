package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// AuthMembers reads members from auth's tables in the shared PostgreSQL, the
// way adksession does: auth_users joined to auth_memberships. Auth remains
// the only writer of them.
type AuthMembers struct{ database *postgreskit.Database }

func NewAuthMembers(database *postgreskit.Database) *AuthMembers {
	return &AuthMembers{database: database}
}

// ByEmail implements Members. Addresses are unique in auth without regard to
// case (auth_users_email_unique), so at most one user matches, and the join
// asks whether that user belongs to this organization.
func (members *AuthMembers) ByEmail(ctx context.Context, organizationID, email string) (Member, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if strings.TrimSpace(organizationID) == "" || email == "" {
		return Member{}, ErrNotMember
	}
	var member Member
	err := members.database.Pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.display_name, m.role
		FROM auth_users u
		JOIN auth_memberships m ON m.user_id = u.id
		WHERE m.organization_id = $1 AND lower(u.email) = $2`, organizationID, email).
		Scan(&member.UserID, &member.Email, &member.DisplayName, &member.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotMember
	}
	if err != nil {
		return Member{}, fmt.Errorf("read a member by address: %w", err)
	}
	return member, nil
}

// OrganizationName implements Members.
func (members *AuthMembers) OrganizationName(ctx context.Context, organizationID string) (string, error) {
	var name string
	err := members.database.Pool.QueryRow(ctx, `SELECT name FROM auth_organizations WHERE id = $1`, organizationID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrGone
	}
	if err != nil {
		return "", fmt.Errorf("read an organization's name: %w", err)
	}
	return name, nil
}

var _ Members = (*AuthMembers)(nil)

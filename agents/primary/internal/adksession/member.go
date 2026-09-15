package adksession

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// Member is the signed-in member a Krude session is created for, read from the
// auth tables in the shared PostgreSQL. The fields mirror the session state
// keys Krude's instruction reads.
type Member struct {
	// UserID is the member's auth user ID; ADK stores it as the session's user.
	UserID string
	// DisplayName is auth_users.display_name, the member's chosen name.
	DisplayName string
	// Email is auth_users.email, the member's sign-in address.
	Email string
	// OrganizationID, OrganizationName and Role come from the organization
	// the session is created in the context of. Role is owner, admin or
	// member.
	OrganizationID   string
	OrganizationName string
	Role             string
}

// ErrMemberNotFound reports that the user belongs to no organization, so there
// is nothing to seed a session with. The session is still created; it simply
// starts with no member state.
var ErrMemberNotFound = errors.New("member belongs to no organization")

// memberReader reads members from auth's tables over the same shared pool the
// ADK session service uses. Auth remains the only writer of these tables.
type memberReader struct {
	database *postgreskit.Database
}

// Member returns the member for the given user, and their first organization
// and role in it. Callers seed a session's state from it.
func (r memberReader) Member(ctx context.Context, userID string) (Member, error) {
	var member Member
	err := r.database.Pool.QueryRow(ctx, `
		SELECT u.id, u.display_name, u.email, o.id, o.name, m.role
		FROM auth_users u
		JOIN auth_memberships m ON m.user_id = u.id
		JOIN auth_organizations o ON o.id = m.organization_id
		WHERE u.id = $1
		ORDER BY m.joined_at, m.organization_id
		LIMIT 1`, userID).
		Scan(&member.UserID, &member.DisplayName, &member.Email, &member.OrganizationID, &member.OrganizationName, &member.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrMemberNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("read member: %w", err)
	}
	return member, nil
}

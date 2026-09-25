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

// ErrMemberNotFound reports that the user belongs to no organization, or not
// to the one asked for, so there is nothing to seed a session with. The
// session is still created; it simply starts with the state its caller sent.
var ErrMemberNotFound = errors.New("member belongs to no organization")

// memberReader reads members from auth's tables over the same shared pool the
// ADK session service uses. Auth remains the only writer of these tables.
type memberReader struct {
	database *postgreskit.Database
}

// Member returns the member for the given user in organizationID, or in
// their first organization when organizationID is empty, with their role in
// it. Callers seed a session's state from it.
func (r memberReader) Member(ctx context.Context, userID, organizationID string) (Member, error) {
	var member Member
	err := r.database.Pool.QueryRow(ctx, `
		SELECT u.id, u.display_name, u.email, o.id, o.name, m.role
		FROM auth_users u
		JOIN auth_memberships m ON m.user_id = u.id
		JOIN auth_organizations o ON o.id = m.organization_id
		WHERE u.id = $1 AND ($2 = '' OR o.id = $2)
		ORDER BY m.joined_at, m.organization_id
		LIMIT 1`, userID, organizationID).
		Scan(&member.UserID, &member.DisplayName, &member.Email, &member.OrganizationID, &member.OrganizationName, &member.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrMemberNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("read member: %w", err)
	}
	return member, nil
}

// IsMember reports whether userID still belongs to organizationID, read from
// auth's tables. Work that nobody asked for in person, such as a wake-up,
// checks it first: a member who left must not have threads made for them.
func (s *Store) IsMember(ctx context.Context, organizationID, userID string) (bool, error) {
	_, err := s.Member(ctx, organizationID, userID)
	if errors.Is(err, ErrMemberNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Member reads userID as a member of organizationID from auth's tables: the
// name they chose, their sign-in address, and their role there. It is
// ErrMemberNotFound for someone who does not belong to it, and for a blank
// organization or user, rather than their first organization's record.
func (s *Store) Member(ctx context.Context, organizationID, userID string) (Member, error) {
	if organizationID == "" || userID == "" {
		return Member{}, ErrMemberNotFound
	}
	return memberReader{database: s.database}.Member(ctx, userID, organizationID)
}

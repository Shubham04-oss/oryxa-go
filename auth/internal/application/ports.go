package application

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

// Relationships writes org roles to SpiceDB as they commit, sooner than the
// projector writes them from the events (docs/design/permissions).
type Relationships interface {
	MemberJoined(ctx context.Context, organizationID, userID string, role domain.Role) error
	MemberRoleChanged(ctx context.Context, organizationID, userID string, role domain.Role) error
	MemberRemoved(ctx context.Context, organizationID, userID string) error
	OrganizationDeleted(ctx context.Context, organizationID string) error
}

type Store interface {
	SaveLoginFlow(context.Context, domain.LoginFlow) error
	ConsumeLoginFlow(context.Context, string) (domain.LoginFlow, error)
	CreateActionToken(context.Context, domain.ActionToken, domain.Event) error
	ClaimActionToken(context.Context, domain.ActionPurpose, string, time.Time) (domain.ActionToken, error)
	ReleaseActionToken(context.Context, domain.ActionPurpose, string) error
	CompleteActionToken(context.Context, domain.ActionPurpose, string, time.Time, domain.Event) error

	UpsertFederatedUser(context.Context, domain.User, domain.Event, domain.Event) (bool, error)
	UserByID(context.Context, string) (domain.User, error)

	CreateOrganization(context.Context, domain.Organization, domain.Membership, []domain.Event) error
	OrganizationsForUser(context.Context, string) ([]domain.Organization, error)
	OrganizationByID(context.Context, string) (domain.Organization, error)
	UpdateOrganization(context.Context, domain.Organization, domain.Event) error
	// DeleteOrganization records the deletion event and, in the same
	// transaction, the event removed returns for each membership it ends.
	DeleteOrganization(ctx context.Context, organizationID string, deleted domain.Event, removed func(domain.Membership) (domain.Event, error)) error
	Membership(context.Context, string, string) (domain.Membership, error)
	Members(context.Context, string) ([]domain.Membership, error)

	CreateInvitation(context.Context, domain.Invitation, domain.Event) error
	Invitations(context.Context, string) ([]domain.Invitation, error)
	InvitationByTokenHash(context.Context, string) (domain.Invitation, error)
	InvitationByID(context.Context, string, string) (domain.Invitation, error)
	RevokeInvitation(context.Context, string, string, domain.Event) error
	// ReceivedInvitations lists the invitations to email still pending at
	// the given time, in organizations userID has not joined.
	ReceivedInvitations(ctx context.Context, userID, email string, at time.Time) ([]domain.ReceivedInvitation, error)
	// AcceptInvitation adds membership for user on the invitation
	// invitationID, which must still be pending, unexpired, addressed to
	// user, and for the organization and role membership names.
	AcceptInvitation(ctx context.Context, invitationID string, user domain.User, membership domain.Membership, joined domain.Event) error
	ChangeMemberRole(context.Context, string, string, domain.Role, domain.Role, domain.Event) error
	RemoveMember(context.Context, string, string, domain.Role, domain.Event) error
	ApplyEntitlements(ctx context.Context, organizationID string, revision int64, memberLimit *int, changedAt time.Time) error

	EnqueueEvent(context.Context, domain.Event) error
}

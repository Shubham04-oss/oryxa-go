package application

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

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
	DeleteOrganization(context.Context, string, domain.Event) error
	Membership(context.Context, string, string) (domain.Membership, error)
	Members(context.Context, string) ([]domain.Membership, error)

	CreateInvitation(context.Context, domain.Invitation, domain.Event) error
	Invitations(context.Context, string) ([]domain.Invitation, error)
	InvitationByTokenHash(context.Context, string) (domain.Invitation, error)
	InvitationByID(context.Context, string, string) (domain.Invitation, error)
	RevokeInvitation(context.Context, string, string, domain.Event) error
	AcceptInvitation(context.Context, string, domain.User, domain.Membership, domain.Event) error
	ChangeMemberRole(context.Context, string, string, domain.Role, domain.Role, domain.Event) error
	RemoveMember(context.Context, string, string, domain.Role, domain.Event) error

	EnqueueEvent(context.Context, domain.Event) error
	ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error)
	MarkOutboxPublished(context.Context, string) error
	MarkOutboxFailed(context.Context, string, time.Duration, string) error
}

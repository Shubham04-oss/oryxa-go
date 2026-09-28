// Package v1 contains the stable version-one event contracts emitted by auth.
package v1

import "time"

const (
	Source = "oryxa.auth"

	UserRegisteredDetailType                     = "auth.user.registered.v1"
	UserSignedInDetailType                       = "auth.user.signed-in.v1"
	UserSignedOutDetailType                      = "auth.user.signed-out.v1"
	EmailVerificationDeliveryRequestedDetailType = "auth.user.email-verification.delivery-requested.v1"
	EmailVerifiedDetailType                      = "auth.user.email-verified.v1"
	PasswordResetDeliveryRequestedDetailType     = "auth.user.password-reset.delivery-requested.v1"
	PasswordResetDetailType                      = "auth.user.password-reset.v1"
	OrganizationCreatedDetailType                = "auth.organization.created.v1"
	OrganizationUpdatedDetailType                = "auth.organization.updated.v1"
	OrganizationDeletedDetailType                = "auth.organization.deleted.v1"
	MemberInvitedDetailType                      = "auth.member.invited.v1"
	MemberInvitationRevokedDetailType            = "auth.member.invitation-revoked.v1"
	MemberJoinedDetailType                       = "auth.member.joined.v1"
	MemberRoleChangedDetailType                  = "auth.member.role-changed.v1"
	MemberRemovedDetailType                      = "auth.member.removed.v1"
)

// Reasons a membership ends, as MemberRemoved.Reason.
const (
	MemberRemovedReasonLeft                = "left"
	MemberRemovedReasonRemoved             = "removed"
	MemberRemovedReasonOrganizationDeleted = "organization_deleted"
)

type UserRegistered struct {
	UserID       string    `json:"user_id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"display_name,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

type UserSignedIn struct {
	UserID     string    `json:"user_id"`
	Provider   string    `json:"provider"`
	SignedInAt time.Time `json:"signed_in_at"`
}

type UserSignedOut struct {
	UserID      string    `json:"user_id"`
	SignedOutAt time.Time `json:"signed_out_at"`
}

type EmailVerificationDeliveryRequested struct {
	RequestID       string    `json:"request_id"`
	UserID          string    `json:"user_id"`
	Email           string    `json:"email"`
	VerificationURL string    `json:"verification_url"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type EmailVerified struct {
	UserID     string    `json:"user_id"`
	Email      string    `json:"email"`
	VerifiedAt time.Time `json:"verified_at"`
}

type PasswordResetDeliveryRequested struct {
	RequestID string    `json:"request_id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	ResetURL  string    `json:"reset_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PasswordReset struct {
	UserID  string    `json:"user_id"`
	Email   string    `json:"email"`
	ResetAt time.Time `json:"reset_at"`
}

type OrganizationCreated struct {
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

type OrganizationUpdated struct {
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	UpdatedBy      string    `json:"updated_by"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type OrganizationDeleted struct {
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	DeletedBy      string    `json:"deleted_by"`
	DeletedAt      time.Time `json:"deleted_at"`
}

type MemberInvited struct {
	InvitationID     string    `json:"invitation_id"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	Email            string    `json:"email"`
	Role             string    `json:"role"`
	InvitedBy        string    `json:"invited_by"`
	AcceptURL        string    `json:"accept_url"`
	Quiet            bool      `json:"quiet,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type MemberInvitationRevoked struct {
	InvitationID     string    `json:"invitation_id"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	Email            string    `json:"email"`
	Role             string    `json:"role"`
	RevokedBy        string    `json:"revoked_by"`
	RevokedAt        time.Time `json:"revoked_at"`
}

type MemberJoined struct {
	OrganizationID string    `json:"organization_id"`
	UserID         string    `json:"user_id"`
	Email          string    `json:"email"`
	Role           string    `json:"role"`
	JoinedAt       time.Time `json:"joined_at"`
}

type MemberRoleChanged struct {
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	UserID           string    `json:"user_id"`
	Email            string    `json:"email"`
	PreviousRole     string    `json:"previous_role"`
	Role             string    `json:"role"`
	ChangedBy        string    `json:"changed_by"`
	ChangedAt        time.Time `json:"changed_at"`
}

// MemberRemoved is emitted for every membership that ends: a member leaving,
// being removed, or losing access because the organization was deleted.
type MemberRemoved struct {
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	UserID           string    `json:"user_id"`
	Email            string    `json:"email"`
	PreviousRole     string    `json:"previous_role"`
	Reason           string    `json:"reason"`
	RemovedBy        string    `json:"removed_by"`
	RemovedAt        time.Time `json:"removed_at"`
}

package domain

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
	ErrRateLimited  = errors.New("rate limited")
	ErrExpired      = errors.New("expired")
	// ErrLimitReached means the organization's plan allows no more of what
	// was asked for.
	ErrLimitReached = errors.New("limit reached")
	// ErrUnavailable means a dependency the request needed, such as Cognito,
	// did not answer.
	ErrUnavailable = errors.New("unavailable")
)

type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

func (role Role) Valid() bool {
	return role == RoleOwner || role == RoleAdmin || role == RoleMember
}

func (role Role) CanManageMembers() bool {
	return role == RoleOwner || role == RoleAdmin
}

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name,omitempty"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Membership struct {
	OrganizationID string    `json:"organization_id"`
	UserID         string    `json:"user_id"`
	Email          string    `json:"email"`
	DisplayName    string    `json:"display_name,omitempty"`
	Role           Role      `json:"role"`
	JoinedAt       time.Time `json:"joined_at"`
}

type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "pending"
	InvitationAccepted InvitationStatus = "accepted"
	InvitationRevoked  InvitationStatus = "revoked"
	InvitationExpired  InvitationStatus = "expired"
)

type Invitation struct {
	ID             string           `json:"id"`
	OrganizationID string           `json:"organization_id"`
	Email          string           `json:"email"`
	Role           Role             `json:"role"`
	TokenHash      string           `json:"-"`
	InvitedBy      string           `json:"invited_by"`
	Status         InvitationStatus `json:"status"`
	ExpiresAt      time.Time        `json:"expires_at"`
	CreatedAt      time.Time        `json:"created_at"`
	AcceptedAt     *time.Time       `json:"accepted_at,omitempty"`
}

// ReceivedInvitation is a pending invitation as the person it was sent to
// sees it: which organization asked them to join, and as what. It says no
// more than the invitation email does.
type ReceivedInvitation struct {
	ID               string    `json:"id"`
	OrganizationID   string    `json:"organization_id"`
	OrganizationName string    `json:"organization_name"`
	Role             Role      `json:"role"`
	ExpiresAt        time.Time `json:"expires_at"`
	CreatedAt        time.Time `json:"created_at"`
}

type LoginFlow struct {
	StateHash    string
	CodeVerifier string
	Nonce        string
	ReturnTo     string
	ExpiresAt    time.Time
}

type ActionPurpose string

const (
	ActionEmailVerification ActionPurpose = "email_verification"
	ActionPasswordReset     ActionPurpose = "password_reset"
)

type ActionStatus string

const (
	ActionPending    ActionStatus = "pending"
	ActionProcessing ActionStatus = "processing"
	ActionCompleted  ActionStatus = "completed"
	ActionExpired    ActionStatus = "expired"
)

type ActionToken struct {
	ID              string
	UserID          string
	Email           string
	Purpose         ActionPurpose
	TokenHash       string
	Status          ActionStatus
	ExpiresAt       time.Time
	CreatedAt       time.Time
	ProcessingUntil *time.Time
}

type Event struct {
	ID         string
	DetailType string
	OccurredAt time.Time
	Data       json.RawMessage
}

type OutboxEvent struct {
	Event
	Attempts int
}

// SealedData is an envelope-encrypted value. The plaintext data key is never
// stored; EncryptedDataKey opens only with the same KMS encryption context.
type SealedData struct {
	Version          int    `json:"version"`
	Nonce            []byte `json:"nonce"`
	Ciphertext       []byte `json:"ciphertext"`
	EncryptedDataKey []byte `json:"encrypted_data_key"`
}

// AgentGrant is a member's consent for the Cognito agent app client to act as
// them while they are absent: an envelope-encrypted refresh token auth
// exchanges for short-lived access tokens on request. UserID is the member
// who granted it, and Scope records what was granted.
type AgentGrant struct {
	UserID                string
	EncryptedRefreshToken SealedData
	Scope                 string
	CreatedAt             time.Time
	RefreshedAt           *time.Time
}

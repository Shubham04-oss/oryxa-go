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

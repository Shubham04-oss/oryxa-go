// Package managedagents owns the persistent agents a member creates. It keeps
// their display identity separate from ADK's machine agent name, so a member
// can rename an agent without invalidating its sessions or tool state.
package managedagents

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("agent not found")
	ErrInvalid  = errors.New("invalid agent")
	ErrConflict = errors.New("agent name is already in use")
	ErrInUse    = errors.New("agent belongs to an agent team")
	ErrDisabled = errors.New("agent is disabled")
)

type Kind string

const (
	KindPrimary    Kind = "primary"
	KindSpecialist Kind = "specialist"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// Scope is the ownership boundary for every registry operation.
type Scope struct {
	OrganizationID string
	UserID         string
}

// Definition is a persistent, member-owned agent definition.
type Definition struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	OwnerUserID    string    `json:"owner_user_id"`
	Kind           Kind      `json:"kind"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Instructions   string    `json:"instructions"`
	Status         Status    `json:"status"`
	Revision       int64     `json:"revision"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Handle is the safe discoverability view of an active agent. Instructions
// remain private to the owner; another member needs only identity and purpose
// to decide whether this is the right agent to contact.
type Handle struct {
	ID          string `json:"id"`
	OwnerUserID string `json:"owner_user_id"`
	Kind        Kind   `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type CreateInput struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

// UpdateInput uses pointers so an omitted field differs from clearing it.
type UpdateInput struct {
	Name         *string `json:"name,omitempty"`
	Description  *string `json:"description,omitempty"`
	Instructions *string `json:"instructions,omitempty"`
	Status       *Status `json:"status,omitempty"`
}

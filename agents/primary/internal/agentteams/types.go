// Package agentteams owns the persistent teams a member composes from their
// primary agent and specialist agents.
package agentteams

import (
	"errors"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

var (
	ErrNotFound  = errors.New("agent team not found")
	ErrInvalid   = errors.New("invalid agent team")
	ErrConflict  = errors.New("agent team name is already in use")
	ErrDisabled  = errors.New("agent team is disabled")
	ErrForbidden = errors.New("agent is not the team coordinator")
	// ErrInUse is a team something still points at: a durable workflow runs
	// it, so deleting the team would leave that workflow with nothing to
	// dispatch to.
	ErrInUse = errors.New("agent team is in use")
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type Scope = managedagents.Scope

type Member struct {
	AgentID     string               `json:"agent_id"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Status      managedagents.Status `json:"status"`
	Role        string               `json:"role"`
	Position    int                  `json:"position"`
}

type Team struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organization_id"`
	OwnerUserID        string    `json:"owner_user_id"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	CoordinatorAgentID string    `json:"coordinator_agent_id"`
	CoordinatorName    string    `json:"coordinator_name"`
	Status             Status    `json:"status"`
	Revision           int64     `json:"revision"`
	Members            []Member  `json:"members"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type MemberInput struct {
	AgentID string `json:"agent_id"`
	Role    string `json:"role"`
}

type CreateInput struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Members     []MemberInput `json:"members"`
}

type UpdateInput struct {
	Name        *string        `json:"name,omitempty"`
	Description *string        `json:"description,omitempty"`
	Status      *Status        `json:"status,omitempty"`
	Members     *[]MemberInput `json:"members,omitempty"`
}

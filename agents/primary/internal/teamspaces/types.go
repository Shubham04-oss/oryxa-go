// Package teamspaces owns shared multi-user agent spaces bound to human teams.
// One organization-owned agent holds the broad organizational context; each
// teamspace is a shared work ledger for teammates and their bounded agents.
package teamspaces

import (
	"errors"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

var (
	ErrNotFound  = errors.New("teamspace not found")
	ErrInvalid   = errors.New("invalid teamspace")
	ErrForbidden = errors.New("teamspace is not available to this member")
	ErrConflict  = errors.New("teamspace already exists")
	ErrDisabled  = errors.New("teamspace is disabled")
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// DelegationMode is the authority a specialist owner deliberately gives to
// this teamspace. Request keeps the existing decide/respond protocol between
// users. Automatic lets the organization agent start bounded work without
// another approval, while the specialist still runs as its owner and keeps
// the owner's tool authorization and confirmation boundaries.
type DelegationMode string

const (
	DelegationRequest   DelegationMode = "request"
	DelegationAutomatic DelegationMode = "automatic"
)

type Actor struct {
	OrganizationID string
	UserID         string
	AccessToken    string
}

type OrganizationAgent struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	Instructions   string    `json:"instructions"`
	Status         Status    `json:"status"`
	Revision       int64     `json:"revision"`
	CanManage      bool      `json:"can_manage"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Whisperer is the safe teamspace projection of a teammate's primary agent.
// It identifies an active collaborator but never exposes that agent's private
// instructions, conversation context, connections, or credentials.
type Whisperer struct {
	UserID          string `json:"user_id"`
	UserDisplayName string `json:"user_display_name,omitempty"`
	AgentID         string `json:"agent_id"`
	AgentName       string `json:"agent_name"`
	Mode            string `json:"mode"`
}

// Contribution is a safe shared view of one member-owned specialist. Private
// operating instructions never enter the teamspace record or its events.
type Contribution struct {
	AgentID        string               `json:"agent_id"`
	OwnerUserID    string               `json:"owner_user_id"`
	Name           string               `json:"name"`
	Description    string               `json:"description,omitempty"`
	Status         managedagents.Status `json:"status"`
	Role           string               `json:"role"`
	DelegationMode DelegationMode       `json:"delegation_mode"`
	Position       int                  `json:"position"`
	CreatedAt      time.Time            `json:"created_at"`
}

type Space struct {
	ID                  string            `json:"id"`
	OrganizationID      string            `json:"organization_id"`
	TeamID              string            `json:"team_id"`
	TeamName            string            `json:"team_name"`
	Name                string            `json:"name"`
	Description         string            `json:"description,omitempty"`
	CreatedByUserID     string            `json:"created_by_user_id"`
	OrganizationAgentID string            `json:"-"`
	OrganizationAgent   OrganizationAgent `json:"organization_agent"`
	Whisperers          []Whisperer       `json:"whisperers"`
	Status              Status            `json:"status"`
	Revision            int64             `json:"revision"`
	Contributions       []Contribution    `json:"contributions"`
	CanManage           bool              `json:"can_manage"`
	IsMember            bool              `json:"is_member"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

type CreateInput struct {
	TeamID      string `json:"team_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type UpdateInput struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *Status `json:"status,omitempty"`
}

type OrganizationAgentUpdate struct {
	Name         *string `json:"name,omitempty"`
	Description  *string `json:"description,omitempty"`
	Instructions *string `json:"instructions,omitempty"`
	Status       *Status `json:"status,omitempty"`
}

type ContributionInput struct {
	AgentID        string         `json:"agent_id"`
	Role           string         `json:"role"`
	DelegationMode DelegationMode `json:"delegation_mode"`
}

type ContributionUpdate struct {
	Role           *string         `json:"role,omitempty"`
	DelegationMode *DelegationMode `json:"delegation_mode,omitempty"`
}

type EntryKind string

const (
	EntryUpdate       EntryKind = "update"
	EntryResult       EntryKind = "result"
	EntryKindArtifact EntryKind = "artifact"
	EntryHandoff      EntryKind = "handoff"
	EntryDecision     EntryKind = "decision"
)

// EntryArtifact is a reference deliberately published into shared space. The
// underlying file service remains responsible for access control.
type EntryArtifact struct {
	Name      string `json:"name"`
	URI       string `json:"uri"`
	MediaType string `json:"media_type,omitempty"`
}

// Entry is one immutable, attributed publication in the shared teamspace.
// Private prompts and chain-of-thought are never part of this envelope.
type Entry struct {
	ID              string          `json:"id"`
	TeamspaceID     string          `json:"teamspace_id"`
	OrganizationID  string          `json:"organization_id"`
	AuthorUserID    string          `json:"author_user_id"`
	AuthorAgentID   string          `json:"author_agent_id"`
	AuthorAgentName string          `json:"author_agent_name"`
	Kind            EntryKind       `json:"kind"`
	Title           string          `json:"title"`
	Summary         string          `json:"summary"`
	ToolsUsed       []string        `json:"tools_used"`
	Artifacts       []EntryArtifact `json:"artifacts"`
	CreatedAt       time.Time       `json:"created_at"`
}

type PublishEntryInput struct {
	AuthorAgentID string          `json:"author_agent_id"`
	Kind          EntryKind       `json:"kind"`
	Title         string          `json:"title"`
	Summary       string          `json:"summary"`
	ToolsUsed     []string        `json:"tools_used,omitempty"`
	Artifacts     []EntryArtifact `json:"artifacts,omitempty"`
}

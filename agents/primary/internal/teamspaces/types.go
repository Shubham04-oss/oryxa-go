// Package teamspaces owns shared multi-user agent spaces bound to human teams.
// One organization-owned agent summarizes each teamspace for its own team and
// proposes follow-ups; each teamspace is a shared work ledger for teammates
// and their bounded agents.
package teamspaces

import (
	"encoding/json"
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
	// ErrPaused refuses a change to what a paused teamspace's team keeps,
	// such as its checklist, which stays readable.
	ErrPaused = errors.New("teamspace is paused")
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// DelegationMode is the authority a specialist owner deliberately gives to
// this teamspace, and the request ledger enforces it for every cross-user
// request made here (RequestSeat reports it when a request is made, and
// RequestStanding whenever it is decided or its agent woken): Request holds
// each such request for the owner to approve or reject in their own session
// before the specialist is woken; Automatic lets the specialist decide them
// itself. The organization agent never starts work under either mode: it only
// proposes follow-ups. A request made outside any teamspace is not governed by it.
// Either way the specialist still runs as its owner and keeps the owner's
// tool authorization and confirmation boundaries.
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
	// TargetDate is the day the team is working towards, as YYYY-MM-DD, and
	// TargetLabel what it is ("Launch"). A manager sets either; null is none.
	TargetDate  *string `json:"target_date"`
	TargetLabel *string `json:"target_label"`
	// PausedByUserID and PausedAt say who disabled the teamspace, and when.
	// Both are null while it is active, and for one disabled before they were
	// recorded.
	PausedByUserID *string    `json:"paused_by_user_id"`
	PausedAt       *time.Time `json:"paused_at"`
	// LastReadSeq, UnreadCount and LastMessage are the caller's view of the
	// teamspace's room. The room service fills them in for a member of the
	// team, on every answer that is a whole teamspace, and they are left out
	// for anybody else. UnreadCount stops at 100 (rooms.UnreadCap), which a
	// client shows as "99+".
	LastReadSeq *int64       `json:"last_read_seq,omitempty"`
	UnreadCount *int64       `json:"unread_count,omitempty"`
	LastMessage *LastMessage `json:"last_message,omitempty"`
	// TaskCounts is how much of the checklist is done.
	TaskCounts TaskCounts `json:"task_counts"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// LastMessage is the newest thing said in a teamspace's room, as a list of
// teamspaces shows it.
type LastMessage struct {
	Seq int64 `json:"seq"`
	// Kind is "message", or "parley_notes" for what a parley left behind.
	Kind string `json:"kind"`
	// AuthorKind is "person" or "agent". AuthorID is the person's user id, or
	// the agent's id.
	AuthorKind   string `json:"author_kind"`
	AuthorID     string `json:"author_id"`
	AuthorHandle string `json:"author_handle"`
	// AuthorName is the name the author has in the teamspace now, or their
	// handle when they no longer have a seat there.
	AuthorName string `json:"author_name"`
	// Preview is the start of the message, at most 140 characters.
	Preview   string    `json:"preview"`
	CreatedAt time.Time `json:"created_at"`
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
	// TargetDate and TargetLabel are left alone when left out, and cleared
	// when sent as null.
	TargetDate  Nullable `json:"target_date,omitzero"`
	TargetLabel Nullable `json:"target_label,omitzero"`
}

// Nullable is a field a PATCH may leave out, set, or clear with null: three
// things a plain pointer cannot tell apart, since it reads null and absent
// alike.
type Nullable struct {
	// Set says the field was sent. Value is nil when it was sent as null.
	Set   bool
	Value *string
}

func (field *Nullable) UnmarshalJSON(data []byte) error {
	field.Set = true
	if string(data) == "null" {
		field.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	field.Value = &value
	return nil
}

func (field Nullable) MarshalJSON() ([]byte, error) { return json.Marshal(field.Value) }

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

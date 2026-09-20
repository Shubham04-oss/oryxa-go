// Package organizationagents turns attributed teamspace publications into a
// durable organization-wide synthesis owned by the singleton organization
// agent. It never receives member-private context or credentials.
package organizationagents

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("organization-agent synthesis source not found")
	ErrDisabled = errors.New("organization agent is disabled")
	ErrInvalid  = errors.New("invalid organization-agent synthesis")
)

type FollowUpResult struct {
	TargetAgentID string `json:"target_agent_id"`
	Task          string `json:"task"`
	Rationale     string `json:"rationale"`
}

type Result struct {
	Summary       string           `json:"summary"`
	Decisions     []string         `json:"decisions"`
	Risks         []string         `json:"risks"`
	OpenQuestions []string         `json:"open_questions"`
	FollowUps     []FollowUpResult `json:"follow_ups"`
}

type Synthesis struct {
	OrganizationID string    `json:"organization_id"`
	AgentID        string    `json:"agent_id"`
	Revision       int64     `json:"revision"`
	Summary        string    `json:"summary"`
	Decisions      []string  `json:"decisions"`
	Risks          []string  `json:"risks"`
	OpenQuestions  []string  `json:"open_questions"`
	SourceEntryID  string    `json:"source_entry_id,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitzero"`
}

type FollowUp struct {
	ID                string    `json:"id"`
	OrganizationID    string    `json:"organization_id"`
	SynthesisRevision int64     `json:"synthesis_revision"`
	SourceEntryID     string    `json:"source_entry_id"`
	TeamspaceID       string    `json:"teamspace_id"`
	TargetAgentID     string    `json:"target_agent_id,omitempty"`
	Task              string    `json:"task"`
	Rationale         string    `json:"rationale"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
}

const FollowUpProposed = "proposed"

// Delivery is the content-minimal EventBridge job. The worker loads the
// authoritative entry, teamspace, organization agent, and synthesis itself.
type Delivery struct {
	EventID        string
	EntryID        string
	TeamspaceID    string
	OrganizationID string
	AuthorUserID   string
	AuthorAgentID  string
	Kind           string
	PublishedAt    time.Time
	Attempts       int
}

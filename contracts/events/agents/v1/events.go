// Package v1 holds the event contracts agents primary publishes on
// EventBridge. Every Oryxa publisher puts the same envelope around one of
// these details: event_id, version, occurred_at and data.
package v1

import "time"

// Source identifies agents primary on the bus.
const Source = "oryxa.agents"

const (
	// SessionCreatedDetailType is published when a member's session is
	// created.
	SessionCreatedDetailType = "agents.session.created.v1"
	// SessionDeletedDetailType is published when a session is deleted.
	SessionDeletedDetailType = "agents.session.deleted.v1"
	// RunCompletedDetailType is published when a run finishes, carrying the
	// model usage it cost. Billing and analytics consume it.
	RunCompletedDetailType = "agents.run.completed.v1"
	// RunFailedDetailType is published when a run ends in an error.
	RunFailedDetailType = "agents.run.failed.v1"
	// AgentCreatedDetailType is published when a member creates a persistent
	// agent, including the primary agent created for them on first use.
	AgentCreatedDetailType = "agents.agent.created.v1"
	// AgentUpdatedDetailType is published when a persistent agent changes.
	AgentUpdatedDetailType = "agents.agent.updated.v1"
	// AgentDeletedDetailType is published when a specialist agent is deleted.
	AgentDeletedDetailType = "agents.agent.deleted.v1"
	// AgentTeamCreatedDetailType is published when a member composes a team.
	AgentTeamCreatedDetailType = "agents.team.created.v1"
	// AgentTeamUpdatedDetailType is published when a team's configuration changes.
	AgentTeamUpdatedDetailType = "agents.team.updated.v1"
	// AgentTeamDeletedDetailType is published when a member deletes a team.
	AgentTeamDeletedDetailType = "agents.team.deleted.v1"
	// AgentWorkflowCreatedDetailType is published when a member creates a
	// durable workflow around one of their teams.
	AgentWorkflowCreatedDetailType = "agents.workflow.created.v1"
	// AgentWorkflowUpdatedDetailType is published when workflow configuration
	// or schedule state changes.
	AgentWorkflowUpdatedDetailType = "agents.workflow.updated.v1"
	// AgentWorkflowDeletedDetailType is published when a workflow is deleted.
	AgentWorkflowDeletedDetailType = "agents.workflow.deleted.v1"
	// AgentWorkflowExecutionQueuedDetailType is published once an API or
	// schedule request has a durable execution ledger entry.
	AgentWorkflowExecutionQueuedDetailType = "agents.workflow.execution.queued.v1"
	// AgentWorkflowExecutionStartedDetailType is published when a worker claims
	// an execution and starts delegating its assignments.
	AgentWorkflowExecutionStartedDetailType = "agents.workflow.execution.started.v1"
	// AgentWorkflowExecutionCompletedDetailType is published for successful
	// executions and executions paused for member confirmation.
	AgentWorkflowExecutionCompletedDetailType = "agents.workflow.execution.completed.v1"
	// AgentWorkflowExecutionFailedDetailType is published when execution ends
	// without a result.
	AgentWorkflowExecutionFailedDetailType = "agents.workflow.execution.failed.v1"
	// InteractionRequestedDetailType routes a newly persisted request to the
	// dedicated service that wakes its target agent.
	InteractionRequestedDetailType = "agents.interaction.requested.v1"
	// InteractionDecidedDetailType announces the target's approval or rejection.
	InteractionDecidedDetailType = "agents.interaction.decided.v1"
	// InteractionCompletedDetailType announces a completed or failed response.
	InteractionCompletedDetailType = "agents.interaction.completed.v1"
	// TeamspaceCreatedDetailType announces a shared work space bound to a
	// human team. Private prompts and contributed-agent instructions stay out
	// of EventBridge.
	TeamspaceCreatedDetailType = "agents.teamspace.created.v1"
	// TeamspaceUpdatedDetailType announces shared identity or policy changes.
	TeamspaceUpdatedDetailType = "agents.teamspace.updated.v1"
	// TeamspaceDeletedDetailType announces removal of a shared teamspace.
	TeamspaceDeletedDetailType = "agents.teamspace.deleted.v1"
	// TeamspaceContributionChangedDetailType announces a member adding,
	// changing, or removing an agent contribution.
	TeamspaceContributionChangedDetailType = "agents.teamspace.contribution.changed.v1"
	// OrganizationAgentCreatedDetailType announces the singleton coordinating
	// agent for an organization without copying its instructions to EventBridge.
	OrganizationAgentCreatedDetailType = "agents.organization-agent.created.v1"
	// OrganizationAgentUpdatedDetailType announces a lifecycle change to that
	// organization-owned coordinating agent.
	OrganizationAgentUpdatedDetailType = "agents.organization-agent.updated.v1"
	// TeamspaceEntryPublishedDetailType wakes consumers for newly shared work;
	// the content itself remains in PostgreSQL behind team authorization.
	TeamspaceEntryPublishedDetailType = "agents.teamspace.entry.published.v1"
	// OrganizationAgentSynthesisUpdatedDetailType announces a durable shared
	// context revision without copying synthesized content to EventBridge.
	OrganizationAgentSynthesisUpdatedDetailType = "agents.organization-agent.synthesis.updated.v1"
)

// AgentChanged is the shared lifecycle detail for a persistent member-owned
// agent. Instructions are deliberately absent: events are discoverability and
// audit signals, not another copy of member-authored prompts.
type AgentChanged struct {
	AgentID        string    `json:"agent_id"`
	UserID         string    `json:"user_id"`
	OrganizationID string    `json:"organization_id"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	Revision       int64     `json:"revision"`
	ChangedAt      time.Time `json:"changed_at"`
}

// AgentTeamChanged is the lifecycle projection of a member-owned agent team.
// Agent instructions and delegated tasks never enter EventBridge.
type AgentTeamChanged struct {
	TeamID             string    `json:"team_id"`
	UserID             string    `json:"user_id"`
	OrganizationID     string    `json:"organization_id"`
	Name               string    `json:"name"`
	CoordinatorAgentID string    `json:"coordinator_agent_id"`
	MemberAgentIDs     []string  `json:"member_agent_ids"`
	Status             string    `json:"status"`
	Revision           int64     `json:"revision"`
	ChangedAt          time.Time `json:"changed_at"`
}

// AgentWorkflowChanged is the safe lifecycle projection of a member-owned
// workflow. Assignments, instructions, schedules and model output remain in
// the agents database rather than being copied onto EventBridge.
type AgentWorkflowChanged struct {
	WorkflowID      string    `json:"workflow_id"`
	UserID          string    `json:"user_id"`
	OrganizationID  string    `json:"organization_id"`
	TeamID          string    `json:"team_id"`
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	ScheduleEnabled bool      `json:"schedule_enabled"`
	Revision        int64     `json:"revision"`
	ChangedAt       time.Time `json:"changed_at"`
}

// AgentWorkflowExecutionChanged announces durable execution state without
// exposing delegated tasks, tool calls, confirmation content, or results.
type AgentWorkflowExecutionChanged struct {
	ExecutionID    string    `json:"execution_id"`
	WorkflowID     string    `json:"workflow_id"`
	UserID         string    `json:"user_id"`
	OrganizationID string    `json:"organization_id"`
	StartedBy      string    `json:"started_by"`
	Status         string    `json:"status"`
	ChangedAt      time.Time `json:"changed_at"`
}

// InteractionChanged is the routing and audit projection of a cross-user
// agent request. Purpose, task, context, decisions and results remain in the
// agents database; EventBridge carries only identities and lifecycle state.
type InteractionChanged struct {
	RequestID        string    `json:"request_id"`
	OrganizationID   string    `json:"organization_id"`
	RequesterUserID  string    `json:"requester_user_id"`
	RequesterAgentID string    `json:"requester_agent_id"`
	TargetUserID     string    `json:"target_user_id"`
	TargetAgentID    string    `json:"target_agent_id"`
	Status           string    `json:"status"`
	Revision         int64     `json:"revision"`
	ChangedAt        time.Time `json:"changed_at"`
}

// TeamspaceChanged is the safe lifecycle projection of a multi-user shared
// space. Organization-agent instructions, whisperer context, contributed-agent
// roles, and tool authority remain in the agents database.
type TeamspaceChanged struct {
	TeamspaceID         string    `json:"teamspace_id"`
	OrganizationID      string    `json:"organization_id"`
	TeamID              string    `json:"team_id"`
	OrganizationAgentID string    `json:"organization_agent_id"`
	Name                string    `json:"name"`
	Status              string    `json:"status"`
	Revision            int64     `json:"revision"`
	ContributorUserIDs  []string  `json:"contributor_user_ids"`
	ChangedAt           time.Time `json:"changed_at"`
}

// OrganizationAgentChanged is the safe lifecycle projection of the one
// organization-owned coordinator. Its authored instructions stay private.
type OrganizationAgentChanged struct {
	OrganizationID string    `json:"organization_id"`
	AgentID        string    `json:"agent_id"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	Revision       int64     `json:"revision"`
	ChangedAt      time.Time `json:"changed_at"`
}

// TeamspaceEntryPublished routes an attributed shared-space publication
// without copying the result, tool list, or artifact references to EventBridge.
type TeamspaceEntryPublished struct {
	EntryID        string    `json:"entry_id"`
	TeamspaceID    string    `json:"teamspace_id"`
	OrganizationID string    `json:"organization_id"`
	AuthorUserID   string    `json:"author_user_id"`
	AuthorAgentID  string    `json:"author_agent_id"`
	Kind           string    `json:"kind"`
	PublishedAt    time.Time `json:"published_at"`
}

type OrganizationAgentSynthesisUpdated struct {
	OrganizationID    string    `json:"organization_id"`
	AgentID           string    `json:"agent_id"`
	Revision          int64     `json:"revision"`
	SourceEntryID     string    `json:"source_entry_id"`
	ProposedFollowUps int       `json:"proposed_follow_ups"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// SessionCreated announces a new agent session.
type SessionCreated struct {
	SessionID      string    `json:"session_id"`
	UserID         string    `json:"user_id"`
	OrganizationID string    `json:"organization_id,omitempty"`
	AppName        string    `json:"app_name"`
	CreatedAt      time.Time `json:"created_at"`
}

// SessionDeleted announces that a session and its events are gone.
type SessionDeleted struct {
	SessionID      string    `json:"session_id"`
	UserID         string    `json:"user_id"`
	OrganizationID string    `json:"organization_id,omitempty"`
	DeletedAt      time.Time `json:"deleted_at"`
}

// Usage is what one run cost in tokens, as the model reported it.
type Usage struct {
	InputTokens  int32 `json:"input_tokens"`
	OutputTokens int32 `json:"output_tokens"`
	// CachedTokens are the input tokens Vertex served from a cache, whether
	// explicit or implicit. They are part of InputTokens.
	CachedTokens int32 `json:"cached_tokens,omitempty"`
	TotalTokens  int32 `json:"total_tokens"`
}

// ModelPrices are the rates a run was priced at, in millionths of a US dollar
// per million tokens, so consumers can audit the cost.
type ModelPrices struct {
	Currency               string `json:"currency"`
	InputPerMillionMicros  int64  `json:"input_per_million_micros"`
	OutputPerMillionMicros int64  `json:"output_per_million_micros"`
	CachedPerMillionMicros int64  `json:"cached_per_million_micros"`
}

// Cost is what a run's usage cost, in millionths of a US dollar.
type Cost struct {
	InputMicros  int64 `json:"input_micros"`
	OutputMicros int64 `json:"output_micros"`
	CachedMicros int64 `json:"cached_micros"`
	TotalMicros  int64 `json:"total_micros"`
}

// Who started a run.
const (
	// StartedByMember is a member's own turn in a conversation.
	StartedByMember = "member"
	// StartedByTrigger is a run that events from the member's connected apps
	// woke, with the member not there.
	StartedByTrigger = "trigger"
	// StartedByTeam is a specialist run delegated by the member's primary
	// agent while coordinating one of their persistent teams.
	StartedByTeam = "team"
	// StartedByWorkflow is a specialist run dispatched by a member-owned,
	// durable workflow, either through the API or EventBridge Scheduler.
	StartedByWorkflow = "workflow"
	// StartedByOrganization is an isolated organization-agent synthesis of
	// content deliberately published into shared teamspace state.
	StartedByOrganization = "organization"
)

// ModelUsage is what one model spent within a run, priced at the rates it
// was billed at.
type ModelUsage struct {
	Model  string      `json:"model"`
	Usage  Usage       `json:"usage"`
	Cost   Cost        `json:"cost"`
	Prices ModelPrices `json:"prices"`
	// Priced is false for a model the price table does not know. Its Cost and
	// Prices are then zero rather than a guess, and the run's totals leave it
	// out of Cost while still counting its tokens in Usage.
	Priced bool `json:"priced"`
}

// RunCompleted announces a finished run with what it cost. A run calls more
// than one model — the conversation's own, and the lite model its planner,
// helpers and sub-agents run on — so Usage and Cost are totals across every
// model it called, and Models breaks them down.
type RunCompleted struct {
	RunID          string       `json:"run_id"`
	SessionID      string       `json:"session_id"`
	UserID         string       `json:"user_id"`
	OrganizationID string       `json:"organization_id,omitempty"`
	StartedBy      string       `json:"started_by"`
	Usage          Usage        `json:"usage"`
	Cost           Cost         `json:"cost"`
	Models         []ModelUsage `json:"models"`
	DurationMillis int64        `json:"duration_millis"`
	CompletedAt    time.Time    `json:"completed_at"`
}

// RunFailed announces a run that ended in an error. The tokens it spent
// before failing were still billed, so it carries them too.
type RunFailed struct {
	RunID          string       `json:"run_id"`
	SessionID      string       `json:"session_id"`
	UserID         string       `json:"user_id"`
	OrganizationID string       `json:"organization_id,omitempty"`
	StartedBy      string       `json:"started_by"`
	Error          string       `json:"error"`
	Usage          Usage        `json:"usage"`
	Cost           Cost         `json:"cost"`
	Models         []ModelUsage `json:"models"`
	DurationMillis int64        `json:"duration_millis"`
	FailedAt       time.Time    `json:"failed_at"`
}

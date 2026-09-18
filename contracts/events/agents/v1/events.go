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
)

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

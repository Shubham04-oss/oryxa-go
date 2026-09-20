// Package agentworkflows owns durable, member-scoped workflows that dispatch
// bounded assignments to persistent agent teams.
package agentworkflows

import (
	"encoding/json"
	"errors"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

var (
	ErrNotFound    = errors.New("agent workflow not found")
	ErrInvalid     = errors.New("invalid agent workflow")
	ErrConflict    = errors.New("agent workflow name is already in use")
	ErrDisabled    = errors.New("agent workflow is disabled")
	ErrUnavailable = errors.New("agent workflow orchestration is unavailable")
	ErrInProgress  = errors.New("agent workflow execution is already running")
)

type Scope = managedagents.Scope

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type ScheduleState string

const (
	ScheduleEnabled  ScheduleState = "enabled"
	ScheduleDisabled ScheduleState = "disabled"
)

type ExecutionStatus string

const (
	ExecutionQueued            ExecutionStatus = "queued"
	ExecutionRunning           ExecutionStatus = "running"
	ExecutionSucceeded         ExecutionStatus = "succeeded"
	ExecutionNeedsConfirmation ExecutionStatus = "needs_confirmation"
	ExecutionFailed            ExecutionStatus = "failed"
)

const (
	StartedByAPI      = "api"
	StartedBySchedule = "schedule"
)

type Assignment = primaryagents.SystemTeamAssignment

type Schedule struct {
	Expression string        `json:"expression"`
	Timezone   string        `json:"timezone"`
	State      ScheduleState `json:"state"`
}

type Workflow struct {
	ID             string       `json:"id"`
	OrganizationID string       `json:"organization_id"`
	OwnerUserID    string       `json:"owner_user_id"`
	Name           string       `json:"name"`
	Description    string       `json:"description,omitempty"`
	TeamID         string       `json:"team_id"`
	Status         Status       `json:"status"`
	Assignments    []Assignment `json:"assignments"`
	Schedule       *Schedule    `json:"schedule,omitempty"`
	Revision       int64        `json:"revision"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

type CreateInput struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	TeamID      string       `json:"team_id"`
	Assignments []Assignment `json:"assignments"`
	Schedule    *Schedule    `json:"schedule,omitempty"`
}

type UpdateInput struct {
	Name          *string       `json:"name,omitempty"`
	Description   *string       `json:"description,omitempty"`
	TeamID        *string       `json:"team_id,omitempty"`
	Status        *Status       `json:"status,omitempty"`
	Assignments   *[]Assignment `json:"assignments,omitempty"`
	Schedule      *Schedule     `json:"schedule,omitempty"`
	ClearSchedule bool          `json:"clear_schedule,omitempty"`
}

type Execution struct {
	ID             string          `json:"id"`
	WorkflowID     string          `json:"workflow_id"`
	OrganizationID string          `json:"organization_id"`
	OwnerUserID    string          `json:"owner_user_id"`
	StartedBy      string          `json:"started_by"`
	Status         ExecutionStatus `json:"status"`
	ExternalID     string          `json:"step_functions_execution_arn,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      time.Time       `json:"started_at,omitzero"`
	CompletedAt    time.Time       `json:"completed_at,omitzero"`
}

// Dispatch is the identifier-only payload a Step Functions execution sends
// through SQS. TaskToken is intentionally absent: the transport owns that
// callback capability and never persists or logs it with the workflow.
type Dispatch struct {
	ExecutionID    string `json:"execution_id,omitempty"`
	WorkflowID     string `json:"workflow_id"`
	OrganizationID string `json:"organization_id"`
	OwnerUserID    string `json:"owner_user_id"`
	StartedBy      string `json:"started_by"`
	ExternalID     string `json:"step_functions_execution_arn,omitempty"`
}

type StartRequest struct {
	ExecutionID    string `json:"execution_id"`
	WorkflowID     string `json:"workflow_id"`
	OrganizationID string `json:"organization_id"`
	OwnerUserID    string `json:"owner_user_id"`
	StartedBy      string `json:"started_by"`
}

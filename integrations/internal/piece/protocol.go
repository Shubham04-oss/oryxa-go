package piece

import (
	"context"
	"encoding/json"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

type TriggerInvocation struct {
	InvocationID   string
	OrganizationID string
	PieceName      string
	PieceVersion   string
	Operation      string
	TriggerName    string
	Auth           json.RawMessage
	Props          json.RawMessage
	WebhookURL     string
	PublicURL      string
	Payload        webhooks.Envelope
	InitialStore   map[string]any
}

type StoreMutation struct {
	Operation string `json:"operation"`
	Key       string `json:"key"`
	Value     any    `json:"value,omitempty"`
	Scope     string `json:"scope,omitempty"`
}

type Listener struct {
	Events          []string `json:"events"`
	IdentifierValue string   `json:"identifierValue"`
}

type TriggerResult struct {
	Output         json.RawMessage
	StoreMutations []StoreMutation
	Listeners      []Listener
}

type TriggerRunner interface {
	RunTrigger(context.Context, TriggerInvocation) (TriggerResult, error)
}

type ActionInvocation struct {
	InvocationID   string
	OrganizationID string
	PieceName      string
	PieceVersion   string
	ActionName     string
	Auth           json.RawMessage
	Props          json.RawMessage
	PublicURL      string
}

type ActionResult struct {
	Output json.RawMessage
}

type ActionRunner interface {
	RunAction(context.Context, ActionInvocation) (ActionResult, error)
}

// Package v1 contains the stable version-one events emitted by batching.
//
// Batching receives app trigger events, decides when they are ready to occupy
// an agent turn, and preserves each original event inside the released batch.
// Classification decides the lane; it does not rewrite the app's payload.
package v1

import (
	"encoding/json"
	"time"
)

const (
	Source = "oryxa.batching"

	TriggerBatchReadyDetailType = "batching.trigger-batch.ready.v1"
)

const (
	LaneDigest    = "digest"
	LaneImmediate = "immediate"
)

// MaxBatchDataBytes is the largest encoded event data batching will put in
// one EventBridge event. The remaining space is reserved for EventBridge and
// Oryxa's envelope. A batch that would exceed it is split without rewriting
// or truncating any individual trigger.
const MaxBatchDataBytes = 192 << 10

// Trigger is one original apps.trigger.received.v1 event. EventID is the
// original envelope id, not the id of the batch-ready event that carries it.
type Trigger struct {
	EventID        string          `json:"event_id"`
	TriggerID      string          `json:"trigger_id"`
	ConnectionID   string          `json:"connection_id"`
	Toolkit        string          `json:"toolkit"`
	Trigger        string          `json:"trigger"`
	Data           json.RawMessage `json:"data,omitempty"`
	DataOmitted    bool            `json:"data_omitted,omitempty"`
	ReceivedAt     time.Time       `json:"received_at"`
	Priority       float64         `json:"priority"`
	Classifier     string          `json:"classifier"`
	DecisionReason string          `json:"decision_reason,omitempty"`
	TargetSize     int             `json:"target_size"`
	Pressure       float64         `json:"pressure"`
	SoftDueAt      time.Time       `json:"soft_due_at"`
	HardDueAt      time.Time       `json:"hard_due_at"`
	PolicyVersion  string          `json:"policy_version"`
}

// TriggerBatchReady says a durable group of app triggers is ready for one
// agent turn. An immediate event is represented as a batch of one, so agents
// need only one input contract.
type TriggerBatchReady struct {
	BatchID         string    `json:"batch_id"`
	OrganizationID  string    `json:"organization_id"`
	MemberID        string    `json:"member_id"`
	Lane            string    `json:"lane"`
	EventCount      int       `json:"event_count"`
	TargetSize      int       `json:"target_size"`
	Pressure        float64   `json:"pressure"`
	FlushReason     string    `json:"flush_reason"`
	PolicyVersion   string    `json:"policy_version"`
	FirstReceivedAt time.Time `json:"first_received_at"`
	LastReceivedAt  time.Time `json:"last_received_at"`
	SoftDueAt       time.Time `json:"soft_due_at"`
	HardDueAt       time.Time `json:"hard_due_at"`
	ReadyAt         time.Time `json:"ready_at"`
	Triggers        []Trigger `json:"triggers"`
}

// Package domain contains batching's service-owned values.
package domain

import (
	"encoding/json"
	"errors"
	"time"
)

type Lane string

const (
	LaneDigest    Lane = "digest"
	LaneImmediate Lane = "immediate"
)

func (lane Lane) Valid() bool { return lane == LaneDigest || lane == LaneImmediate }

// Classification is the typed boundary implemented by urgency classifiers.
// Priority is a calibrated value from zero to one. The batching engine only
// acts on this result; it does not know how the decision was made.
type Classification struct {
	Lane       Lane
	Priority   float64
	Classifier string
	Reason     string
}

func (classification Classification) Validate() error {
	if !classification.Lane.Valid() {
		return errors.New("classification lane must be digest or immediate")
	}
	if classification.Priority < 0 || classification.Priority > 1 {
		return errors.New("classification priority must be between zero and one")
	}
	if classification.Classifier == "" {
		return errors.New("classification needs a classifier")
	}
	return nil
}

// Trigger is one app event waiting to be released.
type Trigger struct {
	ID             string
	TriggerID      string
	ConnectionID   string
	OrganizationID string
	MemberID       string
	Toolkit        string
	Trigger        string
	Data           json.RawMessage
	DataOmitted    bool
	ReceivedAt     time.Time
	Classification Classification
	Schedule       Schedule
}

// Batch is one member's triggers in one organization and lane.
type Batch struct {
	ID             string
	OrganizationID string
	MemberID       string
	Lane           Lane
	Triggers       []Trigger
	TargetSize     int
	Pressure       float64
	SoftDueAt      time.Time
	HardDueAt      time.Time
	PolicyVersion  string
	FlushReason    string
}

type Event struct {
	ID         string
	DetailType string
	Data       json.RawMessage
	OccurredAt time.Time
}

// Rule supplies the adaptive policy's cold-start target, hard cadence, and
// absolute batch-size bound. Classification may bypass it by choosing the
// immediate lane.
type Rule struct {
	Size    int
	MaxWait time.Duration
	Largest int
}

// Schedule is the immutable promise assigned to one digest trigger when it
// is recorded. Later traffic may make the group ready sooner, but may not
// move either deadline later.
type Schedule struct {
	TargetSize    int
	Pressure      float64
	SoftDueAt     time.Time
	HardDueAt     time.Time
	PolicyVersion string
}

// TrafficProfile is the small, payload-free history used to estimate a
// member's current and normal event rates.
type TrafficProfile struct {
	FastMass    float64
	SlowMass    float64
	ObservedAt  time.Time
	LastEventAt time.Time
	TotalEvents int64
}

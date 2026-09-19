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

// Admission is what batching does with a trigger event it receives.
type Admission int

const (
	// Admitted is a trigger batching keeps: new, and from nothing removed
	// since apps received it.
	Admitted Admission = iota
	// AlreadyRecorded is an event batching kept before and the bus, which
	// delivers at least once, delivered again.
	AlreadyRecorded
	// Forgotten is a trigger from a member, organization, connection or
	// trigger that was removed after apps received it. Nothing of it is kept.
	Forgotten
)

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
	// TraceContext is the outbox row's stored trace, carried through from
	// outbox.Event so Publisher can link its producer span to the write that
	// created this event. Code building an Event to record leaves it unset:
	// the write path captures the current trace itself (see store.go's
	// Complete), rather than trusting a caller to have filled this in.
	TraceContext string
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

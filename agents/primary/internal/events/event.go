// Package events publishes agents primary's events on EventBridge in the
// envelope every Oryxa publisher uses: event_id, version, occurred_at and
// data. Events are recorded in the outbox with the state they describe, and a
// dispatcher publishes them at least once.
package events

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Event is one event on its way to EventBridge.
type Event struct {
	ID         string
	DetailType string
	OccurredAt time.Time
	Data       json.RawMessage
	// TraceContext is the outbox row's stored trace, carried through from
	// outbox.Event so Publisher can link its producer span to the write that
	// created this event. NewEvent leaves it unset: the write path (Outbox.
	// RecordEvents, runstore.Store.Finish) captures the current trace itself,
	// rather than trusting a caller to have filled this in.
	TraceContext string
}

// NewEvent builds an event with a fresh id and the detail encoded, ready for
// the outbox.
func NewEvent(detailType string, data any, occurredAt time.Time) (Event, error) {
	if detailType == "" {
		return Event{}, errors.New("an event needs a detail type")
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	id, err := randomID()
	if err != nil {
		return Event{}, err
	}
	detail, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return Event{ID: id, DetailType: detailType, OccurredAt: occurredAt, Data: detail}, nil
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create event id: %w", err)
	}
	return "evt_" + base64.RawURLEncoding.EncodeToString(bytes), nil
}

package events

import (
	"context"
	"time"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// eventSink takes events; the Outbox implements it.
type eventSink interface {
	RecordEvents(context.Context, []Event) error
}

// SessionRecorder records session lifecycle events in the outbox. The session
// service calls it after the session exists or is gone, and the dispatcher
// publishes what it wrote.
type SessionRecorder struct {
	sink eventSink
}

func NewSessionRecorder(sink eventSink) *SessionRecorder {
	return &SessionRecorder{sink: sink}
}

// RecordSessionCreated records agents.session.created.v1.
func (recorder *SessionRecorder) RecordSessionCreated(ctx context.Context, appName, sessionID, userID, organizationID string) error {
	now := time.Now().UTC()
	return recorder.record(ctx, agentsv1.SessionCreatedDetailType, agentsv1.SessionCreated{
		SessionID:      sessionID,
		UserID:         userID,
		OrganizationID: organizationID,
		AppName:        appName,
		CreatedAt:      now,
	}, now)
}

// RecordSessionDeleted records agents.session.deleted.v1.
func (recorder *SessionRecorder) RecordSessionDeleted(ctx context.Context, appName, sessionID, userID, organizationID string) error {
	now := time.Now().UTC()
	return recorder.record(ctx, agentsv1.SessionDeletedDetailType, agentsv1.SessionDeleted{
		SessionID:      sessionID,
		UserID:         userID,
		OrganizationID: organizationID,
		DeletedAt:      now,
	}, now)
}

func (recorder *SessionRecorder) record(ctx context.Context, detailType string, data any, occurredAt time.Time) error {
	event, err := NewEvent(detailType, data, occurredAt)
	if err != nil {
		return err
	}
	return recorder.sink.RecordEvents(ctx, []Event{event})
}

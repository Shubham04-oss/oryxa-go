package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

type fakeSink struct {
	events []Event
	err    error
}

func (sink *fakeSink) RecordEvents(_ context.Context, events []Event) error {
	if sink.err != nil {
		return sink.err
	}
	sink.events = append(sink.events, events...)
	return nil
}

func TestSessionRecorderWritesTheLifecycleEvents(t *testing.T) {
	sink := &fakeSink{}
	recorder := NewSessionRecorder(sink)

	if err := recorder.RecordSessionCreated(context.Background(), "krude", "s_1", "u_1", "org_1"); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordSessionDeleted(context.Background(), "krude", "s_1", "u_1", "org_1"); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("%d events recorded, want 2", len(sink.events))
	}

	created := sink.events[0]
	if created.DetailType != agentsv1.SessionCreatedDetailType || created.ID == "" || created.OccurredAt.IsZero() {
		t.Fatalf("created event = %+v", created)
	}
	var payload agentsv1.SessionCreated
	if err := json.Unmarshal(created.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SessionID != "s_1" || payload.UserID != "u_1" || payload.OrganizationID != "org_1" || payload.AppName != "krude" || payload.CreatedAt.IsZero() {
		t.Fatalf("created payload = %+v", payload)
	}

	deleted := sink.events[1]
	if deleted.DetailType != agentsv1.SessionDeletedDetailType {
		t.Fatalf("deleted event = %+v", deleted)
	}
	var deletedPayload agentsv1.SessionDeleted
	if err := json.Unmarshal(deleted.Data, &deletedPayload); err != nil {
		t.Fatal(err)
	}
	if deletedPayload.SessionID != "s_1" || deletedPayload.UserID != "u_1" || deletedPayload.DeletedAt.IsZero() {
		t.Fatalf("deleted payload = %+v", deletedPayload)
	}
}

func TestSessionRecorderReportsSinkFailures(t *testing.T) {
	recorder := NewSessionRecorder(&fakeSink{err: errors.New("outbox down")})
	if err := recorder.RecordSessionCreated(context.Background(), "krude", "s_1", "u_1", ""); err == nil {
		t.Fatal("RecordSessionCreated() error = nil when the sink fails")
	}
}

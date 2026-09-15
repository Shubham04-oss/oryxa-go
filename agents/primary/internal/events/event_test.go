package events

import (
	"encoding/json"
	"testing"
	"time"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

func TestNewEventEncodesTheDetail(t *testing.T) {
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	event, err := NewEvent(agentsv1.SessionCreatedDetailType, agentsv1.SessionCreated{SessionID: "s_1", UserID: "u_1", CreatedAt: at}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(event.ID) <= len("evt_") || event.ID[:4] != "evt_" {
		t.Fatalf("event id = %q", event.ID)
	}
	var decoded agentsv1.SessionCreated
	if err := json.Unmarshal(event.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SessionID != "s_1" || decoded.UserID != "u_1" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if event.DetailType != agentsv1.SessionCreatedDetailType || !event.OccurredAt.Equal(at) {
		t.Fatalf("event = %+v", event)
	}
}

func TestNewEventRequiresADetailType(t *testing.T) {
	if _, err := NewEvent("", struct{}{}, time.Now()); err == nil {
		t.Fatal("NewEvent() error = nil without a detail type")
	}
}

func TestNewEventGivesEachEventItsOwnID(t *testing.T) {
	first, err := NewEvent("agents.test.v1", struct{}{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEvent("agents.test.v1", struct{}{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("two events share an id")
	}
}

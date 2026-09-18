package runstore

import (
	"context"
	"errors"
	"testing"
	"time"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// These tests cover only validation: every case here must fail before the
// store issues a query, so they run against postgreskit.Detached, whose pool
// never dials, rather than a real database.

// baseRun is a Run with every field Start requires filled in, so each test
// below can zero out just the one field it means to check.
func baseRun() Run {
	return Run{
		ID:             "run_1",
		SessionID:      "session_1",
		UserID:         "user_1",
		OrganizationID: "org_1",
		StartedBy:      agentsv1.StartedByMember,
	}
}

func mutateRun(run Run, fn func(*Run)) Run {
	fn(&run)
	return run
}

func wantPlainError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want a validation error")
	}
	if errors.Is(err, ErrRunInProgress) {
		t.Fatalf("error = %v, want a plain validation error, not ErrRunInProgress", err)
	}
	if errors.Is(err, ErrNotRunning) {
		t.Fatalf("error = %v, want a plain validation error, not ErrNotRunning", err)
	}
}

func TestStartValidatesTheRun(t *testing.T) {
	store := New(postgreskit.Detached(nil))
	ctx := context.Background()

	tests := []struct {
		name string
		run  Run
	}{
		{"empty id", mutateRun(baseRun(), func(r *Run) { r.ID = "" })},
		{"empty session id", mutateRun(baseRun(), func(r *Run) { r.SessionID = "" })},
		{"empty user id", mutateRun(baseRun(), func(r *Run) { r.UserID = "" })},
		{"empty organization id", mutateRun(baseRun(), func(r *Run) { r.OrganizationID = "" })},
		{"empty started by", mutateRun(baseRun(), func(r *Run) { r.StartedBy = "" })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantPlainError(t, store.Start(ctx, tt.run, time.Minute))
		})
	}
}

func TestStartValidatesTheLease(t *testing.T) {
	store := New(postgreskit.Detached(nil))
	ctx := context.Background()

	tests := []struct {
		name  string
		lease time.Duration
	}{
		{"zero lease", 0},
		{"negative lease", -time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantPlainError(t, store.Start(ctx, baseRun(), tt.lease))
		})
	}
}

// baseEvent is an event with every field Finish requires filled in, so each
// test below can zero out just the one field it means to check.
func baseEvent() events.Event {
	return events.Event{
		ID:         "evt_1",
		DetailType: agentsv1.RunCompletedDetailType,
		OccurredAt: time.Now().UTC(),
		Data:       []byte(`{}`),
	}
}

func mutateEvent(event events.Event, fn func(*events.Event)) events.Event {
	fn(&event)
	return event
}

func TestFinishValidatesTheOutcomeStatus(t *testing.T) {
	store := New(postgreskit.Detached(nil))
	ctx := context.Background()

	tests := []struct {
		name   string
		status string
	}{
		{"empty status", ""},
		{"running is not a final status", StatusRunning},
		{"abandoned is not an outcome Finish accepts", StatusAbandoned},
		{"unknown status", "bogus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := Outcome{Status: tt.status}
			wantPlainError(t, store.Finish(ctx, "run_1", outcome, baseEvent()))
		})
	}
}

func TestFinishValidatesTheEvent(t *testing.T) {
	store := New(postgreskit.Detached(nil))
	ctx := context.Background()

	tests := []struct {
		name  string
		event events.Event
	}{
		{"missing id", mutateEvent(baseEvent(), func(e *events.Event) { e.ID = "" })},
		{"missing detail type", mutateEvent(baseEvent(), func(e *events.Event) { e.DetailType = "" })},
		{"zero occurred at", mutateEvent(baseEvent(), func(e *events.Event) { e.OccurredAt = time.Time{} })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := Outcome{Status: StatusCompleted}
			wantPlainError(t, store.Finish(ctx, "run_1", outcome, tt.event))
		})
	}
}

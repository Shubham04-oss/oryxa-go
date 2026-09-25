package postgres

import (
	"strings"
	"testing"
)

// An event's id is derived from what it is about: the same each time, and
// apart for the Worker's events and auth's.
func TestEventIDsAreStableAndApart(t *testing.T) {
	if WorkerEventID("evt_1") != WorkerEventID("evt_1") || WorkerEventID("evt_1") == WorkerEventID("evt_2") ||
		WorkerEventID("evt_1") == PurgeCompletedEventID("evt_1") {
		t.Fatal("event ids are not stable, or collide")
	}
	if id := WorkerEventID("evt_1"); len(id) != len("evt_")+22 || !strings.HasPrefix(id, "evt_") {
		t.Fatalf("WorkerEventID() = %q, want evt_ and 22 characters", id)
	}
}

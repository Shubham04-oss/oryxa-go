package projector

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"
	"time"
)

func TestOrderingKeys(t *testing.T) {
	check, record := orderingKeys(Change{
		Delete: []Filter{
			{ResourceType: "team", ResourceID: "t1", Relation: "member", SubjectType: "member", SubjectID: "u1"},
			{ResourceType: "team", ResourceID: "t2"},
			{SubjectType: "org", SubjectID: "acme"},
			{ResourceType: "team"},
		},
		Touch: []Rel{rel("team", "t3", "lead", "member", "u2")},
	})
	wantCheck := []string{"team:t1@member:u1", "team:t2", "org:acme", "team:t3", "member:u2", "team:t3@member:u2"}
	wantRecord := []string{"team:t1@member:u1", "team:t2", "org:acme", "team:t3@member:u2"}
	if !slices.Equal(check, wantCheck) || !slices.Equal(record, wantRecord) {
		t.Fatalf("check = %v\nrecord = %v", check, record)
	}
}

// memorySeen is tokens.Seen's rule without the database.
type memorySeen map[string]time.Time

func (seen memorySeen) Guard(ctx context.Context, check, record []string, at time.Time, apply func(context.Context) error) (bool, error) {
	for _, key := range check {
		if seen[key].After(at) {
			return false, nil
		}
	}
	if err := apply(ctx); err != nil {
		return false, err
	}
	for _, key := range record {
		if at.After(seen[key]) {
			seen[key] = at
		}
	}
	return true, nil
}

func TestConsumerSkipsAnEventOlderThanOneApplied(t *testing.T) {
	writer := &fakeApplier{}
	consumer := &Consumer{writer: writer, grants: &fakeGrants{}, seen: memorySeen{}, logger: slog.New(slog.DiscardHandler)}
	added := func(json.RawMessage) (Change, error) {
		return Change{Touch: []Rel{rel("team", "t1", "member", "member", "u1")}}, nil
	}
	removed := func(json.RawMessage) (Change, error) {
		return Change{Delete: []Filter{{ResourceType: "team", ResourceID: "t1", Relation: "member", SubjectType: "member", SubjectID: "u1"}}}, nil
	}
	teamDeleted := func(json.RawMessage) (Change, error) {
		return Change{Delete: []Filter{{ResourceType: "team", ResourceID: "t2"}}}, nil
	}
	addedToT2 := func(json.RawMessage) (Change, error) {
		return Change{Touch: []Rel{rel("team", "t2", "member", "member", "u9")}}, nil
	}
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	// The removal (later) is delivered before the add (earlier): the add is stale.
	for _, step := range []struct {
		mapping mapper
		at      time.Time
	}{
		{removed, t0.Add(time.Minute)},
		{added, t0},
		{teamDeleted, t0.Add(time.Hour)},
		{addedToT2, t0.Add(time.Minute)}, // a member added before the team went
		{added, t0.Add(2 * time.Minute)}, // a newer add of the first member
		{added, t0.Add(2 * time.Minute)}, // and its duplicate delivery
	} {
		if err := consumer.project(ctx, "test", step.mapping, nil, step.at); err != nil {
			t.Fatal(err)
		}
	}
	if len(writer.changes) != 4 {
		t.Fatalf("applied %d changes, want 4 (removal, team deletion, newer add and its duplicate): %+v", len(writer.changes), writer.changes)
	}
	if len(writer.changes[0].Delete) != 1 || len(writer.changes[2].Touch) != 1 {
		t.Fatalf("wrong changes applied: %+v", writer.changes)
	}
}

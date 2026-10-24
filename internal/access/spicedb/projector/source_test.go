package projector

import (
	"slices"
	"testing"
)

// What knowledge writes as a source is added is what the reconcile expects of
// it, so neither undoes the other.
func TestSourceRegisteredMatchesTheReconcile(t *testing.T) {
	change, err := SourceRegistered("acme", "src_1", "con_1", "sam")
	if err != nil {
		t.Fatal(err)
	}
	expected := Expected(World{Sources: []Source{{ID: "src_1", Org: "acme", Connection: "con_1", CreatedBy: "sam"}}})
	if !slices.Equal(change.Touch, expected) || change.Organization != "acme" {
		t.Fatalf("registered %+v, reconcile expects %+v", change.Touch, expected)
	}
	if _, err := SourceRegistered("", "src_1", "", ""); err == nil {
		t.Fatal("a source with no organization was accepted")
	}
}

// A subteam is written with its parent, which the event can't give.
func TestTeamPlacedWritesTheParent(t *testing.T) {
	change, err := TeamPlaced("acme", "payments", "eng")
	if err != nil {
		t.Fatal(err)
	}
	want := []Rel{rel("team", "payments", "org", "org", "acme"), rel("team", "payments", "parent", "team", "eng")}
	if !slices.Equal(change.Touch, want) {
		t.Fatalf("touch = %+v", change.Touch)
	}
	if root, _ := TeamPlaced("acme", "eng", ""); len(root.Touch) != 1 {
		t.Fatalf("a root team: %+v", root.Touch)
	}
}

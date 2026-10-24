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

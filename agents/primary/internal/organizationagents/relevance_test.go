package organizationagents

import (
	"context"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

// TestChatterNeverReachesTheOrganizationAgent: a working group publishes the
// way people talk. What they deliberately publish as a decision, result,
// handoff or artifact is organization context; an update counts when it
// carries specifics or says enough to stand alone, and otherwise stays in the
// teamspace where it was written.
func TestChatterNeverReachesTheOrganizationAgent(t *testing.T) {
	cases := []struct {
		name     string
		entry    teamspaces.Entry
		relevant bool
	}{
		{"a decision always counts", teamspaces.Entry{Kind: teamspaces.EntryDecision, Summary: "We go with Northwind."}, true},
		{"a result always counts", teamspaces.Entry{Kind: teamspaces.EntryResult, Summary: "All samples passed."}, true},
		{"a handoff always counts", teamspaces.Entry{Kind: teamspaces.EntryHandoff, Summary: "Over to Nia."}, true},
		{"an artifact always counts", teamspaces.Entry{Kind: teamspaces.EntryKindArtifact, Summary: "Drawing attached."}, true},
		{"thanks does not", teamspaces.Entry{Kind: teamspaces.EntryUpdate, Summary: "Thanks for running those."}, false},
		{"agreement does not", teamspaces.Entry{Kind: teamspaces.EntryUpdate, Summary: "+1 to the later standup."}, false},
		{"the coffee machine does not", teamspaces.Entry{Kind: teamspaces.EntryUpdate, Summary: "The coffee machine on the third floor is broken again."}, false},
		{"an update with a date does", teamspaces.Entry{Kind: teamspaces.EntryUpdate, Summary: "Ship date slips to the 21st."}, true},
		{"an update that says enough does", teamspaces.Entry{Kind: teamspaces.EntryUpdate, Summary: strings.Repeat("the enclosure tooling needs rework before we can commit to a date ", 3)}, true},
	}
	for _, testCase := range cases {
		relevant, why, err := (ByShape{}).OrganizationRelevant(context.Background(), testCase.entry)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if relevant != testCase.relevant {
			t.Errorf("%s: relevant = %t, want %t (%q)", testCase.name, relevant, testCase.relevant, testCase.entry.Summary)
		}
		if !relevant && why == "" {
			t.Errorf("%s: nothing said about why it was left out", testCase.name)
		}
	}
}

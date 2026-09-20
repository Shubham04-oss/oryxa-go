package teamspaces

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// TestWhispererModeLongLoopBoundaries repeatedly changes the live membership
// and availability around five teammates. It protects the central whisperer
// contract: each primary acts only as its human, shares only attributed work,
// and disappears from collaboration immediately when access is withdrawn.
func TestWhispererModeLongLoopBoundaries(t *testing.T) {
	const rounds = 1_000
	ctx := context.Background()
	directory := fivePersonDirectory("")
	repo := &fakeRepository{spaces: []Space{{
		ID: "tsp_loop", OrganizationID: "org_loop", TeamID: "team_loop", Name: "Launch room", Status: StatusActive,
		OrganizationAgent: OrganizationAgent{
			ID: "oag_loop", OrganizationID: "org_loop", Name: "Atlas", Instructions: "Coordinate shared work.", Status: StatusActive,
		},
	}}}
	service, err := New(repo, fakeDirectory{directory: directory})
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	for round := 0; round < rounds; round++ {
		index := round % len(directory.People)
		person := directory.People[index]
		next := directory.People[(index+1)%len(directory.People)]
		member := Actor{OrganizationID: "org_loop", UserID: person.UserID, AccessToken: "member-token"}
		primary := person.Agents[0]

		entry, err := service.PublishEntry(ctx, member, "tsp_loop", PublishEntryInput{
			AuthorAgentID: primary.ID, Kind: EntryUpdate,
			Title: fmt.Sprintf("Round %d", round), Summary: "A deliberately shared, bounded update.",
			ToolsUsed: []string{"crm.read"},
		})
		if err != nil {
			t.Fatalf("round %d own publication: %v", round, err)
		}
		if entry.AuthorUserID != person.UserID || entry.AuthorAgentID != primary.ID {
			t.Fatalf("round %d attribution=%+v", round, entry)
		}

		if _, err := service.PublishEntry(ctx, member, "tsp_loop", PublishEntryInput{
			AuthorAgentID: next.Agents[0].ID, Kind: EntryUpdate, Title: "Spoof", Summary: "Must be rejected.",
		}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("round %d cross-user author error=%v", round, err)
		}

		space, err := service.Get(ctx, member, "tsp_loop")
		if err != nil || len(space.Whisperers) != len(directory.People) {
			t.Fatalf("round %d whisperers=%+v error=%v", round, space.Whisperers, err)
		}
		for _, whisperer := range space.Whisperers {
			if whisperer.AgentID == person.Agents[1].ID {
				t.Fatalf("round %d exposed private specialist as a whisperer", round)
			}
		}

		if round%25 == 0 {
			withdrawn, _ := New(repo, fakeDirectory{directory: fivePersonDirectory(person.UserID)})
			spaces, err := withdrawn.List(ctx, member)
			if err != nil || len(spaces) != 0 {
				t.Fatalf("round %d withdrawn list=%+v error=%v", round, spaces, err)
			}
			if _, err := withdrawn.Get(ctx, member, "tsp_loop"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("round %d withdrawn get error=%v", round, err)
			}
			agentActor := primaryagents.TeamspaceActor{
				OrganizationID: "org_loop", UserID: person.UserID, AgentID: primary.ID, AgentName: primary.Name, AgentKind: "primary",
			}
			collaboration, err := withdrawn.CollaborationSpaces(ctx, agentActor, "member-token")
			if err != nil || len(collaboration) != 0 {
				t.Fatalf("round %d withdrawn collaboration=%+v error=%v", round, collaboration, err)
			}
		}

		if round%40 == 0 {
			repo.spaces[0].Status = StatusDisabled
			agentActor := primaryagents.TeamspaceActor{
				OrganizationID: "org_loop", UserID: person.UserID, AgentID: primary.ID, AgentName: primary.Name, AgentKind: "primary",
			}
			collaboration, err := service.CollaborationSpaces(ctx, agentActor, "member-token")
			if err != nil || len(collaboration) != 0 {
				t.Fatalf("round %d disabled collaboration=%+v error=%v", round, collaboration, err)
			}
			if _, err := service.CollaborationPublish(ctx, agentActor, "member-token", "tsp_loop", primaryagents.SharedEntryInput{
				Kind: "update", Title: "Disabled", Summary: "Must not publish.",
			}); !errors.Is(err, ErrDisabled) {
				t.Fatalf("round %d disabled publish error=%v", round, err)
			}
			repo.spaces[0].Status = StatusActive
		}
	}

	if len(repo.entries) != rounds {
		t.Fatalf("published entries=%d, want %d", len(repo.entries), rounds)
	}
	t.Logf("completed %d five-user whisperer rounds (%d authorization/publication assertions) in %s",
		rounds, rounds*8, time.Since(started).Round(time.Millisecond))
}

func fivePersonDirectory(withdrawnUserID string) primaryagents.OrganizationDirectory {
	people := make([]primaryagents.DirectoryPerson, 0, 5)
	for index, name := range []string{"Ada", "Grace", "Lin", "Maya", "Noor"} {
		userID := fmt.Sprintf("user_%d", index+1)
		teams := []primaryagents.DirectoryTeamRef{{ID: "team_loop", Name: "Launch", Role: "member"}}
		if userID == withdrawnUserID {
			teams = []primaryagents.DirectoryTeamRef{}
		}
		people = append(people, primaryagents.DirectoryPerson{
			UserID: userID, DisplayName: name, OrgRole: "member", Teams: teams,
			Agents: []primaryagents.DirectoryAgent{
				{ID: fmt.Sprintf("agt_primary_%d", index+1), OwnerUserID: userID, Kind: "primary", Name: name + " Primary"},
				{ID: fmt.Sprintf("agt_private_%d", index+1), OwnerUserID: userID, Kind: "specialist", Name: name + " Private"},
			},
		})
	}
	return primaryagents.OrganizationDirectory{
		Organization: primaryagents.DirectoryOrganization{ID: "org_loop", Name: "Loop Org"},
		Teams:        []primaryagents.DirectoryTeam{{ID: "team_loop", Name: "Launch", CanManage: false}},
		People:       people,
	}
}

package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestToolCatalogIsTheThreeOptions(t *testing.T) {
	tools := ToolCatalog()
	if len(tools) != 3 || tools[0].Name != ToolListTeams || tools[1].Name != ToolCallTeam || tools[2].Name != ToolCallTeamMembers {
		t.Fatalf("tools = %v", names(tools))
	}
	for _, tool := range tools {
		if tool.Description == "" || !json.Valid(tool.Parameters) {
			t.Fatalf("tool = %+v", tool)
		}
	}
}

func TestResolveTeamPrefersNameThenPathThenID(t *testing.T) {
	teams := []Team{
		{ID: "team_1", Name: "Engineering", Path: "org_1.team_1"},
		{ID: "team_2", Name: "Design", Path: "org_1.team_2"},
	}
	got, err := ResolveTeam(teams, "engineering")
	if err != nil || got.ID != "team_1" {
		t.Fatalf("by name = %+v, err = %v", got, err)
	}
	got, err = ResolveTeam(teams, "org_1.team_2")
	if err != nil || got.ID != "team_2" {
		t.Fatalf("by path = %+v, err = %v", got, err)
	}
	got, err = ResolveTeam(teams, "team_1")
	if err != nil || got.Name != "Engineering" {
		t.Fatalf("by id = %+v, err = %v", got, err)
	}
	if _, err := ResolveTeam(teams, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v, want not found", err)
	}
	if _, err := ResolveTeam(teams, "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty error = %v, want invalid", err)
	}
}

func TestMembersOfIncludesUIDAndEmail(t *testing.T) {
	got := MembersOf(
		Team{ID: "team_1", Name: "Engineering"},
		[]Membership{
			{UserID: "user-2", Email: "ben@example.com", Role: RoleMember},
			{UserID: "user-1", Email: "asha@example.com", DisplayName: "Asha", Role: RoleAdmin},
		},
	)
	if got.TeamID != "team_1" || got.Name != "Engineering" || len(got.Members) != 2 {
		t.Fatalf("members = %+v", got)
	}
	if got.Members[0].UserID != "user-1" || got.Members[0].Email != "asha@example.com" || got.Members[0].Role != RoleAdmin {
		t.Fatalf("admin first = %+v", got.Members)
	}
	if got.Members[1].UserID != "user-2" || got.Members[1].Email != "ben@example.com" {
		t.Fatalf("member = %+v", got.Members[1])
	}
}

func names(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

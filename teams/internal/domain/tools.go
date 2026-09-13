package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const (
	ToolListTeams       = "list_teams"
	ToolCallTeam        = "call_team"
	ToolCallTeamMembers = "call_team_members"
)

var (
	emptyToolParameters = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	teamQueryParameters = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Team name first. Path or team id if the name is not what they said."}},"required":["query"],"additionalProperties":false}`)
)

// Tool is one callable the catalog exposes to an AI.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolMember is who is on a team: uid, email, and role.
type ToolMember struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Role        Role   `json:"role"`
	NotifyEmail bool   `json:"notify_email"`
}

// TeamHandle is a team without its roster — list_teams and call_team.
type TeamHandle struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id,omitempty"`
	Name           string `json:"name"`
	Path           string `json:"path,omitempty"`
}

// TeamMembers is call_team_members: the team and who is on it.
type TeamMembers struct {
	TeamID  string       `json:"team_id"`
	Name    string       `json:"name"`
	Members []ToolMember `json:"members"`
}

func ListTeamsTool() Tool {
	return Tool{
		Name:        ToolListTeams,
		Description: "List teams the caller can see (id, name, path). Use this to discover teams. To open one team, call call_team. To get its people, call call_team_members.",
		Parameters:  emptyToolParameters,
	}
}

func CallTeamTool() Tool {
	return Tool{
		Name:        ToolCallTeam,
		Description: "Call one team the caller can see. Returns that team (id, organization_id, name, path), not its members. Pass the name they used. Lookup order is name, then path, then team id. For the roster, use call_team_members.",
		Parameters:  teamQueryParameters,
	}
}

func CallTeamMembersTool() Tool {
	return Tool{
		Name:        ToolCallTeamMembers,
		Description: "Call the members of one team the caller can see. Returns user_id, email, display_name, and role. Pass the team name they used. Lookup order is name, then path, then team id.",
		Parameters:  teamQueryParameters,
	}
}

// ToolCatalog is the three tools: list teams, call a team, call its members.
func ToolCatalog() []Tool {
	return []Tool{ListTeamsTool(), CallTeamTool(), CallTeamMembersTool()}
}

func TeamHandles(teams []Team) []TeamHandle {
	sorted := slices.Clone(teams)
	sortTeams(sorted)
	handles := make([]TeamHandle, 0, len(sorted))
	for _, team := range sorted {
		handles = append(handles, TeamAsHandle(team))
	}
	return handles
}

func TeamAsHandle(team Team) TeamHandle {
	return TeamHandle{
		ID:             team.ID,
		OrganizationID: team.OrganizationID,
		Name:           team.Name,
		Path:           team.Path,
	}
}

func MembersOf(team Team, members []Membership) TeamMembers {
	reached := TeamMembers{
		TeamID:  team.ID,
		Name:    team.Name,
		Members: make([]ToolMember, 0, len(members)),
	}
	sorted := slices.Clone(members)
	slices.SortFunc(sorted, func(a, b Membership) int {
		if a.Role != b.Role {
			if a.Role == RoleAdmin {
				return -1
			}
			if b.Role == RoleAdmin {
				return 1
			}
		}
		if n := strings.Compare(strings.ToLower(a.Email), strings.ToLower(b.Email)); n != 0 {
			return n
		}
		return strings.Compare(a.UserID, b.UserID)
	})
	for _, member := range sorted {
		reached.Members = append(reached.Members, ToolMember{
			UserID:      member.UserID,
			Email:       member.Email,
			DisplayName: member.DisplayName,
			Role:        member.Role,
			NotifyEmail: member.NotifyEmail,
		})
	}
	return reached
}

// ResolveTeam finds one team among those the caller can see. Order is name,
// then path, then id — a name is what someone says; the id is last because
// they usually do not have it.
func ResolveTeam(teams []Team, query string) (Team, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Team{}, fmt.Errorf("query is required: %w", ErrInvalid)
	}

	var named []Team
	for _, team := range teams {
		if strings.EqualFold(team.Name, query) {
			named = append(named, team)
		}
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
	default:
		return Team{}, ErrNotFound
	}

	for _, team := range teams {
		if team.Path == query {
			return team, nil
		}
	}
	for _, team := range teams {
		if team.ID == query {
			return team, nil
		}
	}
	return Team{}, ErrNotFound
}

func sortTeams(teams []Team) {
	slices.SortFunc(teams, func(a, b Team) int {
		if n := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
}

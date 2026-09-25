package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

func TestToolsAreTheThreeOptions(t *testing.T) {
	service, _, _ := newService()
	tools, err := service.Tools(context.Background(), creds("user-member"), "org_1")
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if len(tools) != 3 || !hasTool(tools, domain.ToolListTeams) || !hasTool(tools, domain.ToolCallTeam) || !hasTool(tools, domain.ToolCallTeamMembers) {
		t.Fatalf("tools = %v", toolNames(tools))
	}
}

func TestCallTeamAndCallTeamMembersAndList(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", team.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}

	listed, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolListTeams, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_teams error = %v", err)
	}
	handles := listed.([]domain.TeamHandle)
	if len(handles) != 1 || handles[0].ID != team.ID || handles[0].Name != "Engineering" {
		t.Fatalf("list_teams = %+v", listed)
	}

	called, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeam, json.RawMessage(`{"query":"engineering"}`))
	if err != nil {
		t.Fatalf("call_team error = %v", err)
	}
	handle := called.(domain.TeamHandle)
	if handle.ID != team.ID || handle.Name != "Engineering" || handle.OrganizationID != "org_1" {
		t.Fatalf("call_team = %+v", called)
	}

	members, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeamMembers, json.RawMessage(`{"query":"`+team.ID+`"}`))
	if err != nil {
		t.Fatalf("call_team_members error = %v", err)
	}
	roster := members.(domain.TeamMembers)
	if roster.TeamID != team.ID || roster.Name != "Engineering" || len(roster.Members) != 2 {
		t.Fatalf("call_team_members = %+v", members)
	}
	if !hasMember(roster.Members, "user-owner", "owner@example.com") || !hasMember(roster.Members, "user-member", "member@example.com") {
		t.Fatalf("members = %+v", roster.Members)
	}
	if !roster.Members[0].NotifyEmail && !roster.Members[1].NotifyEmail {
		t.Fatalf("new members should have notify_email on: %+v", roster.Members)
	}

	if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", team.ID, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown tool error = %v, want not found", err)
	}
}

func TestCallToolPingsEveryMemberOnEventBridge(t *testing.T) {
	service, store, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", team.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	before := len(store.events)

	if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeam, json.RawMessage(`{"query":"Engineering"}`)); err != nil {
		t.Fatal(err)
	}
	called := store.events[before:]
	if countDetail(called, teamseventsv1.TeamCalledDetailType) != 1 || countDetail(called, teamseventsv1.MemberPingedDetailType) != 2 {
		t.Fatalf("call_team events = %v", detailTypes(called))
	}
	if !pinged(called, "user-owner", "owner@example.com") || !pinged(called, "user-member", "member@example.com") {
		t.Fatalf("call_team pings missing in %+v", called)
	}

	before = len(store.events)
	if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeamMembers, json.RawMessage(`{"query":"Engineering"}`)); err != nil {
		t.Fatal(err)
	}
	membersCalled := store.events[before:]
	if countDetail(membersCalled, teamseventsv1.TeamMembersCalledDetailType) != 1 || countDetail(membersCalled, teamseventsv1.MemberPingedDetailType) != 2 {
		t.Fatalf("call_team_members events = %v", detailTypes(membersCalled))
	}

	before = len(store.events)
	if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolListTeams, nil); err != nil {
		t.Fatal(err)
	}
	listed := store.events[before:]
	if countDetail(listed, teamseventsv1.TeamsListedDetailType) != 1 || countDetail(listed, teamseventsv1.MemberPingedDetailType) != 2 {
		t.Fatalf("list_teams events = %v", detailTypes(listed))
	}
}

func TestSetNotificationIsTheMembersOwnSwitch(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", team.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}

	updated, err := service.SetNotification(ctx, creds("user-member"), "org_1", team.ID, false)
	if err != nil || updated.NotifyEmail || updated.UserID != "user-member" {
		t.Fatalf("SetNotification() = %+v, err = %v", updated, err)
	}
	membership, err := service.Members(ctx, creds("user-member"), "org_1", team.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range membership {
		if member.UserID == "user-member" && member.NotifyEmail {
			t.Fatalf("member switch still on: %+v", member)
		}
		if member.UserID == "user-owner" && !member.NotifyEmail {
			t.Fatalf("owner switch should stay on: %+v", member)
		}
	}
	if _, err := service.SetNotification(ctx, creds("user-outsider"), "org_1", team.ID, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("outsider SetNotification() error = %v, want not found", err)
	}
}

// TestCallToolAsAnAdminsAgentReachesOnlyItsMembersTeams: list_teams and
// call_team are the tools an AI actually calls, so an admin's agent must not
// get the admin's org-wide reach through them either — it lists and calls
// only the teams its member genuinely belongs to.
func TestCallToolAsAnAdminsAgentReachesOnlyItsMembersTeams(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	alpha, err := service.CreateTeam(ctx, creds("user-admin"), "org_1", CreateTeamInput{Name: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Gamma"}); err != nil {
		t.Fatal(err)
	}

	listed, err := service.CallTool(ctx, agentCreds("user-admin"), "org_1", domain.ToolListTeams, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list_teams error = %v", err)
	}
	if handles := listed.([]domain.TeamHandle); len(handles) != 1 || handles[0].ID != alpha.ID {
		t.Fatalf("admin's agent list_teams = %+v, want only Alpha", handles)
	}

	if _, err := service.CallTool(ctx, agentCreds("user-admin"), "org_1", domain.ToolCallTeam, json.RawMessage(`{"query":"Gamma"}`)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("admin's agent call_team(Gamma) error = %v, want not found", err)
	}
	called, err := service.CallTool(ctx, agentCreds("user-admin"), "org_1", domain.ToolCallTeam, json.RawMessage(`{"query":"Alpha"}`))
	if err != nil || called.(domain.TeamHandle).ID != alpha.ID {
		t.Fatalf("admin's agent call_team(Alpha) = %+v, err = %v", called, err)
	}
}

func TestCallToolConcealsTeamsTheCallerCannotSee(t *testing.T) {
	service, store, _ := newService()
	ctx := context.Background()
	if _, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CallTool(ctx, creds("user-member"), "org_1", domain.ToolCallTeam, json.RawMessage(`{"query":"Design"}`)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("hidden call_team error = %v, want not found", err)
	}
	if _, err := service.CallTool(ctx, creds("user-member"), "org_1", domain.ToolCallTeamMembers, json.RawMessage(`{"query":"Design"}`)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("hidden call_team_members error = %v, want not found", err)
	}
	listed, err := service.CallTool(ctx, creds("user-member"), "org_1", domain.ToolListTeams, nil)
	if err != nil {
		t.Fatal(err)
	}
	if handles := listed.([]domain.TeamHandle); len(handles) != 0 {
		t.Fatalf("list_teams = %+v, want empty", handles)
	}
	if countDetail(store.events, teamseventsv1.TeamCalledDetailType) != 0 || countDetail(store.events, teamseventsv1.TeamMembersCalledDetailType) != 0 || countDetail(store.events, teamseventsv1.MemberPingedDetailType) != 0 {
		t.Fatalf("hidden calls still published: %v", store.detailTypes())
	}
}

func hasTool(tools []domain.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func toolNames(tools []domain.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func countDetail(events []domain.Event, detailType string) int {
	n := 0
	for _, event := range events {
		if event.DetailType == detailType {
			n++
		}
	}
	return n
}

func detailTypes(events []domain.Event) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.DetailType)
	}
	return types
}

func pinged(events []domain.Event, userID, email string) bool {
	for _, event := range events {
		if event.DetailType != teamseventsv1.MemberPingedDetailType {
			continue
		}
		if strings.Contains(string(event.Data), `"`+userID+`"`) && strings.Contains(string(event.Data), `"`+email+`"`) {
			return true
		}
	}
	return false
}

func hasMember(members []domain.ToolMember, userID, email string) bool {
	for _, member := range members {
		if member.UserID == userID && member.Email == email {
			return true
		}
	}
	return false
}

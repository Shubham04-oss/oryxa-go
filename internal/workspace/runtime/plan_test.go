package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestApplyResumesAfterInvitationAcceptance(t *testing.T) {
	state := &fakeState{
		orgExists: true,
		domains:   []string{"acme.com"},
		members:   []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}},
	}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme", Domains: []string{"acme.com"}},
		People:   &spec.People{Members: []spec.Member{{Email: "new@acme.com"}}},
		Teams:    spec.Teams{Items: []spec.Team{{Name: "Engineering", Members: []string{"owner@acme.com", "new@acme.com"}}}},
	}
	first, err := Apply(context.Background(), client, document)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.NeedsHuman) == 0 || state.invites != 1 || state.teamCreates != 1 || state.adds != 0 {
		t.Fatalf("first apply = %+v; counts = %d/%d/%d", first, state.invites, state.teamCreates, state.adds)
	}
	if _, err := Apply(context.Background(), client, document); err != nil {
		t.Fatal(err)
	}
	if state.invites != 1 || state.teamCreates != 1 {
		t.Fatal("repeated apply created resources again")
	}
	state.members = append(state.members, Member{UserID: "u2", Email: "new@acme.com", Role: "member"})
	final, err := Apply(context.Background(), client, document)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Changes) != 0 || len(final.NeedsHuman) != 0 || state.adds != 1 {
		t.Fatalf("resumed apply = %+v; adds = %d", final, state.adds)
	}
	if _, err := Apply(context.Background(), client, document); err != nil {
		t.Fatal(err)
	}
	if state.adds != 1 {
		t.Fatal("team member was added twice")
	}
}

func TestApplyRequestsQuietInvitations(t *testing.T) {
	state := &fakeState{orgExists: true, members: []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}}}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme"},
		People:   &spec.People{Invite: "quiet", Members: []spec.Member{{Email: "new@acme.com"}}},
	}
	if _, err := Apply(context.Background(), client, document); err != nil {
		t.Fatal(err)
	}
	if state.invites != 1 || !state.lastInviteQuiet {
		t.Fatalf("invitations = %d, quiet = %v", state.invites, state.lastInviteQuiet)
	}
}

func TestApplyOrganizationDomainTimezoneAndGuestPolicy(t *testing.T) {
	state := &fakeState{orgExists: true, domains: []string{"acme.com"}, timezone: "UTC", members: []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}}}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	timezone, guests := "Asia/Kolkata", true
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata:     spec.Metadata{Organization: "acme", Domains: []string{"acme.com", "example.com"}},
		Organization: &spec.Organization{Timezone: &timezone}, People: &spec.People{Guests: &guests},
	}
	remaining, err := Apply(context.Background(), client, document)
	if err != nil || len(remaining.Changes) != 0 || state.timezone != timezone || !state.guestsAllowed || len(state.domains) != 2 {
		t.Fatalf("apply = %+v, %v; state = %+v", remaining, err, state)
	}
	if _, err := Apply(context.Background(), client, document); err != nil {
		t.Fatal(err)
	}
}

func TestGuestInvitationNeedsOrganizationGuestPolicy(t *testing.T) {
	state := Snapshot{Organization: &Organization{ID: "org_1", Slug: "acme", Domains: []string{"acme.com"}},
		Members: map[string]Member{}, Invitations: map[string]Invitation{}, Teams: map[string]Team{}, TeamMembers: map[string]map[string]TeamMember{}}
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme", Domains: []string{"acme.com"}},
		People:   &spec.People{Members: []spec.Member{{Email: "guest@example.com", Guest: true}}},
	}
	if _, err := BuildPlan(document, state); err == nil || !strings.Contains(err.Error(), "people.guests") {
		t.Fatalf("guest invitation without policy = %v", err)
	}
	allow := true
	document.People.Guests = &allow
	if _, err := BuildPlan(document, state); err != nil {
		t.Fatalf("guest invitation with policy = %v", err)
	}
}

func TestApplyTeamLeadPromotesExistingMember(t *testing.T) {
	state := &fakeState{orgExists: true,
		members:       []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}, {UserID: "u2", Email: "lead@acme.com", Role: "member"}},
		existingTeams: []Team{{ID: "team_1", Name: "Engineering", Path: "engineering"}},
		teamMembers:   []TeamMember{{UserID: "u1", Email: "owner@acme.com", Role: "admin"}, {UserID: "u2", Email: "lead@acme.com", Role: "member"}},
	}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"},
		Teams: spec.Teams{Items: []spec.Team{{Name: "Engineering", Leads: []string{"lead@acme.com"}}}},
	}
	remaining, err := Apply(context.Background(), client, document)
	if err != nil || len(remaining.Changes) != 0 || state.roleChanges != 1 || state.teamMembers[1].Role != "admin" {
		t.Fatalf("lead apply = %+v, %v, role changes %d", remaining, err, state.roleChanges)
	}
	if _, err := Apply(context.Background(), client, document); err != nil || state.roleChanges != 1 {
		t.Fatalf("repeated lead apply = %v, changes %d", err, state.roleChanges)
	}
}

func TestTeamsPruneDemotesOmittedLeadOnlyAfterApproval(t *testing.T) {
	state := &fakeState{orgExists: true, teamCreates: 1,
		members:     []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}, {UserID: "u2", Email: "lead@acme.com", Role: "member"}},
		teamMembers: []TeamMember{{UserID: "u1", Email: "owner@acme.com", Role: "admin"}, {UserID: "u2", Email: "lead@acme.com", Role: "admin"}},
	}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"},
		Teams: spec.Teams{Items: []spec.Team{{Name: "Engineering", Members: []string{"owner@acme.com", "lead@acme.com"}, Leads: []string{"owner@acme.com"}}}},
	}
	snapshot, err := client.Snapshot(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	withoutPrune, err := BuildPlan(document, snapshot)
	if err != nil || len(withoutPrune.Changes) != 0 {
		t.Fatalf("additive plan = %+v, %v", withoutPrune, err)
	}
	document.Teams.Prune = true
	approved, err := BuildPlan(document, snapshot)
	if err != nil || len(deletions(approved)) != 1 || deletions(approved)[0].Kind != "team_admin" {
		t.Fatalf("prune plan = %+v, %v", approved, err)
	}
	if _, err := Apply(context.Background(), client, document); err == nil || state.roleChanges != 0 {
		t.Fatalf("unapproved demotion = %v, role changes %d", err, state.roleChanges)
	}
	remaining, err := ApplyApproved(context.Background(), client, document, approved)
	if err != nil || len(remaining.Changes) != 0 || state.roleChanges != 1 || state.teamMembers[1].Role != "member" {
		t.Fatalf("approved demotion = %+v, %v; role changes %d, member %+v", remaining, err, state.roleChanges, state.teamMembers[1])
	}
}

func TestTeamsPruneWaitsForReplacementLeadToJoin(t *testing.T) {
	state := Snapshot{
		Organization: &Organization{ID: "org_1", Slug: "acme"}, ActorID: "u1", ActorRole: "owner",
		Members:     map[string]Member{"owner@acme.com": {UserID: "u1", Email: "owner@acme.com", Role: "owner"}},
		Invitations: map[string]Invitation{"new@acme.com": {Email: "new@acme.com", Role: "member", Status: "pending"}},
		Teams:       map[string]Team{"Engineering": {ID: "team_1", Name: "Engineering", Path: "engineering"}},
		TeamMembers: map[string]map[string]TeamMember{"Engineering": {
			"owner@acme.com": {UserID: "u1", Email: "owner@acme.com", Role: "admin"},
		}},
	}
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"},
		Teams: spec.Teams{Prune: true, Items: []spec.Team{{Name: "Engineering", Members: []string{"owner@acme.com", "new@acme.com"}, Leads: []string{"new@acme.com"}}}},
	}
	plan, err := BuildPlan(document, state)
	if err != nil || len(deletions(plan)) != 0 || len(plan.NeedsHuman) == 0 {
		t.Fatalf("pending lead plan = %+v, %v", plan, err)
	}
}

func TestPeoplePruneNeedsExactApprovedRemovalPlan(t *testing.T) {
	state := &fakeState{
		orgExists: true,
		members: []Member{
			{UserID: "u1", Email: "owner@acme.com", Role: "owner"},
			{UserID: "u2", Email: "old@acme.com", Role: "member"},
		},
		otherInvitations: []Invitation{{ID: "inv_old", Email: "pending@acme.com", Role: "member", Status: "pending"}},
	}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme"},
		People:   &spec.People{Prune: true, Members: []spec.Member{{Email: "owner@acme.com", Role: "owner"}}},
	}
	snapshot, err := client.Snapshot(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := BuildPlan(document, snapshot)
	if err != nil || len(deletions(approved)) != 2 {
		t.Fatalf("plan = %+v, %v", approved, err)
	}
	if _, err := Apply(context.Background(), client, document); err == nil || state.removedMembers != 0 {
		t.Fatalf("unapproved prune error = %v; removals = %d", err, state.removedMembers)
	}
	remaining, err := ApplyApproved(context.Background(), client, document, approved)
	if err != nil || state.removedMembers != 1 || state.revokedInvitations != 1 || len(remaining.Changes) != 0 {
		t.Fatalf("approved prune = %+v, %v; removals = %d/%d", remaining, err, state.removedMembers, state.revokedInvitations)
	}
}

func TestPeoplePruneCannotRemoveSignedInMember(t *testing.T) {
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme"},
		People:   &spec.People{Prune: true, Members: []spec.Member{{Email: "other@acme.com", Role: "owner"}}},
	}
	state := Snapshot{
		Organization: &Organization{ID: "org_1", Slug: "acme"}, ActorID: "u1", ActorRole: "owner",
		Members: map[string]Member{
			"owner@acme.com": {UserID: "u1", Email: "owner@acme.com", Role: "owner"},
			"other@acme.com": {UserID: "u2", Email: "other@acme.com", Role: "owner"},
		},
	}
	if _, err := BuildPlan(document, state); err == nil || !strings.Contains(err.Error(), "signed-in member") {
		t.Fatalf("plan error = %v", err)
	}
}

func TestRemovalApprovalIsBoundToStableResourcesAndRoles(t *testing.T) {
	for _, scenario := range []string{"member replacement", "member promotion", "invitation replacement", "team replacement", "team member replacement", "team admin replacement", "organization replacement"} {
		t.Run(scenario, func(t *testing.T) {
			state := Snapshot{
				Organization: &Organization{ID: "org_1", Slug: "acme"}, ActorID: "u1", ActorRole: "owner",
				Members: map[string]Member{
					"owner@acme.com": {UserID: "u1", Email: "owner@acme.com", Role: "owner"},
					"old@acme.com":   {UserID: "u2", Email: "old@acme.com", Role: "member"},
				},
				Invitations: map[string]Invitation{"pending@acme.com": {ID: "inv_1", Email: "pending@acme.com", Role: "member", Status: "pending"}},
				Teams:       map[string]Team{"Engineering": {ID: "team_1", Name: "Engineering", Path: "engineering"}},
				TeamMembers: map[string]map[string]TeamMember{"Engineering": {
					"owner@acme.com": {UserID: "u1", Email: "owner@acme.com", Role: "admin"},
					"old@acme.com":   {UserID: "u2", Email: "old@acme.com", Role: "admin"},
				}},
			}
			document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"},
				People: &spec.People{Prune: true, Members: []spec.Member{{Email: "owner@acme.com", Role: "owner"}}},
				Teams:  spec.Teams{Prune: true},
			}
			if strings.HasPrefix(scenario, "team member") || strings.HasPrefix(scenario, "team admin") {
				document.Teams.Items = []spec.Team{{Name: "Engineering", Members: []string{"owner@acme.com"}, Leads: []string{"owner@acme.com"}}}
				if scenario == "team admin replacement" {
					document.Teams.Items[0].Members = append(document.Teams.Items[0].Members, "old@acme.com")
					document.People = nil
				}
			}
			approved, err := BuildPlan(document, state)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyApprovedRemovals(approved, &approved); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "member replacement":
				member := state.Members["old@acme.com"]
				member.UserID = "replacement"
				state.Members["old@acme.com"] = member
			case "member promotion":
				member := state.Members["old@acme.com"]
				member.Role = "owner"
				state.Members["old@acme.com"] = member
			case "invitation replacement":
				invite := state.Invitations["pending@acme.com"]
				invite.ID = "replacement"
				state.Invitations["pending@acme.com"] = invite
			case "team replacement":
				team := state.Teams["Engineering"]
				team.ID = "replacement"
				state.Teams["Engineering"] = team
			case "team member replacement", "team admin replacement":
				member := state.TeamMembers["Engineering"]["old@acme.com"]
				member.UserID = "replacement"
				state.TeamMembers["Engineering"]["old@acme.com"] = member
			case "organization replacement":
				state.Organization.ID = "replacement"
			}
			current, err := BuildPlan(document, state)
			if err != nil {
				t.Fatal(err)
			}
			if !RemovalsDiffer(current, approved) || verifyApprovedRemovals(current, &approved) == nil {
				t.Fatalf("accepted stale approval after %s: %+v", scenario, current)
			}
		})
	}
}

func TestApplyRejectsRecreatedInvitationBeforeMutating(t *testing.T) {
	state := &fakeState{orgExists: true, members: []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}},
		otherInvitations: []Invitation{{ID: "inv_original", Email: "pending@acme.com", Role: "member", Status: "pending"}}}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"},
		People: &spec.People{Prune: true, Members: []spec.Member{{Email: "owner@acme.com", Role: "owner"}}}}
	snapshot, err := client.Snapshot(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := BuildPlan(document, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state.otherInvitations[0].ID = "inv_old"
	if _, err := ApplyApproved(context.Background(), client, document, approved); err == nil || state.revokedInvitations != 0 {
		t.Fatalf("replacement invitation used stale approval: error=%v revocations=%d", err, state.revokedInvitations)
	}
}

func TestTeamsPruneListsAndAppliesNestedRemovals(t *testing.T) {
	state := &fakeState{
		orgExists: true, teamCreates: 1,
		members: []Member{
			{UserID: "u1", Email: "owner@acme.com", Role: "owner"},
			{UserID: "u2", Email: "old@acme.com", Role: "member"},
		},
		teamMembers: []TeamMember{
			{UserID: "u1", Email: "owner@acme.com", Role: "admin"},
			{UserID: "u2", Email: "old@acme.com", Role: "member"},
		},
		existingTeams:  []Team{{ID: "team_old", Name: "Old", Path: "old"}},
		oldTeamMembers: []TeamMember{{UserID: "u2", Email: "old@acme.com", Role: "member"}},
	}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme"},
		Teams:    spec.Teams{Prune: true, Items: []spec.Team{{Name: "Engineering", Members: []string{"owner@acme.com"}}}},
	}
	snapshot, err := client.Snapshot(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := BuildPlan(document, snapshot)
	if err != nil || len(deletions(approved)) != 3 {
		t.Fatalf("plan = %+v, %v", approved, err)
	}
	remaining, err := ApplyApproved(context.Background(), client, document, approved)
	if err != nil || len(remaining.Changes) != 0 || state.teamMemberRemoves != 1 || state.teamDeletes != 1 {
		t.Fatalf("apply = %+v, %v; team member/team deletes = %d/%d", remaining, err, state.teamMemberRemoves, state.teamDeletes)
	}
}

func TestApplyCreatesOrganizationWithCurrentUserAsOwner(t *testing.T) {
	state := &fakeState{members: []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}}}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme"},
		Teams:    spec.Teams{Items: []spec.Team{{Name: "Engineering", Members: []string{"owner@acme.com"}}}},
	}
	plan, err := Apply(context.Background(), client, document)
	if err != nil || !state.orgExists || state.orgCreates != 1 || len(plan.Changes) != 0 || len(plan.NeedsHuman) != 0 {
		t.Fatalf("apply = %+v, %v; organization creates = %d", plan, err, state.orgCreates)
	}
}

func TestPlanRejectsUnresolvedTeamMember(t *testing.T) {
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme"},
		Teams:    spec.Teams{Items: []spec.Team{{Name: "Engineering", Members: []string{"missing@acme.com"}}}},
	}
	_, err := BuildPlan(document, Snapshot{Members: map[string]Member{}, Invitations: map[string]Invitation{}})
	if err == nil || !strings.Contains(err.Error(), "absent from the organization") {
		t.Fatalf("error = %v", err)
	}
}

func TestOrganizationAgentPlanApplyAndPull(t *testing.T) {
	state := &fakeState{
		orgExists: true,
		members:   []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}},
		agent:     &OrganizationAgent{Name: "Acme Agent", Description: "Old", Instructions: "Coordinate.", Status: "active", CanManage: true},
	}
	server := httptest.NewServer(http.HandlerFunc(state.serve))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	enabled, name, instructions := false, "Atlas", "Coordinate team work."
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme"},
		Agents: &spec.Agents{Organization: &spec.OrganizationAgent{
			Enabled: &enabled, DisplayName: &name, Instructions: &instructions,
		}},
	}
	snapshot, err := client.Snapshot(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(document, snapshot)
	if err != nil || len(plan.Changes) != 3 || state.agentUpdates != 0 || state.agentReads != 1 {
		t.Fatalf("plan = %+v, %v; reads/updates = %d/%d", plan, err, state.agentReads, state.agentUpdates)
	}
	if _, err := Apply(context.Background(), client, document); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), client, document); err != nil {
		t.Fatal(err)
	}
	if state.agentUpdates != 1 || state.agent.Name != name || state.agent.Instructions != instructions || state.agent.Status != "disabled" || state.agent.Description != "Old" {
		t.Fatalf("agent = %+v, updates = %d", state.agent, state.agentUpdates)
	}
	snapshot, err = client.Snapshot(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	pulled, err := Pull(snapshot)
	if err != nil || pulled.Agents == nil || pulled.Agents.Organization == nil || *pulled.Agents.Organization.Enabled || *pulled.Agents.Organization.DisplayName != name {
		t.Fatalf("pulled agent = %+v, %v", pulled.Agents, err)
	}
}

func TestOrganizationAgentPlanRejectsMemberUpdates(t *testing.T) {
	name := "Atlas"
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: "acme"},
		Agents:   &spec.Agents{Organization: &spec.OrganizationAgent{DisplayName: &name}},
	}
	state := Snapshot{
		Organization: &Organization{ID: "org_1", Slug: "acme"},
		Agent:        &OrganizationAgent{Name: "Acme Agent", Status: "active"},
	}
	if _, err := BuildPlan(document, state); err == nil || !strings.Contains(err.Error(), "owner or admin") {
		t.Fatalf("plan error = %v", err)
	}
}

type fakeState struct {
	orgExists          bool
	domains            []string
	timezone           string
	guestsAllowed      bool
	orgCreates         int
	members            []Member
	teamMembers        []TeamMember
	existingTeams      []Team
	oldTeamMembers     []TeamMember
	teamMemberRemoves  int
	teamDeletes        int
	invites            int
	lastInviteQuiet    bool
	otherInvitations   []Invitation
	removedMembers     int
	revokedInvitations int
	teamCreates        int
	adds               int
	roleChanges        int
	agent              *OrganizationAgent
	agentReads         int
	agentUpdates       int
}

func (state *fakeState) serve(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer test-token" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	write := func(value any) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(value)
	}
	switch request.Method + " " + request.URL.Path {
	case "GET /v1/organizations":
		organizations := []Organization{}
		if state.orgExists {
			organizations = append(organizations, Organization{ID: "org_1", Name: "Acme", Slug: "acme", Domains: state.domains, Timezone: state.timezone, GuestsAllowed: state.guestsAllowed})
		}
		write(map[string]any{"organizations": organizations})
	case "GET /v1/auth/me":
		write(map[string]any{"user": map[string]string{"id": "u1", "email": "owner@acme.com"}})
	case "POST /v1/organizations":
		state.orgExists = true
		state.orgCreates++
		write(map[string]string{"id": "org_1"})
	case "PATCH /v1/organizations/org_1":
		var input struct {
			Domains       []string `json:"domains"`
			Timezone      *string  `json:"timezone"`
			GuestsAllowed *bool    `json:"guests_allowed"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		if input.Domains != nil {
			state.domains = input.Domains
		}
		if input.Timezone != nil {
			state.timezone = *input.Timezone
		}
		if input.GuestsAllowed != nil {
			state.guestsAllowed = *input.GuestsAllowed
		}
		write(map[string]string{"id": "org_1"})
	case "GET /v1/organizations/org_1/members":
		write(map[string]any{"members": state.members})
	case "GET /v1/organizations/org_1":
		write(map[string]any{"membership": map[string]string{"user_id": "u1", "role": "owner"}})
	case "GET /v1/organizations/org_1/invitations":
		invitations := append([]Invitation{}, state.otherInvitations...)
		if state.invites > 0 {
			invitations = append(invitations, Invitation{Email: "new@acme.com", Role: "member", Status: "pending"})
		}
		write(map[string]any{"invitations": invitations})
	case "POST /v1/organizations/org_1/invitations":
		var input struct {
			Quiet bool `json:"quiet"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		state.lastInviteQuiet = input.Quiet
		state.invites++
		write(map[string]string{"status": "created"})
	case "DELETE /v1/organizations/org_1/invitations/inv_old":
		state.revokedInvitations++
		state.otherInvitations = nil
		writer.WriteHeader(http.StatusNoContent)
	case "DELETE /v1/organizations/org_1/members/u2":
		state.removedMembers++
		state.members = state.members[:1]
		writer.WriteHeader(http.StatusNoContent)
	case "GET /v1/organizations/org_1/teams":
		teams := append([]Team{}, state.existingTeams...)
		if state.teamCreates > 0 {
			teams = append(teams, Team{ID: "team_1", Name: "Engineering", Path: "engineering"})
		}
		write(map[string]any{"teams": teams})
	case "POST /v1/organizations/org_1/teams":
		state.teamCreates++
		state.teamMembers = append(state.teamMembers, TeamMember{UserID: "u1", Email: "owner@acme.com", Role: "admin"})
		write(map[string]string{"id": "team_1"})
	case "GET /v1/organizations/org_1/teams/team_1/members":
		write(map[string]any{"members": state.teamMembers})
	case "GET /v1/organizations/org_1/teams/team_old/members":
		write(map[string]any{"members": state.oldTeamMembers})
	case "DELETE /v1/organizations/org_1/teams/team_1/members/u2":
		state.teamMemberRemoves++
		state.teamMembers = state.teamMembers[:1]
		writer.WriteHeader(http.StatusNoContent)
	case "DELETE /v1/organizations/org_1/teams/team_old":
		state.teamDeletes++
		state.existingTeams = nil
		writer.WriteHeader(http.StatusNoContent)
	case "POST /v1/organizations/org_1/teams/team_1/members":
		var input struct {
			Role string `json:"role"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		if input.Role == "" {
			input.Role = "member"
		}
		state.adds++
		state.teamMembers = append(state.teamMembers, TeamMember{UserID: "u2", Email: "new@acme.com", Role: input.Role})
		write(map[string]string{"status": "created"})
	case "PATCH /v1/organizations/org_1/teams/team_1/members/u2":
		var input struct {
			Role string `json:"role"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		state.roleChanges++
		for i := range state.teamMembers {
			if state.teamMembers[i].UserID == "u2" {
				state.teamMembers[i].Role = input.Role
			}
		}
		write(map[string]string{"status": "updated"})
	case "GET /v1/organizations/org_1/organization-agent":
		if request.URL.RawQuery != "preview=1" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		state.agentReads++
		if state.agent == nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		write(state.agent)
	case "PATCH /v1/organizations/org_1/organization-agent":
		state.agentUpdates++
		var fields map[string]string
		if err := json.NewDecoder(request.Body).Decode(&fields); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		for key, value := range fields {
			switch key {
			case "name":
				state.agent.Name = value
			case "description":
				state.agent.Description = value
			case "instructions":
				state.agent.Instructions = value
			case "status":
				state.agent.Status = value
			}
		}
		write(state.agent)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

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
		Teams:    []spec.Team{{Name: "Engineering", Members: []string{"owner@acme.com", "new@acme.com"}}},
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
		Teams:    []spec.Team{{Name: "Engineering", Members: []string{"owner@acme.com"}}},
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
		Teams:    []spec.Team{{Name: "Engineering", Members: []string{"missing@acme.com"}}},
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
	orgExists    bool
	orgCreates   int
	members      []Member
	teamMembers  []TeamMember
	invites      int
	teamCreates  int
	adds         int
	agent        *OrganizationAgent
	agentReads   int
	agentUpdates int
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
			organizations = append(organizations, Organization{ID: "org_1", Name: "Acme", Slug: "acme"})
		}
		write(map[string]any{"organizations": organizations})
	case "GET /v1/auth/me":
		write(map[string]any{"user": map[string]string{"id": "u1", "email": "owner@acme.com"}})
	case "POST /v1/organizations":
		state.orgExists = true
		state.orgCreates++
		write(map[string]string{"id": "org_1"})
	case "GET /v1/organizations/org_1/members":
		write(map[string]any{"members": state.members})
	case "GET /v1/organizations/org_1/invitations":
		invitations := []Invitation{}
		if state.invites > 0 {
			invitations = append(invitations, Invitation{Email: "new@acme.com", Role: "member", Status: "pending"})
		}
		write(map[string]any{"invitations": invitations})
	case "POST /v1/organizations/org_1/invitations":
		state.invites++
		write(map[string]string{"status": "created"})
	case "GET /v1/organizations/org_1/teams":
		teams := []Team{}
		if state.teamCreates > 0 {
			teams = append(teams, Team{ID: "team_1", Name: "Engineering"})
		}
		write(map[string]any{"teams": teams})
	case "POST /v1/organizations/org_1/teams":
		state.teamCreates++
		state.teamMembers = append(state.teamMembers, TeamMember{UserID: "u1", Email: "owner@acme.com", Role: "admin"})
		write(map[string]string{"id": "team_1"})
	case "GET /v1/organizations/org_1/teams/team_1/members":
		write(map[string]any{"members": state.teamMembers})
	case "POST /v1/organizations/org_1/teams/team_1/members":
		state.adds++
		state.teamMembers = append(state.teamMembers, TeamMember{UserID: "u2", Email: "new@acme.com", Role: "member"})
		write(map[string]string{"status": "created"})
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

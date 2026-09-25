package teamsclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

const teamBody = `{"team":{"id":"team_eng","organization_id":"org_1","name":"Engineering"},"membership":{"team_id":"team_eng","user_id":"user-1","role":"member"},"can_manage":false}`

func TestTeamForwardsCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/organizations/org_1/teams/team_eng" || request.Header.Get("Authorization") != "Bearer token-1" {
			t.Errorf("unexpected teams request %s %s %v", request.Method, request.URL.Path, request.Header)
		}
		_, _ = writer.Write([]byte(teamBody))
	}))
	defer server.Close()

	access, err := New(server.URL+"/").Team(context.Background(), "org_1", "team_eng", authclient.Credentials{Authorization: "Bearer token-1"})
	if err != nil {
		t.Fatalf("Team() error = %v", err)
	}
	want := domain.TeamAccess{TeamID: "team_eng", CanManage: false, Member: true}
	if access != want {
		t.Fatalf("access = %+v, want %+v", access, want)
	}
}

func TestTeamMapsTeamsRefusals(t *testing.T) {
	tests := map[int]error{
		http.StatusUnauthorized: domain.ErrUnauthorized,
		http.StatusForbidden:    domain.ErrForbidden,
		http.StatusNotFound:     domain.ErrNotFound,
	}
	for status, want := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := New(server.URL).Team(context.Background(), "org_1", "team_eng", authclient.Credentials{Authorization: "Bearer token"})
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("status %d: error = %v, want %v", status, err, want)
		}
	}
}

func TestTeamWithoutCredentials(t *testing.T) {
	if _, err := New("http://teams.invalid").Team(context.Background(), "org_1", "team_eng", authclient.Credentials{}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Team() error = %v, want ErrUnauthorized", err)
	}
}

func TestTeamTreatsNullMembershipAsNotAMember(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"team":{"id":"team_eng"},"membership":null,"can_manage":true}`))
	}))
	defer server.Close()

	access, err := New(server.URL).Team(context.Background(), "org_1", "team_eng", authclient.Credentials{Authorization: "Bearer token"})
	if err != nil {
		t.Fatalf("Team() error = %v", err)
	}
	if !access.CanManage || access.Member || !access.CanSee() {
		t.Fatalf("access = %+v", access)
	}
}

// TestTeamReadsTheCallersOwnRoleOnTheTeam: teams says can_manage for every
// org admin, on the team or not, so whether the caller manages the team in
// their own right comes from the role their membership carries. An agent,
// which gets no org admin's reach, may manage a team on nothing else.
func TestTeamReadsTheCallersOwnRoleOnTheTeam(t *testing.T) {
	tests := map[string]domain.TeamAccess{
		`{"team":{"id":"team_eng"},"membership":{"user_id":"user-1","role":"admin"},"can_manage":true}`:  {TeamID: "team_eng", CanManage: true, Member: true, TeamAdmin: true},
		`{"team":{"id":"team_eng"},"membership":{"user_id":"user-1","role":"member"},"can_manage":true}`: {TeamID: "team_eng", CanManage: true, Member: true},
		`{"team":{"id":"team_eng"},"membership":null,"can_manage":true}`:                                 {TeamID: "team_eng", CanManage: true},
		`{"team":{"id":"team_eng"},"membership":{"user_id":"","role":"admin"},"can_manage":false}`:       {TeamID: "team_eng"},
	}
	for body, want := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(body))
		}))
		access, err := New(server.URL).Team(context.Background(), "org_1", "team_eng", authclient.Credentials{Authorization: "Bearer token"})
		server.Close()
		if err != nil || access != want {
			t.Fatalf("Team() for %s = %+v, %v; want %+v", body, access, err, want)
		}
	}
}

package teamsclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func TestTeamForwardsCredentialsAndReadsAccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/organizations/org_1/teams/team_1" || request.Header.Get("Authorization") != "Bearer token-1" {
			t.Errorf("unexpected teams request %s %v", request.URL.Path, request.Header)
		}
		_, _ = writer.Write([]byte(`{"team":{"id":"team_1","name":"Platform"},"membership":{"user_id":"user-1","role":"admin"},"can_manage":true}`))
	}))
	defer server.Close()

	access, err := New(server.URL+"/").Team(context.Background(), "org_1", "team_1", authclient.Credentials{Authorization: "Bearer token-1"})
	if err != nil || access != (domain.TeamAccess{TeamID: "team_1", CanManage: true, Member: true}) {
		t.Fatalf("Team() = %+v %v", access, err)
	}
}

func TestTeamForOrganizationAdminOffTheTeam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"team":{"id":"team_1"},"membership":null,"can_manage":true}`))
	}))
	defer server.Close()
	access, err := New(server.URL).Team(context.Background(), "org_1", "team_1", authclient.Credentials{AccessCookie: "cookie"})
	if err != nil || access != (domain.TeamAccess{TeamID: "team_1", CanManage: true}) || !access.CanSee() {
		t.Fatalf("Team() = %+v %v", access, err)
	}
}

func TestTeamMapsRefusalsAndMismatches(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized: domain.ErrUnauthorized,
		http.StatusForbidden:    domain.ErrForbidden,
		http.StatusNotFound:     domain.ErrNotFound,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := New(server.URL).Team(context.Background(), "org_1", "team_1", authclient.Credentials{Authorization: "Bearer token"})
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("status %d: error = %v, want %v", status, err, want)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"team":{"id":"team_2"},"can_manage":true}`))
	}))
	defer server.Close()
	if _, err := New(server.URL).Team(context.Background(), "org_1", "team_1", authclient.Credentials{Authorization: "Bearer token"}); err == nil {
		t.Fatal("Team() accepted an answer for another team")
	}
	if _, err := New(server.URL).Team(context.Background(), "org_1", " ", authclient.Credentials{Authorization: "Bearer token"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("empty team error = %v", err)
	}
	if _, err := New(server.URL).Team(context.Background(), "org_1", "team_1", authclient.Credentials{}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("missing credentials error = %v", err)
	}
}

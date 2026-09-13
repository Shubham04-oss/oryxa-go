package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/application"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

type fakeService struct {
	created application.CreateTeamInput
	added   application.AddMemberInput
	team    domain.Team
	err     error
}

func (fake *fakeService) CreateTeam(_ context.Context, _ authclient.Credentials, _ string, input application.CreateTeamInput) (domain.Team, error) {
	fake.created = input
	if fake.err != nil {
		return domain.Team{}, fake.err
	}
	return domain.Team{ID: "team_1", OrganizationID: "org_1", Name: input.Name}, nil
}

func (fake *fakeService) Teams(context.Context, authclient.Credentials, string) ([]application.TeamSummary, error) {
	return []application.TeamSummary{{Team: fake.team, CanManage: true}}, fake.err
}

func (fake *fakeService) Team(context.Context, authclient.Credentials, string, string) (application.TeamDetail, error) {
	return application.TeamDetail{Team: fake.team, CanManage: true}, fake.err
}

func (fake *fakeService) UpdateTeam(_ context.Context, _ authclient.Credentials, _, _ string, input application.UpdateTeamInput) (domain.Team, error) {
	return domain.Team{ID: "team_1", Name: input.Name}, fake.err
}

func (fake *fakeService) DeleteTeam(context.Context, authclient.Credentials, string, string) error {
	return fake.err
}

func (fake *fakeService) Members(context.Context, authclient.Credentials, string, string) ([]domain.Membership, error) {
	return []domain.Membership{{UserID: "user-1", Role: domain.RoleAdmin}}, fake.err
}

func (fake *fakeService) AddMember(_ context.Context, _ authclient.Credentials, _, _ string, input application.AddMemberInput) (domain.Membership, error) {
	fake.added = input
	return domain.Membership{UserID: input.UserID, Role: input.Role}, fake.err
}

func (fake *fakeService) ChangeMemberRole(_ context.Context, _ authclient.Credentials, _, _, _ string, role domain.Role) (domain.Membership, error) {
	return domain.Membership{UserID: "user-2", Role: role}, fake.err
}

func (fake *fakeService) RemoveMember(context.Context, authclient.Credentials, string, string, string) error {
	return fake.err
}

func (fake *fakeService) Tools(context.Context, authclient.Credentials, string) ([]domain.Tool, error) {
	return domain.ToolCatalog(), fake.err
}

func (fake *fakeService) SetNotification(_ context.Context, _ authclient.Credentials, _, _ string, notifyEmail bool) (domain.Membership, error) {
	return domain.Membership{UserID: "user-1", NotifyEmail: notifyEmail}, fake.err
}

func (fake *fakeService) CallTool(_ context.Context, _ authclient.Credentials, _, name string, _ json.RawMessage) (any, error) {
	if fake.err != nil {
		return nil, fake.err
	}
	if name == domain.ToolCallTeamMembers {
		return domain.MembersOf(domain.Team{ID: "team_1", Name: "Engineering"}, []domain.Membership{{
			UserID: "user-1", Email: "asha@example.com", Role: domain.RoleAdmin,
		}}), nil
	}
	return domain.TeamAsHandle(domain.Team{ID: "team_1", OrganizationID: "org_1", Name: "Engineering"}), nil
}

func TestHealthz(t *testing.T) {
	response := httptest.NewRecorder()
	New(&fakeService{}, Config{}, slog.New(slog.DiscardHandler)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestCreateTeam(t *testing.T) {
	service := &fakeService{}
	response := do(New(service, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/teams", `{"name":"Engineering"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	if service.created.Name != "Engineering" {
		t.Fatalf("created = %+v", service.created)
	}
}

func TestAddMember(t *testing.T) {
	service := &fakeService{}
	response := do(New(service, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/teams/team_1/members", `{"user_id":"user-2","role":"member"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	if service.added.UserID != "user-2" || service.added.Role != domain.RoleMember {
		t.Fatalf("added = %+v", service.added)
	}
}

func TestMapsDomainErrors(t *testing.T) {
	tests := map[error]int{
		domain.ErrInvalid:   http.StatusBadRequest,
		domain.ErrForbidden: http.StatusForbidden,
		domain.ErrNotFound:  http.StatusNotFound,
		domain.ErrConflict:  http.StatusConflict,
	}
	for err, status := range tests {
		service := &fakeService{err: err}
		response := do(New(service, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/teams", `{"name":"Engineering"}`)
		if response.Code != status {
			t.Fatalf("%v: status = %d, want %d", err, response.Code, status)
		}
		var body map[string]string
		if decodeErr := json.Unmarshal(response.Body.Bytes(), &body); decodeErr != nil || body["error"] == "" {
			t.Fatalf("body = %s", response.Body.Bytes())
		}
	}
}

func TestToolsCatalog(t *testing.T) {
	service := &fakeService{team: domain.Team{ID: "team_1", Name: "Engineering"}}
	response := do(New(service, Config{}, nil), http.MethodGet, "/v1/organizations/org_1/tools", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	var body struct {
		Tools []domain.Tool `json:"tools"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Tools) != 3 || body.Tools[0].Name != domain.ToolListTeams || body.Tools[1].Name != domain.ToolCallTeam || body.Tools[2].Name != domain.ToolCallTeamMembers {
		t.Fatalf("body = %s", response.Body.Bytes())
	}
}

func TestCallTool(t *testing.T) {
	response := do(New(&fakeService{}, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/tools/call_team_members", `{"query":"Engineering"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	var members domain.TeamMembers
	if err := json.Unmarshal(response.Body.Bytes(), &members); err != nil {
		t.Fatalf("body = %s", response.Body.Bytes())
	}
	if members.TeamID != "team_1" || len(members.Members) != 1 || members.Members[0].UserID != "user-1" || members.Members[0].Email != "asha@example.com" {
		t.Fatalf("members = %+v", members)
	}
}

func TestSetNotification(t *testing.T) {
	response := do(New(&fakeService{}, Config{}, nil), http.MethodPatch, "/v1/organizations/org_1/teams/team_1/notification", `{"notify_email":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	var membership domain.Membership
	if err := json.Unmarshal(response.Body.Bytes(), &membership); err != nil || membership.NotifyEmail {
		t.Fatalf("membership = %s", response.Body.Bytes())
	}
}

func TestCookieMutationRequiresOrigin(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/org_1/teams", strings.NewReader(`{"name":"Engineering"}`))
	request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: "cookie"})
	response := httptest.NewRecorder()
	New(&fakeService{}, Config{AllowedOrigins: []string{"http://localhost:3000"}}, nil).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want forbidden", response.Code)
	}
}

func do(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Authorization", "Bearer token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

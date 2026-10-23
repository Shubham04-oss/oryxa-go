package authstub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/test/identities"
)

func TestOrganizationByBearer(t *testing.T) {
	response := do(http.MethodGet, "/v1/organizations/org_sim", "Bearer owner", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	var body struct {
		Organization struct {
			ID string `json:"id"`
		} `json:"organization"`
		Membership struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		} `json:"membership"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Organization.ID != "org_sim" || body.Membership.UserID != "user-owner" || body.Membership.Role != "owner" {
		t.Fatalf("body = %+v", body)
	}
}

func TestStrangerIsConcealed(t *testing.T) {
	response := do(http.MethodGet, "/v1/organizations/org_sim", "Bearer stranger", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestUnknownTokenIsUnauthorized(t *testing.T) {
	response := do(http.MethodGet, "/v1/organizations/org_sim", "Bearer nope", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestMembersOmitsStranger(t *testing.T) {
	response := do(http.MethodGet, "/v1/organizations/org_sim/members", "Bearer admin", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	var body struct {
		Members []struct {
			UserID string `json:"user_id"`
		} `json:"members"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Members) != 4 {
		t.Fatalf("members = %+v", body.Members)
	}
	for _, member := range body.Members {
		if member.UserID == "user-stranger" {
			t.Fatal("stranger must not appear on the organization roster")
		}
	}
}

func TestCookieIsAccepted(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org_sim", nil)
	request.AddCookie(&http.Cookie{Name: identities.AccessCookie, Value: "member"})
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
}

func do(method, path, authorization, cookie string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: identities.AccessCookie, Value: cookie})
	}
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, request)
	return response
}

func TestMeByCookie(t *testing.T) {
	response := do(http.MethodGet, "/v1/auth/me", "", "member")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Organizations []struct {
			ID string `json:"id"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.User.ID != "user-member" || len(body.Organizations) != 1 || body.Organizations[0].ID != "org_sim" {
		t.Fatalf("body = %+v", body)
	}
}

func TestStrangerHasNoOrganizations(t *testing.T) {
	response := do(http.MethodGet, "/v1/organizations", "Bearer stranger", "")
	if response.Code != http.StatusOK || response.Body.String() != "{\"organizations\":[]}\n" {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
}

func TestMeNeedsToken(t *testing.T) {
	if response := do(http.MethodGet, "/v1/auth/me", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestSessionRoutesWebNextCalls(t *testing.T) {
	if response := do(http.MethodGet, "/v1/auth/config", "", ""); response.Code != http.StatusOK {
		t.Fatalf("config status = %d", response.Code)
	}
	for _, path := range []string{"/v1/auth/refresh", "/v1/auth/logout"} {
		if response := do(http.MethodPost, path, "", ""); response.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
}

func TestUnmatchedGoesUpstream(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Path", request.Method+" "+request.URL.Path)
	}))
	defer api.Close()
	upstream, _ := url.Parse(api.URL)
	handler := NewHandler(Config{Upstream: upstream})
	for path, want := range map[string]string{
		"/v1/agents":                  "GET /v1/agents",
		"/v1/organizations/org_sim/x": "GET /v1/organizations/org_sim/x",
		"/v1/auth/me":                 "",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer member")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if got := response.Header().Get("X-Path"); got != want {
			t.Fatalf("%s reached upstream as %q, want %q", path, got, want)
		}
	}
}

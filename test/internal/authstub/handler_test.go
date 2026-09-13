package authstub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

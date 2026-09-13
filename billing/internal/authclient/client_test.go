package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

const organizationBody = `{"organization":{"id":"org_1","name":"Acme","slug":"acme"},"membership":{"organization_id":"org_1","user_id":"user-1","email":"asha@example.com","display_name":"Asha","role":"owner","joined_at":"2026-09-13T12:00:00Z"}}`

func TestMembershipForwardsCredentials(t *testing.T) {
	tests := []struct {
		name        string
		credentials Credentials
		check       func(*http.Request) bool
	}{
		{
			name:        "bearer token",
			credentials: Credentials{Authorization: "Bearer token-1", AccessCookie: "cookie-1"},
			check: func(request *http.Request) bool {
				_, err := request.Cookie(AccessCookieName)
				return request.Header.Get("Authorization") == "Bearer token-1" && err != nil
			},
		},
		{
			name:        "session cookie",
			credentials: Credentials{AccessCookie: "cookie-1"},
			check: func(request *http.Request) bool {
				cookie, err := request.Cookie(AccessCookieName)
				return err == nil && cookie.Value == "cookie-1" && request.Header.Get("Authorization") == ""
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/v1/organizations/org_1" || !test.check(request) {
					t.Errorf("unexpected auth request %s %s %v", request.Method, request.URL.Path, request.Header)
				}
				_, _ = writer.Write([]byte(organizationBody))
			}))
			defer server.Close()

			membership, err := New(server.URL+"/").Membership(context.Background(), "org_1", test.credentials)
			if err != nil {
				t.Fatalf("Membership() error = %v", err)
			}
			want := domain.Membership{OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-1", Email: "asha@example.com", DisplayName: "Asha", Role: domain.RoleOwner}
			if membership != want {
				t.Fatalf("membership = %+v, want %+v", membership, want)
			}
		})
	}
}

func TestMembershipMapsAuthRefusals(t *testing.T) {
	tests := map[int]error{
		http.StatusUnauthorized: domain.ErrUnauthorized,
		http.StatusForbidden:    domain.ErrForbidden,
		http.StatusNotFound:     domain.ErrNotFound,
	}
	for status, want := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token"})
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("status %d: error = %v, want %v", status, err, want)
		}
	}
}

func TestMembershipFailures(t *testing.T) {
	tests := map[string]http.HandlerFunc{
		"server error": func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
		},
		"another organization": func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"organization":{"id":"org_2"},"membership":{"organization_id":"org_2","user_id":"user-1","role":"owner"}}`))
		},
		"not JSON": func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`<html>`))
		},
	}
	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			_, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token"})
			if err == nil || errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("Membership() error = %v, want a plain failure", err)
			}
		})
	}
}

func TestMembershipWithoutCredentials(t *testing.T) {
	if _, err := New("http://auth.invalid").Membership(context.Background(), "org_1", Credentials{}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Membership() error = %v, want ErrUnauthorized", err)
	}
}

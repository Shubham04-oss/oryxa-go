package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStatusErrorShowsOnlyWorkspaceValidationDetails(t *testing.T) {
	for _, test := range []struct {
		name, path, body, contentType, want string
		status                              int
	}{
		{"workspace conflict", "/v1/organizations/org1/workspace:plan", `{"error":"external user ID would change its Oryxa binding"}`, "application/json", "external user ID would change its Oryxa binding", http.StatusBadRequest},
		{"other API", "/v1/organizations/org1/apps/connections", `{"error":"provider secret"}`, "application/json", "", http.StatusBadRequest},
		{"server failure", "/v1/organizations/org1/workspace", `{"error":"database detail"}`, "application/json", "", http.StatusInternalServerError},
		{"non JSON", "/v1/organizations/org1/workspace", `{"error":"not trusted"}`, "text/plain", "", http.StatusBadRequest},
		{"multiline", "/v1/organizations/org1/workspace", `{"error":"first\nsecond"}`, "application/json", "", http.StatusBadRequest},
		{"member denied", "/v1/organizations/org1/members/user1", `{"error":"only owners may change roles"}`, "application/json", "only owners may change roles", http.StatusForbidden},
		{"invitation conflict", "/v1/organizations/org1/invitations", `{"error":"invitation already exists"}`, "application/json", "invitation already exists", http.StatusConflict},
		{"credentials suppressed", "/v1/organizations/org1/workspace", `{"error":"invalid bearer abc123"}`, "application/json", "", http.StatusBadRequest},
		{"terminal escape suppressed", "/v1/organizations/org1/workspace", `{"error":"invalid \u001b[31m"}`, "application/json", "", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": []string{test.contentType}}, Body: io.NopCloser(strings.NewReader(test.body))}
			message := statusError(response, http.MethodPost, test.path).Error()
			want := fmt.Sprintf("POST %s: API returned %d", test.path, test.status)
			if test.want != "" {
				want += ": " + test.want
			}
			if message != want {
				t.Fatalf("error = %q, want %q", message, want)
			}
		})
	}
}

func TestStatusErrorPreservesStructuredCode(t *testing.T) {
	response := &http.Response{StatusCode: 409, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":"stale_plan","error":"review changes again"}`))}
	if got := statusError(response, http.MethodPut, "/v1/organizations/org1/workspace"); got.Code != "stale_plan" {
		t.Fatalf("structured code lost: %+v", got)
	}
}

type responseTransport func(*http.Request) (*http.Response, error)

func (transport responseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}
func TestApplyUsesLongBudgetAndRejectsExpiringSession(t *testing.T) {
	client, err := NewClient("https://api.example.com", "access")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	client.HTTP.Timeout = time.Millisecond
	client.HTTP.Transport = responseTransport(func(request *http.Request) (*http.Response, error) {
		called = true
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) < 2*time.Minute {
			t.Errorf("short apply deadline: %v", deadline)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	})
	if err := client.request(context.Background(), http.MethodPut, "/v1/organizations/org/workspace", map[string]string{}, nil); err != nil || !called {
		t.Fatalf("long apply: %v", err)
	}
	called = false
	client.TokenExpiresAt = time.Now().Add(time.Minute)
	if err := client.request(context.Background(), http.MethodPut, "/v1/organizations/org/workspace", nil, nil); !errors.Is(err, ErrSessionExpiring) || called {
		t.Fatalf("expiring token reached apply: %v called=%v", err, called)
	}
}

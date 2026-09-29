package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestSnapshotBoundsTeamReadsAndUsesAdminDirectory(t *testing.T) {
	for _, role := range []string{"owner", "admin"} {
		t.Run(role, func(t *testing.T) {
			var active, maximum atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/organizations":
					fmt.Fprint(w, `{"organizations":[{"id":"org","slug":"acme"}]}`)
				case "/v1/auth/me":
					fmt.Fprint(w, `{"user":{"id":"actor","email":"actor@acme.com"}}`)
				case "/v1/organizations/org":
					fmt.Fprintf(w, `{"membership":{"user_id":"actor","role":%q}}`, role)
				case "/v1/organizations/org/members":
					if role != "owner" {
						t.Error("admin used owner-only members route")
					}
					fmt.Fprint(w, `{"members":[{"user_id":"actor","email":"actor@acme.com","role":"owner"}]}`)
				case "/v1/organizations/org/directory":
					fmt.Fprint(w, `{"members":[{"user_id":"actor","email":"actor@acme.com","org_role":"admin"}]}`)
				case "/v1/organizations/org/invitations":
					fmt.Fprint(w, `{"invitations":[]}`)
				case "/v1/organizations/org/teams":
					teams := []Team{}
					for index := range 20 {
						teams = append(teams, Team{ID: fmt.Sprintf("team%d", index), Name: fmt.Sprintf("Team %d", index)})
					}
					json.NewEncoder(w).Encode(map[string]any{"teams": teams})
				case "/v1/organizations/org/organization-agent":
					w.WriteHeader(404)
				default:
					if !strings.HasSuffix(r.URL.Path, "/members") {
						t.Errorf("unexpected path %s", r.URL)
						w.WriteHeader(404)
						return
					}
					count := active.Add(1)
					defer active.Add(-1)
					for old := maximum.Load(); count > old && !maximum.CompareAndSwap(old, count); old = maximum.Load() {
					}
					time.Sleep(5 * time.Millisecond)
					fmt.Fprint(w, `{"members":[]}`)
				}
			}))
			defer api.Close()
			client, err := NewClient(api.URL, "access")
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := client.Snapshot(context.Background(), "acme")
			if err != nil || len(snapshot.TeamMembers) != 20 || maximum.Load() < 2 || maximum.Load() > 8 {
				t.Fatalf("snapshot=%d concurrency=%d err=%v", len(snapshot.TeamMembers), maximum.Load(), err)
			}
			if snapshot.Members["actor@acme.com"].Role != role {
				t.Fatalf("member role lost: %+v", snapshot.Members)
			}
		})
	}
}

func TestWorkspacePartialFailureKeepsSafeProgressWithoutCredentialText(t *testing.T) {
	response := &http.Response{StatusCode: 502, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"partial","apply_id":"apply_123","code":"upstream_rejected","error":"provider token=secret","completed":[{"method":"POST","path":"/v1/organizations/org/teams"},{"method":"POST","path":"/v1/organizations/org?token=secret"}]}`))}
	failure := statusError(response, "PUT", "/v1/organizations/org/workspace")
	if !failure.Partial || failure.ApplyID != "apply_123" || len(failure.Completed) != 1 || failure.Detail != "" {
		t.Fatalf("unsafe or missing partial progress: %+v", failure)
	}
}

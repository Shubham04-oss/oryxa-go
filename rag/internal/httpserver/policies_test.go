package httpserver

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/authz"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

func TestListPoliciesShapesAnEmptyListAsAnArray(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	recorder := do(t, newServer(fake), http.MethodGet, "/v1/organizations/org_1/corpora/cor_1/policies", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Body.String(); got != `{"policies":[]}`+"\n" {
		t.Fatalf("body = %q, want an empty array, not null", got)
	}
}

func TestListPoliciesReturnsEveryStoredPolicy(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	fake := &fakeService{policies: []authz.Policy{
		{ID: "pol_1", Name: "no confidential docs", Text: `forbid (principal, action, resource);`,
			CreatedBy: "user-admin", CreatedAt: createdAt, UpdatedAt: createdAt},
	}}
	recorder := do(t, newServer(fake), http.MethodGet, "/v1/organizations/org_1/corpora/cor_1/policies", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	want := `{"policies":[{"id":"pol_1","name":"no confidential docs","text":"forbid (principal, action, resource);",` +
		`"created_by":"user-admin","created_at":"2026-09-28T12:00:00Z","updated_at":"2026-09-28T12:00:00Z"}]}` + "\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestAddPolicyDecodesAndReturnsTheStoredPolicy(t *testing.T) {
	t.Parallel()
	fake := &fakeService{policy: authz.Policy{ID: "pol_2", Name: "admins only", Text: `forbid (principal, action, resource);`}}
	recorder := do(t, newServer(fake), http.MethodPost, "/v1/organizations/org_1/corpora/cor_1/policies",
		`{"name":"admins only","text":"forbid (principal, action, resource);"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.addPolicyArgs.Name != "admins only" || fake.addPolicyArgs.Text != `forbid (principal, action, resource);` {
		t.Fatalf("addPolicyArgs = %+v", fake.addPolicyArgs)
	}
	if got := recorder.Body.String(); got == "" || got[0] != '{' {
		t.Fatalf("body = %q, want a JSON object wrapping the policy", got)
	}
}

func TestDeletePolicyPassesTheIDAndAnswersNoContent(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	recorder := do(t, newServer(fake), http.MethodDelete, "/v1/organizations/org_1/corpora/cor_1/policies/pol_3", "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.removedPolicy != "pol_3" {
		t.Fatalf("removedPolicy = %q, want pol_3", fake.removedPolicy)
	}
}

func TestPolicyRoutesMapNotFoundAndInvalidLikeEveryOtherRoute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", domain.ErrNotFound, http.StatusNotFound},
		{"invalid", fmt.Errorf("does not parse as Cedar: %w", domain.ErrInvalid), http.StatusBadRequest},
		{"conflict", fmt.Errorf("a policy with this name already exists: %w", domain.ErrConflict), http.StatusConflict},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			recorder := do(t, newServer(&fakeService{err: testCase.err}), http.MethodGet,
				"/v1/organizations/org_1/corpora/cor_1/policies", "")
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}

// TestPolicyRoutesShowTheSpecificForbiddenReason checks that a refused
// administer check answers with application.Service's own explanation rather
// than handleError's generic "you may not do that here" — the one way these
// three routes read an error differently from the rest of this service (see
// handlePolicyError).
func TestPolicyRoutesShowTheSpecificForbiddenReason(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("only organization admins can manage a knowledge base's policies: %w", domain.ErrForbidden)
	recorder := do(t, newServer(&fakeService{err: err}), http.MethodGet, "/v1/organizations/org_1/corpora/cor_1/policies", "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body)
	}
	want := `{"error":"only organization admins can manage a knowledge base's policies"}` + "\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

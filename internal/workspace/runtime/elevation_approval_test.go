package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestTeamCreationRefreshCannotElevateReplacementAccount(t *testing.T) {
	state := &fakeState{orgExists: true, members: []Member{{UserID: "u1", Email: "owner@acme.com", Role: "owner"}, {UserID: "reviewed-user", Email: "lead@acme.com", Role: "member"}}}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.serve(w, r)
		if r.Method == http.MethodPost && r.URL.Path == "/v1/organizations/org_1/teams" {
			state.members[1].UserID = "replacement-user"
		}
	}))
	defer api.Close()
	client, err := NewClient(api.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"}, Teams: spec.Teams{Prune: true, Items: []spec.Team{{Name: "Engineering", Leads: []string{"lead@acme.com"}}}}}
	initial, err := client.Snapshot(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := BuildPlan(document, initial)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ApplyApprovedFromSnapshot(context.Background(), client, document, approved, initial)
	if !errors.Is(err, ErrApprovalChanged) || state.adds != 0 || len(state.teamMembers) != 0 {
		t.Fatalf("replacement account inherited elevation after team creation: %v adds=%d roster=%+v", err, state.adds, state.teamMembers)
	}
}

package runtime

import (
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestRolePlanErrorsNameTheAffectedPerson(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		role       string
		invitation *Invitation
	}{
		{"pending role differs", "admin", &Invitation{ID: "invite1", Email: "person@acme.com", Role: "member", Status: "pending"}},
		{"owner invitation unsupported", "owner", nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			state := Snapshot{Organization: &Organization{ID: "org1", Slug: "acme"}, ActorID: "owner1", ActorRole: "owner", Members: map[string]Member{"owner@acme.com": {UserID: "owner1", Email: "owner@acme.com", Role: "owner"}}, Invitations: map[string]Invitation{}}
			if scenario.invitation != nil {
				state.Invitations[scenario.invitation.Email] = *scenario.invitation
			}
			document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"}, People: &spec.People{Members: []spec.Member{{Email: "person@acme.com", Role: scenario.role}}}}
			plan, err := BuildPlan(document, state)
			if err == nil || !strings.Contains(err.Error(), "people.members[person@acme.com]") || len(plan.Changes) != 0 {
				t.Fatalf("role failure lacks affected person or exposes executable plan: %+v %v", plan, err)
			}
		})
	}
}

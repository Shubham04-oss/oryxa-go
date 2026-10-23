package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// An org admin's own session reaches every member's sources; an agent acting
// for that admin reaches only its member's own, like every other service
// (docs/design/permissions, gap G2).
func TestAnAgentActingForAnAdminHasAMembersReach(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.sources["src_other"] = domain.Source{
		ID: "src_other", OrganizationID: "org_1", Provider: "files",
		Scope: domain.ScopeOrganization, CreatedBy: "user-2", Status: domain.StatusActive,
	}
	credentials := authclient.Credentials{Authorization: "Bearer user-1"}
	admin := member()
	admin.Role = domain.OrgRoleAdmin
	agent := admin
	agent.Agent, agent.AgentRole = true, "primary"

	listed, err := registrar(t, store, admin).Sources(ctx, credentials, "org_1", domain.SourceQuery{Admin: true})
	if err != nil || len(listed) != 1 {
		t.Fatalf("admin's own session lists %d, %v; want the other member's source", len(listed), err)
	}
	if _, err := registrar(t, store, admin).Source(ctx, credentials, "org_1", "src_other"); err != nil {
		t.Fatalf("admin's own session reads it: %v", err)
	}

	listed, err = registrar(t, store, agent).Sources(ctx, credentials, "org_1", domain.SourceQuery{Admin: true})
	if err != nil || len(listed) != 0 {
		t.Fatalf("an agent acting for the admin lists %d, %v; want none", len(listed), err)
	}
	if _, err := registrar(t, store, agent).Source(ctx, credentials, "org_1", "src_other"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an agent acting for the admin reads another member's source: %v", err)
	}
}

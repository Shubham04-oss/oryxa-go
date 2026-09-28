package authz

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// authzTestDatabase opens a postgreskit.Database against
// AUTHZ_TEST_DATABASE_URL and migrates it twice — the second run exercises
// Migrate's checksum-based idempotency — skipping the test when that
// variable is not set, the convention every other service's Postgres
// integration test follows (see e.g. teams/internal/postgres's).
func authzTestDatabase(t *testing.T) *postgreskit.Database {
	t.Helper()
	databaseURL := os.Getenv("AUTHZ_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AUTHZ_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-authz-test"})
	if err != nil {
		t.Fatalf("postgreskit.Open() error = %v", err)
	}
	t.Cleanup(database.Close)
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	return database
}

// TestStoreLifecycle runs Store against a real PostgreSQL, such as the one
// in docker-compose.yml:
//
//	AUTHZ_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./internal/authz/...
func TestStoreLifecycle(t *testing.T) {
	database := authzTestDatabase(t)
	store := NewStore(database)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	organizationID := "it-org-" + suffix
	const createdBy = "it-user-1"
	forbidUnlessAdmin := `forbid (principal, action, resource) unless { context.acts_as_admin };`
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = database.Pool.Exec(cleanupCtx, `DELETE FROM authz_policies WHERE organization_id = $1`, organizationID)
	})

	created, err := store.CreatePolicy(ctx, Policy{
		OrganizationID: organizationID, Scope: organizationScope(), Name: "no-search", CreatedBy: createdBy,
		Text: `forbid (principal, action == Oryxa::Action::"search", resource is Oryxa::KnowledgeBase) unless { context.acts_as_admin };`,
	})
	if err != nil {
		t.Fatalf("CreatePolicy() error = %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreatePolicy() left the id empty")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("CreatePolicy() left a timestamp zero")
	}

	if _, err := store.CreatePolicy(ctx, Policy{
		OrganizationID: organizationID, Scope: organizationScope(), Name: "no-search", CreatedBy: createdBy,
		Text: forbidUnlessAdmin,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate (organization, scope, name) CreatePolicy() error = %v, want ErrConflict", err)
	}

	if _, err := store.CreatePolicy(ctx, Policy{
		OrganizationID: organizationID, Scope: organizationScope(), Name: "bad", CreatedBy: createdBy,
		Text: `permit (principal, action, resource);`,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CreatePolicy(permit) error = %v, want ErrInvalid", err)
	}

	organizationPolicies, err := store.OrganizationPolicies(ctx, organizationID)
	if err != nil {
		t.Fatalf("OrganizationPolicies() error = %v", err)
	}
	if len(organizationPolicies) != 1 || organizationPolicies[0].ID != created.ID {
		t.Fatalf("OrganizationPolicies() = %+v, want just %+v", organizationPolicies, created)
	}

	scopedPolicies, err := store.Policies(ctx, organizationID, organizationScope())
	if err != nil {
		t.Fatalf("Policies() error = %v", err)
	}
	if len(scopedPolicies) != 1 || scopedPolicies[0].ID != created.ID {
		t.Fatalf("Policies() = %+v, want just %+v", scopedPolicies, created)
	}

	knowledgeBaseScope := Scope{Type: "knowledge_base", ID: "kb-1"}
	if _, err := store.CreatePolicy(ctx, Policy{
		OrganizationID: organizationID, Scope: knowledgeBaseScope, Name: "kb-only", CreatedBy: createdBy,
		Text: forbidUnlessAdmin,
	}); err != nil {
		t.Fatalf("CreatePolicy(knowledge base scope) error = %v", err)
	}
	if policies, err := store.Policies(ctx, organizationID, knowledgeBaseScope); err != nil || len(policies) != 1 {
		t.Fatalf("Policies(knowledge base scope) = %+v, err = %v, want just the one policy", policies, err)
	}

	if err := store.DeleteScope(ctx, organizationID, knowledgeBaseScope); err != nil {
		t.Fatalf("DeleteScope() error = %v", err)
	}
	if policies, err := store.Policies(ctx, organizationID, knowledgeBaseScope); err != nil || len(policies) != 0 {
		t.Fatalf("Policies(knowledge base scope) after DeleteScope = %+v, err = %v, want none", policies, err)
	}
	// A Scope with nothing stored on it is not an error to delete: the
	// resource it names may simply never have had a policy of its own.
	if err := store.DeleteScope(ctx, organizationID, knowledgeBaseScope); err != nil {
		t.Fatalf("DeleteScope() on an already-empty scope error = %v", err)
	}

	if err := store.DeletePolicy(ctx, organizationID, organizationScope(), created.ID); err != nil {
		t.Fatalf("DeletePolicy() error = %v", err)
	}
	if err := store.DeletePolicy(ctx, organizationID, organizationScope(), created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeletePolicy() again error = %v, want ErrNotFound", err)
	}

	if _, err := store.CreatePolicy(ctx, Policy{
		OrganizationID: organizationID, Scope: organizationScope(), Name: "for-wipe", CreatedBy: createdBy,
		Text: forbidUnlessAdmin,
	}); err != nil {
		t.Fatalf("CreatePolicy(for-wipe) error = %v", err)
	}
	if err := store.DeleteOrganization(ctx, organizationID); err != nil {
		t.Fatalf("DeleteOrganization() error = %v", err)
	}
	if policies, err := store.OrganizationPolicies(ctx, organizationID); err != nil || len(policies) != 0 {
		t.Fatalf("OrganizationPolicies() after DeleteOrganization = %+v, err = %v, want none", policies, err)
	}
}

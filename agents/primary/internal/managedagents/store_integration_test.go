package managedagents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestStoreLifecycle needs the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/managedagents
func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-managed-agents-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	scope := Scope{OrganizationID: fmt.Sprintf("org_managed_test_%d", suffix), UserID: fmt.Sprintf("user_managed_test_%d", suffix)}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'user_id'=$1`, scope.UserID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1 AND owner_user_id=$2`, scope.OrganizationID, scope.UserID)
	}()

	primary, err := store.EnsurePrimary(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.EnsurePrimary(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if primary.ID != again.ID || primary.Kind != KindPrimary || primary.Name == "" {
		t.Fatalf("primary=%+v again=%+v", primary, again)
	}

	specialist, err := store.Create(ctx, scope, Definition{Name: "Sales", Description: "Owns pipeline", Instructions: "Review CRM."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, scope, Definition{Name: "sales", Instructions: "Duplicate."}); !errors.Is(err, ErrConflict) {
		t.Fatalf("case-insensitive duplicate error = %v", err)
	}
	name := "Revenue"
	updated, err := store.Update(ctx, scope, specialist.ID, UpdateInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.Revision != 2 {
		t.Fatalf("updated=%+v", updated)
	}
	listed, err := store.List(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Kind != KindPrimary {
		t.Fatalf("listed=%+v", listed)
	}
	handles, err := store.OrganizationAgents(ctx, scope.OrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 2 || handles[0].ID != primary.ID || handles[1].ID != specialist.ID || handles[1].Name != name {
		t.Fatalf("organization handles=%+v", handles)
	}
	disabled := StatusDisabled
	if _, err := store.Update(ctx, scope, specialist.ID, UpdateInput{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	handles, err = store.OrganizationAgents(ctx, scope.OrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 1 || handles[0].ID != primary.ID {
		t.Fatalf("handles with disabled specialist=%+v", handles)
	}
	if err := store.Delete(ctx, scope, primary.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete primary error = %v", err)
	}
	if err := store.Delete(ctx, scope, specialist.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, scope, specialist.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted specialist error = %v", err)
	}
	var lifecycleEvents int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox WHERE detail->>'user_id'=$1`, scope.UserID).Scan(&lifecycleEvents); err != nil {
		t.Fatal(err)
	}
	if lifecycleEvents != 5 {
		t.Fatalf("lifecycle events=%d, want 5", lifecycleEvents)
	}
	if err := store.Forget(ctx, scope.OrganizationID, scope.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, scope, primary.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgotten primary error=%v", err)
	}
}

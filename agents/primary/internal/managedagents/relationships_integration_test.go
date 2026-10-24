package managedagents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// fakeRelationships records each write; err fails every one.
type fakeRelationships struct {
	writes []string
	err    error
}

func (fake *fakeRelationships) AgentChanged(_ context.Context, agent Definition) error {
	fake.writes = append(fake.writes, "changed "+agent.ID)
	return fake.err
}

func (fake *fakeRelationships) AgentDeleted(_ context.Context, _, agentID string) error {
	fake.writes = append(fake.writes, "deleted "+agentID)
	return fake.err
}

// TestStoreWritesAgentsToSpiceDBAsTheyCommit needs the shared local
// PostgreSQL, as TestStoreLifecycle does.
func TestStoreWritesAgentsToSpiceDBAsTheyCommit(t *testing.T) {
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
	if err := NewStore(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	for _, failing := range []error{nil, errors.New("spicedb is down")} {
		suffix := time.Now().UnixNano()
		scope := Scope{OrganizationID: fmt.Sprintf("org_rel_test_%d", suffix), UserID: fmt.Sprintf("user_rel_test_%d", suffix)}
		t.Cleanup(func() {
			_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'user_id'=$1`, scope.UserID)
			_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, scope.OrganizationID)
		})
		written := &fakeRelationships{err: failing}
		store := NewStore(database).WithRelationships(written, slog.New(slog.NewTextHandler(io.Discard, nil)))

		// A failed write fails none of them: the projector follows.
		primary, err := store.EnsurePrimary(ctx, scope)
		if err != nil {
			t.Fatalf("EnsurePrimary() error = %v", err)
		}
		if _, err := store.EnsurePrimary(ctx, scope); err != nil {
			t.Fatalf("EnsurePrimary() again error = %v", err)
		}
		specialist, err := store.Create(ctx, scope, Definition{Name: "Sales"})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if _, err := store.Update(ctx, scope, specialist.ID, UpdateInput{Grant: &runprofile.Grant{}}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if err := store.Delete(ctx, scope, specialist.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		// A refused change writes nothing.
		if err := store.Delete(ctx, scope, primary.ID); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Delete(primary) error = %v, want invalid", err)
		}
		want := []string{"changed " + primary.ID, "changed " + specialist.ID, "changed " + specialist.ID, "deleted " + specialist.ID}
		if !slices.Equal(written.writes, want) {
			t.Fatalf("writes (failing %v) = %q, want %q", failing, written.writes, want)
		}
	}
}

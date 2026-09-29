package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// Workspace's persistence tests own random organizations and never read another
// service's tables. The same fixture works in a database containing workspace
// alone, and in the shared integration database used by CI.
func newIntegrationStore(t *testing.T) (*Store, string) {
	t.Helper()
	url := os.Getenv("WORKSPACE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WORKSPACE_TEST_DATABASE_URL is not set")
	}
	database, err := postgreskit.Open(context.Background(), postgreskit.Config{URL: url, ApplicationName: "oryxa-workspace-test"})
	if err != nil {
		t.Fatal(err)
	}
	store := New(database)
	if err := store.Migrate(context.Background()); err != nil {
		database.Close()
		t.Fatal(err)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	org := "workspace_test_" + hex.EncodeToString(random)
	t.Cleanup(func() {
		for _, table := range []string{"configs", "versions", "external_mappings", "reviewed_plans", "apply_attempts"} {
			if _, err := database.Pool.Exec(context.Background(), "DELETE FROM workspace."+table+" WHERE organization_id=$1", org); err != nil {
				t.Errorf("clean up workspace %s: %v", table, err)
			}
		}
		database.Close()
	})
	return store, org
}

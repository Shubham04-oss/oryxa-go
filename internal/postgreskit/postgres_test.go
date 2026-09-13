package postgreskit

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"
)

func TestRequireExtensionsNormalizesNames(t *testing.T) {
	database := &Database{extensions: map[string]string{"vector": "0.8.6", "ltree": "1.3", "graph": "1.1.0"}}
	if err := database.RequireExtensions("pgvector", " ltree ", "pgGraph", "pg_graph", "pg graph"); err != nil {
		t.Fatal(err)
	}
	err := database.RequireExtensions("pg_trgm")
	if err == nil || !strings.Contains(err.Error(), "pg_trgm") {
		t.Fatalf("RequireExtensions() error = %v", err)
	}
}

func TestOpenDoesNotEchoInvalidDatabaseCredentials(t *testing.T) {
	const secret = "database-password-must-not-leak"
	_, err := Open(context.Background(), Config{
		URL: "postgres://user:" + secret + "@%/database", ApplicationName: "oryxa-test",
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Open() error = %v", err)
	}
}

func TestOpenSharesPGXPoolWithSQLXAndRegistersVector(t *testing.T) {
	databaseURL := os.Getenv("POSTGRESKIT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("POSTGRESKIT_TEST_DATABASE_URL is not set")
	}
	database, err := Open(context.Background(), Config{URL: databaseURL, ApplicationName: "oryxa-postgreskit-test",
		RequiredExtensions: []string{"ltree", "pg_trgm", "vector"}})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var applicationName string
	if err := database.SQLX.GetContext(context.Background(), &applicationName, `SELECT current_setting('application_name')`); err != nil {
		t.Fatal(err)
	}
	if applicationName != "oryxa-postgreskit-test" {
		t.Fatalf("application_name = %q", applicationName)
	}
	var value pgvector.Vector
	if err := database.Pool.QueryRow(context.Background(), `SELECT '[1,2,3]'::vector`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if got := value.Slice(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("vector = %v", got)
	}
}

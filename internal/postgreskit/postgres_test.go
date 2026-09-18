package postgreskit

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"
)

func TestRequireExtensionsNormalizesNames(t *testing.T) {
	database := &Database{extensions: map[string]string{"vector": "0.8.6", "ltree": "1.3", "age": "1.5.0"}}
	if err := database.RequireExtensions("pgvector", " ltree ", "Apache AGE", "apache_age", "age"); err != nil {
		t.Fatal(err)
	}
	err := database.RequireExtensions("pg_trgm")
	if err == nil || !strings.Contains(err.Error(), "pg_trgm") {
		t.Fatalf("RequireExtensions() error = %v", err)
	}
}

func TestDetachedReportsItsExtensions(t *testing.T) {
	extensions := map[string]string{"vector": "0.8.6"}
	database := Detached(extensions)
	extensions["vector"] = "changed"
	if got := database.ExtensionVersions()["vector"]; got != "0.8.6" {
		t.Fatalf("vector = %q, want the version it was given", got)
	}
	if err := database.RequireExtensions("pgvector"); err != nil {
		t.Fatal(err)
	}
	database.Close()
}

func TestWithSchemaLeadsTheSearchPathOfALazyPool(t *testing.T) {
	shared := Detached(map[string]string{"vector": "0.8.6"})
	defer shared.Close()

	scoped, err := shared.WithSchema(context.Background(), "agents_primary", "oryxa-agents", 3)
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	config := scoped.Pool.Config()
	if got := config.ConnConfig.RuntimeParams["search_path"]; got != "agents_primary,public" {
		t.Fatalf("search_path = %q, want agents_primary,public", got)
	}
	if got := config.ConnConfig.RuntimeParams["application_name"]; got != "oryxa-agents" {
		t.Fatalf("application_name = %q", got)
	}
	if config.MaxConns != 3 {
		t.Fatalf("MaxConns = %d, want 3", config.MaxConns)
	}
	// The pool it came from keeps its own search_path.
	if got, set := shared.Pool.Config().ConnConfig.RuntimeParams["search_path"]; set {
		t.Fatalf("the shared pool's search_path changed to %q", got)
	}
	if scoped.SQLX == nil {
		t.Fatal("the scoped pool has no sqlx facade")
	}
	if err := scoped.RequireExtensions("pgvector"); err != nil {
		t.Fatalf("the scoped pool lost the extensions it shares: %v", err)
	}
	if _, err := shared.WithSchema(context.Background(), "agents_primary, public", "", 0); err == nil {
		t.Fatal("WithSchema() accepted a schema that is not one identifier")
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

func TestOpenRejectsUnsafeSchemaBeforeConnecting(t *testing.T) {
	_, err := Open(context.Background(), Config{
		URL: "postgres://oryxa@localhost/oryxa", ApplicationName: "oryxa-test", Schema: "agents_primary, public",
	})
	if err == nil || !strings.Contains(err.Error(), "schema") {
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

func TestOpenSizesThePoolUnlessTheURLDoes(t *testing.T) {
	databaseURL := os.Getenv("POSTGRESKIT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("POSTGRESKIT_TEST_DATABASE_URL is not set")
	}
	sized, err := Open(context.Background(), Config{URL: databaseURL, ApplicationName: "oryxa-postgreskit-test", MaxConns: 17})
	if err != nil {
		t.Fatal(err)
	}
	defer sized.Close()
	if got := sized.Pool.Config().MaxConns; got != 17 {
		t.Fatalf("MaxConns = %d, want 17", got)
	}

	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	named, err := Open(context.Background(), Config{
		URL: databaseURL + separator + "pool_max_conns=3", ApplicationName: "oryxa-postgreskit-test", MaxConns: 17,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer named.Close()
	if got := named.Pool.Config().MaxConns; got != 3 {
		t.Fatalf("MaxConns = %d, want the URL's 3", got)
	}
}

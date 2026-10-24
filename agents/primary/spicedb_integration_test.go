package primary

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector/tokens"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestGrantsReachSpiceDBAsTheyChange writes agents through the store and
// projector.OpenWriter to a real SpiceDB. Point SPICEDB_ENDPOINT at `spicedb
// serve-testing` (SPICEDB_INSECURE=true) and AGENTS_PRIMARY_TEST_DATABASE_URL
// at a PostgreSQL. An agent's grants are read from apps' and files' tables too,
// so it puts empty ones in a schema of its own and drops it after.
func TestGrantsReachSpiceDBAsTheyChange(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if os.Getenv("SPICEDB_ENDPOINT") == "" || databaseURL == "" {
		t.Skip("SPICEDB_ENDPOINT and AGENTS_PRIMARY_TEST_DATABASE_URL are not both set")
	}
	ctx := context.Background()
	random := make([]byte, 8)
	_, _ = rand.Read(random)
	// serve-testing keeps a datastore per key, so this test has its own.
	config := spicedb.FromEnv()
	config.PresharedKey = "agents-" + hex.EncodeToString(random)
	env := map[string]string{
		"SPICEDB_ENDPOINT": config.Endpoint, "SPICEDB_PRESHARED_KEY": config.PresharedKey,
		"SPICEDB_INSECURE": os.Getenv("SPICEDB_INSECURE"),
	}
	client, err := spicedb.Dial(config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	schema, err := os.ReadFile("../../internal/access/spicedb/schema.zed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: string(schema)}); err != nil {
		t.Fatal(err)
	}

	shared, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agents-spicedb-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	// The stand-in apps and files tables live in a schema of the test's own,
	// dropped after, so they never shadow those services' real tables.
	stand := "spicedb_grants_" + hex.EncodeToString(random)
	if _, err := shared.Pool.Exec(ctx, `CREATE SCHEMA `+stand); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = shared.Pool.Exec(context.Background(), `DROP SCHEMA `+stand+` CASCADE`) }()
	database, err := shared.WithSchema(ctx, stand, "oryxa-agents-spicedb-test", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := managedagents.NewStore(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// The projector reads an agent's requests with its grants.
	if err := interactions.NewStore(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tokens.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		`CREATE TABLE ` + stand + `.apps_connections (id text, organization_id text, member_id text, toolkit text)`,
		`CREATE TABLE ` + stand + `.files_objects (id text, organization_id text, scope text, team_id text, member_id text, access text, created_by text)`,
	} {
		if _, err := database.Pool.Exec(ctx, table); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	writer, err := projector.OpenWriter(host.Platform{Getenv: func(name string) string { return env[name] }, Database: database, Logger: logger})
	if err != nil || writer == nil {
		t.Fatalf("OpenWriter() = %v, %v", writer, err)
	}
	defer writer.Close()

	scope := managedagents.Scope{OrganizationID: "org_grants_" + hex.EncodeToString(random), UserID: "user_grants_" + hex.EncodeToString(random)}
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'user_id'=$1`, scope.UserID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, scope.OrganizationID)
	})
	store := managedagents.NewStore(database).WithRelationships(agentRelationships{writer}, logger)

	held := func(filter *v1.RelationshipFilter) []string {
		t.Helper()
		stream, err := client.ReadRelationships(ctx, &v1.ReadRelationshipsRequest{
			Consistency:        &v1.Consistency{Requirement: &v1.Consistency_FullyConsistent{FullyConsistent: true}},
			RelationshipFilter: filter,
		})
		if err != nil {
			t.Fatal(err)
		}
		var rels []string
		for {
			response, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			relationship := response.GetRelationship()
			rels = append(rels, relationship.GetResource().GetObjectType()+":"+relationship.GetResource().GetObjectId()+"#"+relationship.GetRelation())
		}
		slices.Sort(rels)
		return rels
	}

	specialist, err := store.Create(ctx, scope, managedagents.Definition{Name: "Sales"})
	if err != nil {
		t.Fatal(err)
	}
	agent := &v1.SubjectFilter{SubjectType: "agent", OptionalSubjectId: specialist.ID}
	if got := held(&v1.RelationshipFilter{ResourceType: "agent", OptionalResourceId: specialist.ID}); !slices.Equal(got, []string{"agent:" + specialist.ID + "#acts_for", "agent:" + specialist.ID + "#org"}) {
		t.Fatalf("a created agent holds %v", got)
	}
	if _, err := store.Update(ctx, scope, specialist.ID, managedagents.UpdateInput{Grant: &runprofile.Grant{Corpora: []string{"cor_a", "cor_b"}}}); err != nil {
		t.Fatal(err)
	}
	if got := held(&v1.RelationshipFilter{ResourceType: "corpus", OptionalSubjectFilter: agent}); !slices.Equal(got, []string{"corpus:cor_a#granted", "corpus:cor_b#granted"}) {
		t.Fatalf("after granting: %v", got)
	}
	// Narrowing the grant is the revoke: it is gone as the update returns.
	if _, err := store.Update(ctx, scope, specialist.ID, managedagents.UpdateInput{Grant: &runprofile.Grant{Corpora: []string{"cor_b"}}}); err != nil {
		t.Fatal(err)
	}
	if got := held(&v1.RelationshipFilter{ResourceType: "corpus", OptionalSubjectFilter: agent}); !slices.Equal(got, []string{"corpus:cor_b#granted"}) {
		t.Fatalf("after narrowing: %v", got)
	}
	if err := store.Delete(ctx, scope, specialist.ID); err != nil {
		t.Fatal(err)
	}
	if got := append(held(&v1.RelationshipFilter{ResourceType: "agent", OptionalResourceId: specialist.ID}),
		held(&v1.RelationshipFilter{ResourceType: "corpus", OptionalSubjectFilter: agent})...); len(got) != 0 {
		t.Fatalf("a deleted agent left %v", got)
	}
}

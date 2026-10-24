package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"testing"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector/tokens"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestOrgRolesReachSpiceDBAsTheyChange writes through projector.OpenWriter to
// a real SpiceDB and checks each change is decided at once. Point
// SPICEDB_ENDPOINT at `spicedb serve-testing` (SPICEDB_INSECURE=true) and
// AUTH_TEST_DATABASE_URL at a PostgreSQL; it touches only access.spicedb_tokens.
func TestOrgRolesReachSpiceDBAsTheyChange(t *testing.T) {
	databaseURL := os.Getenv("AUTH_TEST_DATABASE_URL")
	if os.Getenv("SPICEDB_ENDPOINT") == "" || databaseURL == "" {
		t.Skip("SPICEDB_ENDPOINT and AUTH_TEST_DATABASE_URL are not both set")
	}
	ctx := context.Background()
	random := make([]byte, 8)
	_, _ = rand.Read(random)
	// serve-testing keeps a datastore per key, so this test has its own.
	config := spicedb.FromEnv()
	config.PresharedKey = "auth-" + hex.EncodeToString(random)
	env := map[string]string{
		"SPICEDB_ENDPOINT": config.Endpoint, "SPICEDB_PRESHARED_KEY": config.PresharedKey,
		"SPICEDB_INSECURE": os.Getenv("SPICEDB_INSECURE"),
	}
	client, err := spicedb.Dial(config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	schema, err := os.ReadFile("../internal/access/spicedb/schema.zed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: string(schema)}); err != nil {
		t.Fatal(err)
	}
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-auth-spicedb-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := tokens.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	writer, err := projector.OpenWriter(host.Platform{
		Getenv: func(name string) string { return env[name] }, Database: database,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil || writer == nil {
		t.Fatalf("OpenWriter() = %v, %v", writer, err)
	}
	defer writer.Close()
	roles := relationships{writer}
	org := "org_" + hex.EncodeToString(random)

	can := func(permission, user string) bool {
		t.Helper()
		answer, err := client.CheckPermission(ctx, &v1.CheckPermissionRequest{
			Consistency: &v1.Consistency{Requirement: &v1.Consistency_FullyConsistent{FullyConsistent: true}},
			Resource:    &v1.ObjectReference{ObjectType: "org", ObjectId: org}, Permission: permission,
			Subject: &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: "member", ObjectId: user}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return answer.GetPermissionship() == v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(roles.MemberJoined(ctx, org, "olu", domain.RoleOwner))
	must(roles.MemberJoined(ctx, org, "sam", domain.RoleAdmin))
	if !can("administer", "sam") {
		t.Fatal("a joined admin cannot administer")
	}
	must(roles.MemberRoleChanged(ctx, org, "sam", domain.RoleMember))
	if can("administer", "sam") || !can("participate", "sam") {
		t.Fatal("a demoted admin still administers, or no longer participates")
	}
	must(roles.MemberRemoved(ctx, org, "sam"))
	if can("participate", "sam") {
		t.Fatal("a removed member still participates")
	}
	must(roles.OrganizationDeleted(ctx, org))
	if can("administer", "olu") {
		t.Fatal("the owner of a deleted org still administers it")
	}
}

package edgemembers

import (
	"context"
	"os"
	"testing"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// These tests need the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/edgemembers
func TestStoreKeepsWhatWasPushed(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-edgemembers-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	store := NewStore(database)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	const org = "org_edgemembers_test"
	t.Cleanup(func() { _ = store.ForgetOrganization(context.Background(), org) })
	_ = store.ForgetOrganization(ctx, org)

	teamspace := Membership{Org: org, Room: edgev1.Room{Kind: edgev1.RoomTeamspace, ID: "tsp_x"}, Member: "usr_a", TeamspaceID: "tsp_x"}
	thread := Membership{Org: org, Room: edgev1.Room{Kind: edgev1.RoomThread, ID: "thr_x"}, Member: "usr_a"}
	for _, membership := range []Membership{teamspace, teamspace, thread} {
		if err := store.Record(ctx, membership); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.Rooms(ctx, org, "usr_a", nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("all rooms = %v, %v", all, err)
	}
	inTeamspace, err := store.Rooms(ctx, org, "usr_a", []string{"tsp_x"})
	if err != nil || len(inTeamspace) != 1 || inTeamspace[0] != teamspace {
		t.Fatalf("teamspace rooms = %v, %v", inTeamspace, err)
	}
	if exists, err := store.Exists(ctx, teamspace); err != nil || !exists {
		t.Fatalf("Exists = %v, %v", exists, err)
	}
	if rooms, err := store.InTeamspace(ctx, org, "tsp_x"); err != nil || len(rooms) != 1 || rooms[0] != teamspace {
		t.Fatalf("InTeamspace = %v, %v", rooms, err)
	}
	if err := store.Forget(ctx, teamspace); err != nil {
		t.Fatal(err)
	}
	if exists, _ := store.Exists(ctx, teamspace); exists {
		t.Error("a forgotten membership still exists")
	}
	if left, _ := store.Rooms(ctx, org, "usr_a", nil); len(left) != 1 || left[0] != thread {
		t.Errorf("left = %v", left)
	}
}

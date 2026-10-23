package access

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

func TestDisagreementsAreKeptOnceAnHourAndPurged(t *testing.T) {
	url := os.Getenv("ACCESS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ACCESS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: url, ApplicationName: "oryxa-access-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	org := fmt.Sprintf("org_shadow_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		database.Pool.Exec(context.Background(), `DELETE FROM access.disagreements WHERE organization_id = $1`, org)
	})
	log := NewLog(database.Pool, nil)
	now := time.Date(2026, 10, 23, 9, 10, 0, 0, time.UTC)
	log.now = func() time.Time { return now }

	disagreement := Disagreement{
		Ask: Ask{OrganizationID: org, Service: "files", Action: ActionRead, ResourceKind: "file", ResourceID: "f1",
			UserID: "u1", AgentID: "agt_1", Actor: Actor{Role: Admin, Agent: true, AgentRole: AgentPrimary}},
		Code: false, SpiceDB: true, Checks: []string{"file:f1#read_member_reach@member:u1"},
	}
	log.Disagree(ctx, disagreement)
	log.Disagree(ctx, disagreement) // same hour: kept once
	later := disagreement
	later.At = now.Add(time.Hour)
	log.Disagree(ctx, later) // next hour: kept again

	count := func() (n int) {
		t.Helper()
		if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM access.disagreements WHERE organization_id = $1`, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 2 {
		t.Fatalf("kept %d disagreements, want 2", n)
	}
	var role string
	var checks []string
	var code, spicedb bool
	if err := database.Pool.QueryRow(ctx, `SELECT actor_agent_role, checks, code_allowed, spicedb_allowed
		FROM access.disagreements WHERE organization_id = $1 ORDER BY at LIMIT 1`, org).Scan(&role, &checks, &code, &spicedb); err != nil {
		t.Fatal(err)
	}
	if role != AgentPrimary || len(checks) != 1 || code || !spicedb {
		t.Fatalf("kept %q %v code=%v spicedb=%v", role, checks, code, spicedb)
	}

	log.now = func() time.Time { return now.Add(Retention + 30*time.Minute) }
	if _, err := log.Purge(ctx, Retention); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("after purge %d disagreements, want the later one", n)
	}
}

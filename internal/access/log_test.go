package access

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

func TestOnlyDecisionsWorthKeepingAreKept(t *testing.T) {
	for _, c := range []struct {
		decision Decision
		want     bool
	}{
		{Decision{OwnerID: "u1", ActorUserID: "u1"}, false},
		{Decision{OwnerID: "u1", ActorUserID: "u1", ActorAgentID: "agt_1"}, true},
		{Decision{OwnerID: "u2", ActorUserID: "u1"}, true},
		{Decision{OwnerID: "", ActorUserID: "u1"}, true},
	} {
		if got := c.decision.Worth(); got != c.want {
			t.Errorf("%+v Worth = %v, want %v", c.decision, got, c.want)
		}
	}
	var none *Log
	none.Record(context.Background(), Decision{OwnerID: "u2", ActorUserID: "u1"}) // a nil log records nothing, and doesn't panic
}

func TestTheLogKeepsOneDecisionAnHourAndShowsAMemberTheirPart(t *testing.T) {
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
	for range 2 {
		if err := Migrate(ctx, database); err != nil {
			t.Fatal(err)
		}
	}
	org := fmt.Sprintf("org_access_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		database.Pool.Exec(context.Background(), `DELETE FROM access.decisions WHERE organization_id = $1`, org)
	})
	log := NewLog(database.Pool, nil)
	now := time.Date(2026, 10, 23, 9, 10, 0, 0, time.UTC)
	log.now = func() time.Time { return now }

	teammate := Decision{OrganizationID: org, Service: "files", Action: ActionRead, ResourceKind: "file", ResourceID: "f1",
		OwnerID: "u1", ActorUserID: "u2", ActorAgentID: "agt_u2", ActorAgentRole: AgentPrimary, Reason: ReasonTeamMember}
	log.Record(ctx, teammate)
	log.Record(ctx, teammate) // same hour: kept once
	later := teammate
	later.At = now.Add(time.Hour)
	log.Record(ctx, later) // next hour: kept again
	ownSpecialist := Decision{OrganizationID: org, Service: "rag", Action: ActionSearch, ResourceKind: "corpus", ResourceID: "cor_1",
		ActorUserID: "u1", ActorAgentID: "agt_u1", ActorAgentRole: AgentSpecialist, Reason: ReasonGrant}
	log.Record(ctx, ownSpecialist)
	log.Record(ctx, Decision{OrganizationID: org, Service: "files", Action: ActionRead, ResourceKind: "file", ResourceID: "f9",
		OwnerID: "u1", ActorUserID: "u1", Reason: ReasonOwner}) // their own session on their own: not kept
	log.Record(ctx, Decision{OrganizationID: org, Service: "files", Action: ActionRead, ResourceKind: "file", ResourceID: "f7",
		OwnerID: "u3", ActorUserID: "u2", Reason: ReasonOrgAdmin}) // nobody u1 cares about

	part, err := log.ForMember(ctx, org, "u1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(part) != 3 {
		t.Fatalf("u1 sees %d decisions, want 3: %+v", len(part), part)
	}
	if part[0].ResourceID != "f1" || !part[0].At.Equal(later.At) || part[0].Reason != ReasonTeamMember {
		t.Errorf("newest first: %+v", part[0])
	}

	log.now = func() time.Time { return now.Add(Retention + 30*time.Minute) }
	purged, err := log.Purge(ctx, Retention)
	if err != nil || purged < 2 {
		t.Fatalf("purge = %d, %v; want the two from the first hour", purged, err)
	}
	if part, _ := log.ForMember(ctx, org, "u1", 50); len(part) != 1 || part[0].ResourceID != "f1" {
		t.Fatalf("after purge u1 sees %+v, want only the later decision", part)
	}
}

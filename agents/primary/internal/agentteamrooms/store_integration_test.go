package agentteamrooms

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

func TestStorePersistsScopedConcurrentTeamConversation(t *testing.T) {
	url := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: url, ApplicationName: "oryxa-agent-team-rooms-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	agents := managedagents.NewStore(database)
	if err := agents.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	teams := agentteams.NewStore(database)
	if err := teams.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	scope := agentteams.Scope{OrganizationID: fmt.Sprintf("org_team_room_%d", suffix), UserID: fmt.Sprintf("user_team_room_%d", suffix)}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agent_teams WHERE organization_id=$1`, scope.OrganizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, scope.OrganizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, scope.OrganizationID)
	}()
	if _, err := agents.EnsurePrimary(ctx, scope); err != nil {
		t.Fatal(err)
	}
	specialist, err := agents.Create(ctx, scope, managedagents.Definition{Name: "Research", Instructions: "Verify claims."})
	if err != nil {
		t.Fatal(err)
	}
	teamService, err := agentteams.New(teams)
	if err != nil {
		t.Fatal(err)
	}
	team, err := teamService.Create(ctx, scope, agentteams.CreateInput{Name: "Research cell", Members: []agentteams.MemberInput{{AgentID: specialist.ID, Role: "Verify claims"}}})
	if err != nil {
		t.Fatal(err)
	}

	const count = 8
	var group sync.WaitGroup
	errs := make([]error, count)
	for i := range count {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			_, errs[i] = store.Append(ctx, scope, Message{TeamID: team.ID, Kind: "member", AuthorName: "You", Body: fmt.Sprintf("message %d", i)})
		}(i)
	}
	group.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	listed, err := store.List(ctx, scope, team.ID, 0, 100)
	if err != nil || len(listed) != count {
		t.Fatalf("listed=%+v error=%v", listed, err)
	}
	for i, message := range listed {
		if message.Seq != int64(i+1) {
			t.Fatalf("sequence %d at index %d", message.Seq, i)
		}
	}
	latest, err := store.List(ctx, scope, team.ID, 0, 3)
	if err != nil || len(latest) != 3 || latest[0].Seq != 6 {
		t.Fatalf("latest=%+v error=%v", latest, err)
	}
	recent, err := store.Recent(ctx, scope, team.ID, 3)
	if err != nil || len(recent) != 3 || recent[0].Seq != 6 {
		t.Fatalf("recent=%+v error=%v", recent, err)
	}
	if err := store.SetThread(ctx, scope, team.ID, specialist.ID, "thread_1"); err != nil {
		t.Fatal(err)
	}
	threadID, err := store.Thread(ctx, scope, team.ID, specialist.ID)
	if err != nil || threadID != "thread_1" {
		t.Fatalf("thread=%q error=%v", threadID, err)
	}
	foreign := agentteams.Scope{OrganizationID: scope.OrganizationID, UserID: "other"}
	hidden, err := store.List(ctx, foreign, team.ID, 0, 100)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("foreign read=%+v error=%v", hidden, err)
	}
	if err := teamService.Delete(ctx, scope, team.ID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.member_agent_team_messages WHERE team_id=$1`, team.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("messages survived team removal: %d", remaining)
	}
}

package organizationagents

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestStoreLifecycle needs the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/organizationagents
func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-organization-agent-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	agentStore := managedagents.NewStore(database)
	if err := agentStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	teamspaceStore := teamspaces.NewStore(database)
	if err := teamspaceStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_synthesis_test_%d", suffix)
	userID := fmt.Sprintf("user_synthesis_test_%d", suffix)
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agent_followups WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agent_deliveries WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agent_syntheses WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	}()

	author, err := agentStore.Create(ctx, managedagents.Scope{OrganizationID: organizationID, UserID: userID}, managedagents.Definition{
		Name: "Author", Description: "Publishes bounded shared work", Instructions: "Report attributable results.",
	})
	if err != nil {
		t.Fatal(err)
	}
	organizationAgent, err := teamspaceStore.EnsureOrganizationAgent(ctx, organizationID, "Synthesis Test", userID)
	if err != nil {
		t.Fatal(err)
	}
	space, err := teamspaceStore.Create(ctx, teamspaces.Space{
		ID: fmt.Sprintf("tsp_%d", suffix), OrganizationID: organizationID, TeamID: fmt.Sprintf("team_%d", suffix),
		Name: "Shared Context", CreatedByUserID: userID, OrganizationAgentID: organizationAgent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := teamspaceStore.PublishEntry(ctx, teamspaces.Entry{
		ID: fmt.Sprintf("tse_first_%d", suffix), TeamspaceID: space.ID, OrganizationID: organizationID,
		AuthorUserID: userID, AuthorAgentID: author.ID, AuthorAgentName: author.Name,
		Kind: teamspaces.EntryDecision, Title: "Ship", Summary: "Ship the bounded release.",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := teamspaceStore.PublishEntry(ctx, teamspaces.Entry{
		ID: fmt.Sprintf("tse_second_%d", suffix), TeamspaceID: space.ID, OrganizationID: organizationID,
		AuthorUserID: userID, AuthorAgentID: author.ID, AuthorAgentName: author.Name,
		Kind: teamspaces.EntryUpdate, Title: "Capacity", Summary: "Capacity remains open.",
	})
	if err != nil {
		t.Fatal(err)
	}

	firstDelivery := Delivery{
		EventID: fmt.Sprintf("evt_first_%d", suffix), EntryID: first.ID, TeamspaceID: space.ID,
		OrganizationID: organizationID, AuthorUserID: userID, AuthorAgentID: first.AuthorAgentID,
		Kind: string(first.Kind), PublishedAt: first.CreatedAt,
	}
	secondDelivery := Delivery{
		EventID: fmt.Sprintf("evt_second_%d", suffix), EntryID: second.ID, TeamspaceID: space.ID,
		OrganizationID: organizationID, AuthorUserID: userID, AuthorAgentID: second.AuthorAgentID,
		Kind: string(second.Kind), PublishedAt: second.CreatedAt,
	}
	created, err := store.Record(ctx, firstDelivery)
	if err != nil || !created {
		t.Fatalf("record first created=%v error=%v", created, err)
	}
	created, err = store.Record(ctx, firstDelivery)
	if err != nil || created {
		t.Fatalf("record duplicate created=%v error=%v", created, err)
	}
	redelivery := firstDelivery
	redelivery.EventID = fmt.Sprintf("evt_first_redelivered_%d", suffix)
	created, err = store.Record(ctx, redelivery)
	if err != nil || created {
		t.Fatalf("record same entry with a new event ID created=%v error=%v", created, err)
	}
	if created, err = store.Record(ctx, secondDelivery); err != nil || !created {
		t.Fatalf("record second created=%v error=%v", created, err)
	}

	claimed, err := store.Claim(ctx, time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].EventID != firstDelivery.EventID {
		t.Fatalf("first claim=%+v error=%v", claimed, err)
	}
	// One organization's later publication cannot overtake its earlier one,
	// even while the earlier delivery is leased.
	claimed, err = store.Claim(ctx, time.Minute, 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("overtaking claim=%+v error=%v", claimed, err)
	}

	result := Result{
		Summary: "The release is ready.", Decisions: []string{"Ship"}, Risks: []string{"Capacity"},
		OpenQuestions: []string{"Who owns rollout?"},
		FollowUps:     []FollowUpResult{{TargetAgentID: author.ID, Task: "Confirm rollout owner", Rationale: "Ownership remains open"}},
	}
	synthesis, err := store.Apply(ctx, firstDelivery, organizationAgent.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	if synthesis.Revision != 1 || synthesis.TeamspaceID != space.ID || synthesis.AgentID != organizationAgent.ID || synthesis.SourceEntryID != first.ID ||
		synthesis.Summary != result.Summary || len(synthesis.Decisions) != 1 || len(synthesis.Risks) != 1 || len(synthesis.OpenQuestions) != 1 {
		t.Fatalf("synthesis=%+v", synthesis)
	}
	followUps, err := store.ListFollowUps(ctx, organizationID, Audience{All: true}, 10)
	if err != nil || len(followUps) != 1 || followUps[0].Status != FollowUpProposed || followUps[0].Task != result.FollowUps[0].Task {
		t.Fatalf("follow-ups=%+v error=%v", followUps, err)
	}
	// Replaying the worker transaction is a no-op: no new revision, proposal,
	// or lifecycle event is produced.
	again, err := store.Apply(ctx, firstDelivery, organizationAgent.ID, result)
	if err != nil || again.Revision != 1 {
		t.Fatalf("replayed synthesis=%+v error=%v", again, err)
	}
	followUps, err = store.ListFollowUps(ctx, organizationID, Audience{All: true}, 10)
	if err != nil || len(followUps) != 1 {
		t.Fatalf("replayed follow-ups=%+v error=%v", followUps, err)
	}
	// What one team posted reaches only that team's synthesis and follow-ups:
	// not another team's, and not the organization-wide one every member reads.
	if got, err := store.Synthesis(ctx, organizationID, ""); err != nil || got.Revision != 0 || got.Summary != "" {
		t.Fatalf("organization-wide synthesis took a team-only post: %+v error=%v", got, err)
	}
	if got, err := store.Synthesis(ctx, organizationID, "tsp_other_team"); err != nil || got.Revision != 0 || got.Summary != "" {
		t.Fatalf("another team's synthesis took this team's post: %+v error=%v", got, err)
	}
	for _, audience := range []Audience{{}, {Teamspaces: []string{"tsp_other_team"}}} {
		if followUps, err := store.ListFollowUps(ctx, organizationID, audience, 10); err != nil || len(followUps) != 0 {
			t.Fatalf("audience %+v read another team's follow-ups: %+v error=%v", audience, followUps, err)
		}
	}
	if followUps, err := store.ListFollowUps(ctx, organizationID, Audience{Teamspaces: []string{space.ID}}, 10); err != nil || len(followUps) != 1 {
		t.Fatalf("the team's own follow-ups=%+v error=%v", followUps, err)
	}
	var synthesisEvents int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail->>'organization_id'=$1 AND detail_type=$2`, organizationID, agentsv1.OrganizationAgentSynthesisUpdatedDetailType).Scan(&synthesisEvents); err != nil {
		t.Fatal(err)
	}
	if synthesisEvents != 1 {
		t.Fatalf("synthesis lifecycle events=%d, want 1", synthesisEvents)
	}
	claimed, err = store.Claim(ctx, time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].EventID != secondDelivery.EventID {
		t.Fatalf("second claim=%+v error=%v", claimed, err)
	}
	if err := store.Drop(ctx, secondDelivery.EventID, "test complete"); err != nil {
		t.Fatal(err)
	}

	if err := store.Forget(ctx, organizationID, userID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Synthesis(ctx, organizationID, space.ID); err != nil || got.Revision != 1 {
		t.Fatalf("member cleanup altered org synthesis=%+v error=%v", got, err)
	}
	if followUps, err := store.ListFollowUps(ctx, organizationID, Audience{All: true}, 10); err != nil || len(followUps) != 1 || followUps[0].TargetAgentID != "" {
		t.Fatalf("member cleanup left private target=%+v error=%v", followUps, err)
	}
	if err := store.Forget(ctx, organizationID, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Synthesis(ctx, organizationID, space.ID); err != nil || got.Revision != 0 {
		t.Fatalf("organization cleanup synthesis=%+v error=%v", got, err)
	}
	if followUps, err := store.ListFollowUps(ctx, organizationID, Audience{All: true}, 10); err != nil || len(followUps) != 0 {
		t.Fatalf("organization cleanup follow-ups=%+v error=%v", followUps, err)
	}
}

// TestAnInterruptedDeliveryIsReleasedWithoutUsingAnAttempt is what a deploy
// depends on: the processor releases a synthesis it was cut short in without
// counting the attempt, however many deploys it is cut short by, while one
// that failed is dropped once it has used its attempts. It needs the same
// PostgreSQL as TestStoreLifecycle.
func TestAnInterruptedDeliveryIsReleasedWithoutUsingAnAttempt(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-organization-agent-test", Schema: "public"})
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
	if err := teamspaces.NewStore(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_release_test_%d", suffix)
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agent_deliveries WHERE organization_id=$1`, organizationID)
	}()
	delivery := Delivery{
		EventID: fmt.Sprintf("evt_release_%d", suffix), EntryID: fmt.Sprintf("tse_release_%d", suffix),
		TeamspaceID: "tsp_release", OrganizationID: organizationID, AuthorUserID: "user_release", AuthorAgentID: "agt_release",
		Kind: "decision", PublishedAt: time.Now().UTC(),
	}
	if created, err := store.Record(ctx, delivery); err != nil || !created {
		t.Fatalf("record created=%v error=%v", created, err)
	}
	state := func() (attempts int, handled, dropped bool, reason string) {
		t.Helper()
		if err := database.Pool.QueryRow(ctx, `SELECT attempts, handled_at IS NOT NULL, dropped, last_error
			FROM agents_primary.organization_agent_deliveries WHERE event_id=$1`, delivery.EventID).Scan(&attempts, &handled, &dropped, &reason); err != nil {
			t.Fatal(err)
		}
		return attempts, handled, dropped, reason
	}

	const maxAttempts = 3
	for range 2 * maxAttempts {
		if dropped, err := store.Release(ctx, delivery.EventID, false, maxAttempts, 0, "interrupted: The run was cancelled."); err != nil || dropped {
			t.Fatalf("release without a failure dropped=%v error=%v", dropped, err)
		}
	}
	if attempts, handled, dropped, reason := state(); attempts != 0 || handled || dropped || reason != "interrupted: The run was cancelled." {
		t.Fatalf("after %d interruptions attempts=%d handled=%v dropped=%v reason=%q, want none used and the reason kept", 2*maxAttempts, attempts, handled, dropped, reason)
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		dropped, err := store.Release(ctx, delivery.EventID, true, maxAttempts, 0, "model unavailable")
		if err != nil || dropped != (attempt == maxAttempts) {
			t.Fatalf("failed release %d dropped=%v error=%v, want it dropped only at attempt %d", attempt, dropped, err, maxAttempts)
		}
	}
	if attempts, handled, dropped, _ := state(); attempts != maxAttempts || !handled || !dropped {
		t.Fatalf("after %d failures attempts=%d handled=%v dropped=%v, want it dropped", maxAttempts, attempts, handled, dropped)
	}
}

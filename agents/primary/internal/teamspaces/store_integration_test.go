package teamspaces

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// testStores opens the PostgreSQL the integration tests share, with the
// teamspaces store's tables and those it relies on migrated, and returns the
// store and the managed agents' store beside it. They need:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/teamspaces
func testStores(t *testing.T) (*postgreskit.Database, *Store, *managedagents.Store) {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-teamspaces-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	agentStore := managedagents.NewStore(database)
	if err := agentStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return database, store, agentStore
}

func TestStoreLifecycle(t *testing.T) {
	database, store, agentStore := testStores(t)
	ctx := context.Background()

	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_teamspace_test_%d", suffix)
	owner := managedagents.Scope{OrganizationID: organizationID, UserID: fmt.Sprintf("user_owner_%d", suffix)}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	}()

	specialist, err := agentStore.Create(ctx, owner, managedagents.Definition{Name: "Reviewer", Description: "Reviews architecture", Instructions: "Return bounded findings."})
	if err != nil {
		t.Fatal(err)
	}
	organizationAgent, err := store.EnsureOrganizationAgent(ctx, organizationID, "Integration Org", owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.EnsureOrganizationAgent(ctx, organizationID, "A different label", owner.UserID)
	if err != nil || again.ID != organizationAgent.ID {
		t.Fatalf("singleton organization agent=%+v again=%+v error=%v", organizationAgent, again, err)
	}
	space, err := store.Create(ctx, Space{
		ID: fmt.Sprintf("tsp_%d", suffix), OrganizationID: organizationID, TeamID: fmt.Sprintf("team_%d", suffix),
		Name: "Engineering Space", CreatedByUserID: owner.UserID,
		OrganizationAgentID: organizationAgent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if space.OrganizationAgent.ID != organizationAgent.ID {
		t.Fatalf("space organization agent=%+v", space.OrganizationAgent)
	}
	space, err = store.AddContribution(ctx, Actor{OrganizationID: organizationID, UserID: owner.UserID}, space.ID, ContributionInput{
		AgentID: specialist.ID, Role: "Review architecture", DelegationMode: DelegationAutomatic,
	})
	if err != nil || len(space.Contributions) != 1 || space.Revision != 2 {
		t.Fatalf("contributed=%+v error=%v", space, err)
	}
	entry, err := store.PublishEntry(ctx, Entry{
		ID: fmt.Sprintf("tse_%d", suffix), TeamspaceID: space.ID, OrganizationID: organizationID,
		AuthorUserID: owner.UserID, AuthorAgentID: specialist.ID, AuthorAgentName: specialist.Name,
		Kind: EntryResult, Title: "Architecture review", Summary: "Ready with one follow-up.",
		ToolsUsed: []string{"github.search"}, Artifacts: []EntryArtifact{{Name: "Review", URI: "https://example.test/review", MediaType: "text/markdown"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListEntries(ctx, organizationID, space.ID, 10)
	if err != nil || len(entries) != 1 || entries[0].ID != entry.ID || len(entries[0].Artifacts) != 1 {
		t.Fatalf("entries=%+v error=%v", entries, err)
	}
	if err := agentStore.Delete(ctx, owner, specialist.ID); !errors.Is(err, managedagents.ErrInUse) {
		t.Fatalf("delete contributed specialist error=%v", err)
	}

	request := DelegationRequest
	updated, err := store.UpdateContribution(ctx, Actor{OrganizationID: organizationID, UserID: owner.UserID}, space.ID, specialist.ID, ContributionUpdate{DelegationMode: &request})
	if err != nil || updated.Contributions[0].DelegationMode != DelegationRequest || updated.Revision != 3 {
		t.Fatalf("updated=%+v error=%v", updated, err)
	}
	if err := store.ForgetTeamMember(ctx, organizationID, space.TeamID, owner.UserID, time.Now()); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.Get(ctx, organizationID, space.ID)
	if err != nil || len(remaining.Contributions) != 0 || remaining.Revision != 4 {
		t.Fatalf("member cleanup space=%+v error=%v", remaining, err)
	}
	if err := agentStore.Delete(ctx, owner, specialist.ID); err != nil {
		t.Fatalf("delete specialist after contribution withdrawal: %v", err)
	}
	if err := store.ForgetTeam(ctx, organizationID, space.TeamID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, organizationID, space.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted human team left space: %v", err)
	}
	var events int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail->>'organization_id'=$1 AND detail_type LIKE 'agents.teamspace.%'`, organizationID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	// Created, the contribution added and changed, the entry, the owner
	// leaving the team, which withdrew their contribution and said they left,
	// and the teamspace deleted.
	if events != 7 {
		t.Fatalf("teamspace lifecycle events=%d, want 7", events)
	}
	var organizationAgentEvents int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail->>'organization_id'=$1 AND detail_type LIKE 'agents.organization-agent.%'`, organizationID).Scan(&organizationAgentEvents); err != nil {
		t.Fatal(err)
	}
	if organizationAgentEvents != 1 {
		t.Fatalf("organization agent lifecycle events=%d, want 1", organizationAgentEvents)
	}
}

// recordedEvent is one event an organization's teamspaces recorded in the
// outbox.
type recordedEvent struct {
	detail     []byte
	occurredAt time.Time
}

// recordedEvents are the events of detailType organizationID's teamspaces
// have recorded in the outbox, in no particular order.
func recordedEvents(t *testing.T, database *postgreskit.Database, organizationID, detailType string) []recordedEvent {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `SELECT detail::text, occurred_at
		FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1 AND detail_type=$2`, organizationID, detailType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var recorded []recordedEvent
	for rows.Next() {
		var event recordedEvent
		var detail string
		if err := rows.Scan(&detail, &event.occurredAt); err != nil {
			t.Fatal(err)
		}
		event.detail = []byte(detail)
		recorded = append(recorded, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return recorded
}

// membersRemoved are the people organizationID's teamspaces have said left
// them, in no particular order.
func membersRemoved(t *testing.T, database *postgreskit.Database, organizationID string) []agentsv1.TeamspaceMemberRemoved {
	t.Helper()
	var removed []agentsv1.TeamspaceMemberRemoved
	for _, event := range recordedEvents(t, database, organizationID, agentsv1.TeamspaceMemberRemovedDetailType) {
		var payload agentsv1.TeamspaceMemberRemoved
		if err := json.Unmarshal(event.detail, &payload); err != nil {
			t.Fatal(err)
		}
		// The event happened when the membership ended, which the outbox
		// keeps to the microsecond.
		if !event.occurredAt.Equal(payload.RemovedAt.Truncate(time.Microsecond)) {
			t.Errorf("the event happened at %s, want when the membership ended, %s", event.occurredAt, payload.RemovedAt)
		}
		removed = append(removed, payload)
	}
	return removed
}

// When Teams says a person left a team, agents says they left its teamspace,
// whether or not they had contributed agents to it, in the transaction that
// withdraws what they had. A team without a teamspace says nothing, and a
// Teams event delivered again is safe.
func TestForgetTeamMemberSaysThePersonLeftTheTeamspace(t *testing.T) {
	database, store, agentStore := testStores(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_teamspace_leave_%d", suffix)
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	})

	organizationAgent, err := store.EnsureOrganizationAgent(ctx, organizationID, "Leaving Org", "user_admin")
	if err != nil {
		t.Fatal(err)
	}
	space, err := store.Create(ctx, Space{
		ID: fmt.Sprintf("tsp_leave_%d", suffix), OrganizationID: organizationID, TeamID: fmt.Sprintf("team_leave_%d", suffix),
		Name: "Design Space", CreatedByUserID: "user_admin", OrganizationAgentID: organizationAgent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	contributor := managedagents.Scope{OrganizationID: organizationID, UserID: fmt.Sprintf("user_contributor_%d", suffix)}
	specialist, err := agentStore.Create(ctx, contributor, managedagents.Definition{Name: "Critic", Description: "Critiques designs", Instructions: "Return bounded critiques."})
	if err != nil {
		t.Fatal(err)
	}
	space, err = store.AddContribution(ctx, Actor{OrganizationID: organizationID, UserID: contributor.UserID}, space.ID, ContributionInput{
		AgentID: specialist.ID, Role: "Critique designs", DelegationMode: DelegationAutomatic,
	})
	if err != nil || len(space.Contributions) != 1 {
		t.Fatalf("contributed=%+v error=%v", space, err)
	}
	contributionChanges := func() int {
		return len(recordedEvents(t, database, organizationID, agentsv1.TeamspaceContributionChangedDetailType))
	}
	changes := contributionChanges()
	// Teams' clock gives nanoseconds, in whatever zone; the event says UTC.
	ist := time.FixedZone("IST", 5*60*60+30*60)
	removedAt := time.Date(2026, time.September, 24, 15, 30, 0, 123456789, ist)
	leaving := func(userID string) agentsv1.TeamspaceMemberRemoved {
		return agentsv1.TeamspaceMemberRemoved{
			OrganizationID: organizationID, TeamspaceID: space.ID, TeamID: space.TeamID, UserID: userID,
			RemovedAt: time.Date(2026, time.September, 24, 10, 0, 0, 123456789, time.UTC),
		}
	}
	// same compares events field by field, their times as instants that
	// were written in UTC.
	same := func(got, want []agentsv1.TeamspaceMemberRemoved) bool {
		return slices.EqualFunc(got, want, func(a, b agentsv1.TeamspaceMemberRemoved) bool {
			return a.OrganizationID == b.OrganizationID && a.TeamspaceID == b.TeamspaceID && a.TeamID == b.TeamID &&
				a.UserID == b.UserID && a.RemovedAt.Equal(b.RemovedAt) && a.RemovedAt.Location() == time.UTC
		})
	}

	t.Run("a person who contributed nothing", func(t *testing.T) {
		watcher := fmt.Sprintf("user_watcher_%d", suffix)
		if err := store.ForgetTeamMember(ctx, organizationID, space.TeamID, watcher, removedAt); err != nil {
			t.Fatal(err)
		}
		if got, want := membersRemoved(t, database, organizationID), []agentsv1.TeamspaceMemberRemoved{leaving(watcher)}; !same(got, want) {
			t.Fatalf("members removed = %+v, want %+v", got, want)
		}
		remaining, err := store.Get(ctx, organizationID, space.ID)
		if err != nil || len(remaining.Contributions) != 1 || remaining.Revision != space.Revision || contributionChanges() != changes {
			t.Fatalf("space=%+v error=%v contribution changes=%d; want the contributor's specialist left alone", remaining, err, contributionChanges())
		}
	})

	t.Run("a person who contributed", func(t *testing.T) {
		if err := store.ForgetTeamMember(ctx, organizationID, space.TeamID, contributor.UserID, removedAt); err != nil {
			t.Fatal(err)
		}
		removed := membersRemoved(t, database, organizationID)
		if len(removed) != 2 || !slices.ContainsFunc(removed, func(event agentsv1.TeamspaceMemberRemoved) bool {
			return same([]agentsv1.TeamspaceMemberRemoved{event}, []agentsv1.TeamspaceMemberRemoved{leaving(contributor.UserID)})
		}) {
			t.Fatalf("members removed = %+v, want the contributor among them", removed)
		}
		remaining, err := store.Get(ctx, organizationID, space.ID)
		if err != nil || len(remaining.Contributions) != 0 || remaining.Revision != space.Revision+1 || contributionChanges() != changes+1 {
			t.Fatalf("space=%+v error=%v contribution changes=%d; want the specialist withdrawn, and said", remaining, err, contributionChanges())
		}
	})

	t.Run("the same Teams event again", func(t *testing.T) {
		if err := store.ForgetTeamMember(ctx, organizationID, space.TeamID, contributor.UserID, removedAt); err != nil {
			t.Fatal(err)
		}
		removed := membersRemoved(t, database, organizationID)
		var contributorLeft int
		for _, event := range removed {
			if same([]agentsv1.TeamspaceMemberRemoved{event}, []agentsv1.TeamspaceMemberRemoved{leaving(contributor.UserID)}) {
				contributorLeft++
			}
		}
		// Said again, as it was the first time, which its consumers take as
		// a repeat; nothing is withdrawn twice.
		remaining, err := store.Get(ctx, organizationID, space.ID)
		if len(removed) != 3 || contributorLeft != 2 || err != nil || remaining.Revision != space.Revision+1 || contributionChanges() != changes+1 {
			t.Fatalf("members removed = %+v, space=%+v error=%v contribution changes=%d", removed, remaining, err, contributionChanges())
		}
	})

	t.Run("a team without a teamspace", func(t *testing.T) {
		before := len(membersRemoved(t, database, organizationID))
		if err := store.ForgetTeamMember(ctx, organizationID, fmt.Sprintf("team_spaceless_%d", suffix), contributor.UserID, removedAt); err != nil {
			t.Fatal(err)
		}
		if after := len(membersRemoved(t, database, organizationID)); after != before {
			t.Fatalf("members removed = %d, want %d: a team without a teamspace says nothing", after, before)
		}
	})
}

// Without its team, a person's leaving would withdraw their specialists from
// every teamspace in the organization, and without its time, the purge it
// asks for could not tell what they wrote before from what they wrote after.
func TestForgetTeamMemberRefusesWhatItCannotBound(t *testing.T) {
	// Refused before anything is asked of the database, which it has none of.
	store := NewStore(nil)
	now := time.Now()
	for name, call := range map[string]struct {
		organization, team, member string
		removedAt                  time.Time
	}{
		"no organization": {"", "team_1", "user_1", now},
		"no team":         {"org_1", "", "user_1", now},
		"no member":       {"org_1", "team_1", "", now},
		"no time":         {"org_1", "team_1", "user_1", time.Time{}},
	} {
		if err := store.ForgetTeamMember(context.Background(), call.organization, call.team, call.member, call.removedAt); err == nil {
			t.Errorf("%s: ForgetTeamMember() = nil", name)
		}
	}
}

// TestStoreNamesATeamspaceInItsOrganizationOnly: a teamspace's name is read
// by its organization and id together, so another organization's teamspace,
// or one that does not exist, has none.
func TestStoreNamesATeamspaceInItsOrganizationOnly(t *testing.T) {
	database, store, _ := testStores(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_teamspace_name_test_%d", suffix)
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	}()
	organizationAgent, err := store.EnsureOrganizationAgent(ctx, organizationID, "Naming Org", "user_owner")
	if err != nil {
		t.Fatal(err)
	}
	space, err := store.Create(ctx, Space{
		ID: fmt.Sprintf("tsp_name_%d", suffix), OrganizationID: organizationID, TeamID: fmt.Sprintf("team_name_%d", suffix),
		Name: "Launch", CreatedByUserID: "user_owner", OrganizationAgentID: organizationAgent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if name, err := store.Name(ctx, organizationID, space.ID); err != nil || name != "Launch" {
		t.Fatalf("Name() = %q, %v, want Launch", name, err)
	}
	for _, other := range [][2]string{{organizationID + "_other", space.ID}, {organizationID, space.ID + "_missing"}} {
		if name, err := store.Name(ctx, other[0], other[1]); !errors.Is(err, ErrNotFound) {
			t.Errorf("Name(%q, %q) = %q, %v, want ErrNotFound", other[0], other[1], name, err)
		}
	}
}

// A teamspace's target and its pause facts are stored as the service set
// them, read back as it gave them, and cleared by null.
func TestStoreKeepsTheTargetAndWhoPaused(t *testing.T) {
	database, store, _ := testStores(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_teamspace_target_%d", suffix)
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	}()
	organizationAgent, err := store.EnsureOrganizationAgent(ctx, organizationID, "Target Org", "user_owner")
	if err != nil {
		t.Fatal(err)
	}
	space, err := store.Create(ctx, Space{
		ID: fmt.Sprintf("tsp_target_%d", suffix), OrganizationID: organizationID, TeamID: "team_target",
		Name: "Launch", CreatedByUserID: "user_owner", OrganizationAgentID: organizationAgent.ID,
	})
	if err != nil || space.TargetDate != nil || space.TargetLabel != nil || space.PausedAt != nil {
		t.Fatalf("created = %+v, %v", space, err)
	}

	date, label := "2026-11-02", "Public launch"
	disabled := StatusDisabled
	updated, err := store.Update(ctx, organizationID, space.ID, "user_pauser", UpdateInput{
		Status: &disabled, TargetDate: Nullable{Set: true, Value: &date}, TargetLabel: Nullable{Set: true, Value: &label},
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Get(ctx, organizationID, space.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []Space{updated, read} {
		if got.TargetDate == nil || *got.TargetDate != date || got.TargetLabel == nil || *got.TargetLabel != label ||
			got.Status != StatusDisabled || got.PausedByUserID == nil || *got.PausedByUserID != "user_pauser" || got.PausedAt == nil {
			t.Fatalf("paused with a target = %+v", got)
		}
	}

	active := StatusActive
	resumed, err := store.Update(ctx, organizationID, space.ID, "user_owner", UpdateInput{Status: &active, TargetDate: Nullable{Set: true}})
	if err != nil || resumed.PausedByUserID != nil || resumed.PausedAt != nil || resumed.TargetDate != nil ||
		resumed.TargetLabel == nil || *resumed.TargetLabel != label {
		t.Fatalf("resumed = %+v, %v", resumed, err)
	}
	listed, err := store.List(ctx, organizationID)
	if err != nil || len(listed) != 1 || listed[0].TargetLabel == nil || listed[0].PausedAt != nil {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
}

// A teamspace's checklist is kept in order, its counts are read with the
// teamspace in the same statement, and somebody leaving the team leaves their
// tasks unowned rather than gone.
func TestStoreKeepsAChecklist(t *testing.T) {
	database, store, _ := testStores(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_teamspace_tasks_%d", suffix)
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	}()
	organizationAgent, err := store.EnsureOrganizationAgent(ctx, organizationID, "Tasks Org", "user_owner")
	if err != nil {
		t.Fatal(err)
	}
	space, err := store.Create(ctx, Space{
		ID: fmt.Sprintf("tsp_tasks_%d", suffix), OrganizationID: organizationID, TeamID: "team_tasks",
		Name: "Launch", CreatedByUserID: "user_owner", OrganizationAgentID: organizationAgent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	owner, due := "user_grace", "2026-10-30"
	first, err := store.CreateTask(ctx, Task{ID: fmt.Sprintf("tst_a_%d", suffix), TeamspaceID: space.ID, OrganizationID: organizationID,
		Title: "Draft the post", OwnerUserID: &owner, DueDate: &due, Position: -1, CreatedByUserID: "user_owner"})
	if err != nil || first.Position != 0 || first.DueDate == nil || *first.DueDate != due || *first.OwnerUserID != owner || first.Done {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := store.CreateTask(ctx, Task{ID: fmt.Sprintf("tst_b_%d", suffix), TeamspaceID: space.ID, OrganizationID: organizationID,
		Title: "Book the venue", Position: -1, CreatedByUserID: "user_owner"})
	if err != nil || second.Position != 1 || second.OwnerUserID != nil || second.DueDate != nil {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if _, err := store.CreateTask(ctx, Task{ID: fmt.Sprintf("tst_c_%d", suffix), TeamspaceID: "tsp_missing", OrganizationID: organizationID,
		Title: "Nowhere", Position: -1, CreatedByUserID: "user_owner"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a task in no teamspace: %v, want ErrNotFound", err)
	}

	ticked, err := store.UpdateTask(ctx, organizationID, space.ID, first.ID, TaskUpdate{Done: ptr(true), Position: ptr(4), DueDate: Nullable{Set: true}})
	if err != nil || !ticked.Done || ticked.DoneAt == nil || ticked.Position != 4 || ticked.DueDate != nil || *ticked.OwnerUserID != owner {
		t.Fatalf("ticked = %+v, %v", ticked, err)
	}
	if _, err := store.UpdateTask(ctx, organizationID, space.ID, "tst_missing", TaskUpdate{Done: ptr(true)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update a missing task: %v, want ErrNotFound", err)
	}
	tasks, err := store.ListTasks(ctx, organizationID, space.ID)
	if err != nil || len(tasks) != 2 || tasks[0].ID != second.ID || tasks[1].ID != first.ID {
		t.Fatalf("tasks = %+v, %v", tasks, err)
	}

	read, err := store.Get(ctx, organizationID, space.ID)
	if err != nil || read.TaskCounts != (TaskCounts{Done: 1, Total: 2}) {
		t.Fatalf("counts on get = %+v, %v", read.TaskCounts, err)
	}
	listed, err := store.List(ctx, organizationID)
	if err != nil || len(listed) != 1 || listed[0].TaskCounts != (TaskCounts{Done: 1, Total: 2}) {
		t.Fatalf("counts on list = %+v, %v", listed, err)
	}

	if err := store.ForgetTeamMember(ctx, organizationID, space.TeamID, owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	if kept, err := store.GetTask(ctx, organizationID, space.ID, first.ID); err != nil || kept.OwnerUserID != nil {
		t.Fatalf("a departed member's task = %+v, %v; want it kept with no owner", kept, err)
	}
	// Leaving the organization, not just the team, does the same.
	leaver := "user_ada"
	if _, err := store.UpdateTask(ctx, organizationID, space.ID, second.ID, TaskUpdate{OwnerUserID: Nullable{Set: true, Value: &leaver}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Forget(ctx, organizationID, leaver); err != nil {
		t.Fatal(err)
	}
	if kept, err := store.GetTask(ctx, organizationID, space.ID, second.ID); err != nil || kept.OwnerUserID != nil {
		t.Fatalf("a departed person's task = %+v, %v; want it kept with no owner", kept, err)
	}
	if err := store.DeleteTask(ctx, organizationID, space.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTask(ctx, organizationID, space.ID, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v, want ErrNotFound", err)
	}

	// Filled to maxTasks, the checklist takes no more.
	if _, err := database.Pool.Exec(ctx, `INSERT INTO agents_primary.teamspace_tasks
		(id, teamspace_id, organization_id, title, position, created_by_user_id)
		SELECT $1 || n, $2, $3, 'Filler', n, 'user_owner' FROM generate_series(1, $4::integer) n`,
		fmt.Sprintf("tst_fill_%d_", suffix), space.ID, organizationID, maxTasks-1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(ctx, Task{ID: fmt.Sprintf("tst_d_%d", suffix), TeamspaceID: space.ID, OrganizationID: organizationID,
		Title: "One too many", Position: -1, CreatedByUserID: "user_owner"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a task past %d: %v, want ErrInvalid", maxTasks, err)
	}
}

// A release from before the pause columns resumes a teamspace by its status
// alone. The stale pause facts it leaves are not shown, and the next update
// clears them.
func TestStoreShowsNoPauseOnATeamspaceAnOldReleaseResumed(t *testing.T) {
	database, store, _ := testStores(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_teamspace_resumed_%d", suffix)
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	}()
	organizationAgent, err := store.EnsureOrganizationAgent(ctx, organizationID, "Resumed Org", "user_owner")
	if err != nil {
		t.Fatal(err)
	}
	space, err := store.Create(ctx, Space{
		ID: fmt.Sprintf("tsp_resumed_%d", suffix), OrganizationID: organizationID, TeamID: "team_resumed",
		Name: "Resumed", CreatedByUserID: "user_owner", OrganizationAgentID: organizationAgent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	disabled := StatusDisabled
	if _, err := store.Update(ctx, organizationID, space.ID, "user_pauser", UpdateInput{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	// What the old release's UPDATE writes: the status, and nothing it does not know.
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.teamspaces SET status='active', revision=revision+1
		WHERE organization_id=$1 AND id=$2`, organizationID, space.ID); err != nil {
		t.Fatal(err)
	}

	read, err := store.Get(ctx, organizationID, space.ID)
	if err != nil || read.Status != StatusActive || read.PausedByUserID != nil || read.PausedAt != nil {
		t.Fatalf("an active teamspace an old release resumed = %+v, %v; want no pause shown", read, err)
	}
	name := "Renamed"
	if _, err := store.Update(ctx, organizationID, space.ID, "user_owner", UpdateInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	var stale int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.teamspaces
		WHERE id=$1 AND (paused_by_user_id IS NOT NULL OR paused_at IS NOT NULL)`, space.ID).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("pause facts left on the row after the next update: %d, %v", stale, err)
	}
}

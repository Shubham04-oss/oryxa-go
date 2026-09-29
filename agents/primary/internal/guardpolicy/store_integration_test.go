package guardpolicy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// The store's tests need the shared development PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' \
//	  go test ./agents/primary/internal/guardpolicy
//
// Each test works in an organization of its own, and one more named after it
// for the tests that need two, and removes its rows.
func testPostgres(t *testing.T, orgs OrgPolicies, ceiling Cap) (*Service, *postgreskit.Database, string) {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agents-guardpolicy-test"})
	if err != nil {
		t.Fatal(err)
	}
	service := New(database, orgs, ceiling)
	for range 2 {
		if err := service.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	organizationID := "guardpolicy_test_" + hex.EncodeToString(random)
	t.Cleanup(func() {
		for _, table := range []string{"guard_preferences", "guard_preference_changes"} {
			_, _ = database.Pool.Exec(context.Background(),
				`DELETE FROM agents_primary.`+table+` WHERE organization_id = $1 OR organization_id = $2`,
				organizationID, organizationID+"_other")
		}
		database.Close()
	})
	return service, database, organizationID
}

// kept says how many rows of choices, and of logged changes, are kept for a
// member; an empty userID counts the organization's.
func kept(t *testing.T, database *postgreskit.Database, organizationID, userID string) (choices, changes int) {
	t.Helper()
	for table, count := range map[string]*int{"guard_preferences": &choices, "guard_preference_changes": &changes} {
		err := database.Pool.QueryRow(context.Background(),
			`SELECT count(*) FROM agents_primary.`+table+` WHERE organization_id = $1 AND ($2 = '' OR user_id = $2)`,
			organizationID, userID).Scan(count)
		if err != nil {
			t.Fatal(err)
		}
	}
	return choices, changes
}

// logged is a member's logged changes, oldest first, each as "owner: none ->
// strict", and when each was made.
func logged(t *testing.T, database *postgreskit.Database, organizationID, userID string) ([]string, []time.Time) {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `
		SELECT field, "old", "new", changed_at FROM agents_primary.guard_preference_changes
		WHERE organization_id = $1 AND user_id = $2 ORDER BY id`, organizationID, userID)
	if err != nil {
		t.Fatal(err)
	}
	type change struct {
		text string
		at   time.Time
	}
	changes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (change, error) {
		var field string
		var from, to *string
		var at time.Time
		if err := row.Scan(&field, &from, &to, &at); err != nil {
			return change{}, err
		}
		return change{text: fmt.Sprintf("%s: %s -> %s", field, orNone(from), orNone(to)), at: at}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	var times []time.Time
	for _, found := range changes {
		texts = append(texts, found.text)
		times = append(times, found.at)
	}
	return texts, times
}

func orNone(stored *string) string {
	if stored == nil {
		return "none"
	}
	return *stored
}

func TestAMembersChoicesRoundTripThroughPostgres(t *testing.T) {
	service, database, organizationID := testPostgres(t, NoOrgPolicies{}, Cap{})
	ctx := context.Background()

	view, err := service.Preferences(ctx, organizationID, "user_1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Owner.Chosen != "" || view.Peer.Chosen != "" || view.Owner.Effective != LevelSecrets || view.Peer.Effective != LevelStrict {
		t.Fatalf("a member with no row = %+v, want the defaults", view)
	}
	if choices, _ := kept(t, database, organizationID, "user_1"); choices != 0 {
		t.Fatalf("reading made %d rows", choices)
	}

	set, err := service.SetPreferences(ctx, organizationID, "user_1", levelPointer(LevelStrict), nil)
	if err != nil {
		t.Fatal(err)
	}
	if set.Owner.Chosen != LevelStrict || set.Owner.Effective != LevelStrict || set.Peer.Chosen != "" || set.Peer.Effective != LevelStrict {
		t.Fatalf("owner strict = %+v", set)
	}
	if read, err := service.Preferences(ctx, organizationID, "user_1"); err != nil || read != set {
		t.Fatalf("read back = %+v, %v; want %+v", read, err, set)
	}

	// Not naming a level leaves it; naming "" clears it.
	set, err = service.SetPreferences(ctx, organizationID, "user_1", nil, levelPointer(LevelSecrets))
	if err != nil {
		t.Fatal(err)
	}
	if set.Owner.Chosen != LevelStrict || set.Peer.Chosen != LevelSecrets || set.Peer.Effective != LevelSecrets {
		t.Fatalf("peer secrets, owner not named = %+v/%+v", set.Owner, set.Peer)
	}
	set, err = service.SetPreferences(ctx, organizationID, "user_1", levelPointer(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if set.Owner.Chosen != "" || set.Owner.Effective != LevelSecrets || set.Peer.Chosen != LevelSecrets {
		t.Fatalf("owner cleared = %+v/%+v", set.Owner, set.Peer)
	}
	if effective, err := service.Effective(ctx, organizationID, "user_1"); err != nil || effective.Owner != LevelSecrets || effective.Peer != LevelSecrets {
		t.Fatalf("Effective() = %+v, %v; want secrets for both", effective, err)
	}

	// Nobody else is touched: another member, and the same person elsewhere.
	for _, who := range [][2]string{{organizationID, "user_2"}, {organizationID + "_other", "user_1"}} {
		other, err := service.Preferences(ctx, who[0], who[1])
		if err != nil || other.Owner.Chosen != "" || other.Peer.Chosen != "" {
			t.Errorf("%v = %+v, %v; want nothing chosen", who, other, err)
		}
	}

	// A level that is not one is refused, and nothing is written for it.
	before, changesBefore := kept(t, database, organizationID, "user_1")
	if _, err := service.SetPreferences(ctx, organizationID, "user_1", levelPointer("off"), nil); err == nil {
		t.Fatal("a level of off was accepted")
	}
	if after, changesAfter := kept(t, database, organizationID, "user_1"); after != before || changesAfter != changesBefore {
		t.Fatalf("a refused change left %d/%d rows, want %d/%d", after, changesAfter, before, changesBefore)
	}
}

func TestEveryChangeIsLoggedInTheTransactionThatMadeIt(t *testing.T) {
	service, database, organizationID := testPostgres(t, NoOrgPolicies{}, Cap{})
	ctx := context.Background()
	set := func(owner, peer *Level) {
		t.Helper()
		if _, err := service.SetPreferences(ctx, organizationID, "user_1", owner, peer); err != nil {
			t.Fatal(err)
		}
	}

	set(levelPointer(LevelStrict), nil)
	set(levelPointer(LevelStrict), nil)                         // no change, so nothing to log
	set(levelPointer(LevelSecrets), levelPointer(LevelSecrets)) // two changes at once
	set(nil, levelPointer(LevelSecrets))                        // no change
	set(nil, nil)                                               // nothing named
	set(nil, levelPointer(""))
	set(levelPointer(""), levelPointer("")) // the owner clears; the peer already has

	got, times := logged(t, database, organizationID, "user_1")
	want := []string{
		"owner: none -> strict",
		"owner: strict -> secrets",
		"peer: none -> secrets",
		"peer: secrets -> none",
		"owner: secrets -> none",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("logged %q, want %q", got, want)
	}
	for index, at := range times {
		if at.IsZero() || (index > 0 && at.Before(times[index-1])) {
			t.Errorf("change %d was logged at %v, after %v; want a time no earlier than the one before", index, at, times[max(index-1, 0)])
		}
	}
	// Another member's changes are their own.
	if others, _ := logged(t, database, organizationID, "user_2"); len(others) != 0 {
		t.Errorf("user_2 has %q logged, want nothing", others)
	}
}

func TestAChangeIsLoggedWithTheChoicesOrNotAtAll(t *testing.T) {
	service, database, organizationID := testPostgres(t, NoOrgPolicies{}, Cap{})
	ctx := context.Background()
	if _, err := service.SetPreferences(ctx, organizationID, "user_1", levelPointer(LevelStrict), nil); err != nil {
		t.Fatal(err)
	}

	// The database itself refuses a level that is not one. The owner's change
	// is valid and comes first, and must not survive the peer's failing.
	bad := Level("everything")
	if _, err := service.store.set(ctx, organizationID, "user_1", levelPointer(LevelSecrets), &bad); err == nil {
		t.Fatal("the store accepted a level that is not one")
	}
	view, err := service.Preferences(ctx, organizationID, "user_1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Owner.Chosen != LevelStrict || view.Peer.Chosen != "" {
		t.Errorf("after a failed change = %+v/%+v, want it as it was", view.Owner, view.Peer)
	}
	if got, _ := logged(t, database, organizationID, "user_1"); !slices.Equal(got, []string{"owner: none -> strict"}) {
		t.Errorf("logged %q after a failed change, want only the change that was made", got)
	}
}

func TestAChoiceBelowTheMinimumIsKeptAndTheMinimumWinsThroughPostgres(t *testing.T) {
	orgs := &fakeOrgs{policy: OrgPolicy{MinimumPeer: LevelStrict, Relevance: RelevanceShadow}}
	service, database, organizationID := testPostgres(t, orgs, Cap{})
	ctx := context.Background()

	view, err := service.SetPreferences(ctx, organizationID, "user_1", nil, levelPointer(LevelSecrets))
	if err != nil {
		t.Fatalf("a choice below the minimum was refused: %v", err)
	}
	if want := (OriginView{Chosen: LevelSecrets, Default: LevelStrict, Floor: LevelStrict, Effective: LevelStrict}); view.Peer != want {
		t.Fatalf("peer = %+v, want %+v", view.Peer, want)
	}
	if view.Relevance != RelevanceShadow {
		t.Errorf("relevance = %q, want the organization's", view.Relevance)
	}
	var stored string
	if err := database.Pool.QueryRow(ctx,
		`SELECT peer_level FROM agents_primary.guard_preferences WHERE organization_id = $1 AND user_id = 'user_1'`,
		organizationID).Scan(&stored); err != nil || stored != "secrets" {
		t.Fatalf("stored peer level = %q, %v; want the member's own choice, kept", stored, err)
	}

	// Lowering the minimum lets the choice apply again.
	orgs.set(OrgPolicy{}, nil)
	effective, err := service.Effective(ctx, organizationID, "user_1")
	if err != nil || effective.Peer != LevelSecrets {
		t.Fatalf("Effective() with the minimum gone = %+v, %v; want the member's own choice", effective, err)
	}
}

func TestForgetRemovesAMembersChoicesAndTheirLogOrAWholeOrganizations(t *testing.T) {
	service, database, organizationID := testPostgres(t, NoOrgPolicies{}, Cap{})
	ctx := context.Background()
	other := organizationID + "_other"
	for _, who := range [][2]string{{organizationID, "user_1"}, {organizationID, "user_2"}, {other, "user_1"}} {
		if _, err := service.SetPreferences(ctx, who[0], who[1], levelPointer(LevelStrict), levelPointer(LevelSecrets)); err != nil {
			t.Fatal(err)
		}
	}
	for _, who := range [][2]string{{organizationID, "user_1"}, {organizationID, "user_2"}, {other, "user_1"}} {
		if choices, changes := kept(t, database, who[0], who[1]); choices != 1 || changes != 2 {
			t.Fatalf("%v holds %d choices and %d changes, want 1 and 2", who, choices, changes)
		}
	}

	// One member: their rows and their log, and nobody else's.
	if err := service.Forget(ctx, organizationID, "user_1"); err != nil {
		t.Fatal(err)
	}
	for who, want := range map[[2]string][2]int{
		{organizationID, "user_1"}: {0, 0},
		{organizationID, "user_2"}: {1, 2},
		{other, "user_1"}:          {1, 2},
	} {
		if choices, changes := kept(t, database, who[0], who[1]); choices != want[0] || changes != want[1] {
			t.Errorf("after forgetting user_1, %v holds %d choices and %d changes, want %d and %d", who, choices, changes, want[0], want[1])
		}
	}
	if view, err := service.Preferences(ctx, organizationID, "user_1"); err != nil || view.Owner.Chosen != "" || view.Peer.Chosen != "" {
		t.Errorf("a forgotten member = %+v, %v; want nothing chosen", view, err)
	}

	// A whole organization: every member's, and no other organization's.
	if err := service.Forget(ctx, organizationID, ""); err != nil {
		t.Fatal(err)
	}
	if choices, changes := kept(t, database, organizationID, ""); choices != 0 || changes != 0 {
		t.Errorf("after forgetting the organization it holds %d choices and %d changes, want none", choices, changes)
	}
	if choices, changes := kept(t, database, other, ""); choices != 1 || changes != 2 {
		t.Errorf("another organization holds %d choices and %d changes, want 1 and 2 untouched", choices, changes)
	}
	if err := service.Forget(ctx, "", "user_1"); err == nil {
		t.Error("Forget without an organization was accepted")
	}
	if choices, _ := kept(t, database, other, ""); choices != 1 {
		t.Errorf("a Forget that named no organization removed something: %d left, want 1", choices)
	}
}

// TestConcurrentChangesTakeTurnsAndEachLogsWhatItReplaced makes many changes
// to one member's owner level at once. If they did not take turns, two would
// read the same level and log the same change, and the log would no longer
// read as a chain: each entry starting from where the one before ended.
func TestConcurrentChangesTakeTurnsAndEachLogsWhatItReplaced(t *testing.T) {
	service, database, organizationID := testPostgres(t, NoOrgPolicies{}, Cap{})
	ctx := context.Background()
	levels := []Level{LevelStrict, LevelSecrets, ""}

	var failures sync.Map
	var wait sync.WaitGroup
	for changer := range 8 {
		wait.Go(func() {
			for round := range 6 {
				if _, err := service.SetPreferences(ctx, organizationID, "user_1", levelPointer(levels[(changer+round)%len(levels)]), nil); err != nil {
					failures.Store(fmt.Sprintf("%d/%d", changer, round), err)
				}
			}
		})
	}
	wait.Wait()
	failures.Range(func(key, err any) bool {
		t.Errorf("change %v failed: %v", key, err)
		return true
	})

	rows, err := database.Pool.Query(ctx, `
		SELECT "old", "new" FROM agents_primary.guard_preference_changes
		WHERE organization_id = $1 AND user_id = 'user_1' AND field = 'owner' ORDER BY id`, organizationID)
	if err != nil {
		t.Fatal(err)
	}
	type step struct{ from, to *string }
	steps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (step, error) {
		var found step
		return found, row.Scan(&found.from, &found.to)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Fatal("nothing was logged")
	}
	current := "none"
	for index, found := range steps {
		if from := orNone(found.from); from != current {
			t.Errorf("change %d was logged as replacing %s, but the level was %s", index, from, current)
		}
		if orNone(found.from) == orNone(found.to) {
			t.Errorf("change %d was logged as %s -> %s, which changed nothing", index, orNone(found.from), orNone(found.to))
		}
		current = orNone(found.to)
	}
	var stored *string
	if err := database.Pool.QueryRow(ctx,
		`SELECT owner_level FROM agents_primary.guard_preferences WHERE organization_id = $1 AND user_id = 'user_1'`,
		organizationID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if orNone(stored) != current {
		t.Errorf("the level kept is %s, but the last change logged was to %s", orNone(stored), current)
	}
}

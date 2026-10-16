package surfaces

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// surfacesTestOrgPrefix marks every row these tests write, so a run that died
// halfway is cleaned up by the next.
const surfacesTestOrgPrefix = "org_surfaces_test_"

func openTestStore(t *testing.T) (*Store, *postgreskit.Database, string) {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-surfaces-test", Schema: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	store := NewStore(database)
	// Migrating twice is what a restart does.
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	clean := func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.surface_jobs WHERE organization_id LIKE $1`, surfacesTestOrgPrefix+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.surface_links WHERE organization_id LIKE $1`, surfacesTestOrgPrefix+"%")
	}
	clean()
	t.Cleanup(clean)
	return store, database, fmt.Sprintf("%s%d", surfacesTestOrgPrefix, time.Now().UnixNano())
}

// claimOwn claims every due job, and keeps the ones these tests wrote.
func claimOwn(t *testing.T, store *Store, organizationID string, lease time.Duration) []Job {
	t.Helper()
	claimed, err := store.Claim(context.Background(), lease, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var own []Job
	for _, job := range claimed {
		if job.OrganizationID == organizationID {
			own = append(own, job)
		}
	}
	return own
}

func wakeFor(organizationID, requestID string) Wake {
	return Wake{RequestID: requestID, OrganizationID: organizationID, EventID: "evt_" + requestID, AskerUserID: "user_asker", OwnerUserID: "user_owner"}
}

func TestAWokenRequestIsClaimedOnceAndDoneUntilTheNextEvent(t *testing.T) {
	store, _, org := openTestStore(t)
	ctx := context.Background()
	request := org + "_irq_1"

	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	claimed := claimOwn(t, store, org, time.Minute)
	if len(claimed) != 1 || claimed[0] != (Job{RequestID: request, OrganizationID: org, AskerUserID: "user_asker", OwnerUserID: "user_owner", Wanted: 1}) {
		t.Fatalf("claimed = %+v", claimed)
	}
	// Leased: nobody else has it.
	if again := claimOwn(t, store, org, time.Minute); len(again) != 0 {
		t.Fatalf("claimed a leased job again: %+v", again)
	}
	if err := store.Done(ctx, claimed[0]); err != nil {
		t.Fatal(err)
	}
	if again := claimOwn(t, store, org, time.Minute); len(again) != 0 {
		t.Fatalf("a finished job was due again: %+v", again)
	}

	// The next event for the request makes it due once more, as one row.
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	second := claimOwn(t, store, org, time.Minute)
	if len(second) != 1 || second[0].Wanted != 2 {
		t.Fatalf("second claim = %+v, want the one row woken twice", second)
	}
}

func TestAnEventThatComesInDuringAPassLeavesTheJobDue(t *testing.T) {
	store, _, org := openTestStore(t)
	ctx := context.Background()
	request := org + "_irq_1"
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	running := claimOwn(t, store, org, time.Minute)
	if len(running) != 1 {
		t.Fatalf("claimed = %+v", running)
	}
	// The decision comes in while the pass for the request's first event runs.
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	if err := store.Done(ctx, running[0]); err != nil {
		t.Fatal(err)
	}
	next := claimOwn(t, store, org, time.Minute)
	if len(next) != 1 || next[0].Wanted != 2 {
		t.Fatalf("next claim = %+v, want a pass for what came in meanwhile", next)
	}
	if err := store.Done(ctx, next[0]); err != nil {
		t.Fatal(err)
	}
	if again := claimOwn(t, store, org, time.Minute); len(again) != 0 {
		t.Fatalf("claimed %+v after both events were covered", again)
	}
}

func TestAFailedPassIsTriedAgainAfterItsDelayAndGivenUpOnAtTheLimit(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	request := org + "_irq_1"
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	job := claimOwn(t, store, org, time.Minute)[0]

	// Long enough that the claims right after the release land inside it even
	// on a slow CI runner (300ms was not: the early claim sometimes came late).
	const retryDelay = 2 * time.Second
	gaveUp, err := store.Release(ctx, job, true, 3, retryDelay, "slack was down")
	if err != nil || gaveUp {
		t.Fatalf("Release() = %v, %v", gaveUp, err)
	}
	if early := claimOwn(t, store, org, time.Minute); len(early) != 0 {
		t.Fatalf("claimed %+v before the retry delay", early)
	}
	// A new event does not end the wait: the retry that is coming reads the
	// ledger, and covers it.
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	if early := claimOwn(t, store, org, time.Minute); len(early) != 0 {
		t.Fatalf("an event ended the wait: claimed %+v", early)
	}
	time.Sleep(retryDelay + 200*time.Millisecond)
	retry := claimOwn(t, store, org, time.Minute)
	if len(retry) != 1 || retry[0].Attempts != 1 || retry[0].Wanted != 2 {
		t.Fatalf("retry = %+v, want the job with its failure still counted and both events covered", retry)
	}

	// A release that is not a failure, such as a shutdown, counts for nothing.
	if gaveUp, err := store.Release(ctx, retry[0], false, 3, 0, "interrupted"); err != nil || gaveUp {
		t.Fatalf("Release(interrupted) = %v, %v", gaveUp, err)
	}
	again := claimOwn(t, store, org, time.Minute)
	if len(again) != 1 || again[0].Attempts != 1 {
		t.Fatalf("after an interruption = %+v, want the same count", again)
	}
	for range 2 {
		gaveUp, err = store.Release(ctx, again[0], true, 3, 0, "still down")
		if err != nil {
			t.Fatal(err)
		}
		if !gaveUp {
			again = claimOwn(t, store, org, time.Minute)
			if len(again) != 1 {
				t.Fatalf("claimed %+v", again)
			}
		}
	}
	if !gaveUp {
		t.Fatal("the third failure did not give the job up")
	}
	if left := claimOwn(t, store, org, time.Minute); len(left) != 0 {
		t.Fatalf("a job given up on was claimed: %+v", left)
	}
	var lastError string
	var gave bool
	if err := database.Pool.QueryRow(ctx, `SELECT last_error, gave_up FROM agents_primary.surface_jobs WHERE request_id = $1`, request).Scan(&lastError, &gave); err != nil || !gave || lastError != "still down" {
		t.Fatalf("row = %q %v, %v", lastError, gave, err)
	}

	// The next event for it starts over.
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	if revived := claimOwn(t, store, org, time.Minute); len(revived) != 1 || revived[0].Attempts != 0 {
		t.Fatalf("after a new event = %+v, want it due with no failures counted", revived)
	}
}

func TestAJobThatCannotBeDoneIsGivenUpOnAndItsReasonKept(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	request := org + "_irq_1"
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	job := claimOwn(t, store, org, time.Minute)[0]
	if err := store.GiveUp(ctx, job, "the request is gone\x00"); err != nil {
		t.Fatal(err)
	}
	if left := claimOwn(t, store, org, time.Minute); len(left) != 0 {
		t.Fatalf("claimed %+v", left)
	}
	var reason string
	if err := database.Pool.QueryRow(ctx, `SELECT last_error FROM agents_primary.surface_jobs WHERE request_id = $1`, request).Scan(&reason); err != nil || reason != "the request is gone" {
		t.Fatalf("reason = %q, %v", reason, err)
	}
}

func TestALeaseThatRanOutMakesTheJobDueAgain(t *testing.T) {
	store, _, org := openTestStore(t)
	ctx := context.Background()
	if err := store.Wake(ctx, wakeFor(org, org+"_irq_1")); err != nil {
		t.Fatal(err)
	}
	if got := claimOwn(t, store, org, 200*time.Millisecond); len(got) != 1 {
		t.Fatalf("claimed = %+v", got)
	}
	time.Sleep(350 * time.Millisecond)
	if got := claimOwn(t, store, org, time.Minute); len(got) != 1 {
		t.Fatalf("claimed = %+v, want the job whose holder died", got)
	}
}

func TestTwoWorkersNeverHoldTheSameJob(t *testing.T) {
	store, _, org := openTestStore(t)
	ctx := context.Background()
	const jobs = 12
	for i := range jobs {
		if err := store.Wake(ctx, wakeFor(org, fmt.Sprintf("%s_irq_%02d", org, i))); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	held := map[string]int{}
	var group sync.WaitGroup
	for range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				claimed, err := store.Claim(ctx, time.Minute, 2)
				if err != nil {
					t.Error(err)
					return
				}
				if len(claimed) == 0 {
					return
				}
				mu.Lock()
				for _, job := range claimed {
					if job.OrganizationID == org {
						held[job.RequestID]++
					}
				}
				mu.Unlock()
			}
		}()
	}
	group.Wait()
	if len(held) != jobs {
		t.Fatalf("claimed %d jobs, want %d", len(held), jobs)
	}
	for request, times := range held {
		if times != 1 {
			t.Errorf("%s was claimed %d times", request, times)
		}
	}
}

func TestALinkIsKeptByRequestAndRoleAndTheStateItSaysCanChange(t *testing.T) {
	store, _, org := openTestStore(t)
	ctx := context.Background()
	request := org + "_irq_1"
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	link := Link{
		RequestID: request, Role: roleNotice, OrganizationID: org, TeamID: "T1", AskerUserID: "user_asker", OwnerUserID: "user_owner",
		ChannelID: "D1", TS: "1700000000.000100", State: statePending, ExpiresAt: &expires,
	}
	if _, found, err := store.Link(ctx, request, roleNotice); err != nil || found {
		t.Fatalf("Link() before = %v, %v", found, err)
	}
	if err := store.PutLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Link(ctx, request, roleNotice)
	if err != nil || !found || got.ChannelID != "D1" || got.TS != "1700000000.000100" || got.State != statePending || got.TeamID != "T1" ||
		got.AskerUserID != "user_asker" || got.OwnerUserID != "user_owner" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) || got.UpdatedAt.IsZero() {
		t.Fatalf("Link() = %+v, %v, %v", got, found, err)
	}
	// Another role of the same request is another link.
	if _, found, _ := store.Link(ctx, request, roleAnswer); found {
		t.Fatal("found a link for a role nothing was posted for")
	}
	if err := store.SetLinkState(ctx, request, roleNotice, "approved_summary"); err != nil {
		t.Fatal(err)
	}
	settled, _, _ := store.Link(ctx, request, roleNotice)
	if settled.State != "approved_summary" || settled.ExpiresAt != nil || settled.TS != "1700000000.000100" {
		t.Fatalf("settled = %+v, want the state changed, no expiry, and the message kept", settled)
	}
	// Putting it again replaces it.
	link.State = stateFailed
	link.ChannelID, link.TS, link.ExpiresAt = "", "", nil
	if err := store.PutLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	if replaced, _, _ := store.Link(ctx, request, roleNotice); replaced.State != stateFailed || replaced.TS != "" {
		t.Fatalf("replaced = %+v", replaced)
	}
	for name, bad := range map[string]Link{
		"no request": {Role: roleNotice, OrganizationID: org},
		"no role":    {RequestID: request, OrganizationID: org},
		"no org":     {RequestID: request, Role: roleNotice},
	} {
		if err := store.PutLink(ctx, bad); err == nil {
			t.Errorf("%s: stored %+v", name, bad)
		}
	}
	if err := store.Wake(ctx, Wake{RequestID: request}); err == nil {
		t.Error("woke a request with no organization")
	}
}

func TestOnlyNoticesWhoseButtonsOutlivedTheirRequestAreWokenAndOnlyOnceAMinute(t *testing.T) {
	store, _, org := openTestStore(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute).UTC()
	future := time.Now().Add(time.Hour).UTC()
	for _, link := range []Link{
		{RequestID: org + "_lapsed", Role: roleNotice, OrganizationID: org, State: statePending, ExpiresAt: &past, AskerUserID: "user_asker", OwnerUserID: "user_owner"},
		{RequestID: org + "_open", Role: roleNotice, OrganizationID: org, State: statePending, ExpiresAt: &future},
		{RequestID: org + "_settled", Role: roleNotice, OrganizationID: org, State: "approved_summary", ExpiresAt: &past},
		{RequestID: org + "_bypass", Role: roleBypassNotice, OrganizationID: org, State: stateSent, ExpiresAt: &past},
	} {
		if err := store.PutLink(ctx, link); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	woken, err := store.WakeExpired(ctx, now, 100)
	if err != nil || woken != 1 {
		t.Fatalf("WakeExpired() = %d, %v, want the one lapsed notice", woken, err)
	}
	jobs := claimOwn(t, store, org, time.Minute)
	if len(jobs) != 1 || jobs[0].RequestID != org+"_lapsed" || jobs[0].AskerUserID != "user_asker" || jobs[0].OwnerUserID != "user_owner" {
		t.Fatalf("jobs = %+v", jobs)
	}
	// Not again straight away, while its pass has not settled it.
	if again, err := store.WakeExpired(ctx, now.Add(10*time.Second), 100); err != nil || again != 0 {
		t.Fatalf("WakeExpired() again = %d, %v, want none within the minute", again, err)
	}
	if later, err := store.WakeExpired(ctx, now.Add(2*time.Minute), 100); err != nil || later != 1 {
		t.Fatalf("WakeExpired() later = %d, %v, want it woken again", later, err)
	}
	// Once settled it is left alone.
	if err := store.SetLinkState(ctx, org+"_lapsed", roleNotice, stateExpired); err != nil {
		t.Fatal(err)
	}
	if none, err := store.WakeExpired(ctx, now.Add(10*time.Minute), 100); err != nil || none != 0 {
		t.Fatalf("WakeExpired() after it was settled = %d, %v", none, err)
	}
	if _, err := store.WakeExpired(ctx, now, 0); err == nil {
		t.Fatal("WakeExpired() accepted no limit")
	}
}

func TestForgettingAMemberOrAnOrganizationRemovesTheirRows(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	other := org + "_other"
	rows := []struct {
		org, request, asker, owner string
	}{
		{org, "irq_a", "ann", "raya"},
		{org, "irq_b", "ben", "ann"},
		{org, "irq_c", "ben", "cy"},
		{other, "irq_d", "ann", "raya"},
	}
	for _, row := range rows {
		if err := store.Wake(ctx, Wake{RequestID: row.org + row.request, OrganizationID: row.org, AskerUserID: row.asker, OwnerUserID: row.owner}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutLink(ctx, Link{RequestID: row.org + row.request, Role: roleNotice, OrganizationID: row.org, AskerUserID: row.asker, OwnerUserID: row.owner, State: statePending}); err != nil {
			t.Fatal(err)
		}
	}
	remaining := func(prefix string) int {
		var n int
		if err := database.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agents_primary.surface_jobs WHERE organization_id LIKE $1)
			+ (SELECT count(*) FROM agents_primary.surface_links WHERE organization_id LIKE $1)`, prefix).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Ann was the one who asked in one request and the one asked in another.
	if err := store.Forget(ctx, org, "ann"); err != nil {
		t.Fatal(err)
	}
	if n := remaining(org); n != 2 {
		t.Fatalf("%d rows left of %s after forgetting ann, want ben and cy's request (a job and a link)", n, org)
	}
	if n := remaining(other); n != 2 {
		t.Fatalf("%d rows left of another organization, want its two untouched", n)
	}
	if err := store.Forget(ctx, org, ""); err != nil {
		t.Fatal(err)
	}
	if n := remaining(org); n != 0 {
		t.Fatalf("%d rows left after the organization was forgotten", n)
	}
	if n := remaining(other); n != 2 {
		t.Fatalf("another organization lost rows: %d left", n)
	}
	if err := store.Forget(ctx, "", "ann"); err == nil {
		t.Fatal("Forget() accepted no organization")
	}
	// Forgetting what is gone removes nothing more.
	if err := store.Forget(ctx, org, "ann"); err != nil {
		t.Fatal(err)
	}
}

func TestUninstallingAWorkspaceForgetsItsMessagesAndTheWorkNotDone(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	for i, team := range []string{"T1", "T1", "T2"} {
		request := fmt.Sprintf("%s_irq_%d", org, i)
		if err := store.PutLink(ctx, Link{RequestID: request, Role: roleNotice, OrganizationID: org, TeamID: team, State: statePending}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Wake(ctx, wakeFor(org, org+"_irq_pending")); err != nil {
		t.Fatal(err)
	}
	if err := store.ForgetTeam(ctx, org, "T1"); err != nil {
		t.Fatal(err)
	}
	var links int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.surface_links WHERE organization_id = $1`, org).Scan(&links); err != nil || links != 1 {
		t.Fatalf("%d links left, want the other workspace's: %v", links, err)
	}
	if due := claimOwn(t, store, org, time.Minute); len(due) != 0 {
		t.Fatalf("a job for an uninstalled workspace is still due: %+v", due)
	}
	if err := store.ForgetTeam(ctx, org, ""); err == nil {
		t.Fatal("ForgetTeam() accepted no team")
	}
}

func TestPurgeForgetsFinishedJobsAndOldLinksButNotWorkStillToDo(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	if err := store.Wake(ctx, wakeFor(org, org+"_finished")); err != nil {
		t.Fatal(err)
	}
	if err := store.Wake(ctx, wakeFor(org, org+"_waiting")); err != nil {
		t.Fatal(err)
	}
	for _, job := range claimOwn(t, store, org, time.Minute) {
		if job.RequestID == org+"_finished" {
			if err := store.Done(ctx, job); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, request := range []string{"_old", "_new"} {
		if err := store.PutLink(ctx, Link{RequestID: org + request, Role: roleNotice, OrganizationID: org, State: stateSent}); err != nil {
			t.Fatal(err)
		}
	}
	// Age the finished job, the waiting one and the old link.
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.surface_jobs SET updated_at = now() - interval '10 days' WHERE organization_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.surface_links SET updated_at = now() - interval '40 days' WHERE request_id = $1`, org+"_old"); err != nil {
		t.Fatal(err)
	}
	if err := store.Purge(ctx, 7*24*time.Hour, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var jobs, links int
	_ = database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.surface_jobs WHERE organization_id = $1`, org).Scan(&jobs)
	_ = database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.surface_links WHERE organization_id = $1`, org).Scan(&links)
	if jobs != 1 || links != 1 {
		t.Fatalf("%d jobs and %d links left, want the waiting job and the new link", jobs, links)
	}
}

func TestARequestWokenAfterItsWorkspaceWasUninstalledStartsOverEvenFromInsideABackoff(t *testing.T) {
	store, _, org := openTestStore(t)
	ctx := context.Background()
	request := org + "_irq_1"
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	job := claimOwn(t, store, org, time.Minute)[0]
	// A failure puts it back for a minute, with the failure counted.
	if gaveUp, err := store.Release(ctx, job, true, 6, time.Minute, "slack was down"); err != nil || gaveUp {
		t.Fatalf("Release() = %v, %v", gaveUp, err)
	}
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	if waiting := claimOwn(t, store, org, time.Minute); len(waiting) != 0 {
		t.Fatalf("an event ended the wait: %+v", waiting)
	}
	// The workspace is uninstalled, which gives up what had not run, and later
	// installs the app again. The next event for the request has no retry coming
	// from before, and neither the wait nor the count that was left.
	if err := store.ForgetTeam(ctx, org, "T0ACME001"); err != nil {
		t.Fatal(err)
	}
	if err := store.Wake(ctx, wakeFor(org, request)); err != nil {
		t.Fatal(err)
	}
	revived := claimOwn(t, store, org, time.Minute)
	if len(revived) != 1 || revived[0].Attempts != 0 {
		t.Fatalf("revived = %+v, want it due at once with no failures counted", revived)
	}
}

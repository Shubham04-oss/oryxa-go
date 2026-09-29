package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// routerTestOrgPrefix marks every row these tests write, so a run that died
// halfway is cleaned up by the next.
const routerTestOrgPrefix = "org_router_test_"

func openTestStore(t *testing.T) (*Store, *postgreskit.Database, string) {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-slack-router-test", Schema: "public",
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
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.slack_jobs WHERE organization_id LIKE $1`, routerTestOrgPrefix+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.slack_identities WHERE organization_id LIKE $1`, routerTestOrgPrefix+"%")
	}
	clean()
	t.Cleanup(clean)
	return store, database, fmt.Sprintf("%s%d", routerTestOrgPrefix, time.Now().UnixNano())
}

func testJob(organizationID, inboundID string) Job {
	return Job{
		InboundID: inboundID, Kind: KindMessage, OrganizationID: organizationID, TeamID: "T1",
		EventID: "evt_" + inboundID, ReceivedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
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

func TestStoreJobLifecycle(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()

	job := testJob(org, org+"_in_1")
	if added, err := store.Enqueue(ctx, job); err != nil || !added {
		t.Fatalf("enqueue: added=%v err=%v", added, err)
	}
	// The same inbound row, whichever event brought it, is the same job.
	other := job
	other.EventID, other.Kind = "evt_other", KindAction
	if added, err := store.Enqueue(ctx, other); err != nil || added {
		t.Fatalf("enqueue again: added=%v err=%v", added, err)
	}
	for name, bad := range map[string]Job{
		"no id":   {Kind: KindMessage, OrganizationID: org, TeamID: "T1", ReceivedAt: time.Now()},
		"no kind": {InboundID: "x", OrganizationID: org, TeamID: "T1", ReceivedAt: time.Now()},
		"no org":  {InboundID: "x", Kind: KindMessage, TeamID: "T1", ReceivedAt: time.Now()},
		"no time": {InboundID: "x", Kind: KindMessage, OrganizationID: org, TeamID: "T1"},
	} {
		if _, err := store.Enqueue(ctx, bad); err == nil {
			t.Errorf("%s: enqueued", name)
		}
	}

	claimed := claimOwn(t, store, org, time.Minute)
	if len(claimed) != 1 || claimed[0].InboundID != job.InboundID || claimed[0].Kind != KindMessage ||
		claimed[0].TeamID != "T1" || claimed[0].EventID != "evt_"+job.InboundID || claimed[0].Attempts != 0 ||
		claimed[0].AskID != "" || !claimed[0].ReceivedAt.Equal(job.ReceivedAt) {
		t.Fatalf("claimed = %+v, want %+v", claimed, job)
	}
	// Leased: nobody else has it.
	if again := claimOwn(t, store, org, time.Minute); len(again) != 0 {
		t.Fatalf("claimed a leased job again: %+v", again)
	}

	if err := store.MarkAsked(ctx, job.InboundID, "ask_1"); err != nil {
		t.Fatal(err)
	}
	// A failed attempt is released for later and counted, and comes back with
	// the ask its attempt made.
	if dropped, err := store.Release(ctx, job.InboundID, true, 3, 0, "apps timed out"); err != nil || dropped {
		t.Fatalf("release: dropped=%v err=%v", dropped, err)
	}
	claimed = claimOwn(t, store, org, time.Minute)
	if len(claimed) != 1 || claimed[0].Attempts != 1 || claimed[0].AskID != "ask_1" {
		t.Fatalf("claimed after a failure = %+v", claimed)
	}
	// A release that is not a failure counts for nothing, and waits.
	if dropped, err := store.Release(ctx, job.InboundID, false, 3, time.Hour, "shutdown"); err != nil || dropped {
		t.Fatalf("release: dropped=%v err=%v", dropped, err)
	}
	if again := claimOwn(t, store, org, time.Minute); len(again) != 0 {
		t.Fatalf("claimed a job that waits for an hour: %+v", again)
	}
	var attempts int
	if err := database.Pool.QueryRow(ctx, `SELECT attempts FROM agents_primary.slack_jobs WHERE inbound_id=$1`, job.InboundID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts = %d, err = %v", attempts, err)
	}

	// The last failure gives up.
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.slack_jobs SET available_at=now() WHERE inbound_id=$1`, job.InboundID); err != nil {
		t.Fatal(err)
	}
	claimOwn(t, store, org, time.Minute)
	if dropped, err := store.Release(ctx, job.InboundID, true, 2, time.Minute, "apps timed out again"); err != nil || !dropped {
		t.Fatalf("release: dropped=%v err=%v, want it given up on", dropped, err)
	}
	var handled, gaveUp bool
	var reason string
	if err := database.Pool.QueryRow(ctx, `SELECT handled_at IS NOT NULL, dropped, last_error FROM agents_primary.slack_jobs WHERE inbound_id=$1`, job.InboundID).Scan(&handled, &gaveUp, &reason); err != nil {
		t.Fatal(err)
	}
	if !handled || !gaveUp || reason != "apps timed out again" {
		t.Fatalf("handled=%v dropped=%v reason=%q", handled, gaveUp, reason)
	}
	// A job given up on is not recorded as new again while its row is kept.
	if added, err := store.Enqueue(ctx, job); err != nil || added {
		t.Fatalf("enqueue after giving up: added=%v err=%v", added, err)
	}
	if again := claimOwn(t, store, org, time.Minute); len(again) != 0 {
		t.Fatalf("claimed a job given up on: %+v", again)
	}
}

func TestStoreDoneAndDropKeepTheRow(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	done, dropped := testJob(org, org+"_done"), testJob(org, org+"_dropped")
	for _, job := range []Job{done, dropped} {
		if _, err := store.Enqueue(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	claimOwn(t, store, org, time.Minute)
	if err := store.Done(ctx, done.InboundID); err != nil {
		t.Fatal(err)
	}
	// A reason is bounded, and cannot carry what the database will not hold.
	if err := store.Drop(ctx, dropped.InboundID, strings.Repeat("é", 2000)+"\x00tail"); err != nil {
		t.Fatal(err)
	}
	var reason string
	var isDropped bool
	if err := database.Pool.QueryRow(ctx, `SELECT dropped, last_error FROM agents_primary.slack_jobs WHERE inbound_id=$1`, dropped.InboundID).Scan(&isDropped, &reason); err != nil {
		t.Fatal(err)
	}
	if !isDropped || len([]rune(reason)) != 1000 {
		t.Fatalf("dropped=%v, reason of %d runes", isDropped, len([]rune(reason)))
	}
	if err := database.Pool.QueryRow(ctx, `SELECT dropped FROM agents_primary.slack_jobs WHERE inbound_id=$1`, done.InboundID).Scan(&isDropped); err != nil || isDropped {
		t.Fatalf("a finished job is marked dropped: %v, %v", isDropped, err)
	}
	for _, job := range []Job{done, dropped} {
		if added, err := store.Enqueue(ctx, job); err != nil || added {
			t.Fatalf("enqueue of a handled job: added=%v err=%v", added, err)
		}
	}
	// Settling twice is harmless.
	if err := store.Done(ctx, done.InboundID); err != nil {
		t.Fatal(err)
	}
	if err := store.Drop(ctx, done.InboundID, "late"); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT dropped FROM agents_primary.slack_jobs WHERE inbound_id=$1`, done.InboundID).Scan(&isDropped); err != nil || isDropped {
		t.Fatalf("a job finished, then dropped: %v, %v", isDropped, err)
	}
}

func TestStoreLeasesExpire(t *testing.T) {
	store, _, org := openTestStore(t)
	ctx := context.Background()
	if _, err := store.Enqueue(ctx, testJob(org, org+"_in")); err != nil {
		t.Fatal(err)
	}
	if got := claimOwn(t, store, org, 200*time.Millisecond); len(got) != 1 {
		t.Fatalf("claimed %+v", got)
	}
	if got := claimOwn(t, store, org, time.Minute); len(got) != 0 {
		t.Fatalf("claimed a leased job: %+v", got)
	}
	time.Sleep(300 * time.Millisecond)
	// Its holder is gone: it is due again.
	if got := claimOwn(t, store, org, time.Minute); len(got) != 1 {
		t.Fatalf("a job whose lease ran out was not claimable: %+v", got)
	}
	if _, err := store.Claim(ctx, 0, 1); err == nil {
		t.Fatal("claimed with no lease")
	}
	if _, err := store.Claim(ctx, time.Minute, 0); err == nil {
		t.Fatal("claimed no jobs")
	}
}

// TestStoreClaimsAreExclusive: replicas claiming together never hold the same
// job, and none is missed.
func TestStoreClaimsAreExclusive(t *testing.T) {
	store, _, org := openTestStore(t)
	ctx := context.Background()
	const jobs = 30
	for index := range jobs {
		if _, err := store.Enqueue(ctx, testJob(org, fmt.Sprintf("%s_in_%02d", org, index))); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			for {
				claimed, err := store.Claim(ctx, time.Minute, 1)
				if err != nil {
					t.Error(err)
					return
				}
				if len(claimed) == 0 {
					return
				}
				mu.Lock()
				if strings.HasPrefix(claimed[0].OrganizationID, org) {
					seen[claimed[0].InboundID]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(seen) != jobs {
		t.Fatalf("claimed %d of %d jobs", len(seen), jobs)
	}
	for id, times := range seen {
		if times != 1 {
			t.Fatalf("job %s claimed %d times", id, times)
		}
	}
}

func TestStorePurgeKeepsWhatIsRecent(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	old, recent, waiting := testJob(org, org+"_old"), testJob(org, org+"_recent"), testJob(org, org+"_waiting")
	for _, job := range []Job{old, recent, waiting} {
		if _, err := store.Enqueue(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.slack_jobs SET handled_at=now()-interval '8 days' WHERE inbound_id=$1`, old.InboundID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.slack_jobs SET handled_at=now()-interval '1 day' WHERE inbound_id=$1`, recent.InboundID); err != nil {
		t.Fatal(err)
	}
	if err := store.Purge(ctx, handledRetention); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := database.Pool.Query(ctx, `SELECT inbound_id FROM agents_primary.slack_jobs WHERE organization_id=$1 ORDER BY inbound_id`, org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if strings.Join(left, ",") != recent.InboundID+","+waiting.InboundID {
		t.Fatalf("left = %v, want the recent and the waiting", left)
	}
}

func TestStoreIdentities(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	identity := Identity{OrganizationID: org, TeamID: "T1", SlackUserID: "URAYA", MemberUserID: "usr_raya", Email: " Raya@Acme.com ", DisplayName: "Raya"}

	if _, err := store.SlackUserFor(ctx, org, "usr_raya"); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("err = %v, want ErrNoIdentity", err)
	}
	if err := store.SaveIdentity(ctx, identity); err != nil {
		t.Fatal(err)
	}
	got, err := store.SlackUserFor(ctx, org, "usr_raya")
	if err != nil || got.SlackUserID != "URAYA" || got.TeamID != "T1" || got.MemberUserID != "usr_raya" ||
		got.Email != "raya@acme.com" || got.DisplayName != "Raya" || got.OrganizationID != org || got.ResolvedAt.IsZero() {
		t.Fatalf("SlackUserFor = %+v, %v", got, err)
	}
	// Another organization's member of the same id is not this one's.
	if _, err := store.SlackUserFor(ctx, org+"_other", "usr_raya"); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("err = %v", err)
	}

	// The same Slack user, matched to a member again, is one match.
	identity.DisplayName = "Raya R."
	if err := store.SaveIdentity(ctx, identity); err != nil {
		t.Fatal(err)
	}
	// A member is one Slack user of a workspace: a new one replaces the old.
	replacement := identity
	replacement.SlackUserID = "URAYA2"
	if err := store.SaveIdentity(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if got, err := store.SlackUserFor(ctx, org, "usr_raya"); err != nil || got.SlackUserID != "URAYA2" {
		t.Fatalf("SlackUserFor = %+v, %v, want the replacement", got, err)
	}
	var count int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.slack_identities WHERE organization_id=$1`, org).Scan(&count); err != nil || count != 1 {
		t.Fatalf("%d matches, %v", count, err)
	}

	// A Slack user whose address moved to another member follows it.
	moved := replacement
	moved.MemberUserID, moved.Email = "usr_maya", "maya@acme.com"
	if err := store.SaveIdentity(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SlackUserFor(ctx, org, "usr_raya"); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("Raya still matched after her Slack user became Maya's: %v", err)
	}
	if got, err := store.SlackUserFor(ctx, org, "usr_maya"); err != nil || got.SlackUserID != "URAYA2" {
		t.Fatalf("SlackUserFor = %+v, %v", got, err)
	}

	for name, bad := range map[string]Identity{
		"no organization": {TeamID: "T1", SlackUserID: "U", MemberUserID: "m"},
		"no team":         {OrganizationID: org, SlackUserID: "U", MemberUserID: "m"},
		"no Slack user":   {OrganizationID: org, TeamID: "T1", MemberUserID: "m"},
		"no member":       {OrganizationID: org, TeamID: "T1", SlackUserID: "U"},
	} {
		if err := store.SaveIdentity(ctx, bad); err == nil {
			t.Errorf("%s: saved", name)
		}
	}

	if err := store.DeleteIdentity(ctx, "T1", "URAYA2"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SlackUserFor(ctx, org, "usr_maya"); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("err = %v", err)
	}
	// Deleting what is not there is nothing.
	if err := store.DeleteIdentity(ctx, "T1", "URAYA2"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreForget(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	for _, identity := range []Identity{
		{OrganizationID: org, TeamID: "T1", SlackUserID: "U1", MemberUserID: "usr_1", Email: "1@acme.com"},
		{OrganizationID: org, TeamID: "T1", SlackUserID: "U2", MemberUserID: "usr_2", Email: "2@acme.com"},
		{OrganizationID: org + "_other", TeamID: "T2", SlackUserID: "U1", MemberUserID: "usr_1", Email: "1@other.example"},
	} {
		if err := store.SaveIdentity(ctx, identity); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Enqueue(ctx, testJob(org, org+"_in")); err != nil {
		t.Fatal(err)
	}
	count := func(query string, args ...any) int {
		var n int
		if err := database.Pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A member who left: their match goes; the other member's and the
	// organization's jobs stay.
	if err := store.Forget(ctx, org, "usr_1"); err != nil {
		t.Fatal(err)
	}
	if got := count(`SELECT count(*) FROM agents_primary.slack_identities WHERE organization_id=$1`, org); got != 1 {
		t.Fatalf("%d matches left in the organization, want the other member's", got)
	}
	if got := count(`SELECT count(*) FROM agents_primary.slack_identities WHERE organization_id=$1`, org+"_other"); got != 1 {
		t.Fatalf("Forget for one organization touched another's: %d", got)
	}
	if got := count(`SELECT count(*) FROM agents_primary.slack_jobs WHERE organization_id=$1`, org); got != 1 {
		t.Fatalf("a departed member's removal took %d jobs", 1-got)
	}

	// An organization that was deleted: everything of it.
	if err := store.Forget(ctx, org, ""); err != nil {
		t.Fatal(err)
	}
	if got := count(`SELECT count(*) FROM agents_primary.slack_identities WHERE organization_id=$1`, org) +
		count(`SELECT count(*) FROM agents_primary.slack_jobs WHERE organization_id=$1`, org); got != 0 {
		t.Fatalf("%d rows of a deleted organization left", got)
	}
	if got := count(`SELECT count(*) FROM agents_primary.slack_identities WHERE organization_id=$1`, org+"_other"); got != 1 {
		t.Fatal("deleting one organization removed another's matches")
	}
	if err := store.Forget(ctx, " ", ""); err == nil {
		t.Fatal("Forget accepted no organization")
	}
}

func TestStoreForgetTeam(t *testing.T) {
	store, database, org := openTestStore(t)
	ctx := context.Background()
	if err := store.SaveIdentity(ctx, Identity{OrganizationID: org, TeamID: "T1", SlackUserID: "U1", MemberUserID: "usr_1"}); err != nil {
		t.Fatal(err)
	}
	pending, other := testJob(org, org+"_pending"), testJob(org, org+"_other_team")
	other.TeamID = "T2"
	for _, job := range []Job{pending, other} {
		if _, err := store.Enqueue(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ForgetTeam(ctx, org, "T1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SlackUserFor(ctx, org, "usr_1"); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("err = %v", err)
	}
	// The uninstalled workspace's waiting job has nothing to answer; the
	// other workspace's is untouched.
	claimed := claimOwn(t, store, org, time.Minute)
	if len(claimed) != 1 || claimed[0].InboundID != other.InboundID {
		t.Fatalf("claimed = %+v, want the other workspace's job alone", claimed)
	}
	var dropped bool
	if err := database.Pool.QueryRow(ctx, `SELECT dropped FROM agents_primary.slack_jobs WHERE inbound_id=$1`, pending.InboundID).Scan(&dropped); err != nil || !dropped {
		t.Fatalf("dropped=%v err=%v", dropped, err)
	}
	if err := store.ForgetTeam(ctx, org, ""); err == nil {
		t.Fatal("ForgetTeam accepted no team")
	}
}

// TestAuthMembers reads auth's own tables, which a laptop's database already
// has and CI's gets from auth's tests.
func TestAuthMembers(t *testing.T) {
	_, database, org := openTestStore(t)
	ctx := context.Background()
	var present bool
	if err := database.Pool.QueryRow(ctx, `SELECT to_regclass('auth_users') IS NOT NULL AND to_regclass('auth_memberships') IS NOT NULL AND to_regclass('auth_organizations') IS NOT NULL`).Scan(&present); err != nil || !present {
		t.Skip("auth's tables are not in this database")
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	orgID, otherOrgID := "org_members_test_"+suffix, "org_members_other_test_"+suffix
	userID, outsiderID := "usr_members_test_"+suffix, "usr_members_outsider_"+suffix
	email, outsiderEmail := "Raya."+suffix+"@Members-Test.example", "outsider."+suffix+"@members-test.example"
	now := time.Now().UTC()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO auth_users (id, email, display_name, avatar_url, created_at, updated_at) VALUES ($1,$2,'Raya Rao','',$3,$3)`, []any{userID, email, now}},
		{`INSERT INTO auth_users (id, email, display_name, avatar_url, created_at, updated_at) VALUES ($1,$2,'Out Sider','',$3,$3)`, []any{outsiderID, outsiderEmail, now}},
		{`INSERT INTO auth_organizations (id, name, slug, created_by, created_at, updated_at) VALUES ($1,'Members Test','members-test-'||$1,$2,$3,$3)`, []any{orgID, userID, now}},
		{`INSERT INTO auth_organizations (id, name, slug, created_by, created_at, updated_at) VALUES ($1,'Members Other','members-other-'||$1,$2,$3,$3)`, []any{otherOrgID, outsiderID, now}},
		{`INSERT INTO auth_memberships (organization_id, user_id, role, joined_at) VALUES ($1,$2,'admin',$3)`, []any{orgID, userID, now}},
		{`INSERT INTO auth_memberships (organization_id, user_id, role, joined_at) VALUES ($1,$2,'member',$3)`, []any{otherOrgID, outsiderID, now}},
	} {
		if _, err := database.Pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM auth_organizations WHERE id = ANY($1)`, []string{orgID, otherOrgID})
		_, _ = database.Pool.Exec(ctx, `DELETE FROM auth_users WHERE id = ANY($1)`, []string{userID, outsiderID})
	})
	_ = org

	members := NewAuthMembers(database)
	// Without regard to case or the space around it, in either direction.
	for _, asked := range []string{email, strings.ToLower(email), strings.ToUpper(email), "  " + email + "  "} {
		member, err := members.ByEmail(ctx, orgID, asked)
		if err != nil || member.UserID != userID || member.Role != "admin" || member.DisplayName != "Raya Rao" || !strings.EqualFold(member.Email, email) {
			t.Fatalf("ByEmail(%q) = %+v, %v", asked, member, err)
		}
	}
	// A member of some other organization is nobody here.
	if _, err := members.ByEmail(ctx, orgID, outsiderEmail); !errors.Is(err, ErrNotMember) {
		t.Fatalf("err = %v, want ErrNotMember for another organization's member", err)
	}
	if member, err := members.ByEmail(ctx, otherOrgID, outsiderEmail); err != nil || member.UserID != outsiderID {
		t.Fatalf("ByEmail in their own organization = %+v, %v", member, err)
	}
	// The organization's name, for a refusal that gives it.
	if name, err := members.OrganizationName(ctx, orgID); err != nil || name != "Members Test" {
		t.Fatalf("OrganizationName = %q, %v", name, err)
	}
	if _, err := members.OrganizationName(ctx, "org_nope_"+suffix); !errors.Is(err, ErrGone) {
		t.Fatalf("err = %v, want ErrGone for an organization that does not exist", err)
	}
	for name, ask := range map[string]struct{ org, email string }{
		"an unknown address":                        {orgID, "nobody@members-test.example"},
		"an address that is a prefix of a member's": {orgID, strings.Split(strings.ToLower(email), "@")[0]},
		"no address":                                {orgID, "  "},
		"no organization":                           {"", email},
		"an unknown organization":                   {"org_nope_" + suffix, email},
	} {
		if _, err := members.ByEmail(ctx, ask.org, ask.email); !errors.Is(err, ErrNotMember) {
			t.Errorf("%s: err = %v, want ErrNotMember", name, err)
		}
	}
}

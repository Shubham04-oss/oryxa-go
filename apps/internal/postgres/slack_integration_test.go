package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/slackbot"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// These tests need a PostgreSQL, like the rest of this package's
// integration tests: they skip without APPS_TEST_DATABASE_URL, and each runs
// in a schema of its own that it drops.

func slackInstall(organizationID, teamID string) slackbot.Install {
	return slackbot.Install{
		TeamID: teamID, OrganizationID: organizationID, Status: "active", TeamName: "Acme", BotUserID: "U0BOT" + teamID[1:],
		AppID: "A0APP", Scopes: []string{"chat:write", "app_mentions:read"}, InstalledBy: "user-1",
		Sealed:      &secrets.SealedData{Version: 1, Nonce: []byte("nonce-" + teamID), Ciphertext: []byte("cipher-" + teamID), EncryptedDataKey: []byte("key-" + teamID)},
		InstalledAt: testClock, UpdatedAt: testClock,
	}
}

func slackEvent(t *testing.T, detailType string) func(bool) (domain.Event, error) {
	t.Helper()
	return func(bool) (domain.Event, error) { return testEvent(t, detailType), nil }
}

func slackEnded(t *testing.T) func(slackbot.Install) (domain.Event, error) {
	t.Helper()
	return func(slackbot.Install) (domain.Event, error) {
		return testEvent(t, "apps.slack.install.changed.v1"), nil
	}
}

func countRows(t *testing.T, store *Store, query string, arguments ...any) int {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(context.Background(), query, arguments...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSlackInstallsAreBoundToOneOrganizationAndOneWorkspaceEach(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.SaveInstall(ctx, slackInstall("org_1", "T0ACME01"), slackEvent(t, "apps.slack.install.changed.v1")); err != nil {
		t.Fatal(err)
	}
	got, err := store.InstallByTeam(ctx, "T0ACME01")
	if err != nil || !got.Active() || got.OrganizationID != "org_1" || got.BotUserID != "U0BOTACME01" || got.TeamName != "Acme" ||
		got.Sealed == nil || string(got.Sealed.Ciphertext) != "cipher-T0ACME01" || len(got.Scopes) != 2 || !got.InstalledAt.Equal(testClock) {
		t.Fatalf("InstallByTeam = %+v, %v", got, err)
	}
	if byOrganization, err := store.InstallForOrganization(ctx, "org_1"); err != nil || byOrganization.TeamID != "T0ACME01" {
		t.Fatalf("InstallForOrganization = %+v, %v", byOrganization, err)
	}
	if _, err := store.InstallByTeam(ctx, "T0NOBODY"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an unknown team = %v", err)
	}
	if _, err := store.InstallForOrganization(ctx, "org_none"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an organization with no install = %v", err)
	}

	// The workspace is active for one organization, and another cannot take it.
	if err := store.SaveInstall(ctx, slackInstall("org_2", "T0ACME01"), slackEvent(t, "apps.slack.install.changed.v1")); !errors.Is(err, slackbot.ErrTeamBound) {
		t.Fatalf("a workspace bound elsewhere = %v", err)
	}
	// And the organization serves one workspace.
	if err := store.SaveInstall(ctx, slackInstall("org_1", "T0OTHER01"), slackEvent(t, "apps.slack.install.changed.v1")); !errors.Is(err, slackbot.ErrOrganizationBound) {
		t.Fatalf("a second workspace = %v", err)
	}
	if n := countRows(t, store, `SELECT count(*) FROM apps_slack_installs`); n != 1 {
		t.Fatalf("installs = %d", n)
	}
	if n := countRows(t, store, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.slack.install.changed.v1'`); n != 1 {
		t.Fatalf("refused installs wrote events: %d", n)
	}

	// Installing over one replaces its token and keeps when it was installed.
	again := slackInstall("org_1", "T0ACME01")
	again.Sealed = &secrets.SealedData{Version: 1, Nonce: []byte("n2"), Ciphertext: []byte("c2"), EncryptedDataKey: []byte("k2")}
	again.InstalledAt, again.UpdatedAt = testClock.Add(time.Hour), testClock.Add(time.Hour)
	var reinstalled bool
	if err := store.SaveInstall(ctx, again, func(r bool) (domain.Event, error) {
		reinstalled = r
		return testEvent(t, "apps.slack.install.changed.v1"), nil
	}); err != nil || !reinstalled {
		t.Fatalf("SaveInstall over = %v, reinstalled = %v", err, reinstalled)
	}
	got, _ = store.InstallByTeam(ctx, "T0ACME01")
	if string(got.Sealed.Ciphertext) != "c2" || !got.InstalledAt.Equal(testClock) || !got.UpdatedAt.Equal(testClock.Add(time.Hour)) {
		t.Fatalf("after reinstalling = %+v", got)
	}

	// Ending it drops the token, and frees the workspace and the organization.
	ended, found, err := store.EndInstall(ctx, "T0ACME01", "app_uninstalled", testClock.Add(2*time.Hour), slackEnded(t))
	if err != nil || !found || ended.Sealed != nil || ended.Status != "ended" || ended.EndReason != "app_uninstalled" {
		t.Fatalf("EndInstall = %+v, %v, %v", ended, found, err)
	}
	if _, found, err := store.EndInstall(ctx, "T0ACME01", "app_uninstalled", testClock, slackEnded(t)); err != nil || found {
		t.Fatalf("ending an ended install = %v, %v", found, err)
	}
	got, _ = store.InstallByTeam(ctx, "T0ACME01")
	if got.Active() || got.Sealed != nil || got.EndedAt == nil {
		t.Fatalf("ended install = %+v", got)
	}
	if err := store.SaveInstall(ctx, slackInstall("org_2", "T0ACME01"), slackEvent(t, "apps.slack.install.changed.v1")); err != nil {
		t.Fatalf("an ended workspace, installed for another organization = %v", err)
	}
	if got, _ := store.InstallByTeam(ctx, "T0ACME01"); got.OrganizationID != "org_2" || !got.Active() {
		t.Fatalf("install = %+v", got)
	}
	// The first organization can now install another workspace.
	if err := store.SaveInstall(ctx, slackInstall("org_1", "T0OTHER01"), slackEvent(t, "apps.slack.install.changed.v1")); err != nil {
		t.Fatalf("a new workspace for an organization whose install ended = %v", err)
	}
}

func TestRacingSlackInstallsOfOneWorkspaceHaveOneWinner(t *testing.T) {
	store := openTestStore(t)
	const organizations = 8
	results := make([]error, organizations)
	var group sync.WaitGroup
	for i := range organizations {
		group.Add(1)
		go func() {
			defer group.Done()
			results[i] = store.SaveInstall(context.Background(), slackInstall("org_"+string(rune('a'+i)), "T0RACE001"), slackEvent(t, "apps.slack.install.changed.v1"))
		}()
	}
	group.Wait()
	winners := 0
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case !errors.Is(err, slackbot.ErrTeamBound):
			t.Fatalf("a losing install = %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d", winners)
	}
	if n := countRows(t, store, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.slack.install.changed.v1'`); n != 1 {
		t.Fatalf("events = %d", n)
	}
}

func slackInbound(id, organizationID string) slackbot.Inbound {
	return slackbot.Inbound{
		ID: id, OrganizationID: organizationID, TeamID: "T0ACME01", Kind: slackbot.InboundMessage, EventID: "Ev1",
		ChannelID: "C0GENERAL", UserID: "U0ASKER", MessageTS: "1780000100.000200", ThreadTS: "1780000000.000100",
		ChannelType: "channel", ExtShared: true, Mentions: []string{"U0RAYA", "U0MAYA"}, Text: "ask <@U0RAYA>", ReceivedAt: testClock,
	}
}

func TestADeliveryIsRecordedWithItsMarkAndEventOrNotAtAll(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	inbound := slackInbound("asi_1", "org_1")

	recorded, err := store.RecordInbound(ctx, "hook:aph_x:Ev1", testClock, inbound, testEvent(t, "apps.slack.message.received.v1"))
	if err != nil || !recorded {
		t.Fatalf("RecordInbound = %v, %v", recorded, err)
	}
	got, err := store.Inbound(ctx, "asi_1")
	if err != nil {
		t.Fatal(err)
	}
	inbound.ReceivedAt = got.ReceivedAt
	if got.Text != inbound.Text || got.ThreadTS != inbound.ThreadTS || !got.ExtShared || len(got.Mentions) != 2 || got.Mentions[0] != "U0RAYA" ||
		got.Kind != slackbot.InboundMessage || got.ChannelType != "channel" || !got.ReceivedAt.Equal(testClock) {
		t.Fatalf("Inbound = %+v", got)
	}
	// hooks' own check finds the mark, so the ingress answers a repeat before it gets here.
	if handled, err := store.HookDeliveryHandled(ctx, "hook:aph_x:Ev1"); err != nil || !handled {
		t.Fatalf("HookDeliveryHandled = %v, %v", handled, err)
	}
	// The same mark again writes nothing.
	again := slackInbound("asi_2", "org_1")
	if recorded, err := store.RecordInbound(ctx, "hook:aph_x:Ev1", testClock, again, testEvent(t, "apps.slack.message.received.v1")); err != nil || recorded {
		t.Fatalf("a repeat = %v, %v", recorded, err)
	}
	if _, err := store.Inbound(ctx, "asi_2"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a repeat's record = %v", err)
	}
	if n := countRows(t, store, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.slack.message.received.v1'`); n != 1 {
		t.Fatalf("events = %d", n)
	}
	if n := countRows(t, store, `SELECT count(*) FROM apps_slack_inbound`); n != 1 {
		t.Fatalf("records = %d", n)
	}
}

func TestRacingCopiesOfOneDeliveryWriteOneRecord(t *testing.T) {
	store := openTestStore(t)
	const copies = 16
	recordedBy := make([]bool, copies)
	var group sync.WaitGroup
	for i := range copies {
		group.Add(1)
		go func() {
			defer group.Done()
			id := "asi_race_" + string(rune('a'+i))
			recorded, err := store.RecordInbound(context.Background(), "hook:aph_x:EvRACE", testClock, slackInbound(id, "org_1"),
				testEvent(t, "apps.slack.message.received.v1"))
			if err != nil {
				t.Errorf("copy %d = %v", i, err)
			}
			recordedBy[i] = recorded
		}()
	}
	group.Wait()
	winners := 0
	for _, recorded := range recordedBy {
		if recorded {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("copies that recorded = %d", winners)
	}
	if n := countRows(t, store, `SELECT count(*) FROM apps_slack_inbound`); n != 1 {
		t.Fatalf("records = %d", n)
	}
	if n := countRows(t, store, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.slack.message.received.v1'`); n != 1 {
		t.Fatalf("events = %d", n)
	}
}

func TestADeliveryThatEndsAnInstallEndsItWithItsMark(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.SaveInstall(ctx, slackInstall("org_1", "T0ACME01"), slackEvent(t, "apps.slack.install.changed.v1")); err != nil {
		t.Fatal(err)
	}
	recorded, err := store.RecordInstallEnd(ctx, "hook:aph_x:EvEND", testClock, "T0ACME01", "app_uninstalled", slackEnded(t))
	if err != nil || !recorded {
		t.Fatalf("RecordInstallEnd = %v, %v", recorded, err)
	}
	if got, _ := store.InstallByTeam(ctx, "T0ACME01"); got.Active() || got.Sealed != nil || got.EndReason != "app_uninstalled" {
		t.Fatalf("install = %+v", got)
	}
	// The same delivery again, and another that finds nothing to end: nothing more, and both marked.
	for _, mark := range []string{"hook:aph_x:EvEND", "hook:aph_x:EvEND2"} {
		if recorded, err := store.RecordInstallEnd(ctx, mark, testClock, "T0ACME01", "tokens_revoked", slackEnded(t)); err != nil || recorded {
			t.Fatalf("RecordInstallEnd(%s) = %v, %v", mark, recorded, err)
		}
	}
	if handled, _ := store.HookDeliveryHandled(ctx, "hook:aph_x:EvEND2"); !handled {
		t.Fatal("a delivery that found nothing to end was not marked")
	}
	if n := countRows(t, store, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.slack.install.changed.v1'`); n != 2 {
		t.Fatalf("events = %d, want the install's and its end", n)
	}
}

func TestPostsGoOnlyWhereTheWorkspaceHasBeenSeenForAWeek(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.RecordInbound(ctx, "hook:aph_x:Ev1", testClock, slackInbound("asi_1", "org_1"), testEvent(t, "apps.slack.message.received.v1")); err != nil {
		t.Fatal(err)
	}
	click := slackInbound("asi_2", "org_1")
	click.Kind, click.EventID, click.ChannelID, click.UserID, click.MessageTS, click.ThreadTS = slackbot.InboundAction, "", "D0RAYA", "U0RAYA", "1780000500.000900", "1780000500.000900"
	click.Mentions = nil
	if _, err := store.RecordInbound(ctx, "hook:aph_x:sha256:abc", testClock, click, testEvent(t, "apps.slack.action.received.v1")); err != nil {
		t.Fatal(err)
	}

	// A thread is found by its root or by a message in it, and answers its root.
	for _, ts := range []string{"1780000000.000100", "1780000100.000200"} {
		root, found, err := store.ThreadSeen(ctx, "org_1", "T0ACME01", "C0GENERAL", ts)
		if err != nil || !found || root != "1780000000.000100" {
			t.Fatalf("ThreadSeen(%s) = %q, %v, %v", ts, root, found, err)
		}
	}
	if root, found, _ := store.ThreadSeen(ctx, "org_1", "T0ACME01", "D0RAYA", "1780000500.000900"); !found || root != "1780000500.000900" {
		t.Fatalf("a click's message = %q, %v", root, found)
	}
	for name, seen := range map[string]func() (string, bool, error){
		"another channel": func() (string, bool, error) {
			return store.ThreadSeen(ctx, "org_1", "T0ACME01", "C0OTHER", "1780000000.000100")
		},
		"another organization": func() (string, bool, error) {
			return store.ThreadSeen(ctx, "org_2", "T0ACME01", "C0GENERAL", "1780000000.000100")
		},
		"another workspace": func() (string, bool, error) {
			return store.ThreadSeen(ctx, "org_1", "T0OTHER01", "C0GENERAL", "1780000000.000100")
		},
		"another thread": func() (string, bool, error) {
			return store.ThreadSeen(ctx, "org_1", "T0ACME01", "C0GENERAL", "1700000000.000001")
		},
	} {
		if _, found, err := seen(); err != nil || found {
			t.Errorf("%s: found = %v, %v", name, found, err)
		}
	}

	// A person is seen by writing, by being named, or by clicking, within the window.
	since := testClock.Add(-time.Hour)
	for user, want := range map[string]bool{"U0ASKER": true, "U0RAYA": true, "U0MAYA": true, "U0STRANGE": false} {
		if seen, err := store.UserSeen(ctx, "org_1", "T0ACME01", user, since); err != nil || seen != want {
			t.Errorf("UserSeen(%s) = %v, %v, want %v", user, seen, err, want)
		}
	}
	if seen, _ := store.UserSeen(ctx, "org_2", "T0ACME01", "U0ASKER", since); seen {
		t.Error("a person seen by another organization")
	}
	if seen, _ := store.UserSeen(ctx, "org_1", "T0ACME01", "U0ASKER", testClock.Add(time.Hour)); seen {
		t.Error("a person seen before the window")
	}

	// A record is forgotten past its retention, and its thread with it.
	purged, err := store.Purge(ctx, slackbot.Cutoffs{Inbound: testClock.Add(time.Minute)})
	if err != nil || purged.Inbound != 2 {
		t.Fatalf("Purge = %+v, %v", purged, err)
	}
	if _, err := store.Inbound(ctx, "asi_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a purged record = %v", err)
	}
	if _, found, _ := store.ThreadSeen(ctx, "org_1", "T0ACME01", "C0GENERAL", "1780000000.000100"); found {
		t.Fatal("a purged record's thread is still seen")
	}
}

func TestAPostIsClaimedOnceCompletedAndFoundByWhereItWent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	post := slackbot.Post{
		OrganizationID: "org_1", Key: "key-0001", RequestHash: "hash-1", TeamID: "T0ACME01", Template: "answer",
		Kind: slackbot.PostMessage, ClaimedAt: testClock,
	}
	claimed, mine, err := store.ClaimPost(ctx, post, testClock.Add(-time.Minute))
	if err != nil || !mine || claimed.Key != "key-0001" || claimed.CompletedAt != nil {
		t.Fatalf("ClaimPost = %+v, %v, %v", claimed, mine, err)
	}
	// Someone else asking for the key is told who has it, and a stale claim is theirs to take.
	held, mine, err := store.ClaimPost(ctx, post, testClock.Add(-time.Minute))
	if err != nil || mine || held.RequestHash != "hash-1" || held.CompletedAt != nil {
		t.Fatalf("a second claim = %+v, %v, %v", held, mine, err)
	}
	later := post
	later.ClaimedAt = testClock.Add(time.Minute)
	if _, mine, err := store.ClaimPost(ctx, later, testClock.Add(30*time.Second)); err != nil || !mine {
		t.Fatalf("taking over a stale claim = %v, %v", mine, err)
	}
	// Not for something else: a claim of another request is never taken over.
	other := later
	other.RequestHash = "hash-2"
	other.ClaimedAt = testClock.Add(2 * time.Hour)
	if held, mine, err := store.ClaimPost(ctx, other, testClock.Add(time.Hour)); err != nil || mine || held.RequestHash != "hash-1" {
		t.Fatalf("a claim of another request = %+v, %v, %v", held, mine, err)
	}

	if err := store.CompletePost(ctx, "org_1", "key-0001", "C0GENERAL", "1780000900.000100", testClock.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	done, mine, err := store.ClaimPost(ctx, post, testClock.Add(time.Hour))
	if err != nil || mine || done.CompletedAt == nil || done.ChannelID != "C0GENERAL" || done.TS != "1780000900.000100" {
		t.Fatalf("a completed post claimed again = %+v, %v, %v", done, mine, err)
	}
	found, err := store.PostAt(ctx, "org_1", "C0GENERAL", "1780000900.000100")
	if err != nil || found.Key != "key-0001" || found.Template != "answer" {
		t.Fatalf("PostAt = %+v, %v", found, err)
	}
	if _, err := store.PostAt(ctx, "org_2", "C0GENERAL", "1780000900.000100"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another organization's PostAt = %v", err)
	}
	// A post that was made is not released; one that was not, is.
	if err := store.ReleasePost(ctx, "org_1", "key-0001"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostAt(ctx, "org_1", "C0GENERAL", "1780000900.000100"); err != nil {
		t.Fatalf("a completed post was released: %v", err)
	}
	fresh := post
	fresh.Key = "key-0002"
	if _, mine, _ := store.ClaimPost(ctx, fresh, testClock); !mine {
		t.Fatal("a fresh claim was not made")
	}
	if err := store.ReleasePost(ctx, "org_1", "key-0002"); err != nil {
		t.Fatal(err)
	}
	if _, mine, _ := store.ClaimPost(ctx, fresh, testClock); !mine {
		t.Fatal("a released key was not free")
	}
	if err := store.CompletePost(ctx, "org_1", "key-none", "C0GENERAL", "1.000001", testClock); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("completing a post never claimed = %v", err)
	}
}

func TestSlackProfilesAreKeptAndFoundWhileFresh(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	profile := slackbot.Profile{
		TeamID: "T0ACME01", UserID: "U0RAYA", Email: "raya@acme.test", EmailConfirmed: true, Name: "Raya Rao", Title: "Engineer",
		IsGuest: true, FetchedAt: testClock,
	}
	if err := store.SaveProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	found, err := store.Profiles(ctx, "T0ACME01", []string{"U0RAYA"}, testClock.Add(-time.Hour))
	if err != nil || len(found) != 1 || !found[0].EmailConfirmed {
		t.Fatalf("a confirmed address = %+v, %v", found, err)
	}
	profile.Email, profile.EmailConfirmed, profile.IsGuest, profile.IsStranger, profile.FetchedAt = "raya@new.test", false, false, true, testClock.Add(time.Hour)
	if err := store.SaveProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	found, err = store.Profiles(ctx, "T0ACME01", []string{"U0RAYA", "U0NOBODY"}, testClock.Add(-time.Hour))
	if err != nil || len(found) != 1 || found[0].Email != "raya@new.test" || found[0].EmailConfirmed || found[0].IsGuest || !found[0].IsStranger || found[0].Title != "Engineer" {
		t.Fatalf("Profiles = %+v, %v", found, err)
	}
	if stale, _ := store.Profiles(ctx, "T0ACME01", []string{"U0RAYA"}, testClock.Add(2*time.Hour)); len(stale) != 0 {
		t.Fatalf("a stale profile = %+v", stale)
	}
	if other, _ := store.Profiles(ctx, "T0OTHER01", []string{"U0RAYA"}, testClock.Add(-time.Hour)); len(other) != 0 {
		t.Fatalf("another workspace's profile = %+v", other)
	}
	purged, err := store.Purge(ctx, slackbot.Cutoffs{Profiles: testClock.Add(2 * time.Hour)})
	if err != nil || purged.Profiles != 1 {
		t.Fatalf("Purge = %+v, %v", purged, err)
	}
}

func TestHighPriorityChannelsAreReplacedAsASet(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mark := func(id, name string) slackbot.MarkedChannel {
		return slackbot.MarkedChannel{ChannelID: id, Name: name}
	}

	if err := store.ReplaceHighPriority(ctx, "org_1", "T0ACME01", "user-1", []slackbot.MarkedChannel{mark("G0ALPHA", "alpha"), mark("G0DELTA", "delta")}, testClock); err != nil {
		t.Fatal(err)
	}
	marked, err := store.HighPriorityChannels(ctx, "org_1", "T0ACME01")
	if err != nil || len(marked) != 2 || marked[0].ChannelID != "G0ALPHA" || marked[0].MarkedBy != "user-1" || !marked[0].MarkedAt.Equal(testClock) {
		t.Fatalf("HighPriorityChannels = %+v, %v", marked, err)
	}
	if on, err := store.IsHighPriority(ctx, "org_1", "T0ACME01", "G0ALPHA"); err != nil || !on {
		t.Fatalf("IsHighPriority = %v, %v", on, err)
	}
	// Another admin, later: a channel that stays keeps who marked it and when; one left out is unmarked.
	if err := store.ReplaceHighPriority(ctx, "org_1", "T0ACME01", "user-2", []slackbot.MarkedChannel{mark("G0ALPHA", "alpha renamed")}, testClock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	marked, _ = store.HighPriorityChannels(ctx, "org_1", "T0ACME01")
	if len(marked) != 1 || marked[0].ChannelID != "G0ALPHA" || marked[0].MarkedBy != "user-1" || !marked[0].MarkedAt.Equal(testClock) || marked[0].Name != "alpha renamed" {
		t.Fatalf("after a second set = %+v", marked)
	}
	if on, _ := store.IsHighPriority(ctx, "org_1", "T0ACME01", "G0DELTA"); on {
		t.Fatal("a channel left out is still marked")
	}
	// Marks are per organization and per workspace.
	if other, _ := store.HighPriorityChannels(ctx, "org_2", "T0ACME01"); len(other) != 0 {
		t.Fatalf("another organization's marks = %+v", other)
	}
	if on, _ := store.IsHighPriority(ctx, "org_1", "T0OTHER01", "G0ALPHA"); on {
		t.Fatal("a mark counted for another workspace")
	}
	// Marking one that was unmarked is a new mark.
	if err := store.ReplaceHighPriority(ctx, "org_1", "T0ACME01", "user-3", []slackbot.MarkedChannel{mark("G0ALPHA", "alpha"), mark("G0DELTA", "delta")}, testClock.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	marked, _ = store.HighPriorityChannels(ctx, "org_1", "T0ACME01")
	if len(marked) != 2 || marked[1].ChannelID != "G0DELTA" || marked[1].MarkedBy != "user-3" {
		t.Fatalf("after marking again = %+v", marked)
	}
	if err := store.ReplaceHighPriority(ctx, "org_1", "T0ACME01", "user-3", nil, testClock.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if marked, _ := store.HighPriorityChannels(ctx, "org_1", "T0ACME01"); len(marked) != 0 {
		t.Fatalf("after unmarking all = %+v", marked)
	}
}

func TestAnInstallStateIsSpentOnceWhileItLasts(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	flow := slackbot.InstallFlow{
		StateHash: "hash-1", OrganizationID: "org_1", MemberID: "user-1", ReturnTo: "https://app.oryxa.test/settings",
		CreatedAt: testClock, ExpiresAt: testClock.Add(10 * time.Minute),
	}
	if err := store.InsertInstallFlow(ctx, flow); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertInstallFlow(ctx, flow); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a state made twice = %v", err)
	}
	got, err := store.ConsumeInstallFlow(ctx, "hash-1", testClock.Add(time.Minute))
	if err != nil || got.OrganizationID != "org_1" || got.MemberID != "user-1" || got.ReturnTo != flow.ReturnTo || got.ConsumedAt == nil {
		t.Fatalf("ConsumeInstallFlow = %+v, %v", got, err)
	}
	if _, err := store.ConsumeInstallFlow(ctx, "hash-1", testClock.Add(2*time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a state used twice = %v", err)
	}
	if _, err := store.ConsumeInstallFlow(ctx, "hash-none", testClock); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an unknown state = %v", err)
	}
	flow.StateHash = "hash-2"
	if err := store.InsertInstallFlow(ctx, flow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeInstallFlow(ctx, "hash-2", testClock.Add(11*time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an expired state = %v", err)
	}
	purged, err := store.Purge(ctx, slackbot.Cutoffs{Flows: testClock.Add(time.Hour)})
	if err != nil || purged.Flows != 2 {
		t.Fatalf("Purge = %+v, %v", purged, err)
	}
}

func TestTheSlackTablesCarryNothingTheirRulesForbid(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	// An install with a token that has ended, or without one that is active, breaks the table's own rule.
	for name, statement := range map[string]string{
		"an ended install that kept its token": `INSERT INTO apps_slack_installs (team_id, organization_id, status, bot_user_id, sealed_bot_token, installed_at, updated_at)
			VALUES ('T0BAD001', 'org_x', 'ended', 'U0BOT', '{}'::jsonb, now(), now())`,
		"an active install with no token": `INSERT INTO apps_slack_installs (team_id, organization_id, status, bot_user_id, installed_at, updated_at)
			VALUES ('T0BAD002', 'org_y', 'active', 'U0BOT', now(), now())`,
		"a status that is neither": `INSERT INTO apps_slack_installs (team_id, organization_id, status, bot_user_id, installed_at, updated_at)
			VALUES ('T0BAD003', 'org_z', 'paused', 'U0BOT', now(), now())`,
		"an inbound record that is neither kind": `INSERT INTO apps_slack_inbound (id, organization_id, team_id, kind, channel_id, user_id, message_ts, thread_ts, received_at)
			VALUES ('asi_bad', 'org_1', 'T0ACME01', 'reaction', 'C0', 'U0', '1.1', '1.1', now())`,
	} {
		if _, err := store.pool.Exec(ctx, statement); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

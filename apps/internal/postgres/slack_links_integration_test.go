package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/slackbot"
)

// TestSlackLinksAreOnePersonEachWay: a workspace's user is linked to one
// member, and a member to one user of a workspace. A member linking another
// account replaces theirs; an account another member linked is refused,
// leaving theirs; and a link is read by the user or the member, and forgotten.
func TestSlackLinksAreOnePersonEachWay(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	at := testClock
	link := func(user, member string) slackbot.Link {
		return slackbot.Link{TeamID: "T0ACME01", SlackUserID: user, OrganizationID: "org_1", MemberID: member, LinkedAt: at}
	}
	if err := store.SaveLink(ctx, link("U0ADA", "user-ada")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLink(ctx, link("U0ADA", "user-raya")); !errors.Is(err, slackbot.ErrLinkTaken) {
		t.Fatalf("an account another member linked: %v, want ErrLinkTaken", err)
	}
	if err := store.SaveLink(ctx, link("U0ADA2", "user-ada")); err != nil {
		t.Fatalf("relinking: %v", err)
	}
	links, err := store.LinksForSlackUsers(ctx, "T0ACME01", []string{"U0ADA", "U0ADA2", "U0NOBODY"})
	if err != nil || len(links) != 1 || links[0].SlackUserID != "U0ADA2" || links[0].MemberID != "user-ada" {
		t.Fatalf("links = %+v, %v; want Ada's new account alone", links, err)
	}
	if got, err := store.LinkForMember(ctx, "org_1", "user-ada"); err != nil || got.SlackUserID != "U0ADA2" || !got.LinkedAt.Equal(at) {
		t.Fatalf("Ada's link = %+v, %v", got, err)
	}
	if removed, err := store.DeleteLink(ctx, "org_1", "user-ada"); err != nil || !removed {
		t.Fatalf("unlinking = %v, %v", removed, err)
	}
	if _, err := store.LinkForMember(ctx, "org_1", "user-ada"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after unlinking: %v", err)
	}
	if removed, err := store.DeleteLink(ctx, "org_1", "user-ada"); err != nil || removed {
		t.Fatalf("unlinking again = %v, %v", removed, err)
	}
	// Freed, the account links to another member.
	if err := store.SaveLink(ctx, link("U0ADA", "user-raya")); err != nil {
		t.Fatalf("a freed account: %v", err)
	}
}

// TestASlackLinkFlowIsSpentOnceAndExpires, as an install's is.
func TestASlackLinkFlowIsSpentOnceAndExpires(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	flow := slackbot.LinkFlow{
		StateHash: "hash-1", OrganizationID: "org_1", MemberID: "user-ada", TeamID: "T0ACME01",
		ReturnTo: "https://app.oryxa.test/settings", CreatedAt: testClock, ExpiresAt: testClock.Add(10 * time.Minute),
	}
	if err := store.InsertLinkFlow(ctx, flow); err != nil {
		t.Fatal(err)
	}
	got, err := store.ConsumeLinkFlow(ctx, "hash-1", testClock.Add(time.Minute))
	if err != nil || got.MemberID != "user-ada" || got.TeamID != "T0ACME01" || got.ReturnTo != flow.ReturnTo {
		t.Fatalf("ConsumeLinkFlow = %+v, %v", got, err)
	}
	if _, err := store.ConsumeLinkFlow(ctx, "hash-1", testClock.Add(time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("spent twice: %v", err)
	}
	flow.StateHash = "hash-2"
	if err := store.InsertLinkFlow(ctx, flow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeLinkFlow(ctx, "hash-2", testClock.Add(11*time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("spent late: %v", err)
	}
	purged, err := store.Purge(ctx, slackbot.Cutoffs{Inbound: testClock, Flows: testClock.Add(time.Hour), Posts: testClock, Profiles: testClock})
	if err != nil || purged.Flows != 2 {
		t.Fatalf("Purge = %+v, %v; want both link flows", purged, err)
	}
}

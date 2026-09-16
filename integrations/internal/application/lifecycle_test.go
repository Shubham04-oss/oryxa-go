package application

import (
	"context"
	"slices"
	"testing"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// population stores live subscriptions across two organizations, connections,
// teams and members, and app webhooks in both organizations.
func (h *harness) population() {
	live := func(id, organizationID, connectionID, teamID, createdBy string) {
		h.store.put(domain.Subscription{
			ID: id, OrganizationID: organizationID, ConnectionID: connectionID, Provider: "hooks", TeamID: teamID, CreatedBy: createdBy,
			CreatedAt: testNow.Add(-time.Hour), Status: domain.SubscriptionActive, Route: "route-" + id, RegistrationID: "reg-" + id,
		})
		h.connections.holds[connectionID] = true
	}
	live("sub_member_own", testOrganization, "conn_member", "", "user_member")
	live("sub_member_team_a", testOrganization, "conn_team_a", "team_a", "user_member")
	live("sub_member_team_b", testOrganization, "conn_team_b", "team_b", "user_member")
	live("sub_other_team_a", testOrganization, "conn_team_a", "team_a", "user_other")
	live("sub_elsewhere", "org_2", "conn_elsewhere", "", "user_member")
	h.store.webhooks["app_chat"] = domain.AppWebhook{AppID: "app_chat", OrganizationID: testOrganization, Provider: "chat", Route: "app-route-1"}
	h.store.webhooks["app_chat_2"] = domain.AppWebhook{AppID: "app_chat_2", OrganizationID: testOrganization, Provider: "chat", Route: "app-route-2"}
	h.store.webhooks["app_elsewhere"] = domain.AppWebhook{AppID: "app_elsewhere", OrganizationID: "org_2", Provider: "chat", Route: "app-route-3"}
}

// stopped are the IDs of stopped subscriptions, each with the reason and who
// stopped it.
func (h *harness) stopped() []string {
	var ids []string
	for _, subscription := range h.store.matching(func(subscription domain.Subscription) bool {
		return subscription.Status == domain.SubscriptionStopped
	}) {
		ids = append(ids, subscription.ID+" "+subscription.Reason+" by "+subscription.StoppedBy)
	}
	slices.Sort(ids)
	return ids
}

func TestOrganizationDeletedStopsItsSubscriptionsAndForgetsItsAppWebhooks(t *testing.T) {
	h := newHarness(t)
	h.population()
	event := autheventsv1.OrganizationDeleted{OrganizationID: testOrganization, DeletedBy: "user_admin"}

	if err := h.service.OrganizationDeleted(context.Background(), event); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"sub_member_own organization_deleted by ",
		"sub_member_team_a organization_deleted by ",
		"sub_member_team_b organization_deleted by ",
		"sub_other_team_a organization_deleted by ",
	}
	if got := h.stopped(); !slices.Equal(got, want) {
		t.Fatalf("stopped = %v, want %v", got, want)
	}
	if _, ok := h.store.webhooks["app_elsewhere"]; !ok || len(h.store.webhooks) != 1 {
		t.Fatalf("app webhooks = %v, want only another organization's", h.store.webhooks)
	}
	// Each registration still has to be removed, under the hold.
	for _, subscription := range h.store.matching(func(subscription domain.Subscription) bool { return subscription.Status == domain.SubscriptionStopped }) {
		if subscription.CleanupAt == nil {
			t.Fatalf("%s has no removal due", subscription.ID)
		}
	}
	if !h.connections.held("conn_member") {
		t.Fatal("a hold was released before its registrations were removed")
	}

	// The event is delivered again.
	if err := h.service.OrganizationDeleted(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if events := h.store.eventsOf(integrationsv1.SubscriptionStoppedDetailType); len(events) != 4 {
		t.Fatalf("stopped events after a repeated delivery = %d, want 4", len(events))
	}
}

func TestMemberRemovedStopsTheirSubscriptions(t *testing.T) {
	h := newHarness(t)
	h.population()

	if err := h.service.MemberRemoved(context.Background(), autheventsv1.MemberRemoved{OrganizationID: testOrganization, UserID: "user_member", Reason: "removed"}); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"sub_member_own member_left by ",
		"sub_member_team_a member_left by ",
		"sub_member_team_b member_left by ",
	}
	if got := h.stopped(); !slices.Equal(got, want) {
		t.Fatalf("stopped = %v, want %v", got, want)
	}
}

func TestMemberRemovedByTheOrganizationsDeletionIsLeftToThatEvent(t *testing.T) {
	h := newHarness(t)
	h.population()

	if err := h.service.MemberRemoved(context.Background(), autheventsv1.MemberRemoved{
		OrganizationID: testOrganization, UserID: "user_member", Reason: autheventsv1.MemberRemovedReasonOrganizationDeleted,
	}); err != nil {
		t.Fatal(err)
	}

	if got := h.stopped(); len(got) != 0 {
		t.Fatalf("stopped = %v, want none", got)
	}
}

func TestTeamMemberRemovedStopsTheirSubscriptionsOnTheTeam(t *testing.T) {
	h := newHarness(t)
	h.population()

	if err := h.service.TeamMemberRemoved(context.Background(), teamseventsv1.MemberRemoved{OrganizationID: testOrganization, TeamID: "team_a", UserID: "user_member"}); err != nil {
		t.Fatal(err)
	}

	if got, want := h.stopped(), []string{"sub_member_team_a team_member_left by "}; !slices.Equal(got, want) {
		t.Fatalf("stopped = %v, want %v", got, want)
	}
}

func TestConnectionDisconnectedStopsItsSubscriptions(t *testing.T) {
	h := newHarness(t)
	h.population()

	if err := h.service.ConnectionDisconnected(context.Background(), connectionseventsv1.ConnectionDisconnected{
		OrganizationID: testOrganization, ConnectionID: "conn_team_a", DisconnectedBy: "user_admin",
	}); err != nil {
		t.Fatal(err)
	}

	want := []string{"sub_member_team_a disconnected by user_admin", "sub_other_team_a disconnected by user_admin"}
	if got := h.stopped(); !slices.Equal(got, want) {
		t.Fatalf("stopped = %v, want %v", got, want)
	}
}

func TestAppDeletedForgetsItsWebhook(t *testing.T) {
	h := newHarness(t)
	h.population()

	if err := h.service.AppDeleted(context.Background(), connectionseventsv1.AppDeleted{OrganizationID: testOrganization, AppID: "app_chat"}); err != nil {
		t.Fatal(err)
	}

	if _, ok := h.store.webhooks["app_chat"]; ok || len(h.store.webhooks) != 2 {
		t.Fatalf("app webhooks = %v, want only app_chat forgotten", h.store.webhooks)
	}
	if got := h.stopped(); len(got) != 0 {
		t.Fatalf("stopped = %v, want none: disconnects stop an app's subscriptions", got)
	}
}

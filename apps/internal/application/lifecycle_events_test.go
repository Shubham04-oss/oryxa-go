package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// A connection and a trigger each have a life with several ends, and the bus
// carries all of them: every one of these announces itself, so nothing that
// happened to a member's apps is visible only in a log line.

// TestConnectingAnnouncesItselfAsItStarts covers the beginning, which used to
// be silent: a member sent to an app's page, with nothing yet able to act.
func TestConnectingAnnouncesItselfAsItStarts(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.toolkits["github"] = composio.Toolkit{Slug: "github", ComposioManagedAuthSchemes: []string{"OAUTH2"}}

	connection, redirect, err := h.service.Connect(context.Background(), memberToken, "org_1",
		ConnectInput{Toolkit: "github", ReturnTo: "https://app.oryxa.test/apps"})
	if err != nil || redirect == "" {
		t.Fatalf("Connect() = %+v, %q, %v", connection, redirect, err)
	}

	var initiated appsv1.ConnectionInitiated
	decodeOne(t, h, appsv1.ConnectionInitiatedDetailType, &initiated)
	if initiated.ConnectionID != connection.ID || initiated.Toolkit != "github" {
		t.Fatalf("initiated = %+v", initiated)
	}
	if initiated.OrganizationID != "org_1" || initiated.MemberID != "user-1" {
		t.Fatalf("initiated names %s in %s", initiated.MemberID, initiated.OrganizationID)
	}
	if initiated.InitiatedAt.IsZero() {
		t.Fatal("initiated carries no time")
	}
}

// TestAConnectionThatNeverCompletesAnnouncesWhy covers the three ways a
// connection ends without ever being usable. The event's reason is a
// constant rather than the prose a member reads, since it is grouped and
// counted.
func TestAConnectionThatNeverCompletesAnnouncesWhy(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		// arrange sets up Composio's answer for the callback.
		arrange func(h harness)
		want    string
		// forbidden says the callback is refused outright rather than
		// answering a failed connection.
		forbidden bool
	}{
		"the member was refused at the app": {
			arrange: func(h harness) {
				h.composio.accounts["ca_new"] = []composio.ConnectedAccount{{
					ID: "ca_new", UserID: domain.ComposioUserID("org_1", "user-1"), Toolkit: "github",
					Status: composio.StatusFailed,
				}}
			},
			want: appsv1.FailedDeniedAtProvider,
		},
		"the callback named another member's account": {
			arrange: func(h harness) {
				h.composio.accounts["ca_new"] = []composio.ConnectedAccount{{
					ID: "ca_new", UserID: domain.ComposioUserID("org_1", "someone-else"), Toolkit: "github",
					Status: composio.StatusActive,
				}}
			},
			want: appsv1.FailedWrongAccount, forbidden: true,
		},
		"the callback named another app": {
			arrange: func(h harness) {
				h.composio.accounts["ca_new"] = []composio.ConnectedAccount{{
					ID: "ca_new", UserID: domain.ComposioUserID("org_1", "user-1"), Toolkit: "slack",
					Status: composio.StatusActive,
				}}
			},
			want: appsv1.FailedWrongAccount, forbidden: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.composio.toolkits["github"] = composio.Toolkit{Slug: "github", ComposioManagedAuthSchemes: []string{"OAUTH2"}}
			testCase.arrange(h)
			state := h.startedConnection(t)

			_, err := h.service.CompleteConnection(context.Background(), ConnectCallback{State: state, CookieState: state, Status: "success", AccountID: "ca_new"})
			if testCase.forbidden && !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CompleteConnection() = %v, want forbidden", err)
			}
			if !testCase.forbidden && err != nil {
				t.Fatal(err)
			}

			var failed appsv1.ConnectionFailed
			decodeOne(t, h, appsv1.ConnectionFailedDetailType, &failed)
			if failed.Reason != testCase.want {
				t.Fatalf("reason = %q, want %q", failed.Reason, testCase.want)
			}
			if failed.Toolkit != "github" || failed.MemberID != "user-1" {
				t.Fatalf("failed = %+v", failed)
			}
			// It never became usable, so nothing says it connected.
			if events := h.store.published(appsv1.ConnectionConnectedDetailType); len(events) != 0 {
				t.Fatalf("a failed connection also announced itself connected: %+v", events)
			}
		})
	}
}

// TestAnAbandonedConnectionIsAnnouncedBySweep covers the end nothing else
// reports: a member who started connecting and never came back.
func TestAnAbandonedConnectionIsAnnouncedBySweep(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.toolkits["github"] = composio.Toolkit{Slug: "github", ComposioManagedAuthSchemes: []string{"OAUTH2"}}
	h.startedConnection(t)

	// Long enough after that the link has expired.
	h.service.now = func() time.Time { return testClock.Add(48 * time.Hour) }
	h.service.maintain(context.Background())

	var failed appsv1.ConnectionFailed
	decodeOne(t, h, appsv1.ConnectionFailedDetailType, &failed)
	if failed.Reason != appsv1.FailedAbandoned {
		t.Fatalf("reason = %q, want %q", failed.Reason, appsv1.FailedAbandoned)
	}

	// Sweeping again finds nothing, so a member is not told twice.
	h.service.maintain(context.Background())
	if events := h.store.published(appsv1.ConnectionFailedDetailType); len(events) != 1 {
		t.Fatalf("failure events after a second sweep = %d, want still 1", len(events))
	}
}

// TestSubscribingAndUnsubscribingAreAnnounced covers a trigger's two ends
// that a member causes; the two it does not are trigger.disabled and
// connection.removed.
func TestSubscribingAndUnsubscribingAreAnnounced(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	ctx := context.Background()

	trigger, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: connection.ID, Trigger: "GITHUB_STAR_ADDED_EVENT",
	})
	if err != nil {
		t.Fatal(err)
	}
	var subscribed appsv1.TriggerSubscribed
	decodeOne(t, h, appsv1.TriggerSubscribedDetailType, &subscribed)
	if subscribed.TriggerID != trigger.ID || subscribed.Trigger != "GITHUB_STAR_ADDED_EVENT" {
		t.Fatalf("subscribed = %+v", subscribed)
	}
	// How it delivers travels with it: a polled trigger explains a silence
	// that would otherwise look like a fault.
	if subscribed.Kind != domain.TriggerKindPoll {
		t.Fatalf("kind = %q, want poll", subscribed.Kind)
	}

	// Asking again answers the subscription that exists, and announces
	// nothing: nothing happened.
	if _, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: connection.ID, Trigger: "GITHUB_STAR_ADDED_EVENT",
	}); err != nil {
		t.Fatal(err)
	}
	if events := h.store.published(appsv1.TriggerSubscribedDetailType); len(events) != 1 {
		t.Fatalf("subscribing twice announced %d times", len(events))
	}

	if err := h.service.RemoveTrigger(ctx, memberToken, "org_1", trigger.ID); err != nil {
		t.Fatal(err)
	}
	var unsubscribed appsv1.TriggerUnsubscribed
	decodeOne(t, h, appsv1.TriggerUnsubscribedDetailType, &unsubscribed)
	if unsubscribed.TriggerID != trigger.ID || unsubscribed.ByAdmin {
		t.Fatalf("unsubscribed = %+v, want the member's own", unsubscribed)
	}

	// Removing again changes nothing, so it announces nothing.
	if err := h.service.RemoveTrigger(ctx, memberToken, "org_1", trigger.ID); err != nil {
		t.Fatal(err)
	}
	if events := h.store.published(appsv1.TriggerUnsubscribedDetailType); len(events) != 1 {
		t.Fatalf("removing twice announced %d times", len(events))
	}
}

// TestAnAdminUnsubscribingSaysSo separates a member giving up a trigger from
// an admin taking it away, which are the same change and not the same event.
func TestAnAdminUnsubscribingSaysSo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trigger := h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-1", "github", "ca_1").ID, "ti_1")

	if err := h.service.RemoveTrigger(context.Background(), adminToken, "org_1", trigger.ID); err != nil {
		t.Fatal(err)
	}
	var unsubscribed appsv1.TriggerUnsubscribed
	decodeOne(t, h, appsv1.TriggerUnsubscribedDetailType, &unsubscribed)
	if !unsubscribed.ByAdmin {
		t.Fatalf("an admin's removal did not say so: %+v", unsubscribed)
	}
	if unsubscribed.MemberID != "user-1" {
		t.Fatalf("the event names %s, want the trigger's own member", unsubscribed.MemberID)
	}
}

// startedConnection connects an app far enough to be pending and answers the
// state its callback would carry.
func (h harness) startedConnection(t *testing.T) string {
	t.Helper()
	if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1",
		ConnectInput{Toolkit: "github"}); err != nil {
		t.Fatal(err)
	}
	h.composio.mu.Lock()
	defer h.composio.mu.Unlock()
	if len(h.composio.links) == 0 {
		t.Fatal("no connect link was made")
	}
	callback, err := url.Parse(h.composio.links[len(h.composio.links)-1].CallbackURL)
	if err != nil {
		t.Fatal(err)
	}
	state := callback.Query().Get("state")
	if state == "" {
		t.Fatalf("the connect link carries no state: %q", callback)
	}
	return state
}

// decodeOne reads the single event of a detail type, failing if there is not
// exactly one: an event announced twice is as wrong as one never announced.
func decodeOne(t *testing.T, h harness, detailType string, into any) {
	t.Helper()
	events := h.store.published(detailType)
	if len(events) != 1 {
		t.Fatalf("%s events = %d, want 1", detailType, len(events))
	}
	if err := json.Unmarshal(events[0].Data, into); err != nil {
		t.Fatalf("decode %s: %v", detailType, err)
	}
}

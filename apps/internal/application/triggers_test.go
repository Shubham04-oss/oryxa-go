package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

func TestCreateTriggerSubscribesTheCallersConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")

	trigger, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		ConnectionID: connection.ID, Trigger: "github_commit_event", Config: json.RawMessage(`{"owner":"oryxa-labs","repo":"oryxa"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if trigger.Slug != "GITHUB_COMMIT_EVENT" || trigger.Status != domain.TriggerActive {
		t.Fatalf("trigger = %+v", trigger)
	}
	if trigger.ComposioTriggerID != "ti_1" || trigger.MemberID != "user-1" || trigger.Toolkit != "github" {
		t.Fatalf("trigger = %+v", trigger)
	}
	if len(h.composio.upserts) != 1 || h.composio.upserts[0] != "GITHUB_COMMIT_EVENT@ca_1" {
		t.Fatalf("upserts = %v", h.composio.upserts)
	}
	// The account's Composio user goes with it: a project with two-factor
	// protection refuses a subscription without one, which is every
	// subscription on such a project.
	if len(h.composio.upsertUsers) != 1 || h.composio.upsertUsers[0] != connection.ComposioUserID || connection.ComposioUserID == "" {
		t.Fatalf("upserted as users %v, want %q", h.composio.upsertUsers, connection.ComposioUserID)
	}

	// Asking again answers the subscription that exists rather than a second.
	again, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		ConnectionID: connection.ID, Trigger: "GITHUB_COMMIT_EVENT", Config: json.RawMessage(`{"owner":"oryxa-labs","repo":"oryxa"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != trigger.ID {
		t.Fatalf("a second subscription was recorded: %q and %q", trigger.ID, again.ID)
	}
	if len(h.store.triggers) != 1 {
		t.Fatalf("triggers = %d", len(h.store.triggers))
	}
}

func TestCreateTriggerRefusesWhatItCannotSubscribe(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	theirs := h.activeConnection("apc_2", "user-2", "github", "ca_2")
	expired := h.activeConnection("apc_3", "user-1", "slack", "ca_3")
	expired.Status = domain.ConnectionExpired
	h.store.connections[expired.ID] = expired

	for name, testCase := range map[string]struct {
		token        authclient.Credentials
		connectionID string
		trigger      string
		config       string
		want         error
	}{
		"another member's connection": {
			token: memberToken, connectionID: theirs.ID, trigger: "GITHUB_COMMIT_EVENT", want: domain.ErrNotFound,
		},
		"a connection that expired": {
			token: memberToken, connectionID: expired.ID, trigger: "SLACK_MESSAGE_EVENT", want: domain.ErrNeedsReconnect,
		},
		"a connection that does not exist": {
			token: memberToken, connectionID: "apc_nope", trigger: "GITHUB_COMMIT_EVENT", want: domain.ErrNotFound,
		},
		"a trigger slug that is not one": {
			token: memberToken, connectionID: mine.ID, trigger: "not a trigger", want: domain.ErrInvalid,
		},
		"a config that is not an object": {
			token: memberToken, connectionID: mine.ID, trigger: "GITHUB_COMMIT_EVENT", config: `[1]`, want: domain.ErrInvalid,
		},
		"an agent acting for the member": {
			token: agentToken, connectionID: mine.ID, trigger: "GITHUB_COMMIT_EVENT", want: domain.ErrForbidden,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var config json.RawMessage
			if testCase.config != "" {
				config = json.RawMessage(testCase.config)
			}
			_, err := h.service.CreateTrigger(context.Background(), testCase.token, "org_1", TriggerInput{
				ConnectionID: testCase.connectionID, Trigger: testCase.trigger, Config: config,
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
	if len(h.composio.upserts) != 0 {
		t.Fatalf("subscribed at Composio anyway: %v", h.composio.upserts)
	}
	if len(h.store.triggers) != 0 {
		t.Fatalf("triggers recorded = %d", len(h.store.triggers))
	}
}

func TestTriggersListTheCallersOwn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mine := h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-1", "github", "ca_1").ID, "ti_1")
	theirs := h.activeTrigger("apt_2", h.activeConnection("apc_2", "user-2", "github", "ca_2").ID, "ti_2")
	theirs.MemberID = "user-2"
	h.store.triggers[theirs.ID] = theirs

	listed, err := h.service.Triggers(context.Background(), memberToken, "org_1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != mine.ID {
		t.Fatalf("listed = %+v", listed)
	}
	// An agent sees the member's, since it acts for them.
	if listed, err = h.service.Triggers(context.Background(), agentToken, "org_1", ""); err != nil || len(listed) != 1 {
		t.Fatalf("an agent's listing = %+v, %v", listed, err)
	}
	// By connection.
	if listed, err = h.service.Triggers(context.Background(), memberToken, "org_1", "apc_2"); err != nil || len(listed) != 0 {
		t.Fatalf("by another member's connection = %+v, %v", listed, err)
	}
}

func TestRemoveTriggerUnsubscribesItAtComposio(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trigger := h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-1", "github", "ca_1").ID, "ti_1")

	if err := h.service.RemoveTrigger(context.Background(), memberToken, "org_1", trigger.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.store.triggers[trigger.ID]; got.Status != domain.TriggerRemoved {
		t.Fatalf("status = %q", got.Status)
	}
	if len(h.composio.deletedTriggers) != 1 || h.composio.deletedTriggers[0] != "ti_1" {
		t.Fatalf("deleted = %v", h.composio.deletedTriggers)
	}
	// Removing again is quiet.
	if err := h.service.RemoveTrigger(context.Background(), memberToken, "org_1", trigger.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveTriggerChecksWhoIsAsking(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	theirs := h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-2", "github", "ca_1").ID, "ti_1")
	theirs.MemberID = "user-2"
	h.store.triggers[theirs.ID] = theirs

	if err := h.service.RemoveTrigger(context.Background(), memberToken, "org_1", theirs.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's = %v, want not found", err)
	}
	if err := h.service.RemoveTrigger(context.Background(), agentToken, "org_1", theirs.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an agent's = %v, want forbidden", err)
	}
	if got := h.store.triggers[theirs.ID]; got.Status != domain.TriggerActive {
		t.Fatalf("status = %q, want active", got.Status)
	}
	// An org admin may unsubscribe a member's trigger.
	if err := h.service.RemoveTrigger(context.Background(), adminToken, "org_1", theirs.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveTriggerToleratesWhatComposioNoLongerHas(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.composio.deleteErr = composio.ErrNotFound
	trigger := h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-1", "github", "ca_1").ID, "ti_1")

	if err := h.service.RemoveTrigger(context.Background(), memberToken, "org_1", trigger.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.store.triggers[trigger.ID]; got.Status != domain.TriggerRemoved {
		t.Fatalf("status = %q", got.Status)
	}
}

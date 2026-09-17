package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// delivery signs a body the way Composio does, so a test exercises the same
// path a real delivery takes.
func delivery(t *testing.T, secret, id string, at time.Time, body string) (http.Header, []byte) {
	t.Helper()
	timestamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + timestamp + "." + body))
	header := http.Header{}
	header.Set("webhook-id", id)
	header.Set("webhook-timestamp", timestamp)
	header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return header, []byte(body)
}

// triggerMessage is a delivery about an event an app pushed.
func triggerMessage(id, composioTriggerID, accountID, data string) string {
	return fmt.Sprintf(`{"id":%q,"type":%q,"timestamp":"2026-09-17T12:00:00Z",
		"metadata":{"trigger_id":%q,"trigger_slug":"GITHUB_COMMIT_EVENT","connected_account_id":%q},
		"data":%s}`, id, composio.EventTriggerMessage, composioTriggerID, accountID, data)
}

// activeTrigger records a live trigger on a connection.
func (h harness) activeTrigger(id, connectionID, composioTriggerID string) domain.Trigger {
	trigger := domain.Trigger{
		ID: id, OrganizationID: "org_1", MemberID: "user-1", ConnectionID: connectionID,
		Toolkit: "github", Slug: "GITHUB_COMMIT_EVENT", ComposioTriggerID: composioTriggerID,
		Status: domain.TriggerActive, CreatedAt: testClock, UpdatedAt: testClock,
	}
	h.store.triggers[id] = trigger
	return trigger
}

func TestReceiveWebhookTurnsATriggerMessageIntoAnEvent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	trigger := h.activeTrigger("apt_1", connection.ID, "ti_1")
	header, body := delivery(t, "whsec_test", "wh_1", testClock,
		triggerMessage("wh_1", "ti_1", "ca_1", `{"commit":"abc123"}`))

	if err := h.service.ReceiveWebhook(context.Background(), header, body); err != nil {
		t.Fatal(err)
	}
	events := h.store.published(appsv1.TriggerReceivedDetailType)
	if len(events) != 1 {
		t.Fatalf("received events = %d", len(events))
	}
	data := string(events[0].Data)
	for _, want := range []string{
		`"trigger_id":"` + trigger.ID + `"`, `"connection_id":"` + connection.ID + `"`,
		`"organization_id":"org_1"`, `"member_id":"user-1"`, `"trigger":"GITHUB_COMMIT_EVENT"`,
		`"commit":"abc123"`,
	} {
		if !strings.Contains(data, want) {
			t.Fatalf("event data = %s, want %s", data, want)
		}
	}

	// Composio redelivers what it is not sure reached us; the event is
	// published once.
	if err := h.service.ReceiveWebhook(context.Background(), header, body); err != nil {
		t.Fatal(err)
	}
	if len(h.store.published(appsv1.TriggerReceivedDetailType)) != 1 {
		t.Fatal("a redelivery published a second event")
	}
}

func TestReceiveWebhookRefusesWhatItCannotVerify(t *testing.T) {
	t.Parallel()
	body := triggerMessage("wh_1", "ti_1", "ca_1", `{}`)

	for name, prepare := range map[string]func(*testing.T, harness) (http.Header, []byte){
		"another secret": func(t *testing.T, h harness) (http.Header, []byte) {
			return delivery(t, "whsec_someone_else", "wh_1", testClock, body)
		},
		"a body changed after signing": func(t *testing.T, h harness) (http.Header, []byte) {
			header, _ := delivery(t, "whsec_test", "wh_1", testClock, body)
			return header, []byte(triggerMessage("wh_1", "ti_9", "ca_1", `{}`))
		},
		"an id changed after signing": func(t *testing.T, h harness) (http.Header, []byte) {
			header, signed := delivery(t, "whsec_test", "wh_1", testClock, body)
			header.Set("webhook-id", "wh_2")
			return header, signed
		},
		"a delivery captured an hour ago": func(t *testing.T, h harness) (http.Header, []byte) {
			return delivery(t, "whsec_test", "wh_1", testClock.Add(-time.Hour), body)
		},
		"no signature at all": func(t *testing.T, h harness) (http.Header, []byte) {
			return http.Header{}, []byte(body)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-1", "github", "ca_1").ID, "ti_1")
			header, signed := prepare(t, h)

			if err := h.service.ReceiveWebhook(context.Background(), header, signed); !errors.Is(err, ErrUnverified) {
				t.Fatalf("error = %v, want unverified", err)
			}
			// Nothing in an unverified delivery is used, not even to mark it
			// handled.
			if len(h.store.events) != 0 || len(h.store.handled) != 0 {
				t.Fatalf("events = %d, handled = %d", len(h.store.events), len(h.store.handled))
			}
		})
	}
}

func TestReceiveWebhookWithoutASecretRefusesEverything(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.service.webhookSecret = ""
	header, body := delivery(t, "", "wh_1", testClock, triggerMessage("wh_1", "ti_1", "ca_1", `{}`))

	if err := h.service.ReceiveWebhook(context.Background(), header, body); !errors.Is(err, ErrUnverified) {
		t.Fatalf("error = %v, want unverified", err)
	}
}

func TestReceiveWebhookDropsWhatItHasNothingToDoWith(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		prepare func(harness)
		body    string
	}{
		"a trigger it does not know": {
			prepare: func(h harness) { h.activeConnection("apc_1", "user-1", "github", "ca_1") },
			body:    triggerMessage("wh_1", "ti_unknown", "ca_1", `{}`),
		},
		"a trigger since disabled": {
			prepare: func(h harness) {
				trigger := h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-1", "github", "ca_1").ID, "ti_1")
				trigger.Status = domain.TriggerDisabled
				h.store.triggers[trigger.ID] = trigger
			},
			body: triggerMessage("wh_1", "ti_1", "ca_1", `{}`),
		},
		"an account that is not the trigger's": {
			prepare: func(h harness) {
				h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-1", "github", "ca_1").ID, "ti_1")
				h.activeConnection("apc_2", "user-2", "github", "ca_2")
			},
			body: triggerMessage("wh_1", "ti_1", "ca_2", `{}`),
		},
		"an event type it does not handle": {
			prepare: func(harness) {},
			body:    `{"id":"wh_1","type":"composio.something.else","data":{}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			testCase.prepare(h)
			header, body := delivery(t, "whsec_test", "wh_1", testClock, testCase.body)

			// Dropping is quiet: Composio would retry a failure forever.
			if err := h.service.ReceiveWebhook(context.Background(), header, body); err != nil {
				t.Fatal(err)
			}
			if len(h.store.events) != 0 {
				t.Fatalf("published %d events", len(h.store.events))
			}
			if !h.store.handled["wh_1"] {
				t.Fatal("the delivery was not recorded as handled")
			}
		})
	}
}

func TestReceiveWebhookRefusesADeliveryThatIsNotWhatItClaims(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"not JSON":                  `not json at all`,
		"no id":                     `{"type":"composio.trigger.message","data":{}}`,
		"no type":                   `{"id":"wh_1","data":{}}`,
		"a message naming nothing":  `{"id":"wh_1","type":"composio.trigger.message","metadata":{},"data":{}}`,
		"an expiry naming nothing":  `{"id":"wh_1","type":"composio.connected_account.expired","data":{}}`,
		"a disabled naming nothing": `{"id":"wh_1","type":"composio.trigger.disabled","data":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			header, signed := delivery(t, "whsec_test", "wh_1", testClock, body)

			if err := h.service.ReceiveWebhook(context.Background(), header, signed); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error = %v, want invalid", err)
			}
		})
	}
}

func TestReceiveWebhookLeavesOversizedDataOut(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-1", "github", "ca_1").ID, "ti_1")
	big := `{"blob":"` + strings.Repeat("x", appsv1.MaxTriggerDataBytes) + `"}`
	header, body := delivery(t, "whsec_test", "wh_1", testClock, triggerMessage("wh_1", "ti_1", "ca_1", big))

	if err := h.service.ReceiveWebhook(context.Background(), header, body); err != nil {
		t.Fatal(err)
	}
	events := h.store.published(appsv1.TriggerReceivedDetailType)
	if len(events) != 1 {
		t.Fatalf("received events = %d", len(events))
	}
	// The event says the payload was left out rather than carrying it onto
	// the bus, which has its own limit.
	data := string(events[0].Data)
	if !strings.Contains(data, `"data_omitted":true`) || strings.Contains(data, "xxxx") {
		t.Fatalf("event data = %.120s…", data)
	}
}

func TestReceiveWebhookExpiresAConnectionComposioLost(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	body := fmt.Sprintf(`{"id":"wh_1","type":%q,"data":{"id":"ca_1","user_id":"oryxa.org_1.user-1",
		"toolkit":{"slug":"github"},"status":"EXPIRED","status_reason":"the refresh token was revoked"}}`,
		composio.EventConnectedAccountExpired)
	header, signed := delivery(t, "whsec_test", "wh_1", testClock, body)

	if err := h.service.ReceiveWebhook(context.Background(), header, signed); err != nil {
		t.Fatal(err)
	}
	if got := h.store.connections[connection.ID]; got.Status != domain.ConnectionExpired {
		t.Fatalf("status = %q", got.Status)
	}
	events := h.store.published(appsv1.ConnectionExpiredDetailType)
	if len(events) != 1 || !strings.Contains(string(events[0].Data), "the refresh token was revoked") {
		t.Fatalf("expired events = %+v", events)
	}

	// A redelivery must not expire a connection its member has reconnected
	// since.
	reconnected := h.store.connections[connection.ID]
	reconnected.Status = domain.ConnectionActive
	h.store.connections[connection.ID] = reconnected
	if err := h.service.ReceiveWebhook(context.Background(), header, signed); err != nil {
		t.Fatal(err)
	}
	if got := h.store.connections[connection.ID]; got.Status != domain.ConnectionActive {
		t.Fatalf("a redelivery expired a reconnected connection: %q", got.Status)
	}
	if len(h.store.published(appsv1.ConnectionExpiredDetailType)) != 1 {
		t.Fatal("a redelivery published a second event")
	}
}

func TestReceiveWebhookDisablesATriggerComposioStopped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	trigger := h.activeTrigger("apt_1", h.activeConnection("apc_1", "user-1", "github", "ca_1").ID, "ti_1")
	body := fmt.Sprintf(`{"id":"wh_1","type":%q,"data":{"id":"ti_1","connected_account_id":"ca_1",
		"trigger_name":"GITHUB_COMMIT_EVENT","disabled_reason":"too many failures"}}`, composio.EventTriggerDisabled)
	header, signed := delivery(t, "whsec_test", "wh_1", testClock, body)

	if err := h.service.ReceiveWebhook(context.Background(), header, signed); err != nil {
		t.Fatal(err)
	}
	if got := h.store.triggers[trigger.ID]; got.Status != domain.TriggerDisabled || got.StatusReason != "too many failures" {
		t.Fatalf("trigger = %+v", got)
	}
	events := h.store.published(appsv1.TriggerDisabledDetailType)
	if len(events) != 1 || !strings.Contains(string(events[0].Data), `"trigger":"GITHUB_COMMIT_EVENT"`) {
		t.Fatalf("disabled events = %+v", events)
	}
}

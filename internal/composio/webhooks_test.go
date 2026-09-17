package composio

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

const testSecret = "whsec_test"

// sign is Composio's documented scheme, written out independently of
// VerifyWebhook so a test proves the two agree.
func sign(secret, id, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + timestamp + "." + string(body)))
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func signedHeader(secret, id string, at time.Time, body []byte) http.Header {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	header := http.Header{}
	header.Set("webhook-id", id)
	header.Set("webhook-timestamp", timestamp)
	header.Set("webhook-signature", sign(secret, id, timestamp, body))
	return header
}

// triggerMessageJSON is the example the API reference gives.
const triggerMessageJSON = `{
  "id": "msg_2f1c9e4a-3b6d-4a1e-9c2f-7d8e5a6b4c31",
  "type": "composio.trigger.message",
  "timestamp": "2026-01-15T10:30:00Z",
  "metadata": {
    "log_id": "log_abc123",
    "trigger_slug": "GITHUB_COMMIT_EVENT",
    "trigger_id": "ti_xyz789",
    "connected_account_id": "ca_def456",
    "auth_config_id": "ac_xyz789",
    "user_id": "user-123"
  },
  "data": {
    "commit_sha": "a1b2c3d",
    "message": "fix: resolve null pointer",
    "author": "jane"
  }
}`

func TestVerifyWebhookAcceptsOnlyAFreshDeliveryComposioSigned(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	body := []byte(triggerMessageJSON)
	id := "msg_2f1c9e4a-3b6d-4a1e-9c2f-7d8e5a6b4c31"

	if err := VerifyWebhook(testSecret, signedHeader(testSecret, id, now, body), body, now); err != nil {
		t.Fatalf("VerifyWebhook(genuine) = %v", err)
	}

	rotated := signedHeader(testSecret, id, now, body)
	rotated.Set("webhook-signature", sign("old-secret", id, rotated.Get("webhook-timestamp"), body)+" "+rotated.Get("webhook-signature"))
	if err := VerifyWebhook(testSecret, rotated, body, now); err != nil {
		t.Fatalf("VerifyWebhook(one of several signatures matches) = %v", err)
	}

	for name, check := range map[string]func() error{
		"another secret": func() error {
			return VerifyWebhook(testSecret, signedHeader("attacker", id, now, body), body, now)
		},
		"a changed body": func() error {
			return VerifyWebhook(testSecret, signedHeader(testSecret, id, now, body), append([]byte(" "), body...), now)
		},
		"another id": func() error {
			header := signedHeader(testSecret, id, now, body)
			header.Set("webhook-id", "msg_other")
			return VerifyWebhook(testSecret, header, body, now)
		},
		"a stale delivery": func() error {
			return VerifyWebhook(testSecret, signedHeader(testSecret, id, now.Add(-6*time.Minute), body), body, now)
		},
		"a delivery from the future": func() error {
			return VerifyWebhook(testSecret, signedHeader(testSecret, id, now.Add(6*time.Minute), body), body, now)
		},
		"no signature": func() error {
			header := signedHeader(testSecret, id, now, body)
			header.Del("webhook-signature")
			return VerifyWebhook(testSecret, header, body, now)
		},
		"an unknown signature version": func() error {
			header := signedHeader(testSecret, id, now, body)
			header.Set("webhook-signature", "v2,"+header.Get("webhook-signature")[3:])
			return VerifyWebhook(testSecret, header, body, now)
		},
		"no configured secret": func() error {
			return VerifyWebhook("", signedHeader("", id, now, body), body, now)
		},
	} {
		if err := check(); !errors.Is(err, ErrUnverified) {
			t.Errorf("VerifyWebhook(%s) = %v, want ErrUnverified", name, err)
		}
	}
}

func TestParseWebhookReadsATriggerMessage(t *testing.T) {
	event, err := ParseWebhook([]byte(triggerMessageJSON))
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != EventTriggerMessage || event.ID != "msg_2f1c9e4a-3b6d-4a1e-9c2f-7d8e5a6b4c31" || event.Timestamp.IsZero() {
		t.Fatalf("event = %+v", event)
	}
	message, err := event.TriggerMessage()
	if err != nil {
		t.Fatal(err)
	}
	if message.TriggerID != "ti_xyz789" || message.ConnectedAccountID != "ca_def456" || message.TriggerSlug != "GITHUB_COMMIT_EVENT" ||
		message.UserID == nil || *message.UserID != "user-123" {
		t.Fatalf("message = %+v", message)
	}
	if _, err := event.DisabledTrigger(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("DisabledTrigger() on a trigger message = %v, want ErrInvalid", err)
	}
}

func TestParseWebhookReadsAnExpiredAccountAndADisabledTrigger(t *testing.T) {
	expired, err := ParseWebhook([]byte(`{
	  "id": "msg_9c8b7a6d-5e4f-4a3b-2c1d-0e9f8a7b6c5d",
	  "type": "composio.connected_account.expired",
	  "timestamp": "2026-01-15T10:30:00Z",
	  "metadata": {"project_id": "proj_abc123", "org_id": "org_abc123"},
	  "data": {
	    "id": "ca_a1b2c3d4e5f6g7h8", "toolkit": {"slug": "github"},
	    "auth_config": {"id": "ac_x1y2z3a4b5c6", "auth_scheme": "OAUTH2", "is_composio_managed": true, "is_disabled": false},
	    "word_id": "github_red-castle", "alias": null, "user_id": "user-123", "status": "EXPIRED",
	    "created_at": "2026-01-10T09:00:00Z", "updated_at": "2026-01-15T10:30:00Z",
	    "state": {"authScheme": "OAUTH2", "val": {"status": "EXPIRED"}}, "data": {}, "params": {},
	    "status_reason": "Connected account authentication expired.", "is_disabled": false
	  }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	account, err := expired.ExpiredAccount()
	if err != nil || account.ID != "ca_a1b2c3d4e5f6g7h8" || account.Status != StatusExpired ||
		account.StatusReason != "Connected account authentication expired." {
		t.Fatalf("ExpiredAccount() = %+v, %v", account, err)
	}

	disabled, err := ParseWebhook([]byte(`{
	  "id": "msg_7b3a1c2d-4e5f-4a6b-8c9d-0e1f2a3b4c5d",
	  "type": "composio.trigger.disabled",
	  "timestamp": "2026-01-15T10:30:00Z",
	  "metadata": {"project_id": "proj_abc123"},
	  "data": {
	    "id": "ti_xyz789", "connected_account_id": "ca_def456", "trigger_name": "GITHUB_COMMIT_EVENT",
	    "user_id": "user-123", "trigger_config": {"repo": "composio"},
	    "disabled_at": "2026-01-15T10:30:00Z", "disabled_reason": "Connected account authentication expired."
	  }
	}`))
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := disabled.DisabledTrigger()
	if err != nil || trigger.ID != "ti_xyz789" || trigger.ConnectedAccountID != "ca_def456" || trigger.DisabledReason == "" {
		t.Fatalf("DisabledTrigger() = %+v, %v", trigger, err)
	}
}

func TestParseWebhookRefusesWhatIsNotAnEvent(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":   `nope`,
		"no id":      `{"type": "composio.trigger.message"}`,
		"no type":    `{"id": "msg_1"}`,
		"no account": `{"id": "msg_1", "type": "composio.trigger.message", "metadata": {"trigger_id": "ti_1"}, "data": {}}`,
	} {
		event, err := ParseWebhook([]byte(body))
		if err == nil {
			_, err = event.TriggerMessage()
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
}

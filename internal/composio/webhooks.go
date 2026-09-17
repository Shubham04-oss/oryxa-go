package composio

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WebhookVersion is the payload version this package reads.
const WebhookVersion = "V3"

// Event types Composio delivers.
const (
	EventTriggerMessage          = "composio.trigger.message"
	EventConnectedAccountExpired = "composio.connected_account.expired"
	EventTriggerDisabled         = "composio.trigger.disabled"
)

// WebhookTolerance is how far a delivery's timestamp may be from now, the
// window Composio's own SDK enforces, so a captured delivery cannot be
// replayed later.
const WebhookTolerance = 5 * time.Minute

// ErrUnverified means a webhook delivery's signature does not prove it came
// from Composio. Nothing in such a delivery may be used.
var ErrUnverified = errors.New("the webhook delivery is not signed by composio")

// VerifyWebhook proves a delivery came from Composio: an HMAC-SHA256, keyed
// with the subscription's secret, over "{webhook-id}.{webhook-timestamp}.{body}",
// base64-encoded in a webhook-signature header of the form "v1,<signature>".
// Several space-separated signatures are accepted, as when a secret rotates.
func VerifyWebhook(secret string, header http.Header, body []byte, now time.Time) error {
	if secret == "" {
		return fmt.Errorf("%w: no secret is configured", ErrUnverified)
	}
	id := header.Get("webhook-id")
	timestamp := header.Get("webhook-timestamp")
	signatures := header.Get("webhook-signature")
	if id == "" || timestamp == "" || signatures == "" {
		return fmt.Errorf("%w: a signature header is missing", ErrUnverified)
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: the timestamp is not a number", ErrUnverified)
	}
	if age := now.Sub(time.Unix(seconds, 0)); age > WebhookTolerance || age < -WebhookTolerance {
		return fmt.Errorf("%w: the timestamp is outside the tolerance", ErrUnverified)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	expected := mac.Sum(nil)
	for _, candidate := range strings.Fields(signatures) {
		version, encoded, ok := strings.Cut(candidate, ",")
		if !ok || version != "v1" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		if hmac.Equal(decoded, expected) {
			return nil
		}
	}
	return fmt.Errorf("%w: no signature matches", ErrUnverified)
}

// WebhookEvent is one delivery, as read after it was verified. Metadata and
// Data are left raw: their shapes depend on Type.
type WebhookEvent struct {
	ID        string
	Type      string
	Timestamp time.Time
	Metadata  json.RawMessage
	Data      json.RawMessage
}

// ParseWebhook reads a verified delivery's envelope.
func ParseWebhook(body []byte) (WebhookEvent, error) {
	var wire struct {
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Metadata  json.RawMessage `json:"metadata"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: the delivery is not JSON", ErrInvalid)
	}
	if wire.ID == "" || wire.Type == "" {
		return WebhookEvent{}, fmt.Errorf("%w: the delivery has no id or type", ErrInvalid)
	}
	return WebhookEvent{
		ID: wire.ID, Type: wire.Type, Timestamp: timeOf(wire.Timestamp), Metadata: wire.Metadata, Data: wire.Data,
	}, nil
}

// TriggerMessage is a composio.trigger.message event's metadata: which
// subscription fired, for which account.
type TriggerMessage struct {
	LogID              string  `json:"log_id"`
	TriggerSlug        string  `json:"trigger_slug"`
	TriggerID          string  `json:"trigger_id"`
	ConnectedAccountID string  `json:"connected_account_id"`
	AuthConfigID       string  `json:"auth_config_id"`
	UserID             *string `json:"user_id"`
}

// TriggerMessage reads a trigger message's metadata. Data is the event itself.
func (event WebhookEvent) TriggerMessage() (TriggerMessage, error) {
	var message TriggerMessage
	if event.Type != EventTriggerMessage {
		return message, fmt.Errorf("%w: %s is not a trigger message", ErrInvalid, event.Type)
	}
	if err := json.Unmarshal(event.Metadata, &message); err != nil || message.TriggerID == "" || message.ConnectedAccountID == "" {
		return TriggerMessage{}, fmt.Errorf("%w: the trigger message names no trigger or account", ErrInvalid)
	}
	return message, nil
}

// ExpiredAccount reads a composio.connected_account.expired event's account.
func (event WebhookEvent) ExpiredAccount() (ConnectedAccount, error) {
	if event.Type != EventConnectedAccountExpired {
		return ConnectedAccount{}, fmt.Errorf("%w: %s is not an expiry", ErrInvalid, event.Type)
	}
	var wire connectedAccountWire
	if err := json.Unmarshal(event.Data, &wire); err != nil || wire.ID == "" {
		return ConnectedAccount{}, fmt.Errorf("%w: the expiry names no account", ErrInvalid)
	}
	return wire.account(), nil
}

// DisabledTrigger is a composio.trigger.disabled event's data.
type DisabledTrigger struct {
	ID                 string          `json:"id"`
	ConnectedAccountID string          `json:"connected_account_id"`
	TriggerName        string          `json:"trigger_name"`
	UserID             *string         `json:"user_id"`
	TriggerConfig      json.RawMessage `json:"trigger_config"`
	DisabledAt         string          `json:"disabled_at"`
	DisabledReason     string          `json:"disabled_reason"`
}

// DisabledTrigger reads a composio.trigger.disabled event's data.
func (event WebhookEvent) DisabledTrigger() (DisabledTrigger, error) {
	var disabled DisabledTrigger
	if event.Type != EventTriggerDisabled {
		return disabled, fmt.Errorf("%w: %s is not a disabled trigger", ErrInvalid, event.Type)
	}
	if err := json.Unmarshal(event.Data, &disabled); err != nil || disabled.ID == "" {
		return DisabledTrigger{}, fmt.Errorf("%w: the event names no trigger", ErrInvalid)
	}
	return disabled, nil
}

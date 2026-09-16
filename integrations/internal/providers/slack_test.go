package providers

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestSlackTarget(t *testing.T) {
	if _, _, err := slackTarget(map[string]string{}, []string{"message"}); err != nil {
		t.Errorf("slackTarget(empty target) error = %v, want nil", err)
	}
	if _, _, err := slackTarget(map[string]string{"channel": "C1"}, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("slackTarget(with a target field) error = %v, want %v", err, domain.ErrInvalid)
	}
	if _, _, err := slackTarget(map[string]string{}, []string{"Message"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("slackTarget(bad event name) error = %v, want %v", err, domain.ErrInvalid)
	}
	if _, events, err := slackTarget(map[string]string{}, []string{"message", "reaction_added"}); err != nil || !slices.Equal(events, []string{"message", "reaction_added"}) {
		t.Errorf("slackTarget(valid events) = %v, %v, want [message reaction_added], nil", events, err)
	}
}

func TestSlackAccountKey(t *testing.T) {
	tests := []struct {
		id      string
		want    string
		wantErr bool
	}{
		{"T123:U456", "T123", false},
		{":U456", "", true},
		{"", "", true},
	}
	for _, tc := range tests {
		got, err := slackAccountKey(domain.ProviderAccount{ID: tc.id})
		if tc.wantErr {
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("slackAccountKey(%q) error = %v, want %v", tc.id, err, domain.ErrInvalid)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("slackAccountKey(%q) = %q, %v, want %q, nil", tc.id, got, err, tc.want)
		}
	}
}

// slackSecret is the fixed signing secret the task specifies for
// slackReceive tests.
var slackSecret = []byte("8f742231b10e8888abcd99yyyzzz85a5")

// slackDelivery signs body as Slack would at "at", for verification against
// fixedNow in slackReceive.
func slackDelivery(secret []byte, at time.Time, body []byte) Delivered {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	message := append([]byte("v0:"+timestamp+":"), body...)
	header := http.Header{}
	header.Set("X-Slack-Request-Timestamp", timestamp)
	header.Set("X-Slack-Signature", "v0="+hmacHex(secret, message))
	return Delivered{Header: header, Body: body}
}

func TestSlackReceive(t *testing.T) {
	t.Run("timestamp 6 minutes old", func(t *testing.T) {
		body := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"message"}}`)
		delivery := slackDelivery(slackSecret, fixedNow.Add(-6*time.Minute), body)
		if _, err := slackReceive(delivery, slackSecret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("slackReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("timestamp 6 minutes ahead", func(t *testing.T) {
		body := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"message"}}`)
		delivery := slackDelivery(slackSecret, fixedNow.Add(6*time.Minute), body)
		if _, err := slackReceive(delivery, slackSecret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("slackReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("non-numeric timestamp", func(t *testing.T) {
		header := http.Header{}
		header.Set("X-Slack-Request-Timestamp", "not-a-number")
		header.Set("X-Slack-Signature", "v0=irrelevant")
		if _, err := slackReceive(Delivered{Header: header, Body: []byte(`{}`)}, slackSecret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("slackReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("wrong signature", func(t *testing.T) {
		body := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"message"}}`)
		delivery := slackDelivery([]byte("some-other-secret"), fixedNow, body)
		if _, err := slackReceive(delivery, slackSecret, fixedNow); !errors.Is(err, ErrUnverified) {
			t.Fatalf("slackReceive() error = %v, want %v", err, ErrUnverified)
		}
	})

	t.Run("url_verification", func(t *testing.T) {
		body := []byte(`{"type":"url_verification","challenge":"abc123"}`)
		delivery := slackDelivery(slackSecret, fixedNow, body)
		reception, err := slackReceive(delivery, slackSecret, fixedNow)
		if err != nil {
			t.Fatalf("slackReceive() error = %v", err)
		}
		if reception.Reply == nil || reception.Reply.Status != http.StatusOK || reception.Reply.ContentType != "text/plain" || string(reception.Reply.Body) != "abc123" {
			t.Fatalf("Reply = %+v, want {200 text/plain abc123}", reception.Reply)
		}
	})

	t.Run("event_callback with authorizations and a message event", func(t *testing.T) {
		body := []byte(`{"token":"deprecated-verify-token","type":"event_callback","team_id":"T1","event_id":"Ev1","event_time":1700000000,` +
			`"authorizations":[{"team_id":"T2"},{"team_id":"T1"}],"event":{"type":"message","ts":"111.222","channel":"C1","user":"U1"}}`)
		delivery := slackDelivery(slackSecret, fixedNow, body)
		reception, err := slackReceive(delivery, slackSecret, fixedNow)
		if err != nil {
			t.Fatalf("slackReceive() error = %v", err)
		}
		if !slices.Equal(reception.AccountKeys, []string{"T1", "T2"}) {
			t.Fatalf("AccountKeys = %v, want [T1 T2]", reception.AccountKeys)
		}
		if len(reception.Events) != 1 {
			t.Fatalf("Events = %v, want 1", reception.Events)
		}
		event := reception.Events[0]
		if event.Name != "message" || event.Type != "message" {
			t.Errorf("Name, Type = %q, %q, want message, message", event.Name, event.Type)
		}
		wantResource := integrationsv1.Resource{Type: "message", ID: "111.222", Name: "C1"}
		if event.Resource != wantResource {
			t.Errorf("Resource = %+v, want %+v", event.Resource, wantResource)
		}
		if event.Actor == nil || event.Actor.ID != "U1" {
			t.Errorf("Actor = %+v, want ID U1", event.Actor)
		}
		if event.Key != "Ev1" || event.DeliveryID != "Ev1" {
			t.Errorf("Key, DeliveryID = %q, %q, want Ev1, Ev1", event.Key, event.DeliveryID)
		}
		if !event.OccurredAt.Equal(time.Unix(1700000000, 0).UTC()) {
			t.Errorf("OccurredAt = %v, want %v", event.OccurredAt, time.Unix(1700000000, 0).UTC())
		}
		if strings.Contains(string(event.Payload), "token") {
			t.Errorf("Payload still carries the token field: %s", event.Payload)
		}
		if !strings.Contains(string(event.Payload), `"event":`) {
			t.Errorf("Payload dropped the event field: %s", event.Payload)
		}
	})

	t.Run("reaction_added", func(t *testing.T) {
		body := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"reaction_added","reaction":"thumbsup",` +
			`"item":{"type":"message","channel":"C1","ts":"111.222"},"user":"U9"}}`)
		delivery := slackDelivery(slackSecret, fixedNow, body)
		reception, err := slackReceive(delivery, slackSecret, fixedNow)
		if err != nil || len(reception.Events) != 1 {
			t.Fatalf("slackReceive() = %+v, %v", reception, err)
		}
		wantResource := integrationsv1.Resource{Type: "reaction", ID: "111.222", Name: "thumbsup"}
		if reception.Events[0].Resource != wantResource {
			t.Errorf("Resource = %+v, want %+v", reception.Events[0].Resource, wantResource)
		}
	})

	t.Run("team_join", func(t *testing.T) {
		body := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"team_join","user":{"id":"U2","name":"newbie"}}}`)
		delivery := slackDelivery(slackSecret, fixedNow, body)
		reception, err := slackReceive(delivery, slackSecret, fixedNow)
		if err != nil || len(reception.Events) != 1 {
			t.Fatalf("slackReceive() = %+v, %v", reception, err)
		}
		event := reception.Events[0]
		wantResource := integrationsv1.Resource{Type: "user", ID: "U2", Name: "newbie"}
		if event.Resource != wantResource {
			t.Errorf("Resource = %+v, want %+v", event.Resource, wantResource)
		}
		if event.Actor == nil || *event.Actor != (integrationsv1.Actor{ID: "U2", Name: "newbie"}) {
			t.Errorf("Actor = %+v, want {U2 newbie}", event.Actor)
		}
	})

	t.Run("app_rate_limited gives an empty Reception", func(t *testing.T) {
		body := []byte(`{"type":"app_rate_limited","team_id":"T1"}`)
		delivery := slackDelivery(slackSecret, fixedNow, body)
		reception, err := slackReceive(delivery, slackSecret, fixedNow)
		if err != nil {
			t.Fatalf("slackReceive() error = %v", err)
		}
		if len(reception.Events) != 0 || reception.Reply != nil || len(reception.AccountKeys) != 0 {
			t.Fatalf("Reception = %+v, want empty", reception)
		}
	})

	t.Run("event_callback without an event type", func(t *testing.T) {
		body := []byte(`{"type":"event_callback","team_id":"T1","event":{}}`)
		delivery := slackDelivery(slackSecret, fixedNow, body)
		if _, err := slackReceive(delivery, slackSecret, fixedNow); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("slackReceive() error = %v, want %v", err, domain.ErrInvalid)
		}
	})
}

package google

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

type pushVerifier struct{ err error }

func (v pushVerifier) Verify(context.Context, string, string) error { return v.err }

type gmailTriggerLookup struct{ triggers []domain.Trigger }

func (l gmailTriggerLookup) ActiveGoogleGmailTriggers(context.Context, string) ([]domain.Trigger, error) {
	return l.triggers, nil
}

type pushClaims struct {
	keys    map[string]string
	commits int
}

func (c *pushClaims) Acquire(_ context.Context, key string, _, _, _ time.Time) (string, bool, error) {
	if c.keys == nil {
		c.keys = map[string]string{}
	}
	if _, exists := c.keys[key]; exists {
		return "", false, nil
	}
	c.keys[key] = "tok"
	return "tok", true, nil
}
func (c *pushClaims) Commit(context.Context, string, string) error {
	c.commits++
	return nil
}
func (c *pushClaims) Release(context.Context, string, string) error { return nil }

type pushQueue struct{ envelopes []webhooks.Envelope }

func (q *pushQueue) EnqueueWebhook(_ context.Context, envelope webhooks.Envelope) error {
	q.envelopes = append(q.envelopes, envelope)
	return nil
}

func TestPushServiceFansOutWithoutMessageContent(t *testing.T) {
	t.Parallel()
	payload := `{"emailAddress":"member@example.com","historyId":"42"}`
	raw := []byte(`{"message":{"data":"` + base64.StdEncoding.EncodeToString([]byte(payload)) + `","messageId":"m1"}}`)
	queue := &pushQueue{}
	claims := &pushClaims{}
	service := PushService{
		Verifier: pushVerifier{}, Triggers: gmailTriggerLookup{triggers: []domain.Trigger{{
			ID: "trg-1", OrganizationID: "org-1", ProfileID: "app-1", ConnectionID: "con-1",
			Provider: "google", PieceName: "@activepieces/piece-gmail", PieceVersion: "0.15.0", TriggerName: "new-email",
		}}},
		Claims: claims, Queue: queue, Audience: "https://connect.example.com/v1/webhooks/google/gmail",
		IDs: func(prefix string) (string, error) { return prefix + "-1", nil },
	}
	decision, err := service.Admit(context.Background(), PushRequest{Authorization: "Bearer good", RawBody: raw})
	if err != nil || decision.Status != 202 || len(queue.envelopes) != 1 {
		t.Fatalf("decision/error/queued = %+v/%v/%d", decision, err, len(queue.envelopes))
	}
	envelope := queue.envelopes[0]
	if envelope.Event != gmailMailboxEvent || envelope.ConnectionID != "con-1" || envelope.RawBody != nil ||
		len(envelope.Headers) != 0 {
		t.Fatalf("envelope = %+v", envelope)
	}
	notification, ok := envelope.Body.(MailboxNotification)
	if !ok || notification.EmailAddress != "member@example.com" || notification.HistoryID != "42" {
		t.Fatalf("body = %+v", envelope.Body)
	}
}

func TestPushServiceDropsUnknownMailboxAndRejectsBadToken(t *testing.T) {
	t.Parallel()
	payload := `{"emailAddress":"nobody@example.com","historyId":"1"}`
	raw := []byte(`{"message":{"data":"` + base64.StdEncoding.EncodeToString([]byte(payload)) + `"}}`)
	service := PushService{
		Verifier: pushVerifier{}, Triggers: gmailTriggerLookup{}, Claims: &pushClaims{}, Queue: &pushQueue{},
		Audience: "https://connect.example.com/v1/webhooks/google/gmail",
		IDs:      func(prefix string) (string, error) { return prefix, nil },
	}
	decision, err := service.Admit(context.Background(), PushRequest{Authorization: "Bearer good", RawBody: raw})
	if err != nil || decision.Status != 200 {
		t.Fatalf("unknown mailbox = %+v/%v", decision, err)
	}
	service.Verifier = pushVerifier{err: errors.New("bad token")}
	decision, err = service.Admit(context.Background(), PushRequest{Authorization: "Bearer bad", RawBody: raw})
	if err == nil || decision.Status != 401 {
		t.Fatalf("bad token = %+v/%v", decision, err)
	}
}

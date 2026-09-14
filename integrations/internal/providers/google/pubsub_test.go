package google

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodePubSubPushKeepsMailboxIdentityOnly(t *testing.T) {
	t.Parallel()
	payload := `{"emailAddress":"Member@Example.com","historyId":98765}`
	raw := []byte(`{"message":{"data":"` + base64.StdEncoding.EncodeToString([]byte(payload)) + `","messageId":"msg-1","publishTime":"2026-09-14T00:00:00Z"},"subscription":"projects/oryxa/subscriptions/gmail"}`)
	notification, err := DecodePubSubPush(raw)
	if err != nil || notification.EmailAddress != "member@example.com" || notification.HistoryID != "98765" ||
		notification.MessageID != "msg-1" {
		t.Fatalf("notification/error = %+v / %v", notification, err)
	}
	if strings.Contains(notification.EmailAddress+notification.HistoryID, "subject") {
		t.Fatal("unexpected content field")
	}
}

func TestDecodePubSubPushRejectsMissingMailbox(t *testing.T) {
	t.Parallel()
	if _, err := DecodePubSubPush([]byte(`{"message":{"data":"` + base64.StdEncoding.EncodeToString([]byte(`{"historyId":"1"}`)) + `"}}`)); err == nil {
		t.Fatal("missing email was accepted")
	}
	if _, err := DecodePubSubPush([]byte(`{"message":{}}`)); err == nil {
		t.Fatal("empty message was accepted")
	}
	if BearerToken("Bearer abc") != "abc" || BearerToken("abc") != "" {
		t.Fatal("bearer parsing")
	}
}

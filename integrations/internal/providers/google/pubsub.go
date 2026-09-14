package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
)

var ErrInvalidPush = errors.New("invalid Gmail push notification")

// MailboxNotification is the only Gmail watch material that may cross the
// admission boundary. Message bodies, subjects, and headers are never copied.
type MailboxNotification struct {
	EmailAddress string `json:"email_address"`
	HistoryID    string `json:"history_id"`
	MessageID    string `json:"message_id,omitempty"`
}

type PushVerifier interface {
	Verify(ctx context.Context, rawToken, audience string) error
}

func DecodePubSubPush(raw []byte) (MailboxNotification, error) {
	var envelope struct {
		Message struct {
			Data      string `json:"data"`
			MessageID string `json:"messageId"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Message.Data == "" {
		return MailboxNotification{}, ErrInvalidPush
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.Message.Data)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(envelope.Message.Data)
	}
	if err != nil || len(decoded) == 0 || len(decoded) > 8*1024 {
		return MailboxNotification{}, ErrInvalidPush
	}
	var payload struct {
		EmailAddress string      `json:"emailAddress"`
		HistoryID    json.Number `json:"historyId"`
	}
	if json.Unmarshal(decoded, &payload) != nil {
		return MailboxNotification{}, ErrInvalidPush
	}
	email := strings.TrimSpace(payload.EmailAddress)
	historyID := payload.HistoryID.String()
	if !validMailboxEmail(email) || !validHistoryID(historyID) {
		return MailboxNotification{}, ErrInvalidPush
	}
	notification := MailboxNotification{EmailAddress: strings.ToLower(email), HistoryID: historyID}
	if envelope.Message.MessageID != "" && len(envelope.Message.MessageID) <= 128 {
		notification.MessageID = envelope.Message.MessageID
	}
	return notification, nil
}

func BearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func validMailboxEmail(value string) bool {
	if value == "" || len(value) > 320 {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && !strings.ContainsAny(value, "\r\n")
}

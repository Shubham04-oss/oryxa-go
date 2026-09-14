package google

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

const gmailMailboxEvent = "mailbox-change"

type GmailTriggers interface {
	ActiveGoogleGmailTriggers(context.Context, string) ([]domain.Trigger, error)
}

type PushRequest struct {
	Authorization string
	RawBody       []byte
}

type PushService struct {
	Verifier PushVerifier
	Triggers GmailTriggers
	Claims   webhooks.Claims
	Queue    webhooks.Queue
	IDs      func(string) (string, error)
	Audience string
	Now      func() time.Time
}

func (s PushService) Admit(ctx context.Context, request PushRequest) (webhooks.Decision, error) {
	if s.Verifier == nil || s.Triggers == nil || s.Claims == nil || s.Queue == nil || s.Audience == "" {
		return webhooks.Decision{Status: 501}, errors.New("Gmail push admission is not configured")
	}
	token := BearerToken(request.Authorization)
	if token == "" {
		return webhooks.Decision{Status: 401}, webhooks.ErrSignatureInvalid
	}
	if err := s.Verifier.Verify(ctx, token, s.Audience); err != nil {
		return webhooks.Decision{Status: 401}, webhooks.ErrSignatureInvalid
	}
	notification, err := DecodePubSubPush(request.RawBody)
	if err != nil {
		return webhooks.Decision{Status: 401}, webhooks.ErrSignatureInvalid
	}
	triggers, err := s.Triggers.ActiveGoogleGmailTriggers(ctx, notification.EmailAddress)
	if err != nil {
		return webhooks.Decision{Status: 503}, fmt.Errorf("load Gmail mailbox triggers: %w", err)
	}
	if len(triggers) == 0 {
		return webhooks.Decision{Status: 200, Accepted: true}, nil
	}
	now := s.now()
	for _, trigger := range triggers {
		if trigger.Provider != "google" || trigger.TriggerName != "new-email" || trigger.ConnectionID == "" ||
			trigger.OrganizationID == "" || trigger.ProfileID == "" {
			return webhooks.Decision{Status: 503}, errors.New("invalid Gmail mailbox trigger")
		}
		invocationID, err := s.newID("whi")
		if err != nil {
			return webhooks.Decision{Status: 503}, err
		}
		claimKey := gmailDedupeKey(trigger, notification)
		claimToken, acquired, err := s.Claims.Acquire(ctx, claimKey, now, now.Add(30*time.Second), now.Add(24*time.Hour))
		if err != nil {
			return webhooks.Decision{Status: 503}, fmt.Errorf("claim Gmail delivery: %w", err)
		}
		if !acquired {
			continue
		}
		envelope := webhooks.Envelope{
			InvocationID:   invocationID,
			DeliveryID:     claimKey,
			OrganizationID: trigger.OrganizationID,
			ProfileID:      trigger.ProfileID,
			ConnectionID:   trigger.ConnectionID,
			Provider:       "google",
			PieceName:      trigger.PieceName,
			PieceVersion:   trigger.PieceVersion,
			TriggerName:    "new-email",
			Event:          gmailMailboxEvent,
			Method:         "POST",
			Headers:        map[string]string{},
			Query:          map[string]string{},
			Body:           MailboxNotification{EmailAddress: notification.EmailAddress, HistoryID: notification.HistoryID},
			ReceivedAt:     now,
		}
		if err := s.Queue.EnqueueWebhook(ctx, envelope); err != nil {
			releaseErr := s.Claims.Release(ctx, claimKey, claimToken)
			return webhooks.Decision{Status: 503}, errors.Join(fmt.Errorf("%w: %v", webhooks.ErrQueueUnavailable, err), releaseErr)
		}
		if err := s.Claims.Commit(ctx, claimKey, claimToken); err != nil {
			return webhooks.Decision{Status: 202, Accepted: true, Warning: fmt.Errorf("commit Gmail dedupe claim after enqueue: %w", err)}, nil
		}
	}
	return webhooks.Decision{Status: 202, Accepted: true}, nil
}

func gmailDedupeKey(trigger domain.Trigger, notification MailboxNotification) string {
	material := trigger.OrganizationID + "\x00" + trigger.ID + "\x00" + notification.HistoryID + "\x00" + notification.MessageID
	digest := sha256.Sum256([]byte(material))
	return "webhook:" + hex.EncodeToString(digest[:])
}

func (s PushService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s PushService) newID(prefix string) (string, error) {
	if s.IDs == nil {
		return "", errors.New("ID generator is required")
	}
	return s.IDs(prefix)
}

func NormalizeMailboxEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

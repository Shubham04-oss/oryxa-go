// Package delivery sends each email at most once per triggering event, keeps a
// log of every email and what became of it, and turns SES feedback into
// delivery statuses and suppressions.
package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/email/internal/mail"
)

type Status string

const (
	StatusSending    Status = "sending"
	StatusFailed     Status = "failed"
	StatusSuppressed Status = "suppressed"
	StatusExpired    Status = "expired"
	StatusSent       Status = "sent"
	StatusDelayed    Status = "delayed"
	StatusDelivered  Status = "delivered"
	StatusRejected   Status = "rejected"
	StatusBounced    Status = "bounced"
	StatusComplained Status = "complained"
)

// Rank orders statuses so that SES feedback, which can arrive out of order,
// never moves a delivery backwards: a late "delayed" cannot undo "delivered".
func (s Status) Rank() int {
	switch s {
	case StatusSent:
		return 1
	case StatusDelayed:
		return 2
	case StatusDelivered:
		return 3
	case StatusRejected, StatusBounced:
		return 4
	case StatusComplained:
		return 5
	default:
		return 0
	}
}

const (
	// ClaimLease is how long a claimed delivery belongs to one attempt. It has
	// to outlast a send, and stay under the queue's visibility timeout so that
	// the retry after a crash finds the claim expired and takes it over.
	ClaimLease = 30 * time.Second
	// Retention is how long a delivery stays in the log.
	Retention = 365 * 24 * time.Hour
)

// ErrInProgress means another attempt holds the delivery's claim, or has just
// failed it. Retrying later settles it: by then that attempt has recorded the
// send, or the claim is free to take.
var ErrInProgress = errors.New("delivery is being sent by another attempt")

// EventRef identifies the event an email is sent for.
type EventRef struct {
	Source     string
	DetailType string
	ID         string
}

type Request struct {
	Event     EventRef
	Recipient string
	Message   mail.Message
	// ExpiresAt, when set, is when the email stops being worth sending: a
	// link in it no longer works after then.
	ExpiresAt time.Time
}

type Delivery struct {
	ID             string
	Event          EventRef
	Template       string
	Recipient      string
	Status         Status
	LeaseExpiresAt time.Time
	SESMessageID   string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type Suppression struct {
	Address      string
	Reason       string
	Detail       string
	DeliveryID   string
	SESMessageID string
	CreatedAt    time.Time
}

const (
	ReasonBounce    = "bounce"
	ReasonComplaint = "complaint"
)

// Feedback is what SES reported about one sent message.
type Feedback struct {
	DeliveryID   string
	SESMessageID string
	Status       Status
	Recipients   []string
	Detail       string
	// Suppress marks the recipients as never to be emailed again: set for
	// permanent bounces and complaints.
	Suppress   bool
	Reason     string
	OccurredAt time.Time
}

type Outcome string

const (
	OutcomeSent       Outcome = "sent"
	OutcomeDuplicate  Outcome = "duplicate"
	OutcomeSuppressed Outcome = "suppressed"
	OutcomeExpired    Outcome = "expired"
)

type Store interface {
	Suppression(ctx context.Context, address string) (Suppression, bool, error)
	Suppress(ctx context.Context, suppression Suppression) error
	// Claim records delivery unless an attempt already sent it, settled it, or
	// holds an unexpired claim on it; it reports whether it did.
	Claim(ctx context.Context, delivery Delivery, now time.Time) (bool, error)
	Get(ctx context.Context, id string) (Delivery, error)
	// MarkSent reports false when the claim had already been settled by someone else.
	MarkSent(ctx context.Context, id, sesMessageID string, at time.Time) (bool, error)
	MarkFailed(ctx context.Context, id, reason string, at time.Time) error
	// RecordFeedback reports false for an unknown delivery, or one already at
	// a later status.
	RecordFeedback(ctx context.Context, feedback Feedback, at time.Time) (bool, error)
}

type sender interface {
	Send(context.Context, mail.Email) (string, error)
}

type Deliveries struct {
	store  Store
	sender sender
	logger *slog.Logger
	now    func() time.Time
}

func New(store Store, sender sender, logger *slog.Logger) *Deliveries {
	return &Deliveries{store: store, sender: sender, logger: logger, now: time.Now}
}

// ID identifies one email for one event, so every redelivery of the event
// resolves to the same record.
func ID(event EventRef, template, recipient string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{event.Source, event.ID, template, AddressKey(recipient)}, "\n")))
	return hex.EncodeToString(sum[:])
}

// AddressKey is the form addresses are suppressed and looked up under.
func AddressKey(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}

// Deliver sends the email unless this event already sent it, the recipient is
// suppressed, or the email has expired. SQS delivers at least once, so the log
// is what keeps a redelivered event from emailing someone twice. The one gap
// left is an attempt that dies between SES accepting the email and the log
// recording it; the retry then sends again once the claim expires.
func (d *Deliveries) Deliver(ctx context.Context, request Request) (Outcome, error) {
	if request.Event.ID == "" || request.Recipient == "" || request.Message.Template == "" {
		return "", errors.New("a delivery needs an event ID, a recipient and a template")
	}

	now := d.now()
	delivery := Delivery{
		ID:             ID(request.Event, request.Message.Template, request.Recipient),
		Event:          request.Event,
		Template:       request.Message.Template,
		Recipient:      request.Recipient,
		Status:         StatusSending,
		LeaseExpiresAt: now.Add(ClaimLease),
		CreatedAt:      now,
		ExpiresAt:      now.Add(Retention),
	}
	logger := d.logger.With("delivery_id", delivery.ID, "event_id", request.Event.ID, "template", delivery.Template)

	suppression, suppressed, err := d.store.Suppression(ctx, AddressKey(request.Recipient))
	if err != nil {
		return "", err
	}
	switch {
	case suppressed:
		delivery.Status = StatusSuppressed
	case !request.ExpiresAt.IsZero() && !now.Before(request.ExpiresAt):
		delivery.Status = StatusExpired
	}
	if delivery.Status != StatusSending {
		delivery.LeaseExpiresAt = time.Time{}
	}

	claimed, err := d.store.Claim(ctx, delivery, now)
	if err != nil {
		return "", err
	}
	if !claimed {
		existing, err := d.store.Get(ctx, delivery.ID)
		if err != nil {
			return "", err
		}
		// Only a settled delivery is a duplicate. One still sending, or failed
		// by an attempt that claimed it after this one tried, has not been
		// sent, and the message must stay on the queue until it is.
		if existing.Status == StatusSending || existing.Status == StatusFailed {
			return "", fmt.Errorf("delivery %s: %w", delivery.ID, ErrInProgress)
		}
		logger.InfoContext(ctx, "email already handled for this event", "status", existing.Status)
		return OutcomeDuplicate, nil
	}
	switch delivery.Status {
	case StatusSuppressed:
		logger.InfoContext(ctx, "email not sent: recipient is suppressed", "reason", suppression.Reason)
		return OutcomeSuppressed, nil
	case StatusExpired:
		logger.InfoContext(ctx, "email not sent: it expired before it could be sent", "expired_at", request.ExpiresAt)
		return OutcomeExpired, nil
	}

	messageID, err := d.sender.Send(ctx, mail.Email{To: request.Recipient, DeliveryID: delivery.ID, Message: request.Message})
	if err != nil {
		if markErr := d.store.MarkFailed(ctx, delivery.ID, err.Error(), d.now()); markErr != nil {
			logger.ErrorContext(ctx, "recording failed delivery failed", "error", markErr)
		}
		return "", err
	}
	recorded, err := d.store.MarkSent(ctx, delivery.ID, messageID, d.now())
	if err != nil {
		return "", fmt.Errorf("record delivery %s as sent (SES message %s): %w", delivery.ID, messageID, err)
	}
	if !recorded {
		logger.WarnContext(ctx, "another attempt settled this delivery first", "ses_message_id", messageID)
	}
	logger.InfoContext(ctx, "email sent", "ses_message_id", messageID)
	return OutcomeSent, nil
}

// RecordFeedback suppresses recipients SES says never to email again, and moves
// the delivery forward to the reported status.
func (d *Deliveries) RecordFeedback(ctx context.Context, feedback Feedback) error {
	logger := d.logger.With("delivery_id", feedback.DeliveryID, "ses_message_id", feedback.SESMessageID, "status", feedback.Status)

	if feedback.Suppress {
		for _, recipient := range feedback.Recipients {
			err := d.store.Suppress(ctx, Suppression{
				Address:      AddressKey(recipient),
				Reason:       feedback.Reason,
				Detail:       feedback.Detail,
				DeliveryID:   feedback.DeliveryID,
				SESMessageID: feedback.SESMessageID,
				CreatedAt:    d.now(),
			})
			if err != nil {
				return err
			}
		}
		logger.InfoContext(ctx, "recipients suppressed", "reason", feedback.Reason, "recipients", len(feedback.Recipients))
	}

	if feedback.DeliveryID == "" {
		logger.WarnContext(ctx, "SES feedback carries no delivery ID; status not recorded")
		return nil
	}
	applied, err := d.store.RecordFeedback(ctx, feedback, d.now())
	if err != nil {
		return err
	}
	if !applied {
		logger.InfoContext(ctx, "feedback not recorded: unknown delivery, or a later status is already recorded")
		return nil
	}
	logger.InfoContext(ctx, "delivery status recorded")
	return nil
}

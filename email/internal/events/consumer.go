package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	lambdaevents "github.com/aws/aws-lambda-go/events"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
)

// Subscription is one EventBridge event the email service consumes.
type Subscription struct {
	Source     string
	DetailType string
}

// The auth events the email service sends mail for: a welcome when a user
// registers; auth's delivery requests, whose links it carries without ever
// changing auth's state; and changes to a member's place in an organization.
var (
	userRegistered             = Subscription{Source: autheventsv1.Source, DetailType: autheventsv1.UserRegisteredDetailType}
	emailVerificationRequested = Subscription{Source: autheventsv1.Source, DetailType: autheventsv1.EmailVerificationDeliveryRequestedDetailType}
	passwordResetRequested     = Subscription{Source: autheventsv1.Source, DetailType: autheventsv1.PasswordResetDeliveryRequestedDetailType}
	memberInvited              = Subscription{Source: autheventsv1.Source, DetailType: autheventsv1.MemberInvitedDetailType}
	memberInvitationRevoked    = Subscription{Source: autheventsv1.Source, DetailType: autheventsv1.MemberInvitationRevokedDetailType}
	memberRoleChanged          = Subscription{Source: autheventsv1.Source, DetailType: autheventsv1.MemberRoleChangedDetailType}
	memberRemoved              = Subscription{Source: autheventsv1.Source, DetailType: autheventsv1.MemberRemovedDetailType}
)

// The billing events the email service writes to an organization's billing
// contact about.
var (
	subscriptionPaymentFailed   = Subscription{Source: billingeventsv1.Source, DetailType: billingeventsv1.SubscriptionPaymentFailedDetailType}
	subscriptionEnded           = Subscription{Source: billingeventsv1.Source, DetailType: billingeventsv1.SubscriptionEndedDetailType}
	subscriptionRenewalUpcoming = Subscription{Source: billingeventsv1.Source, DetailType: billingeventsv1.SubscriptionRenewalUpcomingDetailType}
)

// Subscriptions is every Oryxa domain event NewConsumer sends mail for. The
// subscriptions rule's event pattern is built from it, so an event missing
// here never reaches the queue.
func Subscriptions() []Subscription {
	return []Subscription{
		userRegistered,
		emailVerificationRequested,
		passwordResetRequested,
		memberInvited,
		memberInvitationRevoked,
		memberRoleChanged,
		memberRemoved,
		subscriptionPaymentFailed,
		subscriptionEnded,
		subscriptionRenewalUpcoming,
	}
}

type application interface {
	UserRegistered(context.Context, delivery.EventRef, autheventsv1.UserRegistered) error
	EmailVerificationRequested(context.Context, delivery.EventRef, autheventsv1.EmailVerificationDeliveryRequested) error
	PasswordResetRequested(context.Context, delivery.EventRef, autheventsv1.PasswordResetDeliveryRequested) error
	MemberInvited(context.Context, delivery.EventRef, autheventsv1.MemberInvited) error
	MemberInvitationRevoked(context.Context, delivery.EventRef, autheventsv1.MemberInvitationRevoked) error
	MemberRoleChanged(context.Context, delivery.EventRef, autheventsv1.MemberRoleChanged) error
	MemberRemoved(context.Context, delivery.EventRef, autheventsv1.MemberRemoved) error
	SubscriptionPaymentFailed(context.Context, delivery.EventRef, billingeventsv1.SubscriptionPaymentFailed) error
	SubscriptionEnded(context.Context, delivery.EventRef, billingeventsv1.SubscriptionEnded) error
	SubscriptionRenewalUpcoming(context.Context, delivery.EventRef, billingeventsv1.SubscriptionRenewalUpcoming) error
	SESFeedback(context.Context, delivery.Feedback) error
}

type handler func(context.Context, eventBridgeEvent) error

type Consumer struct {
	handlers map[Subscription]handler
	logger   *slog.Logger
}

func NewConsumer(app application, logger *slog.Logger) *Consumer {
	handlers := map[Subscription]handler{
		userRegistered:              domainEvent(app.UserRegistered),
		emailVerificationRequested:  domainEvent(app.EmailVerificationRequested),
		passwordResetRequested:      domainEvent(app.PasswordResetRequested),
		memberInvited:               domainEvent(app.MemberInvited),
		memberInvitationRevoked:     domainEvent(app.MemberInvitationRevoked),
		memberRoleChanged:           domainEvent(app.MemberRoleChanged),
		memberRemoved:               domainEvent(app.MemberRemoved),
		subscriptionPaymentFailed:   domainEvent(app.SubscriptionPaymentFailed),
		subscriptionEnded:           domainEvent(app.SubscriptionEnded),
		subscriptionRenewalUpcoming: domainEvent(app.SubscriptionRenewalUpcoming),
	}
	for _, detailType := range SESFeedbackDetailTypes() {
		handlers[Subscription{Source: SESSource, DetailType: detailType}] = sesFeedback(app.SESFeedback)
	}
	return &Consumer{handlers: handlers, logger: logger}
}

// eventBridgeEvent is the body of each message an EventBridge rule delivers to SQS.
type eventBridgeEvent struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	DetailType string          `json:"detail-type"`
	Detail     json.RawMessage `json:"detail"`
}

// envelope is the detail Oryxa publishers wrap every domain event's data in.
type envelope struct {
	EventID    string          `json:"event_id"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// HandleSQS processes a batch from Lambda's SQS event source. Failed records
// are reported individually so only they return to the queue for a retry.
func (c *Consumer) HandleSQS(ctx context.Context, batch lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var response lambdaevents.SQSEventResponse
	for _, record := range batch.Records {
		if err := c.handle(ctx, record.Body); err != nil {
			level := slog.LevelError
			if errors.Is(err, delivery.ErrInProgress) {
				level = slog.LevelWarn
			}
			c.logger.Log(ctx, level, "email event failed", "sqs_message_id", record.MessageId, "error", err)
			response.BatchItemFailures = append(response.BatchItemFailures, lambdaevents.SQSBatchItemFailure{
				ItemIdentifier: record.MessageId,
			})
		}
	}
	return response, nil
}

func (c *Consumer) handle(ctx context.Context, body string) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	handle, ok := c.handlers[Subscription{Source: event.Source, DetailType: event.DetailType}]
	if !ok {
		return fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}
	if err := handle(ctx, event); err != nil {
		return fmt.Errorf("event %s: %s: %w", event.ID, event.DetailType, err)
	}
	return nil
}

func domainEvent[T any](handle func(context.Context, delivery.EventRef, T) error) handler {
	return func(ctx context.Context, event eventBridgeEvent) error {
		var detail envelope
		if err := json.Unmarshal(event.Detail, &detail); err != nil {
			return fmt.Errorf("decode detail: %w", err)
		}
		if detail.Version != 1 {
			return fmt.Errorf("unsupported envelope version %d", detail.Version)
		}
		if detail.EventID == "" {
			return errors.New("envelope has no event_id")
		}

		var data T
		if err := json.Unmarshal(detail.Data, &data); err != nil {
			return fmt.Errorf("event %s: decode data: %w", detail.EventID, err)
		}
		ref := delivery.EventRef{Source: event.Source, DetailType: event.DetailType, ID: detail.EventID}
		if err := handle(ctx, ref, data); err != nil {
			return fmt.Errorf("event %s: %w", detail.EventID, err)
		}
		return nil
	}
}

func sesFeedback(handle func(context.Context, delivery.Feedback) error) handler {
	return func(ctx context.Context, event eventBridgeEvent) error {
		feedback, err := feedbackFromSES(event.DetailType, event.Detail)
		if err != nil {
			return err
		}
		return handle(ctx, feedback)
	}
}

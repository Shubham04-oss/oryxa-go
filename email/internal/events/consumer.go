package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// component identifies this consumer in its metrics and logs, low-cardinality
// and fixed regardless of which detail type it is handling.
const component = "email"

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

// The teams events the email service sends mail for when a member's
// notify_email switch is on. Teams owns that switch; this service only
// delivers.
var (
	memberPinged = Subscription{Source: teamseventsv1.Source, DetailType: teamseventsv1.MemberPingedDetailType}
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
		memberPinged,
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
	MemberPinged(context.Context, delivery.EventRef, teamseventsv1.MemberPinged) error
	SESFeedback(context.Context, delivery.Feedback) error
}

type handler func(context.Context, eventBridgeEvent) error

type Consumer struct {
	handlers map[Subscription]handler
	logger   *slog.Logger
	metrics  observability.ConsumerMetrics
	// verifier proves who published each domain event. Nil is a build error
	// caught at mount: a consumer that cannot check signatures would mail
	// whatever a queue handed it. SES's own feedback is exempt: AWS
	// publishes it, and no Oryxa key could sign it.
	verifier *eventauth.Verifier
	// queueName is set once, at the top of Poll, before Poll's single
	// goroutine starts calling handle; it is not touched concurrently.
	queueName string
}

func NewConsumer(app application, verifier *eventauth.Verifier, logger *slog.Logger) (*Consumer, error) {
	if verifier == nil {
		return nil, errors.New("email cannot consume unsigned events: a signature verifier is required")
	}
	metrics, err := observability.NewConsumerMetrics()
	if err != nil {
		logger.Warn("consumer metrics unavailable; handling continues without them", "error", err)
	}
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
		memberPinged:                domainEvent(app.MemberPinged),
	}
	for _, detailType := range SESFeedbackDetailTypes() {
		handlers[Subscription{Source: SESSource, DetailType: detailType}] = sesFeedback(app.SESFeedback)
	}
	return &Consumer{handlers: handlers, logger: logger, metrics: metrics, verifier: verifier}, nil
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
	// Signature is the publisher's proof over the envelope's content. It is
	// checked before any handler runs; a missing or wrong signature means
	// the message never came from the service it claims.
	Signature string `json:"sig"`
	// TraceCarrier decodes the envelope's optional traceparent/tracestate
	// fields; a message published before this rollout, or one that never
	// carried one, decodes to the zero TraceCarrier, and handling continues
	// under a fresh span rather than failing.
	observability.TraceCarrier
}

// HandleSQS processes a batch from Lambda's SQS event source. Failed records
// are reported individually so only they return to the queue for a retry.
//
// Its signature mirrors what an AWS Lambda SQS event source mapping calls
// directly, so it takes no receivedAt of its own the way other services'
// per-message handlers do for their Poll: "now, at the top of this batch" is
// what receipt means whether this runs under Poll's long poll or under an
// actual Lambda invocation.
func (c *Consumer) HandleSQS(ctx context.Context, batch lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	receivedAt := time.Now()
	var response lambdaevents.SQSEventResponse
	for _, record := range batch.Records {
		if err := c.handle(ctx, record.Body, receivedAt); err != nil {
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

func (c *Consumer) handle(ctx context.Context, body string, receivedAt time.Time) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	dispatch, ok := c.handlers[Subscription{Source: event.Source, DetailType: event.DetailType}]
	if !ok {
		return fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}

	// Best-effort: a domain event's detail is our own envelope, decoded again
	// (and this time checked) inside domainEvent's handler below; an SES
	// feedback notification's detail is shaped nothing like it and simply
	// decodes to the zero envelope, which is handled exactly like a message
	// with none of this rollout's fields at all.
	var detail envelope
	_ = json.Unmarshal(event.Detail, &detail)
	// The signature is checked before anything in the event is used, so a
	// message that reached the queue without the named service having sent
	// it is refused whatever its payload says. SES's own feedback carries no
	// Oryxa signature and is exempt inside Verify itself.
	if err := c.verifier.Verify(event.Source, event.DetailType, detail.EventID, detail.Version, detail.OccurredAt, detail.Data, detail.Signature); err != nil {
		return fmt.Errorf("event %s: %w", event.ID, err)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, detail.OccurredAt)
	eventID := detail.EventID
	if eventID == "" {
		eventID = event.ID
	}

	ctx, span := observability.StartConsumerSpan(ctx, detail.TraceCarrier, c.queueName, event.DetailType, eventID)
	started := time.Now()
	err := dispatch(ctx, event)
	observability.EndSpan(span, err)
	c.metrics.Record(ctx, component, event.DetailType, occurredAt, receivedAt, started, err)
	if err != nil {
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

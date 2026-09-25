// Package events hears auth's word that a membership ended, on realtime's own
// queue, and turns each into one purge by the Worker.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
)

// component identifies this consumer in its metrics and logs, low-cardinality
// and fixed regardless of which queue or detail type it is handling.
const component = "realtime"

// AuthSubscriptions are the auth events realtime consumes. The rule that
// delivers them to realtime's queue is built from this list.
func AuthSubscriptions() []string {
	return []string{
		autheventsv1.OrganizationDeletedDetailType,
		autheventsv1.MemberRemovedDetailType,
	}
}

// maxClockSkew is how far after an event was received the removal it names
// may lie. Auth's clock and this one agree far more closely than that, so a
// removal later still did not happen then, and a purge up to it would take
// what the member wrote after being invited back.
const maxClockSkew = 5 * time.Minute

// errFutureRemoval marks an event whose removal lies more than maxClockSkew
// after it was received. Its purge is dropped, not sent: retried until that
// time had passed, it would take whatever the member wrote in between.
var errFutureRemoval = errors.New("its removed_at is still to come")

// Purger is what a purge is sent through; *purge.Client is one.
type Purger interface {
	Purge(context.Context, purge.Request) error
}

// Consumer purges the realtime conversations of members who left an
// organization, and of organizations that were deleted.
type Consumer struct {
	purger Purger
	// verifier proves who published each event. Nil is a build error caught
	// at mount: a consumer that cannot check signatures would purge whatever
	// a queue handed it.
	verifier *eventauth.Verifier
	logger   *slog.Logger
	metrics  observability.ConsumerMetrics
	purged   *recent
	// queueName is set once, at the top of Poll, before Poll's single
	// goroutine starts calling handle; it is not touched concurrently.
	queueName string
}

// NewConsumer returns a consumer that purges through purger.
func NewConsumer(purger Purger, verifier *eventauth.Verifier, logger *slog.Logger) (*Consumer, error) {
	if purger == nil {
		return nil, errors.New("realtime needs the Worker to purge through")
	}
	if verifier == nil {
		return nil, errors.New("realtime cannot consume unsigned events: a signature verifier is required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	metrics, err := observability.NewConsumerMetrics()
	if err != nil {
		logger.Warn("consumer metrics unavailable; handling continues without them", "error", err)
	}
	return &Consumer{purger: purger, verifier: verifier, logger: logger, metrics: metrics, purged: newRecent(recentSize)}, nil
}

type eventBridgeEvent struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	DetailType string          `json:"detail-type"`
	Detail     json.RawMessage `json:"detail"`
}

type receivedEnvelope struct {
	EventID    string          `json:"event_id"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
	// Signature is the publisher's proof over the envelope's content. It is
	// checked before anything in the event is used.
	Signature string `json:"sig"`
	// TraceCarrier decodes the envelope's optional traceparent/tracestate; an
	// envelope without them is handled under a fresh span.
	observability.TraceCarrier
}

// Handle processes one SQS message body: an EventBridge event from auth. It
// returns nil once the message is done with: purged, or purged already. An
// error wrapping purge.ErrPermanent means the Worker refused the purge for
// good, and one for an event whose removed_at lies more than maxClockSkew
// ahead means no purge should be sent; Poll drops the message for either. Any
// other error leaves it for the queue to deliver again.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	_, err := consumer.handle(ctx, body, time.Now())
	return err
}

// handle is Handle, answering also with the purge the event asked for, as far
// as it got to saying, so that one given up for good can be logged whole.
func (consumer *Consumer) handle(ctx context.Context, body string, receivedAt time.Time) (purge.Request, error) {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return purge.Request{}, fmt.Errorf("decode EventBridge event: %w", err)
	}
	if event.Source != autheventsv1.Source || !slices.Contains(AuthSubscriptions(), event.DetailType) {
		return purge.Request{}, fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}
	var detail receivedEnvelope
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		return purge.Request{}, fmt.Errorf("event %s: decode detail: %w", event.ID, err)
	}
	if detail.Version != 1 || detail.EventID == "" {
		return purge.Request{}, fmt.Errorf("event %s: unsupported envelope (version %d, event_id %q)", event.ID, detail.Version, detail.EventID)
	}
	// The signature is checked before anything in the event is used, so a
	// message that reached the queue without auth having sent it purges
	// nothing, whatever it names.
	if err := consumer.verifier.Verify(event.Source, event.DetailType, detail.EventID, detail.Version, detail.OccurredAt, detail.Data, detail.Signature); err != nil {
		return purge.Request{}, fmt.Errorf("event %s: %w", detail.EventID, err)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, detail.OccurredAt)

	ctx, span := observability.StartConsumerSpan(ctx, detail.TraceCarrier, consumer.queueName, event.DetailType, detail.EventID)
	started := time.Now()
	request, err := requestFor(event.DetailType, detail.EventID, detail.Data)
	if err == nil && request.RemovedAt.After(receivedAt.Add(maxClockSkew)) {
		err = fmt.Errorf("%w: %s is more than %s after the event was received", errFutureRemoval,
			request.RemovedAt.UTC().Format(time.RFC3339Nano), maxClockSkew)
	}
	outcome := "realtime conversations purged"
	switch {
	case err != nil:
	case consumer.purged.has(detail.EventID):
		outcome = "realtime purge already done"
	default:
		if err = consumer.purger.Purge(ctx, request); err == nil {
			consumer.purged.add(detail.EventID)
		}
	}
	observability.EndSpan(span, err)
	consumer.metrics.Record(ctx, component, event.DetailType, occurredAt, receivedAt, started, err)
	if err != nil {
		return request, fmt.Errorf("event %s: %s%s: %w", detail.EventID, event.DetailType, scope(request), err)
	}
	consumer.logger.InfoContext(ctx, outcome, "event_id", detail.EventID, "detail_type", event.DetailType,
		"organization_id", request.OrganizationID, "scope", string(request.Scope))
	return request, nil
}

// scope names the organization a failed purge was for, when the event got as
// far as saying.
func scope(request purge.Request) string {
	if request.OrganizationID == "" {
		return ""
	}
	return " for organization " + request.OrganizationID
}

// requestFor is the purge an event asks for, bounded by when the membership
// or the organization ended. A member removed because their organization was
// deleted is purged like any other, although the organization's own purge
// takes everything it kept: that purge reaches only the conversations the
// Worker's registry knows of, which nothing guarantees is all of them, and a
// member's purge that finds nothing left is still answered 204.
func requestFor(detailType, eventID string, data json.RawMessage) (purge.Request, error) {
	switch detailType {
	case autheventsv1.MemberRemovedDetailType:
		var removed autheventsv1.MemberRemoved
		if err := json.Unmarshal(data, &removed); err != nil {
			return purge.Request{}, fmt.Errorf("decode event data: %w", err)
		}
		if removed.OrganizationID == "" || removed.UserID == "" {
			return purge.Request{}, errors.New("member event has no organization_id or user_id")
		}
		// Without the time, the Worker could not tell what the member wrote
		// before from what they wrote after being invited back.
		if removed.RemovedAt.IsZero() {
			return purge.Request{}, errors.New("member event has no removed_at")
		}
		return purge.Request{
			EventID: eventID, OrganizationID: removed.OrganizationID,
			Scope: purge.ScopeMember, UserID: removed.UserID, RemovedAt: removed.RemovedAt,
		}, nil
	case autheventsv1.OrganizationDeletedDetailType:
		var deleted autheventsv1.OrganizationDeleted
		if err := json.Unmarshal(data, &deleted); err != nil {
			return purge.Request{}, fmt.Errorf("decode event data: %w", err)
		}
		if deleted.OrganizationID == "" || deleted.DeletedAt.IsZero() {
			return purge.Request{}, errors.New("organization event has no organization_id or deleted_at")
		}
		return purge.Request{
			EventID: eventID, OrganizationID: deleted.OrganizationID,
			Scope: purge.ScopeOrganization, RemovedAt: deleted.DeletedAt,
		}, nil
	}
	return purge.Request{}, fmt.Errorf("no purge for %s", detailType)
}

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

// ReceiveBatch is how many messages Poll takes at once. Each may wait up to
// purge.Timeout on the Worker, one after another, so the queue's visibility
// timeout, which its provisioner sets, must outlast that many.
const ReceiveBatch = 5

// receiveBackoff is how long Poll waits after a failed receive before trying
// again. A var so tests can shorten it instead of waiting for real time.
var receiveBackoff = 5 * time.Second

// Poll consumes the queue until ctx is cancelled. A message is deleted once
// it is done with, or once its purge is given up for good, which is logged as
// dropped. Any other failure leaves the message to come back after
// its visibility timeout, and the redrive policy moves one that keeps failing
// to the dead-letter queue.
func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
	// Poll runs as this consumer's only goroutine for its whole life, calling
	// handle sequentially, so setting this once up front and never touching
	// it again needs no lock.
	consumer.queueName = observability.QueueName(queueURL)
	for {
		output, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: ReceiveBatch,
			WaitTimeSeconds:     20,
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			consumer.logger.ErrorContext(ctx, "receive from realtime queue failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(receiveBackoff):
			}
			continue
		}
		// One timestamp for the whole batch: ReceiveMessage already returned,
		// so every message in it was equally "received" as of now.
		receivedAt := time.Now()

		var done []sqstypes.DeleteMessageBatchRequestEntry
		for index, message := range output.Messages {
			if ctx.Err() != nil {
				// Stopping: what is left goes back to the queue.
				break
			}
			request, err := consumer.handle(ctx, aws.ToString(message.Body), receivedAt)
			switch {
			case err == nil:
			case errors.Is(err, purge.ErrPermanent), errors.Is(err, errFutureRemoval):
				// The purge whole, as the Worker was or would have been sent it,
				// so it can be sent by hand once whatever stopped it is put
				// right.
				consumer.logger.ErrorContext(ctx, "realtime purge dropped for good: the error says why",
					"sqs_message_id", aws.ToString(message.MessageId), "event_id", request.EventID, "scope", string(request.Scope),
					"organization_id", request.OrganizationID, "user_id", request.UserID,
					"removed_at", request.RemovedAt.UTC().Format(time.RFC3339Nano), "error", err)
			default:
				if ctx.Err() == nil {
					consumer.logger.ErrorContext(ctx, "realtime purge failed; the queue will deliver it again",
						"sqs_message_id", aws.ToString(message.MessageId), "error", err)
				}
				continue
			}
			done = append(done, sqstypes.DeleteMessageBatchRequestEntry{
				Id:            aws.String(strconv.Itoa(index)),
				ReceiptHandle: message.ReceiptHandle,
			})
		}
		if len(done) == 0 {
			continue
		}
		deleted, err := client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: done})
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				consumer.logger.ErrorContext(ctx, "delete handled messages failed", "error", err)
			}
			continue
		}
		for _, failure := range deleted.Failed {
			consumer.logger.ErrorContext(ctx, "delete handled message failed", "code", aws.ToString(failure.Code), "message", aws.ToString(failure.Message))
		}
	}
}

// recentSize is how many purged events recent remembers.
const recentSize = 1024

// recent remembers the events purged lately, so a message the queue hands
// over again — SQS delivers at least once, and a message whose deletion
// failed comes back — is deleted without asking the Worker twice. It is a
// saving, not a guarantee: it holds only the last recentSize events, and
// forgets them all when the process stops. The Worker's purge is idempotent,
// which is the guarantee.
type recent struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
	next  int
}

func newRecent(size int) *recent {
	return &recent{ids: make(map[string]struct{}, size), order: make([]string, size)}
}

func (seen *recent) has(id string) bool {
	seen.mu.Lock()
	defer seen.mu.Unlock()
	_, ok := seen.ids[id]
	return ok
}

// add remembers id, forgetting the oldest once it holds as many as it can.
func (seen *recent) add(id string) {
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if _, ok := seen.ids[id]; ok {
		return
	}
	if oldest := seen.order[seen.next]; oldest != "" {
		delete(seen.ids, oldest)
	}
	seen.order[seen.next] = id
	seen.ids[id] = struct{}{}
	seen.next = (seen.next + 1) % len(seen.order)
}

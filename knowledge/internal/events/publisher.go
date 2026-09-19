// Package events carries what knowledge wrote onto the bus, and brings back
// what other services say that knowledge has to act on.
//
// Publishing goes through the outbox: an event is written in the same
// transaction as the change it describes, and a dispatcher moves it
// afterwards. That is what makes a document and the event announcing it
// impossible to disagree — and it is also what makes where the event goes a
// detail. Today a dispatcher hands events to EventBridge; once a retrieval
// pipeline shares a process with this one, the same dispatcher can hand them
// straight over, and nothing above this package changes.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// Sink is where a dispatched event goes.
//
// It is an interface rather than the EventBridge publisher itself because
// that choice is not the outbox's business. Today it is the bus; in a build
// where the retrieval pipeline shares this process, it is that pipeline, and
// the durability, ordering and retry above it are identical either way.
type Sink interface {
	PublishEvent(context.Context, domain.Event) error
}

type putEventsAPI interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

// Publisher puts events on the bus.
type Publisher struct {
	client  putEventsAPI
	busName string
}

type envelope struct {
	EventID    string          `json:"event_id"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
	// TraceCarrier adds this envelope's optional traceparent/tracestate
	// fields: the producer span's context, for a consumer to become a child
	// of. Additive and backward compatible - a consumer built before this
	// rollout simply never looks for them.
	observability.TraceCarrier
}

func NewPublisher(client putEventsAPI, busName string) *Publisher {
	return &Publisher{client: client, busName: busName}
}

func (publisher *Publisher) PublishEvent(ctx context.Context, event domain.Event) (resultErr error) {
	if event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
		return errors.New("event id, detail type, and occurrence time are required")
	}
	ctx, span := observability.StartProducerSpan(ctx, observability.DecodeTraceCarrier(event.TraceContext), publisher.busName, event.DetailType, event.ID)
	defer func() { observability.EndSpan(span, resultErr) }()

	occurredAt := event.OccurredAt.UTC()
	detail, err := json.Marshal(envelope{
		EventID:      event.ID,
		Version:      1,
		OccurredAt:   occurredAt.Format(time.RFC3339Nano),
		Data:         event.Data,
		TraceCarrier: observability.CaptureTraceCarrier(ctx),
	})
	if err != nil {
		resultErr = fmt.Errorf("encode event detail: %w", err)
		return resultErr
	}

	output, err := publisher.client.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []types.PutEventsRequestEntry{{
			Detail:       aws.String(string(detail)),
			DetailType:   aws.String(event.DetailType),
			EventBusName: aws.String(publisher.busName),
			Source:       aws.String(knowledgev1.Source),
			Time:         aws.Time(occurredAt),
		}},
	})
	if err != nil {
		resultErr = fmt.Errorf("put event: %w", err)
		return resultErr
	}
	if output == nil {
		resultErr = errors.New("put event returned no result")
		return resultErr
	}
	if output.FailedEntryCount > 0 {
		if len(output.Entries) == 0 {
			resultErr = errors.New("put event failed without an entry result")
			return resultErr
		}
		entry := output.Entries[0]
		resultErr = fmt.Errorf("put event failed: %s: %s", aws.ToString(entry.ErrorCode), aws.ToString(entry.ErrorMessage))
		return resultErr
	}
	return nil
}

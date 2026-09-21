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

	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

type putEventsAPI interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

type Publisher struct {
	client  putEventsAPI
	busName string
	// signer is files' event signing identity. Nil is a build error caught
	// at mount: an unsigned event cannot pass any consumer that checks.
	signer *eventauth.Signer
}

type envelope struct {
	EventID    string          `json:"event_id"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
	// Signature proves the service this envelope names as its source: the
	// consumer verifies it over the envelope's content before any handler
	// runs, so a message that reached the queue without this service having
	// sent it is refused no matter how well formed it is.
	Signature string `json:"sig,omitempty"`
	// TraceCarrier adds this envelope's optional traceparent/tracestate
	// fields: the producer span's context, for a consumer to become a child
	// of. Additive and backward compatible - a consumer built before this
	// rollout simply never looks for them.
	observability.TraceCarrier
}

func NewPublisher(client putEventsAPI, busName string, signer *eventauth.Signer) (*Publisher, error) {
	if signer == nil {
		return nil, errors.New("files cannot publish unsigned events: a signing identity is required")
	}
	return &Publisher{client: client, busName: busName, signer: signer}, nil
}

func (publisher *Publisher) PublishEvent(ctx context.Context, event domain.Event) (resultErr error) {
	if event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
		return errors.New("event id, detail type, and occurrence time are required")
	}
	ctx, span := observability.StartProducerSpan(ctx, observability.DecodeTraceCarrier(event.TraceContext), publisher.busName, event.DetailType, event.ID)
	defer func() { observability.EndSpan(span, resultErr) }()

	occurredAt := event.OccurredAt.UTC()
	// Signed over the exact content the consumer reads: what the event is,
	// which one it is, when it happened, and what it carries.
	signature, err := publisher.signer.Sign(fileseventsv1.Source, event.DetailType, event.ID, 1,
		occurredAt.Format(time.RFC3339Nano), event.Data)
	if err != nil {
		resultErr = fmt.Errorf("sign event: %w", err)
		return resultErr
	}
	detail, err := json.Marshal(envelope{
		EventID:      event.ID,
		Version:      1,
		OccurredAt:   occurredAt.Format(time.RFC3339Nano),
		Data:         event.Data,
		Signature:    signature,
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
			Source:       aws.String(fileseventsv1.Source),
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

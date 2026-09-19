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

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

type putEventsAPI interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

type Publisher struct {
	client  putEventsAPI
	busName string
	source  string
}

type envelope struct {
	EventID    string `json:"event_id"`
	Version    int    `json:"version"`
	OccurredAt string `json:"occurred_at"`
	Data       any    `json:"data"`
	// TraceCarrier adds this envelope's optional traceparent/tracestate
	// fields: the producer span's context, for a consumer to become a child
	// of. Additive and backward compatible - a consumer built before this
	// rollout simply never looks for them.
	observability.TraceCarrier
}

func NewPublisher(client putEventsAPI, busName, source string) *Publisher {
	return &Publisher{
		client:  client,
		busName: busName,
		source:  source,
	}
}

func (p *Publisher) PublishEvent(ctx context.Context, event domain.Event) (resultErr error) {
	if p.client == nil {
		return errors.New("eventbridge client is required")
	}
	if p.busName == "" || p.source == "" || event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
		return errors.New("event bus, source, and detail type are required")
	}
	ctx, span := observability.StartProducerSpan(ctx, observability.DecodeTraceCarrier(event.TraceContext), p.busName, event.DetailType, event.ID)
	defer func() { observability.EndSpan(span, resultErr) }()

	now := event.OccurredAt.UTC()
	detail, err := json.Marshal(envelope{
		EventID:      event.ID,
		Version:      1,
		OccurredAt:   now.Format(time.RFC3339Nano),
		Data:         event.Data,
		TraceCarrier: observability.CaptureTraceCarrier(ctx),
	})
	if err != nil {
		resultErr = fmt.Errorf("encode event detail: %w", err)
		return resultErr
	}

	output, err := p.client.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []types.PutEventsRequestEntry{{
			Detail:       aws.String(string(detail)),
			DetailType:   aws.String(event.DetailType),
			EventBusName: aws.String(p.busName),
			Source:       aws.String(p.source),
			Time:         aws.Time(now),
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

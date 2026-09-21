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
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

type putEventsAPI interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

type Publisher struct {
	client  putEventsAPI
	busName string
	source  string
	// signer is auth's event signing identity. Nil is a build error caught
	// at mount: an unsigned event cannot pass any consumer that checks.
	signer *eventauth.Signer
}

type envelope struct {
	EventID    string `json:"event_id"`
	Version    int    `json:"version"`
	OccurredAt string `json:"occurred_at"`
	Data       any    `json:"data"`
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

func NewPublisher(client putEventsAPI, busName, source string, signer *eventauth.Signer) (*Publisher, error) {
	if signer == nil {
		return nil, errors.New("auth cannot publish unsigned events: a signing identity is required")
	}
	return &Publisher{
		client:  client,
		busName: busName,
		source:  source,
		signer:  signer,
	}, nil
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
	// The data is marshaled once, before the envelope, so the signature is
	// over exactly the bytes the consumer will read: an `any` payload does
	// not give the signer a stable form to sign otherwise.
	data, err := json.Marshal(event.Data)
	if err != nil {
		resultErr = fmt.Errorf("encode event data: %w", err)
		return resultErr
	}
	signature, err := p.signer.Sign(p.source, event.DetailType, event.ID, 1, now.Format(time.RFC3339Nano), data)
	if err != nil {
		resultErr = fmt.Errorf("sign event: %w", err)
		return resultErr
	}
	detail, err := json.Marshal(envelope{
		EventID:      event.ID,
		Version:      1,
		OccurredAt:   now.Format(time.RFC3339Nano),
		Data:         json.RawMessage(data),
		Signature:    signature,
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

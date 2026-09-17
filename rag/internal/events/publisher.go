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

	rageventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

type putEventsAPI interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

type Publisher struct {
	client  putEventsAPI
	busName string
}

type envelope struct {
	EventID    string          `json:"event_id"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

func NewPublisher(client putEventsAPI, busName string) *Publisher {
	return &Publisher{client: client, busName: busName}
}

func (publisher *Publisher) PublishEvent(ctx context.Context, event domain.Event) error {
	if event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
		return errors.New("event id, detail type, and occurrence time are required")
	}
	occurredAt := event.OccurredAt.UTC()
	detail, err := json.Marshal(envelope{
		EventID:    event.ID,
		Version:    1,
		OccurredAt: occurredAt.Format(time.RFC3339Nano),
		Data:       event.Data,
	})
	if err != nil {
		return fmt.Errorf("encode event detail: %w", err)
	}

	output, err := publisher.client.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []types.PutEventsRequestEntry{{
			Detail:       aws.String(string(detail)),
			DetailType:   aws.String(event.DetailType),
			EventBusName: aws.String(publisher.busName),
			Source:       aws.String(rageventsv1.Source),
			Time:         aws.Time(occurredAt),
		}},
	})
	if err != nil {
		return fmt.Errorf("put event: %w", err)
	}
	if output == nil {
		return errors.New("put event returned no result")
	}
	if output.FailedEntryCount > 0 {
		if len(output.Entries) == 0 {
			return errors.New("put event failed without an entry result")
		}
		entry := output.Entries[0]
		return fmt.Errorf("put event failed: %s: %s", aws.ToString(entry.ErrorCode), aws.ToString(entry.ErrorMessage))
	}
	return nil
}

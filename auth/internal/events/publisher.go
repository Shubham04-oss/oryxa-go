package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

type putEventsAPI interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

type Publisher struct {
	client  putEventsAPI
	busName string
	source  string
	now     func() time.Time
}

type envelope struct {
	EventID    string `json:"event_id"`
	Version    int    `json:"version"`
	OccurredAt string `json:"occurred_at"`
	Data       any    `json:"data"`
}

func NewPublisher(client putEventsAPI, busName, source string) *Publisher {
	return &Publisher{
		client:  client,
		busName: busName,
		source:  source,
		now:     time.Now,
	}
}

func (p *Publisher) Publish(ctx context.Context, detailType string, data any) error {
	if p.client == nil {
		return errors.New("eventbridge client is required")
	}
	if p.busName == "" || p.source == "" || detailType == "" {
		return errors.New("event bus, source, and detail type are required")
	}

	eventID, err := newEventID()
	if err != nil {
		return fmt.Errorf("create event id: %w", err)
	}

	now := p.now().UTC()
	detail, err := json.Marshal(envelope{
		EventID:    eventID,
		Version:    1,
		OccurredAt: now.Format(time.RFC3339Nano),
		Data:       data,
	})
	if err != nil {
		return fmt.Errorf("encode event detail: %w", err)
	}

	output, err := p.client.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []types.PutEventsRequestEntry{{
			Detail:       aws.String(string(detail)),
			DetailType:   aws.String(detailType),
			EventBusName: aws.String(p.busName),
			Source:       aws.String(p.source),
			Time:         aws.Time(now),
		}},
	})
	if err != nil {
		return fmt.Errorf("put event: %w", err)
	}
	if output.FailedEntryCount > 0 {
		entry := output.Entries[0]
		return fmt.Errorf("put event failed: %s: %s", aws.ToString(entry.ErrorCode), aws.ToString(entry.ErrorMessage))
	}

	return nil
}

func newEventID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

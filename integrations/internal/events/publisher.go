package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

type EventBridgeAPI interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

type Publisher struct {
	Client  EventBridgeAPI
	BusName string
}

type envelope struct {
	EventID        string `json:"event_id"`
	Version        int    `json:"version"`
	OrganizationID string `json:"organization_id"`
	OccurredAt     string `json:"occurred_at"`
	Data           any    `json:"data"`
}

func (p Publisher) PublishEvent(ctx context.Context, event domain.Event) error {
	if p.Client == nil || p.BusName == "" || event.EventID == "" || event.Source == "" || event.DetailType == "" || event.OrganizationID == "" || event.OccurredAt.IsZero() {
		return errors.New("invalid EventBridge event")
	}
	detail, err := json.Marshal(envelope{EventID: event.EventID, Version: event.Version,
		OrganizationID: event.OrganizationID, OccurredAt: event.OccurredAt.UTC().Format(time.RFC3339Nano), Data: event.Data})
	if err != nil {
		return fmt.Errorf("encode integrations event: %w", err)
	}
	output, err := p.Client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []types.PutEventsRequestEntry{{
		Detail: aws.String(string(detail)), DetailType: aws.String(event.DetailType),
		EventBusName: aws.String(p.BusName), Source: aws.String(event.Source), Time: aws.Time(event.OccurredAt.UTC()),
	}}})
	if err != nil {
		return fmt.Errorf("publish integrations event: %w", err)
	}
	if output == nil {
		return errors.New("EventBridge returned no result")
	}
	if output.FailedEntryCount > 0 {
		if len(output.Entries) == 0 {
			return errors.New("EventBridge rejected event")
		}
		return fmt.Errorf("EventBridge rejected event: %s", aws.ToString(output.Entries[0].ErrorCode))
	}
	return nil
}

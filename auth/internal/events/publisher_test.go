package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

type fakeEventBridge struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
	err    error
}

func (fake *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	fake.input = input
	return fake.output, fake.err
}

func TestPublishEventPreservesDomainEventIdentity(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{}}
	publisher := NewPublisher(client, "default", "oryxa.auth")
	occurredAt := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)

	err := publisher.PublishEvent(context.Background(), domain.Event{
		ID:         "evt_1",
		DetailType: "auth.user.registered.v1",
		OccurredAt: occurredAt,
		Data:       json.RawMessage(`{"user_id":"user-1"}`),
	})
	if err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}

	entry := client.input.Entries[0]
	if got := aws.ToString(entry.EventBusName); got != "default" {
		t.Fatalf("event bus = %q, want default", got)
	}
	var detail envelope
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatalf("decode event detail: %v", err)
	}
	if detail.EventID != "evt_1" || detail.OccurredAt != "2026-09-13T12:00:00Z" {
		t.Fatalf("unexpected event envelope: %+v", detail)
	}
}

func TestPublishEventReportsRejectedEntry(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{
		FailedEntryCount: 1,
		Entries: []types.PutEventsResultEntry{{
			ErrorCode:    aws.String("InternalFailure"),
			ErrorMessage: aws.String("rejected"),
		}},
	}}
	publisher := NewPublisher(client, "default", "oryxa.auth")

	err := publisher.PublishEvent(context.Background(), domain.Event{
		ID:         "evt_1",
		DetailType: "auth.user.registered.v1",
		OccurredAt: time.Now(),
		Data:       json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("PublishEvent() error = nil, want rejected entry error")
	}
}

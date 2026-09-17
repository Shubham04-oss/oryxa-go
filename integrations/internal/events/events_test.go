package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

type fakePutEvents struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
	err    error
}

func (fake *fakePutEvents) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	fake.input = input
	return fake.output, fake.err
}

func TestPublisherWrapsEventsInTheSharedEnvelope(t *testing.T) {
	client := &fakePutEvents{output: &eventbridge.PutEventsOutput{}}
	at := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.FixedZone("IST", 19800))
	event := domain.Event{ID: "evt_1", DetailType: "integrations.subscription.created.v1", OccurredAt: at, Data: json.RawMessage(`{"subscription_id":"sub_1"}`)}
	if err := NewPublisher(client, "oryxa").PublishEvent(context.Background(), event); err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != "oryxa.integrations" || aws.ToString(entry.DetailType) != event.DetailType ||
		aws.ToString(entry.EventBusName) != "oryxa" || !aws.ToTime(entry.Time).Equal(at) {
		t.Fatalf("entry = %+v", entry)
	}
	var detail envelope
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.EventID != "evt_1" || detail.Version != 1 || detail.OccurredAt != "2026-09-14T06:30:00Z" || string(detail.Data) != `{"subscription_id":"sub_1"}` {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestPublisherReportsFailures(t *testing.T) {
	event := domain.Event{ID: "evt_1", DetailType: "integrations.subscription.created.v1", OccurredAt: time.Now(), Data: json.RawMessage(`{}`)}
	rejected := &fakePutEvents{output: &eventbridge.PutEventsOutput{FailedEntryCount: 1,
		Entries: []types.PutEventsResultEntry{{ErrorCode: aws.String("ThrottlingException"), ErrorMessage: aws.String("slow down")}}}}
	if err := NewPublisher(rejected, "oryxa").PublishEvent(context.Background(), event); err == nil {
		t.Fatal("PublishEvent() accepted a rejected entry")
	}
	broken := &fakePutEvents{err: errors.New("network down")}
	if err := NewPublisher(broken, "oryxa").PublishEvent(context.Background(), event); err == nil {
		t.Fatal("PublishEvent() hid a client error")
	}
	if err := NewPublisher(broken, "oryxa").PublishEvent(context.Background(), domain.Event{}); err == nil {
		t.Fatal("PublishEvent() accepted an empty event")
	}
}

// Claiming, marking and retrying are internal/outbox's own concern now: see
// its dispatcher_test.go.

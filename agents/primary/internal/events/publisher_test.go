package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

type fakePutEvents struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
	err    error
	calls  int
}

func (f *fakePutEvents) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.calls++
	f.input = input
	return f.output, f.err
}

func testEvent(t *testing.T) Event {
	t.Helper()
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	event, err := NewEvent(agentsv1.RunCompletedDetailType, agentsv1.RunCompleted{RunID: "run_1"}, at)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestPublisherPutsTheStandardEnvelope(t *testing.T) {
	client := &fakePutEvents{output: &eventbridge.PutEventsOutput{}}
	publisher := NewPublisher(client, "oryxa-local")
	event := testEvent(t)

	if err := publisher.PublishEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 || len(client.input.Entries) != 1 {
		t.Fatalf("PutEvents calls = %d", client.calls)
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != agentsv1.Source || aws.ToString(entry.DetailType) != agentsv1.RunCompletedDetailType {
		t.Fatalf("entry = %+v", entry)
	}
	if aws.ToString(entry.EventBusName) != "oryxa-local" || !entry.Time.Equal(event.OccurredAt) {
		t.Fatalf("entry = %+v", entry)
	}

	var detail envelope
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.EventID != event.ID || detail.Version != 1 {
		t.Fatalf("envelope = %+v", detail)
	}
	if detail.OccurredAt != event.OccurredAt.Format(time.RFC3339Nano) {
		t.Fatalf("occurred_at = %q", detail.OccurredAt)
	}
	var data agentsv1.RunCompleted
	if err := json.Unmarshal(detail.Data, &data); err != nil || data.RunID != "run_1" {
		t.Fatalf("data = %s (error %v)", detail.Data, err)
	}
}

func TestPublisherRefusesAnIncompleteEvent(t *testing.T) {
	client := &fakePutEvents{output: &eventbridge.PutEventsOutput{}}
	publisher := NewPublisher(client, "oryxa-local")

	if err := publisher.PublishEvent(context.Background(), Event{DetailType: "agents.test.v1", OccurredAt: time.Now()}); err == nil {
		t.Fatal("PublishEvent() error = nil without an id")
	}
	if err := publisher.PublishEvent(context.Background(), Event{ID: "evt_1", OccurredAt: time.Now()}); err == nil {
		t.Fatal("PublishEvent() error = nil without a detail type")
	}
	if client.calls != 0 {
		t.Fatalf("PutEvents was called %d times for invalid events", client.calls)
	}
}

func TestPublisherReportsFailedEntries(t *testing.T) {
	client := &fakePutEvents{output: &eventbridge.PutEventsOutput{
		FailedEntryCount: 1,
		Entries: []types.PutEventsResultEntry{{
			ErrorCode:    aws.String("ThrottlingException"),
			ErrorMessage: aws.String("slow down"),
		}},
	}}
	publisher := NewPublisher(client, "oryxa-local")

	err := publisher.PublishEvent(context.Background(), testEvent(t))
	if err == nil {
		t.Fatal("PublishEvent() error = nil for a failed entry")
	}
	if !strings.Contains(err.Error(), "ThrottlingException") {
		t.Fatalf("error = %q, want it to name the failed entry", err)
	}
}

func TestPublisherReportsPutFailures(t *testing.T) {
	publisher := NewPublisher(&fakePutEvents{err: errors.New("network down")}, "oryxa-local")
	if err := publisher.PublishEvent(context.Background(), testEvent(t)); err == nil {
		t.Fatal("PublishEvent() error = nil when PutEvents fails")
	}
	publisher = NewPublisher(&fakePutEvents{output: nil}, "oryxa-local")
	if err := publisher.PublishEvent(context.Background(), testEvent(t)); err == nil {
		t.Fatal("PublishEvent() error = nil when PutEvents returns nothing")
	}
}

package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

type fakeEventBridge struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
	err    error
}

func (f *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.input = input
	return f.output, f.err
}

func TestPublisherPublishesVersionedEnvelope(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{}}
	publisher := NewPublisher(client, "oryxa", "oryxa.auth")
	publisher.now = func() time.Time {
		return time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
	}

	if err := publisher.Publish(context.Background(), "auth.user.registered.v1", map[string]string{"user_id": "user-1"}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	if got := len(client.input.Entries); got != 1 {
		t.Fatalf("entry count = %d, want 1", got)
	}
	entry := client.input.Entries[0]
	if got := aws.ToString(entry.EventBusName); got != "oryxa" {
		t.Fatalf("bus = %q, want oryxa", got)
	}

	var detail envelope
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.EventID == "" || detail.Version != 1 || detail.OccurredAt != "2026-09-13T12:00:00Z" {
		t.Fatalf("unexpected envelope: %+v", detail)
	}
}

func TestPublisherReturnsRejectedEntry(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{
		FailedEntryCount: 1,
		Entries: []types.PutEventsResultEntry{{
			ErrorCode:    aws.String("InternalFailure"),
			ErrorMessage: aws.String("rejected"),
		}},
	}}
	publisher := NewPublisher(client, "oryxa", "oryxa.auth")

	if err := publisher.Publish(context.Background(), "auth.user.registered.v1", struct{}{}); err == nil {
		t.Fatal("Publish() error = nil, want rejected entry error")
	}
}

package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
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
	event := domain.Event{ID: "evt_1", DetailType: "connections.app.created.v1", OccurredAt: at, Data: json.RawMessage(`{"app_id":"app_1"}`)}
	if err := NewPublisher(client, "oryxa").PublishEvent(context.Background(), event); err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != "oryxa.connections" || aws.ToString(entry.DetailType) != event.DetailType ||
		aws.ToString(entry.EventBusName) != "oryxa" || !aws.ToTime(entry.Time).Equal(at) {
		t.Fatalf("entry = %+v", entry)
	}
	var detail envelope
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.EventID != "evt_1" || detail.Version != 1 || detail.OccurredAt != "2026-09-14T06:30:00Z" || string(detail.Data) != `{"app_id":"app_1"}` {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestPublisherReportsFailures(t *testing.T) {
	event := domain.Event{ID: "evt_1", DetailType: "connections.app.created.v1", OccurredAt: time.Now(), Data: json.RawMessage(`{}`)}
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

type fakeOutbox struct {
	events    []domain.OutboxEvent
	published []string
	failed    []string
}

func (fake *fakeOutbox) ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error) {
	return fake.events, nil
}

func (fake *fakeOutbox) MarkOutboxPublished(_ context.Context, eventID string) error {
	fake.published = append(fake.published, eventID)
	return nil
}

func (fake *fakeOutbox) MarkOutboxFailed(_ context.Context, eventID string, _ time.Duration, _ string) error {
	fake.failed = append(fake.failed, eventID)
	return nil
}

type failingPublisher struct{ failID string }

func (publisher failingPublisher) PublishEvent(_ context.Context, event domain.Event) error {
	if event.ID == publisher.failID {
		return errors.New("rejected")
	}
	return nil
}

func TestDispatcherMarksEachEvent(t *testing.T) {
	outbox := &fakeOutbox{events: []domain.OutboxEvent{
		{Event: domain.Event{ID: "evt_ok"}},
		{Event: domain.Event{ID: "evt_bad"}, Attempts: 2},
	}}
	NewDispatcher(outbox, failingPublisher{failID: "evt_bad"}, slog.New(slog.DiscardHandler)).flush(context.Background())
	if !slices.Equal(outbox.published, []string{"evt_ok"}) || !slices.Equal(outbox.failed, []string{"evt_bad"}) {
		t.Fatalf("published = %v, failed = %v", outbox.published, outbox.failed)
	}
	if retryDelay(0) != time.Second || retryDelay(2) != 4*time.Second || retryDelay(99) != 64*time.Second {
		t.Fatal("retryDelay() does not back off exponentially up to 64s")
	}
}

// TestPublisherRefusesAnEventWithNoDetail keeps an envelope with no data off
// the bus: encoding would turn a nil detail into "data":null and fail outright
// on an empty one, so a subscriber would either receive nothing useful or the
// dispatcher would retry an event that can never publish.
func TestPublisherRefusesAnEventWithNoDetail(t *testing.T) {
	at := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name string
		data json.RawMessage
	}{
		{"no detail", nil},
		{"empty detail", json.RawMessage{}},
		{"malformed detail", json.RawMessage(`{"unterminated":`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := &fakePutEvents{output: &eventbridge.PutEventsOutput{}}
			event := domain.Event{ID: "evt_1", DetailType: "connections.app.created.v1", OccurredAt: at, Data: testCase.data}
			if err := NewPublisher(client, "oryxa").PublishEvent(context.Background(), event); err == nil {
				t.Fatalf("PublishEvent() with %s error = nil, want an error", testCase.name)
			}
			if client.input != nil {
				t.Fatalf("PublishEvent() with %s still put an event on the bus", testCase.name)
			}
		})
	}

	client := &fakePutEvents{output: &eventbridge.PutEventsOutput{}}
	event := domain.Event{ID: "evt_1", DetailType: "connections.app.created.v1", OccurredAt: at, Data: json.RawMessage(`{"app_id":"app_1"}`)}
	if err := NewPublisher(client, "oryxa").PublishEvent(context.Background(), event); err != nil {
		t.Fatalf("PublishEvent() with a valid detail error = %v", err)
	}
	if client.input == nil {
		t.Fatal("PublishEvent() with a valid detail put nothing on the bus")
	}
}

package events

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	batchingv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/batching/v1"
)

type fakeApplication struct {
	triggerID string
	forgot    string
	discarded string
}

func (app *fakeApplication) TriggerReceived(_ context.Context, eventID string, _ appsv1.TriggerReceived) error {
	app.triggerID = eventID
	return nil
}
func (app *fakeApplication) ConnectionRemoved(_ context.Context, event appsv1.ConnectionRemoved) error {
	app.discarded = "connection " + event.ConnectionID
	return nil
}
func (app *fakeApplication) TriggerUnsubscribed(_ context.Context, event appsv1.TriggerUnsubscribed) error {
	app.discarded = "trigger " + event.TriggerID
	return nil
}
func (app *fakeApplication) MemberRemoved(_ context.Context, event authv1.MemberRemoved) error {
	app.forgot = event.OrganizationID + "/" + event.UserID
	return nil
}
func (app *fakeApplication) OrganizationDeleted(_ context.Context, event authv1.OrganizationDeleted) error {
	app.forgot = event.OrganizationID
	return nil
}

func delivery(t *testing.T, source, detailType, eventID string, data any) string {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := json.Marshal(map[string]any{"event_id": eventID, "version": 1, "data": json.RawMessage(encoded)})
	body, _ := json.Marshal(map[string]any{
		"id": "aws-1", "source": source, "detail-type": detailType, "detail": json.RawMessage(detail),
	})
	return string(body)
}

func TestConsumerRecordsTriggersByOriginalEventID(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, nil)
	body := delivery(t, appsv1.Source, appsv1.TriggerReceivedDetailType, "evt_apps_1", appsv1.TriggerReceived{
		OrganizationID: "org_1", MemberID: "user_1", ReceivedAt: time.Now(),
	})
	if err := consumer.Handle(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if app.triggerID != "evt_apps_1" {
		t.Fatalf("event id = %q", app.triggerID)
	}
}

func TestConsumerHandlesAuthRemoval(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, nil)
	body := delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_auth_1",
		authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user_1"})
	if err := consumer.Handle(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if app.forgot != "org_1/user_1" {
		t.Fatalf("forgot = %q", app.forgot)
	}
}

func TestConsumerHandlesAppRemovals(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, nil)
	for _, test := range []struct {
		detailType string
		data       any
		want       string
	}{
		{appsv1.ConnectionRemovedDetailType, appsv1.ConnectionRemoved{ConnectionID: "apc_1", OrganizationID: "org_1"}, "connection apc_1"},
		{appsv1.TriggerUnsubscribedDetailType, appsv1.TriggerUnsubscribed{TriggerID: "apt_1", OrganizationID: "org_1"}, "trigger apt_1"},
	} {
		if err := consumer.Handle(context.Background(), delivery(t, appsv1.Source, test.detailType, "evt_apps_1", test.data)); err != nil {
			t.Fatal(err)
		}
		if app.discarded != test.want {
			t.Fatalf("%s discarded %q, want %q", test.detailType, app.discarded, test.want)
		}
	}
}

// TestEverySubscribedEventHasAHandler: the provisioner builds the rule from
// Subscriptions, so an event subscribed but not handled fails on the queue
// for ever, and one handled but not subscribed never arrives.
func TestEverySubscribedEventHasAHandler(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{}, nil)
	subscribed := 0
	for source, detailTypes := range Subscriptions() {
		for _, detailType := range detailTypes {
			subscribed++
			if handler, ok := consumer.handlers[detailType]; !ok || handler.source != source {
				t.Fatalf("%s from %s is subscribed but not handled from there", detailType, source)
			}
		}
	}
	if subscribed != len(consumer.handlers) {
		t.Fatalf("%d events subscribed, %d handled", subscribed, len(consumer.handlers))
	}
}

type fakeEventBridge struct{ input *eventbridge.PutEventsInput }

func (fake *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	fake.input = input
	return &eventbridge.PutEventsOutput{}, nil
}

func TestPublisherUsesBatchingSourceAndEnvelope(t *testing.T) {
	client := &fakeEventBridge{}
	now := time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)
	err := NewPublisher(client, "default").PublishEvent(context.Background(), domain.Event{
		ID: "evt_1", DetailType: batchingv1.TriggerBatchReadyDetailType, OccurredAt: now,
		Data: json.RawMessage(`{"batch_id":"bat_1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := client.input.Entries[0]
	if *entry.Source != batchingv1.Source || *entry.EventBusName != "default" || *entry.DetailType != batchingv1.TriggerBatchReadyDetailType {
		t.Fatalf("entry = %+v", entry)
	}
}

// slowApplication holds every trigger until released, as a slow classifier
// would, and handles removals at once.
type slowApplication struct {
	release chan struct{}
	removed chan string
}

func (app *slowApplication) TriggerReceived(ctx context.Context, _ string, _ appsv1.TriggerReceived) error {
	select {
	case <-app.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*slowApplication) ConnectionRemoved(context.Context, appsv1.ConnectionRemoved) error {
	return nil
}
func (*slowApplication) TriggerUnsubscribed(context.Context, appsv1.TriggerUnsubscribed) error {
	return nil
}
func (app *slowApplication) MemberRemoved(_ context.Context, event authv1.MemberRemoved) error {
	app.removed <- event.UserID
	return nil
}
func (*slowApplication) OrganizationDeleted(context.Context, authv1.OrganizationDeleted) error {
	return nil
}

// fakeQueue hands out one receive, then waits; it reports what is deleted.
type fakeQueue struct {
	receives chan *sqs.ReceiveMessageOutput
	deleted  chan []sqstypes.DeleteMessageBatchRequestEntry
}

func (queue *fakeQueue) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	select {
	case output := <-queue.receives:
		return output, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (queue *fakeQueue) DeleteMessageBatch(_ context.Context, input *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	queue.deleted <- input.Entries
	return &sqs.DeleteMessageBatchOutput{}, nil
}

// TestPollHandlesAReceiveSideBySide: a trigger waiting on the classifier
// must not hold up the rest of its receive — a removal least of all — and
// only what was handled is deleted from the queue.
func TestPollHandlesAReceiveSideBySide(t *testing.T) {
	app := &slowApplication{release: make(chan struct{}), removed: make(chan string, 1)}
	queue := &fakeQueue{receives: make(chan *sqs.ReceiveMessageOutput, 1), deleted: make(chan []sqstypes.DeleteMessageBatchRequestEntry, 1)}
	message := func(id, body string) sqstypes.Message {
		return sqstypes.Message{MessageId: aws.String(id), ReceiptHandle: aws.String("receipt_" + id), Body: aws.String(body)}
	}
	queue.receives <- &sqs.ReceiveMessageOutput{Messages: []sqstypes.Message{
		message("slow", delivery(t, appsv1.Source, appsv1.TriggerReceivedDetailType, "evt_apps_1", appsv1.TriggerReceived{
			OrganizationID: "org_1", MemberID: "user_1", ReceivedAt: time.Now(),
		})),
		message("removal", delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_auth_1",
			authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user_2"})),
		message("broken", `{not json`),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		NewConsumer(app, nil).Poll(ctx, queue, "queue")
	}()

	select {
	case user := <-app.removed:
		if user != "user_2" {
			t.Fatalf("removed %q", user)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the removal waited behind a trigger held by the classifier")
	}
	close(app.release)
	select {
	case entries := <-queue.deleted:
		var receipts []string
		for _, entry := range entries {
			receipts = append(receipts, aws.ToString(entry.ReceiptHandle))
		}
		slices.Sort(receipts)
		if strings.Join(receipts, ",") != "receipt_removal,receipt_slow" {
			t.Fatalf("deleted %v, want the two handled messages", receipts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was deleted")
	}
	cancel()
	<-polled
}

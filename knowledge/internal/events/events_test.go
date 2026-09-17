package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	connectionsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

// fakeApplication records what the consumer dispatched to it.
type fakeApplication struct {
	acted   []string
	handled map[string]bool
	// err fails every handler call it is set for.
	err error
	// failTimes fails that many handler calls before letting the rest
	// through, for a test that redelivers after a failure.
	failTimes int
}

func newFakeApplication() *fakeApplication {
	return &fakeApplication{handled: map[string]bool{}}
}

// act records the call and fails it if the fake was told to.
func (app *fakeApplication) act(detailType string) error {
	app.acted = append(app.acted, detailType)
	if app.failTimes > 0 {
		app.failTimes--
		return errors.New("the handler failed")
	}
	return app.err
}

func (app *fakeApplication) OrganizationDeleted(context.Context, authv1.OrganizationDeleted) error {
	return app.act(authv1.OrganizationDeletedDetailType)
}

func (app *fakeApplication) MemberRemoved(context.Context, authv1.MemberRemoved) error {
	return app.act(authv1.MemberRemovedDetailType)
}

func (app *fakeApplication) ConnectionDisconnected(context.Context, connectionsv1.ConnectionDisconnected) error {
	return app.act(connectionsv1.ConnectionDisconnectedDetailType)
}

func (app *fakeApplication) ConnectionNeedsReconnect(context.Context, connectionsv1.ConnectionNeedsReconnect) error {
	return app.act(connectionsv1.ConnectionNeedsReconnectDetailType)
}

func (app *fakeApplication) AppDeleted(context.Context, connectionsv1.AppDeleted) error {
	return app.act(connectionsv1.AppDeletedDetailType)
}

func (app *fakeApplication) AlreadyHandled(_ context.Context, eventID string) (bool, error) {
	return app.handled[eventID], nil
}

func (app *fakeApplication) MarkHandled(_ context.Context, eventID, _ string) error {
	app.handled[eventID] = true
	return nil
}

// delivery is one message as EventBridge would put it on the queue.
func delivery(t *testing.T, source, detailType, eventID string, data any) string {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(map[string]any{
		"event_id": eventID, "version": 1, "occurred_at": testClock.Format(time.RFC3339Nano),
		"data": json.RawMessage(encoded),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"id": "aws-" + eventID, "source": source, "detail-type": detailType,
		"detail": json.RawMessage(detail),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestEverySubscribedEventHasAHandler(t *testing.T) {
	// The provisioner builds the EventBridge rule from Subscriptions, so a
	// detail type listed there but not handled means the queue receives
	// something nothing acts on, and one handled but not listed means an
	// event that never arrives. Neither shows up at runtime as anything but
	// silence.
	consumer := NewConsumer(newFakeApplication(), nil)
	subscribed := []string{}
	for _, detailTypes := range Subscriptions() {
		subscribed = append(subscribed, detailTypes...)
	}
	for _, detailType := range subscribed {
		if _, ok := consumer.handlers[detailType]; !ok {
			t.Fatalf("%s is subscribed but nothing handles it", detailType)
		}
	}
	for detailType := range consumer.handlers {
		if !slices.Contains(subscribed, detailType) {
			t.Fatalf("%s is handled but never subscribed, so it will not arrive", detailType)
		}
	}
}

func TestSubscriptionsNameTheSourceEachEventComesFrom(t *testing.T) {
	consumer := NewConsumer(newFakeApplication(), nil)
	for source, detailTypes := range Subscriptions() {
		for _, detailType := range detailTypes {
			if got := consumer.handlers[detailType].source; got != source {
				t.Fatalf("%s is subscribed from %s but handled as %s", detailType, source, got)
			}
		}
	}
}

func TestHandleActsOnAnEventOnlyOnce(t *testing.T) {
	// Pausing a source and retracting its documents are not idempotent the way
	// stopping an already-stopped subscription is, so a redelivery must not
	// tell a retrieval pipeline to forget the same documents twice.
	app := newFakeApplication()
	consumer := NewConsumer(app, nil)
	body := delivery(t, connectionsv1.Source, connectionsv1.ConnectionDisconnectedDetailType, "evt_1",
		connectionsv1.ConnectionDisconnected{ConnectionID: "con_1", OrganizationID: "org_1"})

	for range 3 {
		if err := consumer.Handle(context.Background(), body); err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
	}
	if len(app.acted) != 1 {
		t.Fatalf("acted %d times, want 1", len(app.acted))
	}
}

func TestHandleDispatchesEachEventToItsOwnHandler(t *testing.T) {
	app := newFakeApplication()
	consumer := NewConsumer(app, nil)
	for index, event := range []struct {
		source     string
		detailType string
		data       any
	}{
		{authv1.Source, authv1.OrganizationDeletedDetailType, authv1.OrganizationDeleted{OrganizationID: "org_1"}},
		{authv1.Source, authv1.MemberRemovedDetailType, authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-1"}},
		{connectionsv1.Source, connectionsv1.ConnectionDisconnectedDetailType, connectionsv1.ConnectionDisconnected{ConnectionID: "con_1"}},
		{connectionsv1.Source, connectionsv1.ConnectionNeedsReconnectDetailType, connectionsv1.ConnectionNeedsReconnect{ConnectionID: "con_1"}},
		{connectionsv1.Source, connectionsv1.AppDeletedDetailType, connectionsv1.AppDeleted{AppID: "app_1"}},
	} {
		body := delivery(t, event.source, event.detailType, fmt.Sprintf("evt_%d", index), event.data)
		if err := consumer.Handle(context.Background(), body); err != nil {
			t.Fatalf("Handle(%s) error = %v", event.detailType, err)
		}
		if app.acted[index] != event.detailType {
			t.Fatalf("acted on %s, want %s", app.acted[index], event.detailType)
		}
	}
}

func TestHandleRefusesADetailTypeFromTheWrongSource(t *testing.T) {
	// A detail type nobody else may claim: an event's name alone is not
	// enough to say who sent it.
	consumer := NewConsumer(newFakeApplication(), nil)
	body := delivery(t, "oryxa.someone-else", connectionsv1.ConnectionDisconnectedDetailType, "evt_1",
		connectionsv1.ConnectionDisconnected{ConnectionID: "con_1"})

	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("Handle() error = nil, want a refusal")
	}
}

func TestHandleRefusesAnEnvelopeFromAnotherVersion(t *testing.T) {
	consumer := NewConsumer(newFakeApplication(), nil)
	body := `{"id":"aws-1","source":"` + connectionsv1.Source + `","detail-type":"` +
		connectionsv1.ConnectionDisconnectedDetailType + `","detail":{"event_id":"evt_1","version":2,"data":{}}}`

	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("Handle() error = nil, want a refusal")
	}
}

func TestHandleLeavesAnEventUndeletedWhenActingOnItFailed(t *testing.T) {
	// Poll only deletes what Handle accepted, so an error here is what makes
	// SQS redeliver rather than lose the event.
	app := newFakeApplication()
	app.err = errors.New("the store was unreachable")
	consumer := NewConsumer(app, nil)
	body := delivery(t, connectionsv1.Source, connectionsv1.AppDeletedDetailType, "evt_1",
		connectionsv1.AppDeleted{AppID: "app_1"})

	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("Handle() error = nil, want the failure to reach Poll")
	}
	// A failed handler must not be recorded: the redelivery Poll causes has
	// to run it again, not find it already marked handled.
	if already, err := app.AlreadyHandled(context.Background(), "evt_1"); err != nil || already {
		t.Fatalf("AlreadyHandled() after a failed handler = %v, %v, want false", already, err)
	}
}

func TestHandleRecordsAnEventOnlyAfterItsHandlerSucceeds(t *testing.T) {
	// Recording an event as handled before running its handler would mean a
	// handler that then fails is never retried: the redelivery would find
	// the event already marked handled, skip the handler, and Poll would
	// still delete the message. The fix is to record the event only once its
	// handler has actually succeeded.
	app := newFakeApplication()
	app.failTimes = 1
	consumer := NewConsumer(app, nil)
	body := delivery(t, connectionsv1.Source, connectionsv1.AppDeletedDetailType, "evt_1",
		connectionsv1.AppDeleted{AppID: "app_1"})

	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("Handle() error = nil on the first delivery, want the handler's failure")
	}
	if already, err := app.AlreadyHandled(context.Background(), "evt_1"); err != nil || already {
		t.Fatalf("AlreadyHandled() after the first delivery failed = %v, %v, want false", already, err)
	}

	if err := consumer.Handle(context.Background(), body); err != nil {
		t.Fatalf("Handle() error on the redelivery = %v", err)
	}
	if len(app.acted) != 2 {
		t.Fatalf("handler ran %d times, want 2: the failure and the retry", len(app.acted))
	}
	if already, err := app.AlreadyHandled(context.Background(), "evt_1"); err != nil || !already {
		t.Fatalf("AlreadyHandled() after the redelivery succeeded = %v, %v, want true", already, err)
	}
}

// fakeOutbox is what the dispatcher claims from.
type fakeOutbox struct {
	pending   []domain.OutboxEvent
	published []string
	failed    []string
	delays    []time.Duration
}

func (outbox *fakeOutbox) ClaimOutbox(_ context.Context, limit int) ([]domain.OutboxEvent, error) {
	if len(outbox.pending) == 0 {
		return nil, nil
	}
	if limit > len(outbox.pending) {
		limit = len(outbox.pending)
	}
	claimed := outbox.pending[:limit]
	outbox.pending = outbox.pending[limit:]
	return claimed, nil
}

func (outbox *fakeOutbox) MarkOutboxPublished(_ context.Context, eventID string) error {
	outbox.published = append(outbox.published, eventID)
	return nil
}

func (outbox *fakeOutbox) MarkOutboxFailed(_ context.Context, eventID string, retryAfter time.Duration, _ string) error {
	outbox.failed = append(outbox.failed, eventID)
	outbox.delays = append(outbox.delays, retryAfter)
	return nil
}

// fakeSink stands where EventBridge, or a retrieval pipeline sharing this
// process, would.
type fakeSink struct {
	got []domain.Event
	err error
}

func (sink *fakeSink) PublishEvent(_ context.Context, event domain.Event) error {
	if sink.err != nil {
		return sink.err
	}
	sink.got = append(sink.got, event)
	return nil
}

func outboxEvent(id string, attempts int) domain.OutboxEvent {
	return domain.OutboxEvent{
		Event: domain.Event{
			ID: id, DetailType: "knowledge.document.published.v1",
			OccurredAt: testClock, Data: json.RawMessage(`{}`),
		},
		Attempts: attempts,
	}
}

func TestDispatcherHandsEveryClaimedEventToTheSink(t *testing.T) {
	// The sink is an interface precisely so this is the same whether it is the
	// bus or a pipeline in this process.
	outbox := &fakeOutbox{pending: []domain.OutboxEvent{outboxEvent("evt_1", 0), outboxEvent("evt_2", 0)}}
	sink := &fakeSink{}

	NewDispatcher(outbox, sink, nil).flush(context.Background())
	if len(sink.got) != 2 {
		t.Fatalf("sink received %d events, want 2", len(sink.got))
	}
	if len(outbox.published) != 2 {
		t.Fatalf("marked %d published, want 2", len(outbox.published))
	}
}

func TestDispatcherKeepsAnEventTheSinkRefused(t *testing.T) {
	outbox := &fakeOutbox{pending: []domain.OutboxEvent{outboxEvent("evt_1", 0)}}
	sink := &fakeSink{err: errors.New("the bus said no")}

	NewDispatcher(outbox, sink, nil).flush(context.Background())
	if len(outbox.published) != 0 {
		t.Fatal("marked an event published that was never published")
	}
	if len(outbox.failed) != 1 {
		t.Fatalf("marked %d failed, want 1", len(outbox.failed))
	}
}

func TestRetryDelayDoublesUntilItIsCapped(t *testing.T) {
	if got := retryDelay(0); got != time.Second {
		t.Fatalf("retryDelay(0) = %s, want 1s", got)
	}
	if got := retryDelay(3); got != 8*time.Second {
		t.Fatalf("retryDelay(3) = %s, want 8s", got)
	}
	if got := retryDelay(50); got != 64*time.Second {
		t.Fatalf("retryDelay(past the ceiling) = %s, want 64s", got)
	}
	if got := retryDelay(-1); got != time.Second {
		t.Fatalf("retryDelay(-1) = %s, want 1s", got)
	}
}

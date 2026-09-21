package events

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	filesv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	teamsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

// signingHarness holds one signer for each source knowledge listens to and
// a verifier that trusts them, so a delivery is signed the way the service
// it names would have signed it. testClock would freeze the signature's age,
// so the verifier reads the real clock: only occurred_at is fixed.
var (
	harnessOnce   sync.Once
	harnessKeys   map[string]*ecdsa.PublicKey
	harnessVerif  *eventauth.Verifier
	sourceSigners = map[string]*eventauth.Signer{}
)

func signingHarness() (*eventauth.Verifier, error) {
	var harnessErr error
	harnessOnce.Do(func() {
		harnessKeys = map[string]*ecdsa.PublicKey{}
		for _, source := range []string{"auth", "apps", "files", "teams"} {
			signer, key, err := eventauth.NewLocalSigner(source, time.Now)
			if err != nil {
				harnessErr = err
				return
			}
			sourceSigners["oryxa."+source] = signer
			harnessKeys[source] = key
		}
		harnessVerif = eventauth.NewVerifier(harnessKeys, nil, time.Now)
	})
	return harnessVerif, harnessErr
}

// consumerFor builds the consumer the way mount does, with a verifier that
// trusts the harness's signers.
func consumerFor(app application) (*Consumer, error) {
	verifier, err := signingHarness()
	if err != nil {
		return nil, err
	}
	return NewConsumer(app, verifier, nil)
}

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

func (app *fakeApplication) ConnectionConnected(context.Context, appsv1.ConnectionConnected) error {
	return app.act(appsv1.ConnectionConnectedDetailType)
}

func (app *fakeApplication) TriggerReceived(context.Context, appsv1.TriggerReceived) error {
	return app.act(appsv1.TriggerReceivedDetailType)
}

func (app *fakeApplication) FileStored(context.Context, filesv1.ObjectCreated) error {
	return app.act(filesv1.ObjectCreatedDetailType)
}

func (app *fakeApplication) FileDeleted(context.Context, filesv1.ObjectDeleted) error {
	return app.act(filesv1.ObjectDeletedDetailType)
}

func (app *fakeApplication) ConnectionExpired(context.Context, appsv1.ConnectionExpired) error {
	return app.act(appsv1.ConnectionExpiredDetailType)
}

func (app *fakeApplication) ConnectionRemoved(context.Context, appsv1.ConnectionRemoved) error {
	return app.act(appsv1.ConnectionRemovedDetailType)
}

func (app *fakeApplication) TeamDeleted(context.Context, teamsv1.TeamDeleted) error {
	return app.act(teamsv1.TeamDeletedDetailType)
}

func (app *fakeApplication) AlreadyHandled(_ context.Context, eventID string) (bool, error) {
	return app.handled[eventID], nil
}

func (app *fakeApplication) MarkHandled(_ context.Context, eventID, _ string) error {
	app.handled[eventID] = true
	return nil
}

// delivery is one message as EventBridge would put it on the queue: the
// envelope, signed the way the named source would have signed it.
func delivery(t *testing.T, source, detailType, eventID string, data any) string {
	t.Helper()
	if _, err := signingHarness(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	signer, ok := sourceSigners[source]
	if !ok {
		t.Fatalf("no harness signer for source %s", source)
	}
	signature, err := signer.Sign(source, detailType, eventID, 1, testClock.Format(time.RFC3339Nano), encoded)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(map[string]any{
		"event_id": eventID, "version": 1, "occurred_at": testClock.Format(time.RFC3339Nano),
		"data": json.RawMessage(encoded), "sig": signature,
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
	consumer, err := consumerFor(newFakeApplication())
	if err != nil {
		t.Fatal(err)
	}
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
	consumer, err := consumerFor(newFakeApplication())
	if err != nil {
		t.Fatal(err)
	}
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
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
	body := delivery(t, authv1.Source, authv1.OrganizationDeletedDetailType, "evt_1",
		authv1.OrganizationDeleted{OrganizationID: "org_1"})

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
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
	for index, event := range []struct {
		source     string
		detailType string
		data       any
	}{
		{authv1.Source, authv1.OrganizationDeletedDetailType, authv1.OrganizationDeleted{OrganizationID: "org_1"}},
		{authv1.Source, authv1.MemberRemovedDetailType, authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-1"}},
		{appsv1.Source, appsv1.ConnectionExpiredDetailType, appsv1.ConnectionExpired{ConnectionID: "apc_1"}},
		{appsv1.Source, appsv1.ConnectionRemovedDetailType, appsv1.ConnectionRemoved{ConnectionID: "apc_1"}},
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
	// enough to say who sent it. No harness signer exists for a source
	// nothing publishes as, so this delivery stays unsigned — which is one
	// more thing the consumer must refuse.
	consumer, err := consumerFor(newFakeApplication())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signingHarness(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(authv1.OrganizationDeleted{OrganizationID: "org_1"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(map[string]any{
		"event_id": "evt_1", "version": 1, "occurred_at": testClock.Format(time.RFC3339Nano),
		"data": json.RawMessage(encoded),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"id": "aws-evt_1", "source": "oryxa.someone-else", "detail-type": authv1.OrganizationDeletedDetailType,
		"detail": json.RawMessage(detail),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := consumer.Handle(context.Background(), string(body)); err == nil {
		t.Fatal("Handle() error = nil, want a refusal")
	}
}

func TestHandleRefusesAnEnvelopeFromAnotherVersion(t *testing.T) {
	consumer, err := consumerFor(newFakeApplication())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"aws-1","source":"` + authv1.Source + `","detail-type":"` +
		authv1.OrganizationDeletedDetailType + `","detail":{"event_id":"evt_1","version":2,"data":{}}}`

	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("Handle() error = nil, want a refusal")
	}
}

func TestHandleLeavesAnEventUndeletedWhenActingOnItFailed(t *testing.T) {
	// Poll only deletes what Handle accepted, so an error here is what makes
	// SQS redeliver rather than lose the event.
	app := newFakeApplication()
	app.err = errors.New("the store was unreachable")
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
	body := delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_1",
		authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-1"})

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
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
	body := delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_1",
		authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-1"})

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

// Claiming, marking, retrying and the backoff behind it are internal/outbox's
// own concern now: see its dispatcher_test.go.

// fakeQueue answers a scripted sequence of receives and records every
// receive and delete Poll makes, each on its own channel, so a test can wait
// for one happening rather than sleeping and hoping. A receive past the
// script blocks until the context is cancelled — the same shape a real long
// poll takes when nothing has arrived — which is what lets a test stop Poll
// deterministically. Poll only ever has one receive in flight at a time, so
// count needs no lock of its own.
type fakeQueue struct {
	receives []receiveResult
	count    int

	calls   chan time.Time
	deleted chan []string
}

type receiveResult struct {
	messages []sqstypes.Message
	err      error
}

func newFakeQueue(receives ...receiveResult) *fakeQueue {
	return &fakeQueue{receives: receives, calls: make(chan time.Time, 100), deleted: make(chan []string, 100)}
}

func (queue *fakeQueue) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	index := queue.count
	queue.count++
	queue.calls <- time.Now()

	if index >= len(queue.receives) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	next := queue.receives[index]
	if next.err != nil {
		return nil, next.err
	}
	return &sqs.ReceiveMessageOutput{Messages: next.messages}, nil
}

func (queue *fakeQueue) DeleteMessageBatch(_ context.Context, input *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	ids := make([]string, len(input.Entries))
	for i, entry := range input.Entries {
		ids[i] = aws.ToString(entry.ReceiptHandle)
	}
	queue.deleted <- ids
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func recvCall(t *testing.T, calls <-chan time.Time) time.Time {
	t.Helper()
	select {
	case when := <-calls:
		return when
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Poll to call ReceiveMessage")
		return time.Time{}
	}
}

func recvDeleted(t *testing.T, deleted <-chan []string) []string {
	t.Helper()
	select {
	case ids := <-deleted:
		return ids
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Poll to delete a handled message")
		return nil
	}
}

func waitPollReturns(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not return once its context was cancelled")
	}
}

func TestPollHandsEachMessageToHandleAndDeletesOnlyWhatSucceeded(t *testing.T) {
	app := newFakeApplication()
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
	good := delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_1",
		authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-1"})

	queue := newFakeQueue(receiveResult{messages: []sqstypes.Message{
		{MessageId: aws.String("m1"), ReceiptHandle: aws.String("rh1"), Body: aws.String(good)},
		{MessageId: aws.String("m2"), ReceiptHandle: aws.String("rh2"), Body: aws.String("not json")},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { consumer.Poll(ctx, queue, "queue-url"); close(done) }()

	deleted := recvDeleted(t, queue.deleted)
	cancel()
	waitPollReturns(t, done)

	if len(app.acted) != 1 || app.acted[0] != authv1.MemberRemovedDetailType {
		t.Fatalf("acted = %v, want one call for the message Handle could decode", app.acted)
	}
	if len(deleted) != 1 || deleted[0] != "rh1" {
		t.Fatalf("deleted receipt handles = %v, want only rh1: the message Handle failed on stays for redelivery", deleted)
	}
}

func TestPollBacksOffAfterAReceiveErrorBeforeTryingAgain(t *testing.T) {
	original := receiveBackoff
	receiveBackoff = 30 * time.Millisecond
	defer func() { receiveBackoff = original }()

	consumer, err := consumerFor(newFakeApplication())
	if err != nil {
		t.Fatal(err)
	}
	queue := newFakeQueue(receiveResult{err: errors.New("the queue did not answer")})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { consumer.Poll(ctx, queue, "queue-url"); close(done) }()

	first := recvCall(t, queue.calls)
	second := recvCall(t, queue.calls)
	cancel()
	waitPollReturns(t, done)

	if gap := second.Sub(first); gap < receiveBackoff/2 {
		t.Fatalf("retried after %s, want at least about %s: a receive error must back off before trying again", gap, receiveBackoff)
	}
}

func TestPollReturnsOnceItsContextIsCancelled(t *testing.T) {
	consumer, err := consumerFor(newFakeApplication())
	if err != nil {
		t.Fatal(err)
	}
	queue := newFakeQueue() // no receive ever answers; a real long poll looks the same
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { consumer.Poll(ctx, queue, "queue-url"); close(done) }()
	waitPollReturns(t, done)
}

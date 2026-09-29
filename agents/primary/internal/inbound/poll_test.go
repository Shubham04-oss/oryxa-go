package inbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactiondelivery"
)

// fakeQueue is a queue for Poll to read. It hands out what was sent to it, no
// more than one receive asks for, and otherwise waits, as a long poll does,
// until something is sent or the consumer stops.
type fakeQueue struct {
	mutex   sync.Mutex
	waiting []sqstypes.Message
	sent    int
	arrived chan struct{}
	// asked is how many messages each receive asked for.
	asked []int32
	// deleted is the receipt handle of each message deleted, and deleteContexts
	// what the context each was deleted under said.
	deleted        []string
	deleteContexts []error
	// extended is the receipt handle of each message kept from coming back, and
	// for how many seconds.
	extended []string
	kept     []int32
	// receiveErrors are what the next receives fail with, one each, before any
	// message is given; empties is how many receives after them find the queue
	// empty, as a long poll that times out does.
	receiveErrors []error
	empties       int
}

func newFakeQueue() *fakeQueue { return &fakeQueue{arrived: make(chan struct{}, 1)} }

// send puts a message on the queue and answers its receipt handle.
func (queue *fakeQueue) send(body string) string {
	queue.mutex.Lock()
	queue.sent++
	receipt := fmt.Sprintf("receipt-%d", queue.sent)
	queue.waiting = append(queue.waiting, sqstypes.Message{
		Body: aws.String(body), MessageId: aws.String(fmt.Sprintf("message-%d", queue.sent)), ReceiptHandle: aws.String(receipt),
	})
	queue.mutex.Unlock()
	select {
	case queue.arrived <- struct{}{}:
	default:
	}
	return receipt
}

func (queue *fakeQueue) ReceiveMessage(ctx context.Context, input *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	queue.mutex.Lock()
	queue.asked = append(queue.asked, input.MaxNumberOfMessages)
	if len(queue.receiveErrors) > 0 {
		err := queue.receiveErrors[0]
		queue.receiveErrors = queue.receiveErrors[1:]
		queue.mutex.Unlock()
		return nil, err
	}
	if queue.empties > 0 {
		queue.empties--
		queue.mutex.Unlock()
		return &sqs.ReceiveMessageOutput{}, nil
	}
	queue.mutex.Unlock()
	for {
		queue.mutex.Lock()
		if take := min(len(queue.waiting), int(input.MaxNumberOfMessages)); take > 0 {
			messages := slices.Clone(queue.waiting[:take])
			queue.waiting = queue.waiting[take:]
			queue.mutex.Unlock()
			return &sqs.ReceiveMessageOutput{Messages: messages}, nil
		}
		queue.mutex.Unlock()
		select {
		case <-queue.arrived:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (queue *fakeQueue) DeleteMessage(ctx context.Context, input *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	queue.deleted = append(queue.deleted, aws.ToString(input.ReceiptHandle))
	queue.deleteContexts = append(queue.deleteContexts, ctx.Err())
	return &sqs.DeleteMessageOutput{}, nil
}

func (queue *fakeQueue) ChangeMessageVisibility(_ context.Context, input *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	queue.extended = append(queue.extended, aws.ToString(input.ReceiptHandle))
	queue.kept = append(queue.kept, input.VisibilityTimeout)
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (queue *fakeQueue) wasDeleted(receipt string) bool {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	return slices.Contains(queue.deleted, receipt)
}

func (queue *fakeQueue) timesExtended(receipt string) int {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	return len(slices.DeleteFunc(slices.Clone(queue.extended), func(got string) bool { return got != receipt }))
}

func (queue *fakeQueue) mostAsked() int32 {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	return slices.Max(queue.asked)
}

// waitFor polls until condition holds, rather than sleeping for as long as it
// might take.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// setVar sets a package variable for the length of one test. No test that
// reads it may run in parallel.
func setVar[T any](t *testing.T, variable *T, value T) {
	t.Helper()
	previous := *variable
	*variable = value
	t.Cleanup(func() { *variable = previous })
}

// syncInteractionRecorder is fakeInteractionRecorder for a consumer whose
// messages are handled side by side.
type syncInteractionRecorder struct {
	mutex sync.Mutex
	ids   []string
}

func (recorder *syncInteractionRecorder) Record(_ context.Context, delivery interactiondelivery.Delivery) (bool, error) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.ids = append(recorder.ids, delivery.EventID)
	return true, nil
}

func (recorder *syncInteractionRecorder) recorded() []string {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return slices.Clone(recorder.ids)
}

// lockedBuffer is a buffer a consumer's goroutines log to while a test reads it.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (logs *lockedBuffer) Write(data []byte) (int, error) {
	logs.mutex.Lock()
	defer logs.mutex.Unlock()
	return logs.buffer.Write(data)
}

func (logs *lockedBuffer) String() string {
	logs.mutex.Lock()
	defer logs.mutex.Unlock()
	return logs.buffer.String()
}

// logBuffer collects what a consumer logs.
func logBuffer() (*slog.Logger, *lockedBuffer) {
	logs := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), logs
}

// interactionRequested is a signed interaction request, as the bus delivers it.
func interactionRequested(t *testing.T, id string) string {
	t.Helper()
	return delivery(t, agentsv1.Source, agentsv1.InteractionRequestedDetailType, "evt_"+id, agentsv1.InteractionChanged{
		RequestID: "irq_" + id, OrganizationID: "org_1",
		RequesterUserID: "user_a", RequesterAgentID: "agt_a", TargetUserID: "user_b", TargetAgentID: "agt_b",
		Status: "pending", Revision: 1, ChangedAt: time.Unix(100, 0).UTC(),
	})
}

// memberRemoved is a signed removal, as auth's bus delivers it.
func memberRemoved(t *testing.T, id, organizationID string) string {
	t.Helper()
	return delivery(t, authv1.Source, authv1.MemberRemovedDetailType, "evt_"+id,
		authv1.MemberRemoved{OrganizationID: organizationID, UserID: "user_1"})
}

// polling is a Poll that is running, for a test to stop.
type polling struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func poll(t *testing.T, consumer *Consumer, queue *fakeQueue) *polling {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	running := &polling{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(running.done)
		consumer.Poll(ctx, queue, "https://sqs.test/000000000000/agents-events")
	}()
	t.Cleanup(func() { running.stop(t) })
	return running
}

// stop ends Poll and waits for it to return, which it does once what it was
// handling has been.
func (running *polling) stop(t *testing.T) {
	t.Helper()
	running.cancel()
	select {
	case <-running.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Poll did not return after it was stopped")
	}
}

// pollingConsumer is a consumer whose ordered forgetters are forget.
func pollingConsumer(t *testing.T, logger *slog.Logger, forget ...Forgetter) (*Consumer, *syncInteractionRecorder) {
	t.Helper()
	recorder := &syncInteractionRecorder{}
	consumer, err := NewConsumer(&fakeRecorder{}, recorder, forget, testVerifier(t), logger)
	if err != nil {
		t.Fatal(err)
	}
	return consumer, recorder
}

// TestASlowMemberRemovalDoesNotHoldUpTheMessagesBesideIt is the reason Poll
// handles several messages at once: a member's cleanup can take long, and
// every agent's interaction deliveries, in every organization, come through
// this one queue. Neither the messages received with the removal nor the ones
// that come after it wait for it.
func TestASlowMemberRemovalDoesNotHoldUpTheMessagesBesideIt(t *testing.T) {
	queue := newFakeQueue()
	started, finish := make(chan struct{}), make(chan struct{})
	consumer, recorder := pollingConsumer(t, nil, func(context.Context, string, string) error {
		close(started)
		<-finish
		return nil
	})
	poll(t, consumer, queue)

	removal := queue.send(memberRemoved(t, "removed", "org_1"))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the removal was never handled")
	}
	// Beside it, and then after it, while it is still going on.
	beside := queue.send(interactionRequested(t, "beside"))
	waitFor(t, "a message received beside a slow removal", func() bool { return queue.wasDeleted(beside) })
	after := queue.send(interactionRequested(t, "after"))
	waitFor(t, "a message received after a slow removal", func() bool { return queue.wasDeleted(after) })
	if queue.wasDeleted(removal) {
		t.Fatal("the removal was deleted before it finished")
	}

	close(finish)
	waitFor(t, "the removal to be deleted once it finished", func() bool { return queue.wasDeleted(removal) })
	if got := recorder.recorded(); len(got) != 2 {
		t.Fatalf("recorded %v, want both interaction deliveries", got)
	}
}

// TestNoMoreMessagesAreHandledAtOnceThanTheBound: concurrency is bounded, and
// so is what Poll takes from the queue, which is no more than it has places to
// handle: a message received with nobody free to handle it would wait out its
// visibility timeout and come back to be handled twice.
func TestNoMoreMessagesAreHandledAtOnceThanTheBound(t *testing.T) {
	const bound = 3
	queue := newFakeQueue()
	var running, most atomic.Int32
	release := make(chan struct{})
	consumer, _ := pollingConsumer(t, nil, func(context.Context, string, string) error {
		now := running.Add(1)
		defer running.Add(-1)
		for previous := most.Load(); now > previous && !most.CompareAndSwap(previous, now); previous = most.Load() {
		}
		<-release
		return nil
	})
	consumer.WithConcurrency(bound)
	poll(t, consumer, queue)

	var receipts []string
	for index := range 4 * bound {
		receipts = append(receipts, queue.send(memberRemoved(t, fmt.Sprintf("removed_%d", index), "org_1")))
	}
	waitFor(t, "the bound to be reached", func() bool { return running.Load() == bound })
	// Room for any that would run beyond the bound to have started.
	time.Sleep(50 * time.Millisecond)
	if got := running.Load(); got != bound {
		t.Fatalf("%d messages handled at once, want the bound of %d", got, bound)
	}

	close(release)
	waitFor(t, "every message to be handled", func() bool {
		for _, receipt := range receipts {
			if !queue.wasDeleted(receipt) {
				return false
			}
		}
		return true
	})
	if got := most.Load(); got != bound {
		t.Fatalf("at most %d messages handled at once, want %d", got, bound)
	}
	if got := queue.mostAsked(); got > bound {
		t.Fatalf("a receive asked for %d messages with only %d places to handle them", got, bound)
	}
}

// Places are given back for the messages a receive did not return, and when a
// receive fails or finds nothing, or Poll would lose them and come to handle
// fewer messages at once than it should.
func TestPlacesAreGivenBackWhenAReceiveReturnsLessOrFails(t *testing.T) {
	const bound = 3
	setVar(t, &receiveBackoff, time.Millisecond)
	queue := newFakeQueue()
	queue.receiveErrors = []error{errors.New("the queue is unreachable"), errors.New("the queue is unreachable")}
	queue.empties = 2
	var running atomic.Int32
	release := make(chan struct{})
	consumer, recorder := pollingConsumer(t, nil, func(context.Context, string, string) error {
		running.Add(1)
		defer running.Add(-1)
		<-release
		return nil
	})
	consumer.WithConcurrency(bound)
	poll(t, consumer, queue)

	// One at a time, each receive returning one of the places it asked for.
	for index := range 4 {
		receipt := queue.send(interactionRequested(t, fmt.Sprintf("one_%d", index)))
		waitFor(t, fmt.Sprintf("message %d to be handled", index), func() bool { return queue.wasDeleted(receipt) })
	}
	if got := recorder.recorded(); len(got) != 4 {
		t.Fatalf("recorded %v, want all four", got)
	}
	// Every place is still there to handle messages at once.
	for index := range bound {
		queue.send(memberRemoved(t, fmt.Sprintf("removed_%d", index), "org_1"))
	}
	waitFor(t, "every place to handle a message at once", func() bool { return running.Load() == bound })
	close(release)
}

// A handler that outlasts the queue's visibility timeout would be run again
// beside itself, and use up the receives that send a message to the
// dead-letter queue, so the message is kept from coming back while it is
// handled, and only then.
func TestAMessageBeingHandledIsKeptFromComingBack(t *testing.T) {
	setVar(t, &visibilityBeat, 5*time.Millisecond)
	queue := newFakeQueue()
	finish := make(chan struct{})
	consumer, _ := pollingConsumer(t, nil, func(context.Context, string, string) error {
		<-finish
		return nil
	})
	poll(t, consumer, queue)

	receipt := queue.send(memberRemoved(t, "removed", "org_1"))
	waitFor(t, "the message to be kept from coming back twice", func() bool { return queue.timesExtended(receipt) >= 2 })
	queue.mutex.Lock()
	kept := slices.Clone(queue.kept)
	queue.mutex.Unlock()
	for _, seconds := range kept {
		if want := int32(visibilityExtension / time.Second); seconds != want {
			t.Fatalf("kept for %d seconds, want %d", seconds, want)
		}
	}

	close(finish)
	waitFor(t, "the message to be deleted", func() bool { return queue.wasDeleted(receipt) })
	settled := queue.timesExtended(receipt)
	time.Sleep(50 * time.Millisecond)
	if got := queue.timesExtended(receipt); got != settled {
		t.Fatalf("the message was kept from coming back %d more times after it was deleted", got-settled)
	}
}

// A message handled in less than a beat, nearly all of them, costs no call.
func TestAMessageHandledQuicklyIsNotKeptFromComingBack(t *testing.T) {
	queue := newFakeQueue()
	consumer, _ := pollingConsumer(t, nil)
	poll(t, consumer, queue)

	receipt := queue.send(interactionRequested(t, "quick"))
	waitFor(t, "the message to be deleted", func() bool { return queue.wasDeleted(receipt) })
	if got := queue.timesExtended(receipt); got != 0 {
		t.Fatalf("a message handled at once was kept from coming back %d times", got)
	}
}

// One that fails stays on the queue for its next delivery, and what was
// handled beside it does not.
func TestAMessageThatFailsIsLeftOnTheQueueAndTheOthersAreDeleted(t *testing.T) {
	queue := newFakeQueue()
	logger, logs := logBuffer()
	consumer, _ := pollingConsumer(t, logger, func(_ context.Context, organizationID, _ string) error {
		if organizationID == "org_broken" {
			return fmt.Errorf("the database is down")
		}
		return nil
	})
	running := poll(t, consumer, queue)

	failing := queue.send(memberRemoved(t, "failing", "org_broken"))
	handled := queue.send(interactionRequested(t, "handled"))
	waitFor(t, "the message beside the failing one to be deleted", func() bool { return queue.wasDeleted(handled) })
	waitFor(t, "the failed message to be reported", func() bool { return strings.Contains(logs.String(), "event failed") })
	running.stop(t)
	if queue.wasDeleted(failing) {
		t.Fatal("a message whose handler failed was deleted")
	}
	if got := logs.String(); !strings.Contains(got, "the database is down") {
		t.Fatalf("a failed message was not reported with its cause:\n%s", got)
	}
}

// A message cut short because the consumer is stopping, a deploy say, did not
// fail: it is left for the next delivery, and said so without an error.
func TestAMessageCutShortByShuttingDownIsNotReportedAsAFailure(t *testing.T) {
	queue := newFakeQueue()
	logger, logs := logBuffer()
	started := make(chan struct{})
	consumer, _ := pollingConsumer(t, logger, func(ctx context.Context, _, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	running := poll(t, consumer, queue)

	receipt := queue.send(memberRemoved(t, "removed", "org_1"))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the removal was never handled")
	}
	running.stop(t)
	if queue.wasDeleted(receipt) {
		t.Fatal("a message cut short was deleted, so it would never be handled")
	}
	got := logs.String()
	if strings.Contains(got, "level=ERROR") || strings.Contains(got, "event failed") {
		t.Fatalf("a message cut short by shutting down was reported as a failure:\n%s", got)
	}
	if !strings.Contains(got, "event interrupted") {
		t.Fatalf("a message cut short was not said to be:\n%s", got)
	}
}

// Deleting what was handled is not cut short with it: a message handled and
// not deleted is handled again by whoever runs next.
func TestAMessageHandledAsTheConsumerStopsIsStillDeleted(t *testing.T) {
	queue := newFakeQueue()
	var running *polling
	consumer, _ := pollingConsumer(t, nil, func(context.Context, string, string) error {
		running.cancel()
		return nil
	})
	running = poll(t, consumer, queue)

	receipt := queue.send(memberRemoved(t, "removed", "org_1"))
	waitFor(t, "the message to be deleted", func() bool { return queue.wasDeleted(receipt) })
	running.stop(t)
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	if len(queue.deleteContexts) != 1 || queue.deleteContexts[0] != nil {
		t.Fatalf("deleted under contexts that said %v, want one that was still live", queue.deleteContexts)
	}
}

// A handler that hangs is cut off, and its place is free again, rather than
// held for ever by a message that is kept from coming back for as long as it
// runs.
func TestAMessageThatHangsIsCutOffAndItsPlaceIsFreed(t *testing.T) {
	setVar(t, &handlingTimeout, 50*time.Millisecond)
	queue := newFakeQueue()
	logger, logs := logBuffer()
	consumer, _ := pollingConsumer(t, logger, func(ctx context.Context, _, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	})
	consumer.WithConcurrency(1)
	running := poll(t, consumer, queue)

	hung := queue.send(memberRemoved(t, "hung", "org_1"))
	next := queue.send(interactionRequested(t, "next"))
	waitFor(t, "the message behind the hung one to be handled", func() bool { return queue.wasDeleted(next) })
	running.stop(t)
	if queue.wasDeleted(hung) {
		t.Fatal("a message whose handler hung was deleted")
	}
	if got := logs.String(); !strings.Contains(got, "level=ERROR") || !strings.Contains(got, "context deadline exceeded") {
		t.Fatalf("a handler that hung was not reported:\n%s", got)
	}
}

// The ordered forgetters stop at the first that fails, since a later one may
// remove what an earlier one needs to find its own on retry. The independent
// ones depend on none of them, so what happens to one changes nothing for the
// rest: not running the others would leave a departed member's data behind
// for as long as one of them, such as memory kept in an outside service,
// stayed down.
func TestIndependentForgettersRunWhateverBecomesOfTheOthers(t *testing.T) {
	var calls []string
	forgetter := func(name string, fail error) Forgetter {
		return func(context.Context, string, string) error {
			calls = append(calls, name)
			return fail
		}
	}
	remove := func(t *testing.T, ordered []Forgetter, independent []Forgetter) error {
		t.Helper()
		calls = nil
		consumer, err := NewConsumer(&fakeRecorder{}, nil, ordered, testVerifier(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		consumer.WithIndependentForgetters(independent...)
		return consumer.Handle(context.Background(), memberRemoved(t, "removed", "org_1"))
	}
	down := fmt.Errorf("memory bank down")

	if err := remove(t, []Forgetter{forgetter("first", nil), forgetter("second", nil)},
		[]Forgetter{forgetter("memory", nil), forgetter("wakeups", nil)}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "first,second,memory,wakeups" {
		t.Fatalf("ran %q, want every forgetter", got)
	}

	// An independent forgetter that fails keeps neither the others nor the
	// ordered ones from running, and is reported.
	err := remove(t, []Forgetter{forgetter("first", nil), forgetter("second", nil)},
		[]Forgetter{forgetter("memory", down), forgetter("wakeups", nil)})
	if err == nil || !strings.Contains(err.Error(), "memory bank down") {
		t.Fatalf("error = %v, want the failure", err)
	}
	if got := strings.Join(calls, ","); got != "first,second,memory,wakeups" {
		t.Fatalf("ran %q after one independent forgetter failed, want every forgetter", got)
	}

	// An ordered one that fails still stops the ordered ones after it, and no
	// independent one is kept from running.
	err = remove(t, []Forgetter{forgetter("first", down), forgetter("second", nil)},
		[]Forgetter{forgetter("memory", nil), forgetter("wakeups", nil)})
	if err == nil || !strings.Contains(err.Error(), "memory bank down") {
		t.Fatalf("error = %v, want the failure", err)
	}
	if got := strings.Join(calls, ","); got != "first,memory,wakeups" {
		t.Fatalf("ran %q, want the independent ones, and the ordered ones only up to the failure", got)
	}

	// Both failing are both reported.
	err = remove(t, []Forgetter{forgetter("first", fmt.Errorf("ledger locked"))},
		[]Forgetter{forgetter("memory", down)})
	if err == nil || !strings.Contains(err.Error(), "memory bank down") || !strings.Contains(err.Error(), "ledger locked") {
		t.Fatalf("error = %v, want both failures", err)
	}
}

package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"
)

type fakeApplication struct {
	deleted []autheventsv1.OrganizationDeleted
	removed []autheventsv1.MemberRemoved
	teams   []teamseventsv1.TeamDeleted
	err     error
}

func (fake *fakeApplication) OrganizationDeleted(_ context.Context, event autheventsv1.OrganizationDeleted) error {
	fake.deleted = append(fake.deleted, event)
	return fake.err
}

func (fake *fakeApplication) MemberRemoved(_ context.Context, event autheventsv1.MemberRemoved) error {
	fake.removed = append(fake.removed, event)
	return fake.err
}

func (fake *fakeApplication) TeamDeleted(_ context.Context, event teamseventsv1.TeamDeleted) error {
	fake.teams = append(fake.teams, event)
	return fake.err
}

// eventBridgeMessage builds the JSON an SQS message body carries: an
// EventBridge event wrapping the {event_id, version, data} envelope every
// Oryxa publisher uses.
func eventBridgeMessage(source, detailType string, version int, eventID, data string) string {
	return fmt.Sprintf(`{"version":"0","id":"eb-1","detail-type":%q,"source":%q,"detail":{"event_id":%q,"version":%d,"data":%s}}`,
		detailType, source, eventID, version, data)
}

func TestConsumerDispatchesToTheRightHandler(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, slog.New(slog.DiscardHandler))

	must(t, consumer.Handle(context.Background(), eventBridgeMessage(autheventsv1.Source, autheventsv1.OrganizationDeletedDetailType, 1, "evt_1",
		`{"organization_id":"org_1","name":"Acme","slug":"acme","deleted_by":"user-1","deleted_at":"2026-09-14T14:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), eventBridgeMessage(autheventsv1.Source, autheventsv1.MemberRemovedDetailType, 1, "evt_2",
		`{"organization_id":"org_1","user_id":"user-2","email":"ben@example.com","reason":"left","removed_at":"2026-09-14T15:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), eventBridgeMessage(teamseventsv1.Source, teamseventsv1.TeamDeletedDetailType, 1, "evt_3",
		`{"team_id":"team_eng","organization_id":"org_1","name":"Engineering","deleted_by":"user-1","deleted_at":"2026-09-14T16:00:00Z"}`)))

	if len(app.deleted) != 1 || app.deleted[0].OrganizationID != "org_1" || app.deleted[0].Name != "Acme" {
		t.Fatalf("OrganizationDeleted dispatched = %+v", app.deleted)
	}
	if len(app.removed) != 1 || app.removed[0].UserID != "user-2" || app.removed[0].Reason != "left" {
		t.Fatalf("MemberRemoved dispatched = %+v", app.removed)
	}
	if len(app.teams) != 1 || app.teams[0].TeamID != "team_eng" {
		t.Fatalf("TeamDeleted dispatched = %+v", app.teams)
	}
}

func TestConsumerRejectsAMalformedEnvelope(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler))
	if err := consumer.Handle(context.Background(), "{"); err == nil {
		t.Fatal("Handle() error = nil for malformed JSON")
	}
}

func TestConsumerRejectsAnUnsubscribedDetailType(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler))
	// auth.organization.created.v1 exists, but connections does not subscribe to it.
	err := consumer.Handle(context.Background(), eventBridgeMessage(autheventsv1.Source, autheventsv1.OrganizationCreatedDetailType, 1, "evt_1", `{}`))
	if err == nil {
		t.Fatal("Handle() error = nil for an unsubscribed detail type")
	}
}

func TestConsumerRejectsASpoofedSource(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler))
	// TeamDeleted's own detail type, claiming to come from auth instead of teams.
	err := consumer.Handle(context.Background(), eventBridgeMessage(autheventsv1.Source, teamseventsv1.TeamDeletedDetailType, 1, "evt_1", `{}`))
	if err == nil {
		t.Fatal("Handle() error = nil for a detail type from the wrong source")
	}
}

func TestConsumerRejectsAnUnsupportedEnvelopeVersion(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler))
	err := consumer.Handle(context.Background(), eventBridgeMessage(autheventsv1.Source, autheventsv1.OrganizationDeletedDetailType, 2, "evt_1", `{"organization_id":"org_1"}`))
	if err == nil {
		t.Fatal("Handle() error = nil for an unsupported envelope version")
	}
}

func TestConsumerRejectsAMissingEventID(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler))
	err := consumer.Handle(context.Background(), eventBridgeMessage(autheventsv1.Source, autheventsv1.OrganizationDeletedDetailType, 1, "", `{"organization_id":"org_1"}`))
	if err == nil {
		t.Fatal("Handle() error = nil for a missing event_id")
	}
}

func TestConsumerReturnsAHandlerError(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{err: errors.New("database down")}, slog.New(slog.DiscardHandler))
	err := consumer.Handle(context.Background(), eventBridgeMessage(autheventsv1.Source, autheventsv1.OrganizationDeletedDetailType, 1, "evt_1",
		`{"organization_id":"org_1"}`))
	if err == nil {
		t.Fatal("Handle() error = nil when the handler fails")
	}
}

// fakeQueue scripts ReceiveMessage: first any queued errors, then any queued
// batches, then it calls cancel (if set) and blocks on ctx like a real
// long-poll waiting on a queue nothing else will fill.
type fakeQueue struct {
	batches  [][]sqstypes.Message
	errs     []error
	receives []*sqs.ReceiveMessageInput
	deleted  []sqstypes.DeleteMessageBatchRequestEntry
	cancel   context.CancelFunc
}

func (fake *fakeQueue) ReceiveMessage(ctx context.Context, input *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	fake.receives = append(fake.receives, input)
	if len(fake.errs) > 0 {
		err := fake.errs[0]
		fake.errs = fake.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(fake.batches) > 0 {
		batch := fake.batches[0]
		fake.batches = fake.batches[1:]
		return &sqs.ReceiveMessageOutput{Messages: batch}, nil
	}
	if fake.cancel != nil {
		fake.cancel()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (fake *fakeQueue) DeleteMessageBatch(_ context.Context, input *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	fake.deleted = append(fake.deleted, input.Entries...)
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func TestPollDeletesOnlyHandledMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{
		{MessageId: aws.String("handled"), ReceiptHandle: aws.String("receipt-handled"), Body: aws.String(
			eventBridgeMessage(autheventsv1.Source, autheventsv1.OrganizationDeletedDetailType, 1, "evt_1", `{"organization_id":"org_1"}`))},
		{MessageId: aws.String("failed"), ReceiptHandle: aws.String("receipt-failed"), Body: aws.String("{")},
	}}}
	NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler)).Poll(ctx, queue, "http://localhost:4566/000000000000/connections-events")
	if len(queue.deleted) != 1 || aws.ToString(queue.deleted[0].ReceiptHandle) != "receipt-handled" {
		t.Fatalf("deleted = %+v", queue.deleted)
	}
}

func TestPollUsesLongPolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel}
	NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler)).Poll(ctx, queue, "http://localhost:4566/000000000000/connections-events")
	if len(queue.receives) == 0 {
		t.Fatal("Poll() never called ReceiveMessage")
	}
	if input := queue.receives[0]; input.MaxNumberOfMessages != 10 || input.WaitTimeSeconds != 20 {
		t.Fatalf("ReceiveMessageInput = %+v", input)
	}
}

func TestPollBacksOffAfterAReceiveError(t *testing.T) {
	original := receiveBackoff
	receiveBackoff = 50 * time.Millisecond
	defer func() { receiveBackoff = original }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, errs: []error{errors.New("throttled")}}

	start := time.Now()
	NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler)).Poll(ctx, queue, "http://localhost:4566/000000000000/connections-events")
	if elapsed := time.Since(start); elapsed < receiveBackoff {
		t.Fatalf("Poll() did not back off after a receive error: elapsed = %s", elapsed)
	}
	if len(queue.receives) < 2 {
		t.Fatalf("Poll() did not retry after a receive error: receives = %d", len(queue.receives))
	}
}

func TestPollReturnsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &fakeQueue{}
	done := make(chan struct{})
	go func() {
		NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler)).Poll(ctx, queue, "http://localhost:4566/000000000000/connections-events")
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Poll() did not return after ctx was cancelled")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

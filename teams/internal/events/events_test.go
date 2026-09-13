package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventbridgetypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

type fakeEventBridge struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
}

func (fake *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	fake.input = input
	return fake.output, nil
}

func TestPublisherWrapsEventsInTheEnvelope(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{}}
	occurredAt := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)

	err := NewPublisher(client, "oryxa").PublishEvent(context.Background(), domain.Event{
		ID:         "evt_1",
		DetailType: "teams.x.v1",
		OccurredAt: occurredAt,
		Data:       json.RawMessage(`{"organization_id":"org_1"}`),
	})
	if err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != teamseventsv1.Source || aws.ToString(entry.EventBusName) != "oryxa" ||
		aws.ToString(entry.DetailType) != "teams.x.v1" {
		t.Fatalf("entry = %+v", entry)
	}
	var detail struct {
		EventID    string          `json:"event_id"`
		Version    int             `json:"version"`
		OccurredAt string          `json:"occurred_at"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.EventID != "evt_1" || detail.Version != 1 || detail.OccurredAt != "2026-09-13T12:00:00Z" || string(detail.Data) != `{"organization_id":"org_1"}` {
		t.Fatalf("detail = %+v (data %s)", detail, detail.Data)
	}
}

func TestPublisherReportsRejectedEntries(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{
		FailedEntryCount: 1,
		Entries:          []eventbridgetypes.PutEventsResultEntry{{ErrorCode: aws.String("InternalFailure"), ErrorMessage: aws.String("rejected")}},
	}}
	err := NewPublisher(client, "default").PublishEvent(context.Background(), domain.Event{ID: "evt_1", DetailType: "teams.x.v1", OccurredAt: time.Now(), Data: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("PublishEvent() error = nil for a rejected entry")
	}
}

func TestPublisherRejectsIncompleteEvents(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{}}
	if err := NewPublisher(client, "default").PublishEvent(context.Background(), domain.Event{}); err == nil {
		t.Fatal("PublishEvent() error = nil for an empty event")
	}
}

type fakeApplication struct {
	created []autheventsv1.OrganizationCreated
	updated []autheventsv1.OrganizationUpdated
	deleted []autheventsv1.OrganizationDeleted
	removed []autheventsv1.MemberRemoved
	err     error
}

func (fake *fakeApplication) OrganizationCreated(_ context.Context, data autheventsv1.OrganizationCreated) error {
	fake.created = append(fake.created, data)
	return fake.err
}

func (fake *fakeApplication) OrganizationUpdated(_ context.Context, data autheventsv1.OrganizationUpdated) error {
	fake.updated = append(fake.updated, data)
	return fake.err
}

func (fake *fakeApplication) OrganizationDeleted(_ context.Context, data autheventsv1.OrganizationDeleted) error {
	fake.deleted = append(fake.deleted, data)
	return fake.err
}

func (fake *fakeApplication) MemberRemoved(_ context.Context, data autheventsv1.MemberRemoved) error {
	fake.removed = append(fake.removed, data)
	return fake.err
}

func authEvent(detailType string, version int, data string) string {
	return fmt.Sprintf(`{"version":"0","id":"eb-1","detail-type":%q,"source":"oryxa.auth","detail":{"event_id":"evt_auth_1","version":%d,"occurred_at":"2026-09-13T12:00:00Z","data":%s}}`, detailType, version, data)
}

func TestConsumerDispatchesOrganizationEvents(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, slog.New(slog.DiscardHandler))
	ctx := context.Background()

	must(t, consumer.Handle(ctx, authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":"org_1","name":"Acme","slug":"acme","created_by":"user-1","created_at":"2026-09-13T12:00:00Z"}`)))
	must(t, consumer.Handle(ctx, authEvent(autheventsv1.OrganizationUpdatedDetailType, 1, `{"organization_id":"org_1","name":"Acme Two","updated_at":"2026-09-13T13:00:00Z"}`)))
	must(t, consumer.Handle(ctx, authEvent(autheventsv1.OrganizationDeletedDetailType, 1, `{"organization_id":"org_1","name":"Acme Two","deleted_at":"2026-09-13T14:00:00Z"}`)))
	must(t, consumer.Handle(ctx, authEvent(autheventsv1.MemberRemovedDetailType, 1, `{"organization_id":"org_1","user_id":"user-2","email":"ben@example.com","reason":"left","removed_at":"2026-09-13T15:00:00Z"}`)))

	if len(app.created) != 1 || app.created[0].Name != "Acme" || app.created[0].CreatedAt.IsZero() ||
		len(app.updated) != 1 || app.updated[0].Name != "Acme Two" ||
		len(app.deleted) != 1 || app.deleted[0].OrganizationID != "org_1" ||
		len(app.removed) != 1 || app.removed[0].UserID != "user-2" {
		t.Fatalf("dispatched created=%+v updated=%+v deleted=%+v removed=%+v", app.created, app.updated, app.deleted, app.removed)
	}
}

func TestConsumerRejectsWhatItCannotHandle(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{err: errors.New("database down")}, slog.New(slog.DiscardHandler))
	tests := map[string]string{
		"not json":          "{",
		"unsubscribed type": authEvent(autheventsv1.MemberJoinedDetailType, 1, `{}`),
		"another source":    `{"id":"eb-1","detail-type":"auth.organization.created.v1","source":"oryxa.billing","detail":{"event_id":"e","version":1,"data":{}}}`,
		"future version":    authEvent(autheventsv1.OrganizationCreatedDetailType, 2, `{"organization_id":"org_1","name":"Acme"}`),
		"bad data":          authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":42}`),
		"application error": authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":"org_1","name":"Acme"}`),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if err := consumer.Handle(context.Background(), body); err == nil {
				t.Fatal("Handle() error = nil")
			}
		})
	}
}

type fakeQueue struct {
	batches [][]sqstypes.Message
	deleted []sqstypes.DeleteMessageBatchRequestEntry
	cancel  context.CancelFunc
}

func (fake *fakeQueue) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	if len(fake.batches) == 0 {
		fake.cancel()
		return nil, ctx.Err()
	}
	batch := fake.batches[0]
	fake.batches = fake.batches[1:]
	return &sqs.ReceiveMessageOutput{Messages: batch}, nil
}

func (fake *fakeQueue) DeleteMessageBatch(_ context.Context, input *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	fake.deleted = append(fake.deleted, input.Entries...)
	return &sqs.DeleteMessageBatchOutput{}, nil
}

type fakeOutbox struct {
	events    []domain.OutboxEvent
	published []string
	failed    []string
}

func (fake *fakeOutbox) ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error) {
	events := fake.events
	fake.events = nil
	return events, nil
}

func (fake *fakeOutbox) MarkOutboxPublished(_ context.Context, id string) error {
	fake.published = append(fake.published, id)
	return nil
}

func (fake *fakeOutbox) MarkOutboxFailed(_ context.Context, id string, _ time.Duration, _ string) error {
	fake.failed = append(fake.failed, id)
	return nil
}

type fakePublisher struct {
	fail map[string]bool
}

func (fake fakePublisher) PublishEvent(_ context.Context, event domain.Event) error {
	if fake.fail[event.ID] {
		return errors.New("unavailable")
	}
	return nil
}

func TestDispatcherMarksEachEvent(t *testing.T) {
	outbox := &fakeOutbox{events: []domain.OutboxEvent{{Event: domain.Event{ID: "evt_1"}}, {Event: domain.Event{ID: "evt_2"}}}}
	dispatcher := NewDispatcher(outbox, fakePublisher{fail: map[string]bool{"evt_2": true}}, slog.New(slog.DiscardHandler))

	dispatcher.flush(context.Background())

	if fmt.Sprint(outbox.published) != "[evt_1]" || fmt.Sprint(outbox.failed) != "[evt_2]" {
		t.Fatalf("published = %v, failed = %v", outbox.published, outbox.failed)
	}
}

func TestPollDeletesOnlyHandledMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{
		{MessageId: aws.String("handled"), ReceiptHandle: aws.String("receipt-handled"), Body: aws.String(authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":"org_1","name":"Acme"}`))},
		{MessageId: aws.String("failed"), ReceiptHandle: aws.String("receipt-failed"), Body: aws.String("{")},
	}}}

	NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler)).Poll(ctx, queue, "http://localhost:4566/000000000000/teams-events")

	if len(queue.deleted) != 1 || aws.ToString(queue.deleted[0].ReceiptHandle) != "receipt-handled" {
		t.Fatalf("deleted = %+v, want only receipt-handled", queue.deleted)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

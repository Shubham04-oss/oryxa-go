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
	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
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
	occurredAt := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)

	err := NewPublisher(client, "oryxa").PublishEvent(context.Background(), domain.Event{
		ID:         "evt_1",
		DetailType: "files.object.created.v1",
		OccurredAt: occurredAt,
		Data:       json.RawMessage(`{"file_id":"file_1"}`),
	})
	if err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != fileseventsv1.Source || aws.ToString(entry.EventBusName) != "oryxa" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestPublisherReportsRejectedEntries(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{
		FailedEntryCount: 1,
		Entries:          []eventbridgetypes.PutEventsResultEntry{{ErrorCode: aws.String("InternalFailure"), ErrorMessage: aws.String("rejected")}},
	}}
	err := NewPublisher(client, "default").PublishEvent(context.Background(), domain.Event{ID: "evt_1", DetailType: "files.x.v1", OccurredAt: time.Now(), Data: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("PublishEvent() error = nil for a rejected entry")
	}
}

type fakeApplication struct {
	created []autheventsv1.OrganizationCreated
	deleted []autheventsv1.OrganizationDeleted
	removed []autheventsv1.MemberRemoved
	teams   []teamseventsv1.TeamDeleted
	err     error
}

func (fake *fakeApplication) OrganizationCreated(_ context.Context, data autheventsv1.OrganizationCreated) error {
	fake.created = append(fake.created, data)
	return fake.err
}
func (fake *fakeApplication) OrganizationUpdated(context.Context, autheventsv1.OrganizationUpdated) error {
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
func (fake *fakeApplication) TeamDeleted(_ context.Context, data teamseventsv1.TeamDeleted) error {
	fake.teams = append(fake.teams, data)
	return fake.err
}

func authEvent(detailType string, version int, data string) string {
	return fmt.Sprintf(`{"version":"0","id":"eb-1","detail-type":%q,"source":"oryxa.auth","detail":{"event_id":"evt_auth_1","version":%d,"occurred_at":"2026-09-14T12:00:00Z","data":%s}}`, detailType, version, data)
}

func teamsEvent(detailType string, version int, data string) string {
	return fmt.Sprintf(`{"version":"0","id":"eb-2","detail-type":%q,"source":"oryxa.teams","detail":{"event_id":"evt_teams_1","version":%d,"occurred_at":"2026-09-14T12:00:00Z","data":%s}}`, detailType, version, data)
}

func TestConsumerDispatchesOrganizationEvents(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, slog.New(slog.DiscardHandler))
	must(t, consumer.Handle(context.Background(), authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":"org_1","name":"Acme","slug":"acme","created_by":"user-1","created_at":"2026-09-14T12:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), authEvent(autheventsv1.OrganizationDeletedDetailType, 1, `{"organization_id":"org_1","name":"Acme","deleted_at":"2026-09-14T14:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), authEvent(autheventsv1.MemberRemovedDetailType, 1, `{"organization_id":"org_1","user_id":"user-2","email":"ben@example.com","reason":"left","removed_at":"2026-09-14T15:00:00Z"}`)))
	must(t, consumer.Handle(context.Background(), teamsEvent(teamseventsv1.TeamDeletedDetailType, 1, `{"team_id":"team_eng","organization_id":"org_1","name":"Engineering","deleted_by":"user-1","deleted_at":"2026-09-14T16:00:00Z"}`)))
	if len(app.created) != 1 || app.created[0].Name != "Acme" || len(app.deleted) != 1 || len(app.removed) != 1 || len(app.teams) != 1 || app.teams[0].TeamID != "team_eng" {
		t.Fatalf("dispatched created=%+v deleted=%+v removed=%+v teams=%+v", app.created, app.deleted, app.removed, app.teams)
	}
}

func TestConsumerRejectsWhatItCannotHandle(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{err: errors.New("database down")}, slog.New(slog.DiscardHandler))
	if err := consumer.Handle(context.Background(), "{"); err == nil {
		t.Fatal("Handle() error = nil")
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

func TestPollDeletesOnlyHandledMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{
		{MessageId: aws.String("handled"), ReceiptHandle: aws.String("receipt-handled"), Body: aws.String(authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":"org_1","name":"Acme"}`))},
		{MessageId: aws.String("failed"), ReceiptHandle: aws.String("receipt-failed"), Body: aws.String("{")},
	}}}
	NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler)).Poll(ctx, queue, "http://localhost:4566/000000000000/files-events")
	if len(queue.deleted) != 1 || aws.ToString(queue.deleted[0].ReceiptHandle) != "receipt-handled" {
		t.Fatalf("deleted = %+v", queue.deleted)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

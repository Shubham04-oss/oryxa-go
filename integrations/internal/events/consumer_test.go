package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
)

type fakeApplication struct {
	deleted []autheventsv1.OrganizationDeleted
	removed []autheventsv1.MemberRemoved
	err     error
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

func TestConsumerDispatchesOrganizationLifecycle(t *testing.T) {
	app := &fakeApplication{}
	consumer := NewConsumer(app, slog.New(slog.DiscardHandler))
	ctx := context.Background()

	must(t, consumer.Handle(ctx, authEvent(autheventsv1.OrganizationDeletedDetailType, 1, `{"organization_id":"org_1","name":"Acme","deleted_at":"2026-09-13T14:00:00Z"}`)))
	must(t, consumer.Handle(ctx, authEvent(autheventsv1.MemberRemovedDetailType, 1, `{"organization_id":"org_1","user_id":"user-2","email":"ben@example.com","reason":"left","removed_at":"2026-09-13T15:00:00Z"}`)))

	if len(app.deleted) != 1 || app.deleted[0].OrganizationID != "org_1" ||
		len(app.removed) != 1 || app.removed[0].UserID != "user-2" {
		t.Fatalf("dispatched deleted=%+v removed=%+v", app.deleted, app.removed)
	}
}

func TestConsumerRejectsWhatItCannotHandle(t *testing.T) {
	consumer := NewConsumer(&fakeApplication{err: errors.New("database down")}, slog.New(slog.DiscardHandler))
	tests := map[string]string{
		"not json":          "{",
		"unsubscribed type": authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":"org_1","name":"Acme"}`),
		"another source":    `{"id":"eb-1","detail-type":"auth.organization.deleted.v1","source":"oryxa.billing","detail":{"event_id":"e","version":1,"data":{}}}`,
		"future version":    authEvent(autheventsv1.OrganizationDeletedDetailType, 2, `{"organization_id":"org_1"}`),
		"bad data":          authEvent(autheventsv1.OrganizationDeletedDetailType, 1, `{"organization_id":42}`),
		"application error": authEvent(autheventsv1.OrganizationDeletedDetailType, 1, `{"organization_id":"org_1"}`),
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

func TestPollDeletesOnlyHandledMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{
		{MessageId: aws.String("handled"), ReceiptHandle: aws.String("receipt-handled"), Body: aws.String(authEvent(autheventsv1.OrganizationDeletedDetailType, 1, `{"organization_id":"org_1"}`))},
		{MessageId: aws.String("failed"), ReceiptHandle: aws.String("receipt-failed"), Body: aws.String("{")},
	}}}

	NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler)).Poll(ctx, queue, "http://localhost:4566/000000000000/integrations-events")

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

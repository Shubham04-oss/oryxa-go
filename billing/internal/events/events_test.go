package events

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventbridgetypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

type fakeEventBridge struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
}

func (fake *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	fake.input = input
	return fake.output, nil
}

func billingPublisher(t *testing.T) *Publisher {
	t.Helper()
	signer, _, err := eventauth.NewLocalSigner("billing", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(&fakeEventBridge{output: &eventbridge.PutEventsOutput{}}, "oryxa", signer)
	if err != nil {
		t.Fatal(err)
	}
	return publisher
}

func TestPublisherWrapsEventsInTheEnvelope(t *testing.T) {
	publisher := billingPublisher(t)
	client := publisher.client.(*fakeEventBridge)
	occurredAt := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)

	err := publisher.PublishEvent(context.Background(), domain.Event{
		ID:         "evt_1",
		DetailType: billingeventsv1.EntitlementsChangedDetailType,
		OccurredAt: occurredAt,
		Data:       json.RawMessage(`{"organization_id":"org_1","revision":1}`),
	})
	if err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != billingeventsv1.Source || aws.ToString(entry.EventBusName) != "oryxa" ||
		aws.ToString(entry.DetailType) != billingeventsv1.EntitlementsChangedDetailType {
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
	if detail.EventID != "evt_1" || detail.Version != 1 || detail.OccurredAt != "2026-09-13T12:00:00Z" || string(detail.Data) != `{"organization_id":"org_1","revision":1}` {
		t.Fatalf("detail = %+v (data %s)", detail, detail.Data)
	}
}

func TestPublisherReportsRejectedEntries(t *testing.T) {
	signer, _, err := eventauth.NewLocalSigner("billing", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{
		FailedEntryCount: 1,
		Entries:          []eventbridgetypes.PutEventsResultEntry{{ErrorCode: aws.String("InternalFailure"), ErrorMessage: aws.String("rejected")}},
	}}
	publisher, err := NewPublisher(client, "default", signer)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.PublishEvent(context.Background(), domain.Event{ID: "evt_1", DetailType: "billing.x.v1", OccurredAt: time.Now(), Data: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("PublishEvent() error = nil for a rejected entry")
	}
}

func TestPublisherRefusesToBuildWithoutASigningIdentity(t *testing.T) {
	if _, err := NewPublisher(&fakeEventBridge{output: &eventbridge.PutEventsOutput{}}, "default", nil); err == nil {
		t.Fatal("a publisher without a signer must not build")
	}
}

type fakeApplication struct {
	created []autheventsv1.OrganizationCreated
	updated []autheventsv1.OrganizationUpdated
	deleted []autheventsv1.OrganizationDeleted
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

// authEvent builds a signed auth event the way the mounted publisher would:
// the envelope, then the signature over exactly what it holds.
var (
	testAuthSigner *eventauth.Signer
	testVerifier   *eventauth.Verifier
	testAuthOnce   sync.Once
)

func authHarness() (*eventauth.Signer, *eventauth.Verifier) {
	testAuthOnce.Do(func() {
		signer, key, err := eventauth.NewLocalSigner("auth", time.Now)
		if err != nil {
			panic(err)
		}
		testAuthSigner = signer
		testVerifier = eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, time.Now)
	})
	return testAuthSigner, testVerifier
}

func authEvent(detailType string, version int, data string) string {
	signer, _ := authHarness()
	occurredAt := "2026-09-13T12:00:00Z"
	signature, err := signer.Sign(autheventsv1.Source, detailType, "evt_auth_1", version, occurredAt, []byte(data))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"version":"0","id":"eb-1","detail-type":%q,"source":"oryxa.auth","detail":{"event_id":"evt_auth_1","version":%d,"occurred_at":%q,"data":%s,"sig":%q}}`,
		detailType, version, occurredAt, data, signature)
}

func TestConsumerDispatchesOrganizationEvents(t *testing.T) {
	app := &fakeApplication{}
	_, verifier := authHarness()
	consumer, err := NewConsumer(app, verifier, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	must(t, consumer.Handle(ctx, authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":"org_1","name":"Acme","slug":"acme","created_by":"user-1","created_at":"2026-09-13T12:00:00Z"}`)))
	must(t, consumer.Handle(ctx, authEvent(autheventsv1.OrganizationUpdatedDetailType, 1, `{"organization_id":"org_1","name":"Acme Two","updated_at":"2026-09-13T13:00:00Z"}`)))
	must(t, consumer.Handle(ctx, authEvent(autheventsv1.OrganizationDeletedDetailType, 1, `{"organization_id":"org_1","name":"Acme Two","deleted_at":"2026-09-13T14:00:00Z"}`)))

	if len(app.created) != 1 || app.created[0].Name != "Acme" || app.created[0].CreatedAt.IsZero() ||
		len(app.updated) != 1 || app.updated[0].Name != "Acme Two" ||
		len(app.deleted) != 1 || app.deleted[0].OrganizationID != "org_1" {
		t.Fatalf("dispatched created=%+v updated=%+v deleted=%+v", app.created, app.updated, app.deleted)
	}
}

func TestConsumerRejectsWhatItCannotHandle(t *testing.T) {
	_, verifier := authHarness()
	consumer, err := NewConsumer(&fakeApplication{err: errors.New("database down")}, verifier, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
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

func TestPollDeletesOnlyHandledMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{
		{MessageId: aws.String("handled"), ReceiptHandle: aws.String("receipt-handled"), Body: aws.String(authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":"org_1","name":"Acme"}`))},
		{MessageId: aws.String("failed"), ReceiptHandle: aws.String("receipt-failed"), Body: aws.String("{")},
	}}}

	_, verifier := authHarness()
	consumer, err := NewConsumer(&fakeApplication{}, verifier, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/billing-events")

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

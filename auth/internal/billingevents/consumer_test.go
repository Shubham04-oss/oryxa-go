package billingevents

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
)

type fakeApplication struct {
	applied []billingeventsv1.EntitlementsChanged
	err     error
}

func (fake *fakeApplication) ApplyBillingEntitlements(_ context.Context, entitlements billingeventsv1.EntitlementsChanged) error {
	fake.applied = append(fake.applied, entitlements)
	return fake.err
}

func billingEvent(source, detailType string, version int, data string) string {
	return fmt.Sprintf(`{"version":"0","id":"eb-1","detail-type":%q,"source":%q,"detail":{"event_id":"evt_1","version":%d,"occurred_at":"2026-09-13T12:00:00Z","data":%s}}`,
		detailType, source, version, data)
}

const entitlementsData = `{"organization_id":"org_1","revision":3,"plan":"team","status":"active","member_limit":20,"changed_at":"2026-09-13T12:00:00Z"}`

func TestHandleAppliesEntitlements(t *testing.T) {
	app := &fakeApplication{}
	err := NewConsumer(app, nil).Handle(context.Background(), billingEvent(billingeventsv1.Source, billingeventsv1.EntitlementsChangedDetailType, 1, entitlementsData))
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(app.applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(app.applied))
	}
	got := app.applied[0]
	if got.OrganizationID != "org_1" || got.Revision != 3 || got.MemberLimit == nil || *got.MemberLimit != 20 || got.ChangedAt.IsZero() {
		t.Fatalf("applied entitlements = %+v", got)
	}

	unlimited := `{"organization_id":"org_1","revision":4,"status":"unpaid","member_limit":null,"changed_at":"2026-09-13T12:00:00Z"}`
	if err := NewConsumer(app, nil).Handle(context.Background(), billingEvent(billingeventsv1.Source, billingeventsv1.EntitlementsChangedDetailType, 1, unlimited)); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if app.applied[1].MemberLimit != nil {
		t.Fatalf("null member limit decoded as %d", *app.applied[1].MemberLimit)
	}
}

func TestHandleRejectsWhatItCannotApply(t *testing.T) {
	tests := map[string]struct {
		body string
		err  error
	}{
		"not json":          {body: "{"},
		"another source":    {body: billingEvent("oryxa.auth", billingeventsv1.EntitlementsChangedDetailType, 1, entitlementsData)},
		"another event":     {body: billingEvent(billingeventsv1.Source, billingeventsv1.SubscriptionEndedDetailType, 1, `{}`)},
		"future version":    {body: billingEvent(billingeventsv1.Source, billingeventsv1.EntitlementsChangedDetailType, 2, entitlementsData)},
		"bad data":          {body: billingEvent(billingeventsv1.Source, billingeventsv1.EntitlementsChangedDetailType, 1, `{"revision":"three"}`)},
		"application error": {body: billingEvent(billingeventsv1.Source, billingeventsv1.EntitlementsChangedDetailType, 1, entitlementsData), err: errors.New("database down")},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := NewConsumer(&fakeApplication{err: test.err}, nil).Handle(context.Background(), test.body); err == nil {
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

func TestPollDeletesOnlyAppliedMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]sqstypes.Message{{
		{MessageId: aws.String("applied"), ReceiptHandle: aws.String("receipt-applied"), Body: aws.String(billingEvent(billingeventsv1.Source, billingeventsv1.EntitlementsChangedDetailType, 1, entitlementsData))},
		{MessageId: aws.String("failed"), ReceiptHandle: aws.String("receipt-failed"), Body: aws.String("{")},
	}}}

	NewConsumer(&fakeApplication{}, nil).Poll(ctx, queue, "http://localhost:4566/000000000000/auth-events")

	if len(queue.deleted) != 1 || aws.ToString(queue.deleted[0].ReceiptHandle) != "receipt-applied" {
		t.Fatalf("deleted = %+v, want only receipt-applied", queue.deleted)
	}
}

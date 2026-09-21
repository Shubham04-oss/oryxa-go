package billingevents

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

type fakeApplication struct {
	applied []billingeventsv1.EntitlementsChanged
	err     error
}

func (fake *fakeApplication) ApplyBillingEntitlements(_ context.Context, entitlements billingeventsv1.EntitlementsChanged) error {
	fake.applied = append(fake.applied, entitlements)
	return fake.err
}

// billingHarness holds a signer named for the source the fixtures claim —
// billing — and a verifier that trusts it, so each delivery is signed the
// way billing would have signed it.
var (
	harnessOnce  sync.Once
	billingSign  *eventauth.Signer
	harnessVerif *eventauth.Verifier
	harnessErr   error
)

func billingHarness() (*eventauth.Verifier, error) {
	harnessOnce.Do(func() {
		signer, key, err := eventauth.NewLocalSigner("billing", time.Now)
		if err != nil {
			harnessErr = err
			return
		}
		billingSign = signer
		harnessVerif = eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"billing": key}, nil, time.Now)
	})
	return harnessVerif, harnessErr
}

// consumerFor builds the consumer the way mount does, with a verifier that
// trusts the harness's signer.
func consumerFor(app application) (*Consumer, error) {
	verifier, err := billingHarness()
	if err != nil {
		return nil, err
	}
	return NewConsumer(app, verifier, nil)
}

func billingEvent(source, detailType string, version int, data string) string {
	occurredAt := "2026-09-13T12:00:00Z"
	detail := fmt.Sprintf(`"event_id":"evt_1","version":%d,"occurred_at":%q,"data":%s`, version, occurredAt, data)
	if source == billingeventsv1.Source {
		if _, err := billingHarness(); err != nil {
			panic(err)
		}
		signature, err := billingSign.Sign(source, detailType, "evt_1", version, occurredAt, []byte(data))
		if err != nil {
			panic(err)
		}
		detail += fmt.Sprintf(`,"sig":%q`, signature)
	}
	return fmt.Sprintf(`{"version":"0","id":"eb-1","detail-type":%q,"source":%q,"detail":{%s}}`,
		detailType, source, detail)
}

const entitlementsData = `{"organization_id":"org_1","revision":3,"plan":"team","status":"active","member_limit":20,"changed_at":"2026-09-13T12:00:00Z"}`

func TestHandleAppliesEntitlements(t *testing.T) {
	app := &fakeApplication{}
	consumer, err := consumerFor(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.Handle(context.Background(), billingEvent(billingeventsv1.Source, billingeventsv1.EntitlementsChangedDetailType, 1, entitlementsData)); err != nil {
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
	if err := consumer.Handle(context.Background(), billingEvent(billingeventsv1.Source, billingeventsv1.EntitlementsChangedDetailType, 1, unlimited)); err != nil {
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
			consumer, err := consumerFor(&fakeApplication{err: test.err})
			if err != nil {
				t.Fatal(err)
			}
			if err := consumer.Handle(context.Background(), test.body); err == nil {
				t.Fatal("Handle() error = nil")
			}
		})
	}
}

// TestHandleRefusesAnUnsignedEventHoweverWellFormed is the attack the
// signatures exist for: a perfectly-shaped entitlements message that no
// service signed, which would set an organization's member limit to
// whatever the attacker chose.
func TestHandleRefusesAnUnsignedEventHoweverWellFormed(t *testing.T) {
	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"version":"0","id":"eb-1","detail-type":` + fmt.Sprintf("%q", billingeventsv1.EntitlementsChangedDetailType) +
		`,"source":` + fmt.Sprintf("%q", billingeventsv1.Source) +
		`,"detail":{"event_id":"evt-forged","version":1,"occurred_at":"2026-09-13T12:00:00Z","data":{"organization_id":"org_1","revision":99,"member_limit":99999}}}`
	if err := consumer.Handle(context.Background(), body); err == nil {
		t.Fatal("an unsigned entitlements event was applied")
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

	consumer, err := consumerFor(&fakeApplication{})
	if err != nil {
		t.Fatal(err)
	}
	consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/auth-events")

	if len(queue.deleted) != 1 || aws.ToString(queue.deleted[0].ReceiptHandle) != "receipt-applied" {
		t.Fatalf("deleted = %+v, want only receipt-applied", queue.deleted)
	}
}

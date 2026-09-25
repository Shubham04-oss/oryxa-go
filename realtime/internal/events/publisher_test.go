package events

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventbridgetypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	realtimev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/realtime/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
)

type fakeEventBridge struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
	err    error
}

func (fake *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	fake.input = input
	return fake.output, fake.err
}

func realtimePublisher(t *testing.T, client *fakeEventBridge) (*Publisher, *eventauth.Verifier) {
	t.Helper()
	signer, key, err := eventauth.NewLocalSigner("realtime", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(client, "oryxa", signer)
	if err != nil {
		t.Fatal(err)
	}
	return publisher, eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"realtime": key}, nil, time.Now)
}

// An event goes out as oryxa.realtime, in the envelope, at the time it
// happened, signed so that a consumer trusting realtime's key verifies it.
func TestPublisherPutsTheEventOnTheBusSigned(t *testing.T) {
	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{}}
	publisher, verifier := realtimePublisher(t, client)
	ist := time.FixedZone("IST", 5*60*60+30*60)
	data := json.RawMessage(`{"organization_id":"org_1","reader_user_id":"user-b","read_through_seq":7}`)
	err := publisher.Publish(context.Background(), outbox.Event{
		ID: "evt_1", DetailType: realtimev1.ConversationReadDetailType, Data: data,
		OccurredAt: time.Date(2026, time.September, 24, 15, 35, 0, 123000000, ist),
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != "oryxa.realtime" || aws.ToString(entry.EventBusName) != "oryxa" ||
		aws.ToString(entry.DetailType) != realtimev1.ConversationReadDetailType ||
		!aws.ToTime(entry.Time).Equal(time.Date(2026, time.September, 24, 10, 5, 0, 123000000, time.UTC)) {
		t.Fatalf("entry = %+v", entry)
	}
	var detail struct {
		EventID    string          `json:"event_id"`
		Version    int             `json:"version"`
		OccurredAt string          `json:"occurred_at"`
		Data       json.RawMessage `json:"data"`
		Signature  string          `json:"sig"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.EventID != "evt_1" || detail.Version != 1 || detail.OccurredAt != "2026-09-24T10:05:00.123Z" || string(detail.Data) != string(data) {
		t.Fatalf("detail = %+v (data %s)", detail, detail.Data)
	}
	if err := verifier.Verify(realtimev1.Source, realtimev1.ConversationReadDetailType, detail.EventID, detail.Version,
		detail.OccurredAt, detail.Data, detail.Signature); err != nil {
		t.Fatalf("a consumer trusting realtime refused its event: %v", err)
	}
	// Signed as realtime, it passes as nobody else.
	if err := verifier.Verify("oryxa.teams", realtimev1.ConversationReadDetailType, detail.EventID, detail.Version,
		detail.OccurredAt, detail.Data, detail.Signature); !errors.Is(err, eventauth.ErrUnverified) {
		t.Fatalf("realtime's event verified as teams': %v", err)
	}
}

func TestPublisherReportsWhatTheBusRefused(t *testing.T) {
	event := outbox.Event{ID: "evt_1", DetailType: realtimev1.MessagePostedDetailType, OccurredAt: time.Now(), Data: json.RawMessage(`{}`)}
	for name, client := range map[string]*fakeEventBridge{
		"a rejected entry": {output: &eventbridge.PutEventsOutput{FailedEntryCount: 1,
			Entries: []eventbridgetypes.PutEventsResultEntry{{ErrorCode: aws.String("InternalFailure"), ErrorMessage: aws.String("rejected")}}}},
		"no entry result":  {output: &eventbridge.PutEventsOutput{FailedEntryCount: 1}},
		"no result at all": {},
		"no answer":        {err: errors.New("connection reset")},
	} {
		publisher, _ := realtimePublisher(t, client)
		if err := publisher.Publish(context.Background(), event); err == nil {
			t.Errorf("%s: Publish() = nil", name)
		}
	}
	publisher, _ := realtimePublisher(t, &fakeEventBridge{output: &eventbridge.PutEventsOutput{}})
	if err := publisher.Publish(context.Background(), outbox.Event{Data: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("Publish() of an event with no id, type or time = nil")
	}
}

func TestPublisherRefusesToBuildWithoutASigningIdentity(t *testing.T) {
	if _, err := NewPublisher(&fakeEventBridge{}, "oryxa", nil); err == nil {
		t.Fatal("NewPublisher() without a signer = nil error")
	}
}

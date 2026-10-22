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

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
)

type fakeEventBridge struct {
	input *eventbridge.PutEventsInput
	err   error
}

func (fake *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	fake.input = input
	return &eventbridge.PutEventsOutput{}, fake.err
}

// An event goes out as oryxa.integrations, in the envelope, signed so that a
// consumer trusting the service's key verifies it.
func TestPublisherPutsTheEventOnTheBusSigned(t *testing.T) {
	signer, key, err := eventauth.NewLocalSigner("integrations", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeEventBridge{}
	publisher, err := NewPublisher(client, "oryxa", signer)
	if err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"connection_id":"cn_1"}`)
	at := time.Date(2026, time.October, 22, 10, 5, 0, 0, time.UTC)
	if err := publisher.Publish(context.Background(), outbox.Event{
		ID: "ev_1", DetailType: integrationsv1.ConnectionCreatedDetailType, Data: data, OccurredAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	entry := client.input.Entries[0]
	if aws.ToString(entry.Source) != "oryxa.integrations" || aws.ToString(entry.DetailType) != integrationsv1.ConnectionCreatedDetailType {
		t.Fatalf("entry = %+v", entry)
	}
	var detail struct {
		OccurredAt string          `json:"occurred_at"`
		Data       json.RawMessage `json:"data"`
		Sig        string          `json:"sig"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail); err != nil {
		t.Fatal(err)
	}
	if err := eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"integrations": key}, nil, time.Now).Verify(
		integrationsv1.Source, integrationsv1.ConnectionCreatedDetailType, "ev_1", 1, detail.OccurredAt, detail.Data, detail.Sig); err != nil {
		t.Fatalf("a consumer trusting integrations refused its event: %v", err)
	}
}

func TestPublisherRefusesToRunUnsignedAndReportsABusError(t *testing.T) {
	if _, err := NewPublisher(&fakeEventBridge{}, "oryxa", nil); err == nil {
		t.Fatal("a publisher without a signer was built")
	}
	signer, _, _ := eventauth.NewLocalSigner("integrations", time.Now)
	publisher, _ := NewPublisher(&fakeEventBridge{err: errors.New("down")}, "oryxa", signer)
	err := publisher.Publish(context.Background(), outbox.Event{ID: "ev_1", DetailType: "x", Data: json.RawMessage(`{}`), OccurredAt: time.Now()})
	if err == nil {
		t.Fatal("a bus error was swallowed")
	}
}

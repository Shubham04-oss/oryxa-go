package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
)

type fakeEventBridge struct{ input *eventbridge.PutEventsInput }

func (f *fakeEventBridge) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.input = input
	return &eventbridge.PutEventsOutput{}, nil
}

func TestPublisherUsesDomainEnvelopeAndOrganization(t *testing.T) {
	t.Parallel()
	client := &fakeEventBridge{}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	err := (Publisher{Client: client, BusName: "oryxa"}).PublishEvent(context.Background(), domain.Event{EventID: "evt-1", Source: "oryxa.integrations", DetailType: "integrations.slack.message-received.v1", Version: 1, OrganizationID: "org-1", OccurredAt: now, Data: json.RawMessage(`{"trigger_id":"trg-1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if client.input == nil || len(client.input.Entries) != 1 {
		t.Fatal("event was not published")
	}
	entry := client.input.Entries[0]
	var body map[string]any
	if err := json.Unmarshal([]byte(*entry.Detail), &body); err != nil {
		t.Fatal(err)
	}
	if body["organization_id"] != "org-1" || body["event_id"] != "evt-1" || *entry.Source != "oryxa.integrations" {
		t.Fatalf("event entry/body = %+v/%+v", entry, body)
	}
}

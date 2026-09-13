package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

type webhookProcessor struct {
	envelope webhooks.Envelope
	err      error
}

func (p *webhookProcessor) ProcessWebhook(_ context.Context, envelope webhooks.Envelope) error {
	p.envelope = envelope
	return p.err
}

func TestConsumerDecodesQueuedWebhookEnvelope(t *testing.T) {
	t.Parallel()
	processor := &webhookProcessor{}
	consumer := Consumer{Processor: processor}
	err := consumer.HandleMessage(context.Background(), `{"invocation_id":"whi-1","organization_id":"org-1","profile_id":"app-1","piece_name":"piece","piece_version":"1","trigger_name":"hook","identifier_value":"tenant"}`)
	if err != nil || processor.envelope.OrganizationID != "org-1" || processor.envelope.IdentifierValue != "tenant" {
		t.Fatalf("error/envelope = %v/%+v", err, processor.envelope)
	}
}

func TestConsumerLeavesProcessingFailureForSQSRedrive(t *testing.T) {
	t.Parallel()
	processor := &webhookProcessor{err: errors.New("piece failed")}
	err := (Consumer{Processor: processor}).HandleMessage(context.Background(), `{"invocation_id":"whi-1"}`)
	if err == nil {
		t.Fatal("expected processing failure")
	}
}

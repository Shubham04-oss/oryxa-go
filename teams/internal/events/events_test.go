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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

// withTracing registers a real TracerProvider (recorded by the returned
// SpanRecorder) and the W3C propagator as the global ones, mirroring what
// observability.Setup does in production, and restores whatever was global
// before so this test cannot leak state into another one.
func withTracing(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	previousTracer := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		otel.SetTracerProvider(previousTracer)
		otel.SetTextMapPropagator(previousPropagator)
	})
	return recorder
}

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

func TestPublisherAddsTraceparentToTheEnvelopeAndEndsAProducerSpan(t *testing.T) {
	recorder := withTracing(t)
	ctx, writeSpan := observability.Tracer().Start(context.Background(), "write")
	writtenTraceID := writeSpan.SpanContext().TraceID()

	client := &fakeEventBridge{output: &eventbridge.PutEventsOutput{}}
	err := NewPublisher(client, "oryxa").PublishEvent(ctx, domain.Event{
		ID: "evt_1", DetailType: "teams.x.v1", OccurredAt: time.Now(), Data: json.RawMessage(`{}`),
		// TraceContext round-trips what Table.Claim would have read back from
		// the outbox row; observability.CaptureTraceCarrier(ctx).Encode() is
		// the production caller (see insertEvents), exercised in the postgres
		// package's own tests.
		TraceContext: observability.CaptureTraceCarrier(ctx).Encode(),
	})
	writeSpan.End()
	if err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}

	var detail struct {
		observability.TraceCarrier
	}
	if err := json.Unmarshal([]byte(aws.ToString(client.input.Entries[0].Detail)), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.TraceParent == "" {
		t.Fatal("published envelope has no traceparent")
	}

	producer := findSpanNamed(t, recorder, "publish oryxa")
	if len(producer.Links()) != 1 || producer.Links()[0].SpanContext.TraceID() != writtenTraceID {
		t.Fatalf("producer span links = %+v, want a link to the write's trace %s", producer.Links(), writtenTraceID)
	}
}

// findSpanNamed fails the test unless recorder recorded exactly one ended
// span with this name. The helper spans these tests create to stand in for a
// caller's own span (named "write" or "publish", never "publish <bus>" or
// "process <queue>") are recorded on the same global provider, so a test
// cannot just assert on the total count.
func findSpanNamed(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var matches []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			matches = append(matches, span)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("spans named %q = %d, want 1 (all ended spans: %+v)", name, len(matches), recorder.Ended())
	}
	return matches[0]
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

func TestConsumerBecomesAChildOfThePublishedTrace(t *testing.T) {
	recorder := withTracing(t)
	ctx, producerSpan := observability.Tracer().Start(context.Background(), "publish")
	producerSpanContext := producerSpan.SpanContext()
	carrier := observability.CaptureTraceCarrier(ctx)
	producerSpan.End()

	body := fmt.Sprintf(
		`{"id":"eb-1","detail-type":%q,"source":"oryxa.auth","detail":{"event_id":"evt_auth_1","version":1,"occurred_at":"2026-09-13T12:00:00Z","data":{"organization_id":"org_1","name":"Acme","slug":"acme","created_by":"user-1","created_at":"2026-09-13T12:00:00Z"},"traceparent":%q}}`,
		autheventsv1.OrganizationCreatedDetailType, carrier.TraceParent)

	consumer := NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler))
	if err := consumer.Handle(context.Background(), body); err != nil {
		t.Fatal(err)
	}

	got := findSpanNamed(t, recorder, "process ")
	if got.Parent().TraceID() != producerSpanContext.TraceID() || got.Parent().SpanID() != producerSpanContext.SpanID() {
		t.Fatalf("consumer span parent = %+v, want the producer's span %+v", got.Parent(), producerSpanContext)
	}
}

func TestConsumerProcessesAMessageWithNoCarrierUnderAFreshSpan(t *testing.T) {
	recorder := withTracing(t)
	consumer := NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler))
	// authEvent (defined above) carries no traceparent: this is the ordinary
	// shape for a message published before this rollout, or from an
	// AWS-native source.
	if err := consumer.Handle(context.Background(), authEvent(autheventsv1.OrganizationCreatedDetailType, 1, `{"organization_id":"org_1","name":"Acme","slug":"acme","created_by":"user-1","created_at":"2026-09-13T12:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if spans[0].Parent().IsValid() {
		t.Fatalf("parent = %+v, want an invalid (root) parent for a message with no carrier", spans[0].Parent())
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

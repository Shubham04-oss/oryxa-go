package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// withRecordedSpans registers a real TracerProvider as the global one for the
// duration of the test, so Tracer() (which every helper in this package
// starts spans on) produces spans a test can inspect, and restores whatever
// was global before, so this test cannot leak state into another one.
func withRecordedSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	otel.SetTracerProvider(tracerProvider)
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	return recorder
}

func TestStartProducerSpanLinksRatherThanParentsTheStoredContext(t *testing.T) {
	recorder := withRecordedSpans(t)
	writeCtx := sampledContext(t)
	carrier := CaptureTraceCarrier(writeCtx)
	written := oteltrace.SpanContextFromContext(writeCtx)

	ctx, span := StartProducerSpan(context.Background(), carrier, "agentic-collaboration-bus", "teams.team.created.v1", "evt_1")
	span.End()

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	got := spans[0]
	if got.SpanKind() != oteltrace.SpanKindProducer {
		t.Fatalf("span kind = %s, want producer", got.SpanKind())
	}
	if got.Name() != "publish agentic-collaboration-bus" {
		t.Fatalf("span name = %q", got.Name())
	}
	if got.Parent().IsValid() {
		t.Fatalf("producer span has parent %+v, want a root span linking to the write instead", got.Parent())
	}
	links := got.Links()
	if len(links) != 1 || links[0].SpanContext.TraceID() != written.TraceID() || links[0].SpanContext.SpanID() != written.SpanID() {
		t.Fatalf("links = %+v, want one link to the write's span %+v", links, written)
	}
	// The returned context/span should be usable as a normal span: it must
	// at least carry itself.
	if oteltrace.SpanContextFromContext(ctx).SpanID() != span.SpanContext().SpanID() {
		t.Fatal("StartProducerSpan's context does not carry the span it returned")
	}
}

func TestStartProducerSpanOfAnEmptyCarrierHasNoLink(t *testing.T) {
	recorder := withRecordedSpans(t)
	_, span := StartProducerSpan(context.Background(), TraceCarrier{}, "bus", "detail.v1", "evt_1")
	span.End()
	if links := recorder.Ended()[0].Links(); len(links) != 0 {
		t.Fatalf("links = %+v, want none for an empty carrier", links)
	}
}

func TestStartConsumerSpanIsAChildOfTheCarrier(t *testing.T) {
	recorder := withRecordedSpans(t)
	produceCtx := sampledContext(t)
	produced := oteltrace.SpanContextFromContext(produceCtx)
	carrier := CaptureTraceCarrier(produceCtx)

	_, span := StartConsumerSpan(context.Background(), carrier, "teams-events", "teams.team.created.v1", "evt_1")
	span.End()

	got := recorder.Ended()[0]
	if got.SpanKind() != oteltrace.SpanKindConsumer {
		t.Fatalf("span kind = %s, want consumer", got.SpanKind())
	}
	if got.Name() != "process teams-events" {
		t.Fatalf("span name = %q", got.Name())
	}
	if got.Parent().TraceID() != produced.TraceID() || got.Parent().SpanID() != produced.SpanID() {
		t.Fatalf("consumer span parent = %+v, want the producer's span %+v", got.Parent(), produced)
	}
}

func TestStartConsumerSpanOfAnEmptyCarrierIsARoot(t *testing.T) {
	recorder := withRecordedSpans(t)
	_, span := StartConsumerSpan(context.Background(), TraceCarrier{}, "queue", "detail.v1", "evt_1")
	span.End()
	if parent := recorder.Ended()[0].Parent(); parent.IsValid() {
		t.Fatalf("parent = %+v, want an invalid (root) parent for a message with no carrier", parent)
	}
}

func TestEndSpanRecordsErrorStatusOnlyOnFailure(t *testing.T) {
	recorder := withRecordedSpans(t)
	_, span := Tracer().Start(context.Background(), "ok")
	EndSpan(span, nil)
	_, failing := Tracer().Start(context.Background(), "failing")
	EndSpan(failing, errors.New("boom"))

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	if status := spans[0].Status(); status.Code == codes.Error {
		t.Fatalf("successful span status = %+v, want no error", status)
	}
	if status := spans[1].Status(); status.Code != codes.Error || status.Description != "boom" {
		t.Fatalf("failed span status = %+v, want error \"boom\"", status)
	}
}

func TestQueueName(t *testing.T) {
	cases := map[string]string{
		"https://sqs.us-west-2.amazonaws.com/123456789012/teams-events": "teams-events",
		"teams-events": "teams-events",
		"":             "",
	}
	for url, want := range cases {
		if got := QueueName(url); got != want {
			t.Errorf("QueueName(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestOutboxMetricsRecordsDurationLagAndFailures(t *testing.T) {
	reader, meterProvider := manualMeterProvider(t)
	metrics, err := NewOutboxMetrics()
	if err != nil {
		t.Fatal(err)
	}

	occurredAt := time.Now().Add(-time.Second)
	metrics.Record(context.Background(), "teams_outbox", "teams.team.created.v1", occurredAt, time.Now().Add(-10*time.Millisecond), nil)
	metrics.Record(context.Background(), "teams_outbox", "teams.team.created.v1", occurredAt, time.Now().Add(-10*time.Millisecond), errors.New("boom"))

	data := collect(t, reader)
	assertHistogramRecorded(t, data, "oryxa.outbox.publish.duration", 2)
	assertHistogramRecorded(t, data, "oryxa.outbox.publish.lag", 2)
	assertCounterTotal(t, data, "oryxa.outbox.publish.failures", 1)
	_ = meterProvider
}

func TestConsumerMetricsRecordsDurationLagOutcomeAndFailures(t *testing.T) {
	reader, _ := manualMeterProvider(t)
	metrics, err := NewConsumerMetrics()
	if err != nil {
		t.Fatal(err)
	}

	occurredAt := time.Now().Add(-2 * time.Second)
	receivedAt := time.Now().Add(-time.Second)
	metrics.Record(context.Background(), "teams", "teams.team.created.v1", occurredAt, receivedAt, time.Now(), nil)
	metrics.Record(context.Background(), "teams", "teams.team.created.v1", occurredAt, receivedAt, time.Now(), errors.New("boom"))

	data := collect(t, reader)
	assertHistogramRecorded(t, data, "oryxa.consumer.process.duration", 2)
	assertHistogramRecorded(t, data, "oryxa.consumer.delivery.lag", 2)
	assertCounterTotal(t, data, "oryxa.consumer.process.failures", 1)
}

func TestOutboxAndConsumerMetricsAreNoOpsOnTheZeroValue(t *testing.T) {
	// A Dispatcher or Consumer that could not build its metrics falls back to
	// the zero value; recording on it must not panic.
	var outboxMetrics OutboxMetrics
	outboxMetrics.Record(context.Background(), "teams_outbox", "teams.team.created.v1", time.Now(), time.Now(), errors.New("boom"))
	var consumerMetrics ConsumerMetrics
	consumerMetrics.Record(context.Background(), "teams", "teams.team.created.v1", time.Now(), time.Now(), time.Now(), errors.New("boom"))
}

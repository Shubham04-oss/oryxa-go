package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// withPropagator registers the same composite propagator Setup does as the
// global one, so CaptureTraceCarrier, TraceCarrier.ContextWith and Transport
// have something to inject into and extract from. Setup itself is not called
// here: it also wires up exporters, which need a live OTLP endpoint these
// unit tests have none of. It restores whatever was global before, so this
// test cannot leak state into another one.
func withPropagator(t *testing.T) {
	t.Helper()
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
}

// sampledContext returns a context holding a real, valid, sampled span from a
// tracer of its own, so a test does not depend on whether some other test in
// this package has already registered a global TracerProvider. It also
// registers the propagator (see withPropagator): a span with nothing to
// inject it with is rarely what a test asking for one wants.
func sampledContext(t *testing.T) context.Context {
	t.Helper()
	withPropagator(t)
	tracerProvider := trace.NewTracerProvider(trace.WithSampler(trace.AlwaysSample()))
	t.Cleanup(func() { _ = tracerProvider.Shutdown(context.Background()) })
	ctx, span := tracerProvider.Tracer("test").Start(context.Background(), "test-span")
	t.Cleanup(func() { span.End() })
	return ctx
}

func TestCaptureTraceCarrierRoundTrips(t *testing.T) {
	ctx := sampledContext(t)
	original := oteltrace.SpanContextFromContext(ctx)

	carrier := CaptureTraceCarrier(ctx)
	if carrier.Empty() {
		t.Fatal("CaptureTraceCarrier of a sampled span returned an empty carrier")
	}

	encoded := carrier.Encode()
	if encoded == "" {
		t.Fatal(`Encode of a non-empty carrier returned ""`)
	}
	decoded := DecodeTraceCarrier(encoded)
	if decoded != carrier {
		t.Fatalf("decoded carrier = %+v, want %+v", decoded, carrier)
	}

	spanContext, ok := decoded.SpanContext()
	if !ok || !spanContext.IsValid() {
		t.Fatalf("SpanContext() = %+v, %v, want the original span's valid context", spanContext, ok)
	}
	if spanContext.TraceID() != original.TraceID() || spanContext.SpanID() != original.SpanID() {
		t.Fatalf("round-tripped span context = %+v, want trace/span ids from %+v", spanContext, original)
	}

	extracted := decoded.ContextWith(context.Background())
	if got := oteltrace.SpanContextFromContext(extracted); got.TraceID() != original.TraceID() {
		t.Fatalf("ContextWith trace id = %s, want %s", got.TraceID(), original.TraceID())
	}
}

type contextKey struct{}

func TestCaptureTraceCarrierOfAContextWithoutASpanIsEmpty(t *testing.T) {
	carrier := CaptureTraceCarrier(context.Background())
	if !carrier.Empty() {
		t.Fatalf("carrier of a plain context = %+v, want empty", carrier)
	}
	if carrier.Encode() != "" {
		t.Fatalf("Encode of an empty carrier = %q, want \"\"", carrier.Encode())
	}
	if _, ok := carrier.SpanContext(); ok {
		t.Fatal("SpanContext of an empty carrier reported ok")
	}
	// ContextWith on an empty carrier must return ctx unchanged, so a message
	// with no carrier (in flight during the rollout, or from an AWS-native
	// source) still processes rather than failing.
	ctx := context.WithValue(context.Background(), contextKey{}, "marker")
	if got := carrier.ContextWith(ctx); got.Value(contextKey{}) != "marker" {
		t.Fatal("ContextWith on an empty carrier did not return ctx unchanged")
	}
}

func TestDecodeTraceCarrierOfGarbageIsEmpty(t *testing.T) {
	for _, value := range []string{"", "not json", `{"traceparent": 5}`, "null"} {
		if carrier := DecodeTraceCarrier(value); !carrier.Empty() {
			t.Errorf("DecodeTraceCarrier(%q) = %+v, want empty", value, carrier)
		}
	}
}

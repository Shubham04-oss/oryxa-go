package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// sqsReceiveMessageAttrs are the attributes otelaws v0.71.0 starts its SQS
// ReceiveMessage client span with (see attributes.go and aws.go in that
// module: SystemAttr, MethodAttr(serviceID, operation), RegionAttr).
var sqsReceiveMessageAttrs = []attribute.KeyValue{
	rpcSystemNameKey.String("aws-api"),
	rpcMethodKey.String("SQS/ReceiveMessage"),
	attribute.Key("aws.region").String("us-west-2"),
}

// sampledTracerProvider builds a TracerProvider on Sampler(), recorded by
// the returned SpanRecorder, and arranges for it to shut down at the end of
// the test.
func sampledTracerProvider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSampler(Sampler()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = tracerProvider.Shutdown(context.Background()) })
	return tracerProvider, recorder
}

func TestSamplerDropsARootSQSReceiveMessageSpan(t *testing.T) {
	tracerProvider, recorder := sampledTracerProvider(t)

	_, span := tracerProvider.Tracer("otelaws-test").Start(context.Background(), "SQS.ReceiveMessage",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient), oteltrace.WithAttributes(sqsReceiveMessageAttrs...))
	span.End()

	if len(recorder.Started()) != 0 || len(recorder.Ended()) != 0 {
		t.Fatalf("a dropped span reached the processor: started=%d ended=%d", len(recorder.Started()), len(recorder.Ended()))
	}
	if span.SpanContext().IsSampled() {
		t.Fatal("a dropped root span reports itself as sampled")
	}
}

func TestSamplerRecordsAnSQSReceiveMessageSpanThatHasAParent(t *testing.T) {
	tracerProvider, recorder := sampledTracerProvider(t)
	tracer := tracerProvider.Tracer("test")

	parentCtx, parent := tracer.Start(context.Background(), "caller")
	_, child := tracer.Start(parentCtx, "SQS.ReceiveMessage",
		oteltrace.WithSpanKind(oteltrace.SpanKindClient), oteltrace.WithAttributes(sqsReceiveMessageAttrs...))
	child.End()
	parent.End()

	if len(recorder.Ended()) != 2 {
		t.Fatalf("ended spans = %d, want 2 (the parent and the non-root ReceiveMessage span)", len(recorder.Ended()))
	}
}

func TestSamplerRecordsAnyOtherRootSpan(t *testing.T) {
	tracerProvider, recorder := sampledTracerProvider(t)

	// Same rpc.system.name, a different AWS operation: only the exact
	// ReceiveMessage method is dropped.
	_, other := tracerProvider.Tracer("test").Start(context.Background(), "SQS.SendMessage",
		oteltrace.WithAttributes(rpcSystemNameKey.String("aws-api"), rpcMethodKey.String("SQS/SendMessage")))
	other.End()

	_, plain := tracerProvider.Tracer("test").Start(context.Background(), "unrelated")
	plain.End()

	if len(recorder.Ended()) != 2 {
		t.Fatalf("ended spans = %d, want 2 (both root spans are not ReceiveMessage)", len(recorder.Ended()))
	}
}

func TestSamplerLeavesConsumerAndProducerSpansAlone(t *testing.T) {
	// Regression check for the actual thing this package's messaging spans
	// need: Sampler must not treat a producer or consumer span as the thing
	// it filters, whether or not it is a root.
	tracerProvider, recorder := sampledTracerProvider(t)
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tracerProvider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	_, producerSpan := StartProducerSpan(context.Background(), TraceCarrier{}, "bus", "detail.v1", "evt_1")
	producerSpan.End()
	_, consumerSpan := StartConsumerSpan(context.Background(), TraceCarrier{}, "queue", "detail.v1", "evt_1")
	consumerSpan.End()

	if len(recorder.Ended()) != 2 {
		t.Fatalf("ended spans = %d, want 2 (the producer and consumer root spans, both kept)", len(recorder.Ended()))
	}
}

func TestRootSamplerFromEnv(t *testing.T) {
	cases := map[string]struct {
		sampler string
		arg     string
		want    string
	}{
		"unset":                  {want: "AlwaysOnSampler"},
		"always_on":              {sampler: "always_on", want: "AlwaysOnSampler"},
		"always_off":             {sampler: "always_off", want: "AlwaysOffSampler"},
		"parentbased_always_off": {sampler: "parentbased_always_off", want: "AlwaysOffSampler"},
		"traceidratio":           {sampler: "traceidratio", arg: "0.5", want: "TraceIDRatioBased{0.5}"},
		"traceidratio invalid":   {sampler: "traceidratio", arg: "not-a-number", want: "TraceIDRatioBased{1}"},
		"unrecognized":           {sampler: "not-a-real-sampler", want: "AlwaysOnSampler"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER", test.sampler)
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", test.arg)
			if got := rootSamplerFromEnv().Description(); got != test.want {
				t.Errorf("rootSamplerFromEnv().Description() = %q, want %q", got, test.want)
			}
		})
	}
}

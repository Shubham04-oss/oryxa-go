package observability

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestNoAgentRequestsContentLeavesInATrace: an agent request's tool calls,
// traced the way ADK traces every tool call, go out with their arguments and
// responses redacted, and so does a span merging several calls one of them
// is among; any other tool's span goes out as it is.
func TestNoAgentRequestsContentLeavesInATrace(t *testing.T) {
	exported := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(RedactToolCalls(exported)))
	tracer := provider.Tracer("test")
	toolSpan := func(tool, args, response string) {
		_, span := tracer.Start(context.Background(), "execute_tool "+tool, trace.WithAttributes(
			adkToolNameKey.String(tool), adkToolArgsKey.String(args)))
		span.SetAttributes(adkToolResponseKey.String(response))
		span.End()
	}
	toolSpan("interactions_respond", `{"summary":"Priya is at priya@example.com."}`, `{"status":"review"}`)
	toolSpan("interactions_get", `{"request_id":"irq_1"}`, `{"held":{"result":{"summary":"Call +1 415 555 0100."}}}`)
	toolSpan("(merged tools)", "N/A", `{"Content":{"parts":[{"functionResponse":{"name":"interactions_get","response":{"summary":"priya@example.com"}}}]}}`)
	toolSpan("apps_run_read_tool", `{"toolkit":"gmail"}`, `{"subject":"Launch"}`)
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exported.GetSpans()
	if len(spans) != 4 {
		t.Fatalf("exported %d spans, want 4", len(spans))
	}
	for _, span := range spans {
		values := map[attribute.Key]string{}
		for _, value := range span.Attributes {
			values[value.Key] = value.Value.AsString()
		}
		if span.Name == "execute_tool apps_run_read_tool" {
			if values[adkToolArgsKey] != `{"toolkit":"gmail"}` || values[adkToolResponseKey] != `{"subject":"Launch"}` {
				t.Errorf("another tool's span lost what it carried: %v", values)
			}
			continue
		}
		for _, key := range []attribute.Key{adkToolArgsKey, adkToolResponseKey} {
			if values[key] != redacted {
				t.Errorf("%s carries %s = %q, want it redacted", span.Name, key, values[key])
			}
		}
		for _, secret := range []string{"priya", "415 555"} {
			for _, value := range values {
				if strings.Contains(value, secret) {
					t.Errorf("%s carries %q", span.Name, value)
				}
			}
		}
	}
}

package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestTracingHandlerAddsTraceFieldsWhenTheContextCarriesASpan(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(tracingHandler{next: slog.NewJSONHandler(&buffer, nil)})

	ctx := sampledContext(t)
	spanContext := oteltrace.SpanContextFromContext(ctx)
	logger.InfoContext(ctx, "hello")

	var line map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &line); err != nil {
		t.Fatalf("log line was not valid JSON: %v (%s)", err, buffer.String())
	}
	if line["trace_id"] != spanContext.TraceID().String() {
		t.Errorf("trace_id = %v, want %s", line["trace_id"], spanContext.TraceID())
	}
	if line["span_id"] != spanContext.SpanID().String() {
		t.Errorf("span_id = %v, want %s", line["span_id"], spanContext.SpanID())
	}
	wantXRay := "1-" + spanContext.TraceID().String()[:8] + "-" + spanContext.TraceID().String()[8:]
	if line["xray_trace_id"] != wantXRay {
		t.Errorf("xray_trace_id = %v, want %s", line["xray_trace_id"], wantXRay)
	}
}

func TestTracingHandlerAddsNothingWithoutASpan(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(tracingHandler{next: slog.NewJSONHandler(&buffer, nil)})

	logger.InfoContext(context.Background(), "hello")

	var line map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &line); err != nil {
		t.Fatalf("log line was not valid JSON: %v (%s)", err, buffer.String())
	}
	for _, key := range []string{"trace_id", "span_id", "xray_trace_id"} {
		if _, present := line[key]; present {
			t.Errorf("log line has %s = %v with no span in context, want it absent", key, line[key])
		}
	}
}

func TestTracingHandlerPreservesWithAttrsAndWithGroup(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(tracingHandler{next: slog.NewJSONHandler(&buffer, nil)}).
		With("service", "teams").
		WithGroup("request")

	logger.InfoContext(context.Background(), "hello", "id", "abc")

	if output := buffer.String(); !strings.Contains(output, `"service":"teams"`) || !strings.Contains(output, `"request":{"id":"abc"}`) {
		t.Fatalf("log line = %s, want the bound attribute and the group preserved", output)
	}
}

func TestXRayTraceIDOfAMalformedIDIsEmpty(t *testing.T) {
	if got := xrayTraceID(oteltrace.TraceID{}); got != "" {
		t.Fatalf("xrayTraceID of an invalid (all-zero) trace id = %q, want \"\"", got)
	}
}

package observability

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ADK traces every tool call as an execute_tool span carrying the call's
// arguments and its response, whatever OTEL_INSTRUMENTATION_GENAI_CAPTURE_
// MESSAGE_CONTENT says (google.golang.org/adk/v2 internal/telemetry: the
// gate covers the model's prompts and answers, not tool calls). An agent
// request's tools carry what one member's agent tells another: its answers
// before the answer guard reads them, and what the guard held back. Those
// must not leave the process in a trace.
const (
	adkToolNameKey     = attribute.Key("gen_ai.tool.name")
	adkToolArgsKey     = attribute.Key("gcp.vertex.agent.tool_call_args")
	adkToolResponseKey = attribute.Key("gcp.vertex.agent.tool_response")

	// redactedToolPrefix names the tools whose calls are never exported:
	// the interactions toolbox's.
	redactedToolPrefix = "interactions_"
	// redacted is what an export carries in place of such a call's
	// arguments and response.
	redacted = "[redacted: an agent request's content]"
)

// RedactToolCalls wraps exporter so that no span it exports carries the
// arguments or response of an agent request's tools: an execute_tool span
// of an interactions_ tool, and a span of merged tool calls one of them is
// among, keep their name, status and timing, with both attributes redacted.
// Every other span goes out as it is.
func RedactToolCalls(exporter sdktrace.SpanExporter) sdktrace.SpanExporter {
	return redactingExporter{SpanExporter: exporter}
}

type redactingExporter struct{ sdktrace.SpanExporter }

func (exporter redactingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	exported := make([]sdktrace.ReadOnlySpan, len(spans))
	for index, span := range spans {
		exported[index] = redactSpan(span)
	}
	return exporter.SpanExporter.ExportSpans(ctx, exported)
}

// redactedSpan is a span with its attributes replaced.
type redactedSpan struct {
	sdktrace.ReadOnlySpan
	attributes []attribute.KeyValue
}

func (span redactedSpan) Attributes() []attribute.KeyValue { return span.attributes }

func redactSpan(span sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	attributes := span.Attributes()
	if !carriesRedactedTool(attributes) {
		return span
	}
	cleaned := make([]attribute.KeyValue, len(attributes))
	for index, value := range attributes {
		if value.Key == adkToolArgsKey || value.Key == adkToolResponseKey {
			value = value.Key.String(redacted)
		}
		cleaned[index] = value
	}
	return redactedSpan{ReadOnlySpan: span, attributes: cleaned}
}

// carriesRedactedTool is whether a span is one of an agent request's tool
// calls: named for one, or merging several of which one is, which its
// response names.
func carriesRedactedTool(attributes []attribute.KeyValue) bool {
	for _, value := range attributes {
		switch value.Key {
		case adkToolNameKey:
			if strings.HasPrefix(value.Value.AsString(), redactedToolPrefix) {
				return true
			}
		case adkToolResponseKey:
			if strings.Contains(value.Value.AsString(), `"`+redactedToolPrefix) {
				return true
			}
		}
	}
	return false
}

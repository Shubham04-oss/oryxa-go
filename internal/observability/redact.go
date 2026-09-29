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
// gate covers the model's prompts and answers, not tool calls), and, when the
// call fails, the error's message on the span's status and its exception
// event. What a tool carries is a member's own content: the mail, messages,
// issues and pages of the apps they connected, the documents of their
// knowledge bases, what a browser read, and what one member's agent tells
// another, an answer before the answer guard reads it and what the guard held
// back. None of it may leave the process in a trace, which goes on to X-Ray.
//
// So the rule is not a list of tools whose calls are private. Every tool
// span goes out without its content, and keeps only what an allowlist says
// holds none: a tool added tomorrow, or an attribute ADK adds to its spans,
// is redacted until someone reads it and says otherwise.
const (
	// ADK's names for what a tool span carries.
	adkOperationKey    = attribute.Key("gen_ai.operation.name")
	adkToolNameKey     = attribute.Key("gen_ai.tool.name")
	adkToolCallIDKey   = attribute.Key("gen_ai.tool.call.id")
	adkEventIDKey      = attribute.Key("gcp.vertex.agent.event_id")
	adkToolArgsKey     = attribute.Key("gcp.vertex.agent.tool_call_args")
	adkToolResponseKey = attribute.Key("gcp.vertex.agent.tool_response")
	// exceptionTypeKey is the kind of an error on an exception event, a Go
	// type's name and not the error's message.
	exceptionTypeKey = attribute.Key("exception.type")

	// toolSpanPrefix begins the name of every tool span, the span of one call
	// and the one that merges parallel calls alike, and toolOperation is the
	// operation both name.
	toolSpanPrefix = "execute_tool"
	toolOperation  = "execute_tool"

	// toolArgsSizeKey and toolResponseSizeKey say how many bytes the
	// arguments and the response held, in place of what they held.
	toolArgsSizeKey     = attribute.Key("oryxa.tool.call_args.size")
	toolResponseSizeKey = attribute.Key("oryxa.tool.response.size")

	// redacted is what an export carries in place of a tool call's content.
	redacted = "[redacted: a tool call's content]"
)

// keptOnToolSpans is every attribute a tool span, and an event on it, keeps
// as it is: the names and ids ADK and the model mint, and the kind of an
// error. None is text that a tool or a member wrote. An attribute joins it
// only when it holds no member content, whatever the tool did.
var keptOnToolSpans = map[attribute.Key]struct{}{
	adkOperationKey:  {},
	adkToolNameKey:   {},
	adkToolCallIDKey: {},
	adkEventIDKey:    {},
	exceptionTypeKey: {},
}

// RedactToolCalls wraps exporter so that no tool span it exports carries what
// the call held. The arguments and the response are replaced by a marker,
// with how many bytes each held beside it, and so is every attribute the
// allowlist does not name. An error's message is replaced too, on the span's
// status and on its exception events, and its type is kept. A tool span keeps
// its name, its status, its timing and its ids. Every other span goes out as
// it is.
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

// redactedSpan is a span with what its call held replaced.
type redactedSpan struct {
	sdktrace.ReadOnlySpan
	attributes []attribute.KeyValue
	events     []sdktrace.Event
	status     sdktrace.Status
}

func (span redactedSpan) Attributes() []attribute.KeyValue { return span.attributes }
func (span redactedSpan) Events() []sdktrace.Event         { return span.events }
func (span redactedSpan) Status() sdktrace.Status          { return span.status }

func redactSpan(span sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	attributes := span.Attributes()
	if !isToolSpan(span.Name(), attributes) {
		return span
	}
	events := span.Events()
	redactedEvents := make([]sdktrace.Event, len(events))
	for index, event := range events {
		event.Attributes = redactAttributes(event.Attributes)
		redactedEvents[index] = event
	}
	status := span.Status()
	if status.Description != "" {
		// A failed call's error, which can quote what the call read or was
		// sent, is the description ADK sets.
		status.Description = redacted
	}
	return redactedSpan{
		ReadOnlySpan: span,
		attributes:   redactAttributes(attributes),
		events:       redactedEvents,
		status:       status,
	}
}

// redactAttributes keeps the attributes the allowlist names and replaces the
// value of every other with the marker, saying how many bytes the arguments
// and the response held.
func redactAttributes(attributes []attribute.KeyValue) []attribute.KeyValue {
	cleaned := make([]attribute.KeyValue, 0, len(attributes)+2)
	for _, value := range attributes {
		if _, kept := keptOnToolSpans[value.Key]; kept {
			cleaned = append(cleaned, value)
			continue
		}
		switch value.Key {
		case adkToolArgsKey:
			cleaned = append(cleaned, toolArgsSizeKey.Int(len(value.Value.Emit())))
		case adkToolResponseKey:
			cleaned = append(cleaned, toolResponseSizeKey.Int(len(value.Value.Emit())))
		}
		cleaned = append(cleaned, value.Key.String(redacted))
	}
	return cleaned
}

// isToolSpan is whether a span is a tool call's: named for one or naming one
// as its operation, which the span merging several calls does too, or
// carrying what only a tool call carries.
func isToolSpan(name string, attributes []attribute.KeyValue) bool {
	if strings.HasPrefix(name, toolSpanPrefix) {
		return true
	}
	for _, value := range attributes {
		switch value.Key {
		case adkOperationKey:
			if value.Value.AsString() == toolOperation {
				return true
			}
		case adkToolNameKey, adkToolArgsKey, adkToolResponseKey:
			return true
		}
	}
	return false
}

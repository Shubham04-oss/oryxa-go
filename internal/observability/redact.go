package observability

import (
	"context"
	"net/url"
	"slices"
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

// A URL is not only an address. Its query string is what somebody typed into a
// search box or a filter: the directory's find_people, find_teams and
// find_agents put a member's words in ?q=, and the client span of that call
// carries the whole URL, so the words would reach X-Ray from a span that is no
// tool's. So every span, a tool's or not, goes out with the query and the
// fragment of each URL it carries left out, on the span and on its events.
// What stays is where the request went: the scheme, the host and the path.
const (
	// The attributes a URL comes in: the current names, and the older ones an
	// instrumentation may still write. http.target is a path with its query
	// and no host.
	urlFullKey    = attribute.Key("url.full")
	urlQueryKey   = attribute.Key("url.query")
	httpURLKey    = attribute.Key("http.url")
	httpTargetKey = attribute.Key("http.target")

	// redactedQuery is what an export carries in place of a URL's query, and
	// of a URL attribute that is not a URL.
	redactedQuery = "[redacted]"
)

// RedactToolCalls wraps exporter so that no tool span it exports carries what
// the call held. The arguments and the response are replaced by a marker,
// with how many bytes each held beside it, and so is every attribute the
// allowlist does not name. An error's message is replaced too, on the span's
// status and on its exception events, and its type is kept. A tool span keeps
// its name, its status, its timing and its ids. Every other span goes out as
// it is, save for the URLs on it and on its events: no span keeps a URL's
// query or its fragment, and a query reads [redacted]. A span with no such URL
// is exported itself, not a copy.
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

// redactedSpan is a span with its attributes, its events and its status
// replaced.
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
		return withoutQueries(span, attributes)
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

// withoutQueries is span with the query and the fragment of every URL it
// carries left out, on the span and on each of its events. It is span itself
// when none carries either.
func withoutQueries(span sdktrace.ReadOnlySpan, attributes []attribute.KeyValue) sdktrace.ReadOnlySpan {
	cleanedAttributes, changed := redactURLs(attributes)
	events := span.Events()
	var cleanedEvents []sdktrace.Event
	for index, event := range events {
		cleaned, eventChanged := redactURLs(event.Attributes)
		if !eventChanged {
			continue
		}
		if cleanedEvents == nil {
			cleanedEvents = slices.Clone(events)
		}
		event.Attributes = cleaned
		cleanedEvents[index] = event
	}
	if !changed && cleanedEvents == nil {
		return span
	}
	if cleanedEvents == nil {
		cleanedEvents = events
	}
	return redactedSpan{ReadOnlySpan: span, attributes: cleanedAttributes, events: cleanedEvents, status: span.Status()}
}

// redactURLs is attributes with each URL among them redacted, and whether any
// was changed. It is attributes itself, not a copy, when none was.
func redactURLs(attributes []attribute.KeyValue) ([]attribute.KeyValue, bool) {
	var cleaned []attribute.KeyValue
	for index, value := range attributes {
		replaced, changed := redactURLAttribute(value)
		if !changed {
			continue
		}
		if cleaned == nil {
			cleaned = slices.Clone(attributes)
		}
		cleaned[index] = replaced
	}
	if cleaned == nil {
		return attributes, false
	}
	return cleaned, true
}

// redactURLAttribute is value as it may be exported, and whether that is not
// value as it was. A URL keeps its scheme, host and path (redactURL); a query
// on its own is replaced whole, and so is a URL attribute of any type but a
// string.
func redactURLAttribute(value attribute.KeyValue) (attribute.KeyValue, bool) {
	var replacement string
	switch value.Key {
	case urlQueryKey:
		replacement = redactedQuery
	case urlFullKey, httpURLKey, httpTargetKey:
		if value.Value.Type() != attribute.STRING {
			replacement = redactedQuery
		} else {
			replacement = redactURL(value.Value.AsString())
		}
	default:
		return value, false
	}
	if value.Value.Type() == attribute.STRING && value.Value.AsString() == replacement {
		return value, false
	}
	return value.Key.String(replacement), true
}

// redactURL is rawURL as it may be exported: its scheme, host and path, with
// redactedQuery after a "?" where there was a query, and no userinfo and no
// fragment. It is rawURL itself when there was nothing to leave out. A value
// that is neither an absolute URL nor a path is not one to trust with a query
// in it, and comes back as redactedQuery whole, as does one that does not
// parse.
func redactURL(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return redactedQuery
	}
	if (parsed.Scheme == "" || parsed.Host == "") && !strings.HasPrefix(rawURL, "/") {
		return redactedQuery
	}
	hasQuery := parsed.RawQuery != "" || parsed.ForceQuery
	if !hasQuery && parsed.Fragment == "" && parsed.User == nil {
		return rawURL
	}
	kept := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path, RawPath: parsed.RawPath}
	if hasQuery {
		return kept.String() + "?" + redactedQuery
	}
	return kept.String()
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

package observability

import (
	"context"
	"encoding/json"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceCarrier is a W3C trace context lifted out of a context.Context so it
// can cross a boundary the registered propagator cannot reach directly: a
// PostgreSQL column, or a JSON envelope on the event bus. The zero value
// carries nothing, and every method treats that as "no context was
// captured" rather than an error, since most of what writes one runs
// unsampled, in a background worker, or on a path OTel does not instrument
// at all (an AWS-native source such as an EventBridge schedule or SES).
type TraceCarrier struct {
	TraceParent string `json:"traceparent,omitempty"`
	TraceState  string `json:"tracestate,omitempty"`
}

// CaptureTraceCarrier reads ctx's current span context through the process's
// configured propagator. It is what an outbox INSERT and a publisher call to
// attach the writing transaction's trace to what they store or send. It
// returns the zero TraceCarrier when ctx carries no valid span, so a caller
// can write NULL or omit the field rather than store an empty object.
func CaptureTraceCarrier(ctx context.Context) TraceCarrier {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return TraceCarrier{}
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return TraceCarrier{TraceParent: carrier.Get("traceparent"), TraceState: carrier.Get("tracestate")}
}

// Empty reports whether the carrier holds no context, which is the ordinary
// case for a message with nothing to link to a caller.
func (carrier TraceCarrier) Empty() bool { return carrier.TraceParent == "" }

// ContextWith returns ctx with the carrier's span context extracted into it
// as the remote context a new span should reference, or ctx unchanged when
// the carrier is empty. It never fails outright: an unparsable traceparent is
// the propagator's problem, and Extract already degrades to returning ctx
// unchanged in that case, exactly as it does for a request header it cannot
// parse.
func (carrier TraceCarrier) ContextWith(ctx context.Context) context.Context {
	if carrier.Empty() {
		return ctx
	}
	mapCarrier := propagation.MapCarrier{"traceparent": carrier.TraceParent}
	if carrier.TraceState != "" {
		mapCarrier["tracestate"] = carrier.TraceState
	}
	return otel.GetTextMapPropagator().Extract(ctx, mapCarrier)
}

// SpanContext resolves the carrier to the trace.SpanContext it encodes, for a
// caller that wants a link rather than a parent. It reports false for an
// empty or unparsable carrier.
func (carrier TraceCarrier) SpanContext() (trace.SpanContext, bool) {
	if carrier.Empty() {
		return trace.SpanContext{}, false
	}
	spanContext := trace.SpanContextFromContext(carrier.ContextWith(context.Background()))
	return spanContext, spanContext.IsValid()
}

// Encode is how a carrier is written to a single text column, such as an
// outbox row's trace_context, or reused as a value elsewhere that needs one
// string. It returns "" for an empty carrier, so the column is left NULL
// rather than holding an empty object.
func (carrier TraceCarrier) Encode() string {
	if carrier.Empty() {
		return ""
	}
	encoded, err := json.Marshal(carrier)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// DecodeTraceCarrier reverses Encode. A blank or unparsable value decodes to
// the empty carrier rather than an error, since both a NULL column from
// before this rollout and a corrupted one should simply leave the message to
// process under a fresh span.
func DecodeTraceCarrier(encoded string) TraceCarrier {
	if encoded == "" {
		return TraceCarrier{}
	}
	var carrier TraceCarrier
	if err := json.Unmarshal([]byte(encoded), &carrier); err != nil {
		return TraceCarrier{}
	}
	return carrier
}

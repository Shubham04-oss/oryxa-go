package observability

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// tracingHandler adds trace_id, span_id and xray_trace_id to every record
// logged with a context that carries a valid span, so a line found in
// CloudWatch Logs can be pasted into the X-Ray console's trace search, or
// correlated with the trace in Grafana or any other backend that reads W3C
// ids. A record logged with context.Background(), or with a context whose
// span was never sampled into existence, gets none of these fields: there is
// nothing to look up.
type tracingHandler struct {
	next slog.Handler
}

func (handler tracingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler tracingHandler) Handle(ctx context.Context, record slog.Record) error {
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", spanContext.TraceID().String()),
			slog.String("span_id", spanContext.SpanID().String()),
			slog.String("xray_trace_id", xrayTraceID(spanContext.TraceID())),
		)
	}
	return handler.next.Handle(ctx, record)
}

func (handler tracingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return tracingHandler{next: handler.next.WithAttrs(attrs)}
}

func (handler tracingHandler) WithGroup(name string) slog.Handler {
	return tracingHandler{next: handler.next.WithGroup(name)}
}

// xrayTraceID renders a W3C trace id in X-Ray's own "1-<8 hex>-<24 hex>"
// form, so a log line is findable from the X-Ray console's trace search box
// as-is. X-Ray's own id format embeds an epoch in those first 8 hex
// characters; the collector's awsxray exporter accepts a plain, random W3C id
// without that meaning (see the package doc), so this is a display
// convenience for the log, not a claim that those characters are a
// timestamp. It returns "" for an invalid (all-zero, or not the usual 16
// bytes) trace id, so a malformed id never gets stamped as if it were a real
// X-Ray one.
func xrayTraceID(id trace.TraceID) string {
	if !id.IsValid() {
		return ""
	}
	hex := id.String()
	if len(hex) != 32 {
		return ""
	}
	return "1-" + hex[:8] + "-" + hex[8:]
}

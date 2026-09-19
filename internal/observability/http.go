package observability

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
)

// unmatchedSpanName is the server span's name for a request no route
// claimed, such as a 404 or a client that hit the wrong method. It is one
// fixed string per service rather than "<service> <method>": the route is
// exactly what is missing, and a method varies too little to be worth the
// extra span-name cardinality.
const unmatchedSpanName = "unmatched"

func HTTPHandler(serviceName string, next http.Handler) http.Handler {
	accessLog := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := requestID(request.Context())
		writer.Header().Set("X-Request-ID", requestID)
		metrics := httpsnoop.CaptureMetrics(next, writer, request)
		// request.Pattern is set by the service's own ServeMux as it
		// dispatches to the leaf handler above, which runs inside
		// CaptureMetrics; it is only readable once that call returns. Until a
		// route matches, it is "", which withRoute and the log below both
		// treat as "unmatched" rather than as this request's identity.
		route := request.Pattern
		withRoute(request, route)
		logRoute := route
		if logRoute == "" {
			logRoute = unmatchedSpanName
		}
		if request.URL.Path == "/healthz" {
			return
		}
		attributes := []slog.Attr{
			slog.String("component", strings.TrimPrefix(serviceName, "oryxa-")),
			slog.String("request_id", requestID),
			slog.String("http.method", request.Method),
			slog.String("http.route", logRoute),
			slog.Int("http.status_code", metrics.Code),
			slog.Int64("http.response_bytes", metrics.Written),
			slog.Float64("duration_ms", float64(metrics.Duration)/float64(time.Millisecond)),
		}
		level := slog.LevelInfo
		if metrics.Code >= http.StatusInternalServerError {
			level = slog.LevelError
		} else if metrics.Code >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		// trace_id, span_id and xray_trace_id are added by tracingHandler
		// (see logging.go) for every context-aware log call, this one
		// included, so they are not repeated here.
		slog.LogAttrs(request.Context(), level, "http request completed", attributes...)
	})
	return otelhttp.NewHandler(accessLog, serviceName+".http",
		otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true }),
		otelhttp.WithFilter(func(request *http.Request) bool { return request.URL.Path != "/healthz" }),
		otelhttp.WithSpanNameFormatter(func(_ string, request *http.Request) string {
			// otelhttp calls this twice: once before the route is known, to
			// open the span, and again after the handler returns if
			// request.Pattern is then set, to rename it. The first call is
			// what makes an unmatched request's span keep this fixed name
			// rather than whatever it was opened with.
			if request.Pattern == "" {
				return serviceName + " " + unmatchedSpanName
			}
			return request.Method + " " + routePath(request.Pattern)
		}),
	)
}

// withRoute records the matched route on the request's server span and on
// otelhttp's metrics Labeler, once the service's own mux has set
// request.Pattern. otelhttp already derives http.route for its own server
// metrics from request.Pattern (see its internal semconv.MetricAttributes),
// so the Labeler call here is belt and suspenders against that becoming
// contingent behaviour in some other otelhttp version; the span attribute is
// not otherwise added, since otelhttp only recomputes trace attributes at
// span-start, before the route is known. An unmatched request (route == "")
// gets neither: there is no route to attribute it to, and the span's name
// already says so.
func withRoute(request *http.Request, route string) {
	if route == "" {
		return
	}
	path := routePath(route)
	span := trace.SpanFromContext(request.Context())
	span.SetAttributes(semconv.HTTPRoute(path))
	labeler, _ := otelhttp.LabelerFromContext(request.Context())
	labeler.Add(semconv.HTTPRoute(path))
}

// routePath strips a ServeMux pattern's leading method token, such as "GET ",
// leaving the path template a span name or route attribute reports. A
// pattern with no method (matching any) is returned unchanged.
func routePath(pattern string) string {
	if _, rest, found := strings.Cut(pattern, " "); found {
		return rest
	}
	return pattern
}

func requestID(ctx context.Context) string {
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		return spanContext.TraceID().String()
	}
	value := make([]byte, 12)
	if _, err := rand.Read(value); err == nil {
		return base64.RawURLEncoding.EncodeToString(value)
	}
	return "unavailable"
}

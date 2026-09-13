package observability

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"time"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

func HTTPHandler(serviceName string, next http.Handler) http.Handler {
	accessLog := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := requestID(request.Context())
		writer.Header().Set("X-Request-ID", requestID)
		metrics := httpsnoop.CaptureMetrics(next, writer, request)
		if request.URL.Path == "/healthz" {
			return
		}
		route := request.Pattern
		if route == "" {
			route = "unmatched"
		}
		attributes := []slog.Attr{
			slog.String("request_id", requestID),
			slog.String("http.method", request.Method),
			slog.String("http.route", route),
			slog.Int("http.status_code", metrics.Code),
			slog.Int64("http.response_bytes", metrics.Written),
			slog.Float64("duration_ms", float64(metrics.Duration)/float64(time.Millisecond)),
		}
		spanContext := trace.SpanContextFromContext(request.Context())
		if spanContext.IsValid() {
			attributes = append(attributes,
				slog.String("trace_id", spanContext.TraceID().String()),
				slog.String("span_id", spanContext.SpanID().String()),
			)
		}
		level := slog.LevelInfo
		if metrics.Code >= http.StatusInternalServerError {
			level = slog.LevelError
		} else if metrics.Code >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		slog.LogAttrs(request.Context(), level, "http request completed", attributes...)
	})
	return otelhttp.NewHandler(accessLog, serviceName+".http",
		otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true }),
		otelhttp.WithFilter(func(request *http.Request) bool { return request.URL.Path != "/healthz" }),
		otelhttp.WithSpanNameFormatter(func(_ string, request *http.Request) string {
			return serviceName + " " + request.Method
		}),
	)
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

package observability

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Export is configured only in a deployment, so this is where the resource
// the exporters carry is built before one starts.
func TestServiceResourceMergesWithTheSDKDefault(t *testing.T) {
	serviceResource, err := newServiceResource(Config{ServiceName: "oryxa", Version: "v1.2.3", Environment: "production"})
	if err != nil {
		t.Fatalf("newServiceResource() error = %v", err)
	}
	attributes := map[string]string{}
	for _, attribute := range serviceResource.Attributes() {
		attributes[string(attribute.Key)] = attribute.Value.Emit()
	}
	for key, want := range map[string]string{
		"service.name":                "oryxa",
		"service.version":             "v1.2.3",
		"deployment.environment.name": "production",
		"telemetry.sdk.language":      "go",
	} {
		if attributes[key] != want {
			t.Errorf("%s = %q, want %q", key, attributes[key], want)
		}
	}
}

// TestSetupSucceedsAndShutsDownPromptlyAgainstAnUnreachableEndpoint guards the
// class of bug newServiceResource fixed: every exporter is built and every
// global provider is set eagerly in Setup, none of it waiting on the
// endpoint actually being reachable, which otherwise only shows up once a
// deployment sets OTEL_EXPORTER_OTLP_ENDPOINT for real. Port 1 on loopback
// refuses the connection immediately rather than timing out, so this is fast
// even without the bounded shutdown context below.
func TestSetupSucceedsAndShutsDownPromptlyAgainstAnUnreachableEndpoint(t *testing.T) {
	previousTracer := otel.GetTracerProvider()
	previousMeter := otel.GetMeterProvider()
	previousPropagator := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTracer)
		otel.SetMeterProvider(previousMeter)
		otel.SetTextMapPropagator(previousPropagator)
	})
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")

	shutdown, err := Setup(context.Background(), Config{ServiceName: "oryxa-test"})
	if err != nil {
		t.Fatalf("Setup() with an unreachable OTLP endpoint returned an error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// A non-nil error here (the flush could not reach the endpoint before
		// shutdownCtx's deadline) is an acceptable outcome; hanging is not.
		_ = shutdown(shutdownCtx)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not return promptly against an unreachable OTLP endpoint")
	}
}

func TestLogLevel(t *testing.T) {
	if logLevel("debug") != slog.LevelDebug || logLevel("warning") != slog.LevelWarn ||
		logLevel("ERROR") != slog.LevelError || logLevel("unknown") != slog.LevelInfo {
		t.Fatal("unexpected log-level mapping")
	}
}

// TestHistogramViewMakesEveryHistogramExponential proves the SDK-level
// default this package's MeterProvider applies (see histogramView's doc in
// observability.go): a plain fixed-bucket histogram instrument still
// collects as a base-2 exponential one, which is what lets the collector's
// awsemf exporter compute percentiles rather than a min/max/sum/count
// statistic set. It does not go through Setup, which would need a live OTLP
// endpoint; it builds a MeterProvider the same way, with the same view.
func TestHistogramViewMakesEveryHistogramExponential(t *testing.T) {
	reader, meterProvider := manualMeterProvider(t)
	histogram, err := meterProvider.Meter("test").Float64Histogram("test.histogram")
	if err != nil {
		t.Fatal(err)
	}
	histogram.Record(context.Background(), 1.5)
	histogram.Record(context.Background(), 2.5)

	data := collect(t, reader)
	instrument, ok := findMetric(data, "test.histogram")
	if !ok {
		t.Fatal("test.histogram was not recorded")
	}
	exponential, ok := instrument.Data.(metricdata.ExponentialHistogram[float64])
	if !ok {
		t.Fatalf("test.histogram data = %T, want metricdata.ExponentialHistogram[float64]", instrument.Data)
	}
	if len(exponential.DataPoints) != 1 || exponential.DataPoints[0].Count != 2 {
		t.Fatalf("data points = %+v, want one point with count 2", exponential.DataPoints)
	}
}

func TestHTTPHandlerAddsRequestIDAndPreservesResponse(t *testing.T) {
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/items/{id}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte("ok"))
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/items/item-1?token=must-not-be-logged", nil)
	request.Header.Set("X-Request-ID", "attacker-controlled")
	recorder := httptest.NewRecorder()
	HTTPHandler("oryxa-test", mux).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || recorder.Body.String() != "ok" || recorder.Header().Get("X-Request-ID") == "" ||
		recorder.Header().Get("X-Request-ID") == "attacker-controlled" {
		t.Fatalf("response = %d %q request-id=%q", recorder.Code, recorder.Body.String(), recorder.Header().Get("X-Request-ID"))
	}
	if output := logs.String(); strings.Contains(output, "must-not-be-logged") || strings.Contains(output, "item-1") ||
		!strings.Contains(output, `"http.route":"GET /v1/items/{id}"`) {
		t.Fatalf("access log was unsafe or unstructured: %s", output)
	}
}

func TestHTTPHandlerNamesTheServerSpanAfterTheMatchedRoute(t *testing.T) {
	recorder := withRecordedSpans(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/items/{id}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/items/item-1", nil)
	HTTPHandler("oryxa-test", mux).ServeHTTP(httptest.NewRecorder(), request)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	got := spans[0]
	if got.Name() != "GET /v1/items/{id}" {
		t.Fatalf("span name = %q, want the method and the route template, no service prefix and no id", got.Name())
	}
	found := false
	for _, attr := range got.Attributes() {
		if string(attr.Key) == "http.route" {
			found = true
			if attr.Value.AsString() != "/v1/items/{id}" {
				t.Fatalf("http.route attribute = %q", attr.Value.AsString())
			}
		}
	}
	if !found {
		t.Fatal("span has no http.route attribute")
	}
}

func TestHTTPHandlerGivesAnUnmatchedRequestAFixedSpanName(t *testing.T) {
	recorder := withRecordedSpans(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/items/{id}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	// Two different paths, both unmatched, must collapse onto the same fixed
	// span name rather than growing one span name per 404.
	for _, path := range []string{"/nope", "/also/not/a/route"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		HTTPHandler("oryxa-test", mux).ServeHTTP(httptest.NewRecorder(), request)
	}

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	for _, span := range spans {
		if span.Name() != "oryxa-test unmatched" {
			t.Fatalf("unmatched span name = %q, want a single fixed name", span.Name())
		}
		for _, attr := range span.Attributes() {
			if string(attr.Key) == "http.route" {
				t.Fatalf("unmatched request has an http.route attribute: %q", attr.Value.AsString())
			}
		}
	}
}

func TestHTTPHandlerAddsRouteToServerMetrics(t *testing.T) {
	reader, _ := manualMeterProvider(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/items/{id}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/items/item-1", nil)
	HTTPHandler("oryxa-test", mux).ServeHTTP(httptest.NewRecorder(), request)

	data := collect(t, reader)
	instrument, ok := findMetric(data, "http.server.request.body.size")
	if !ok {
		t.Fatal("http.server.request.body.size was not recorded")
	}
	histogram, ok := instrument.Data.(metricdata.ExponentialHistogram[int64])
	if !ok {
		t.Fatalf("http.server.request.body.size data = %T, want metricdata.ExponentialHistogram[int64]", instrument.Data)
	}
	if len(histogram.DataPoints) != 1 {
		t.Fatalf("data points = %d, want 1", len(histogram.DataPoints))
	}
	route, ok := histogram.DataPoints[0].Attributes.Value(attribute.Key("http.route"))
	if !ok || route.AsString() != "/v1/items/{id}" {
		t.Fatalf("http.route attribute = %q, %v, want \"/v1/items/{id}\", true", route.Emit(), ok)
	}
}

func TestBuildVersionIsTheReleaseTheImageLinkedIn(t *testing.T) {
	previous := version
	t.Cleanup(func() { version = previous })

	version = "982215135c9b"
	if got := buildVersion(); got != "982215135c9b" {
		t.Fatalf("buildVersion() = %q, want the linked release", got)
	}
	version = ""
	if got := buildVersion(); got == "" {
		t.Fatal("buildVersion() is empty without a linked release")
	}
}

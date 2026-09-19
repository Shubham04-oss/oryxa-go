// Package observability configures the common logs, traces, metrics, and HTTP
// instrumentation used by Oryxa services. OTLP export is opt-in through the
// standard OTEL_EXPORTER_OTLP_* endpoint environment variables.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"

	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

type Config struct {
	ServiceName string
	Version     string
	Environment string
}

type Shutdown func(context.Context) error

func Setup(ctx context.Context, config Config) (Shutdown, error) {
	config.ServiceName = strings.TrimSpace(config.ServiceName)
	if config.ServiceName == "" {
		return nil, errors.New("observability service name is required")
	}
	if config.Version == "" {
		config.Version = buildVersion()
	}
	if config.Environment == "" {
		config.Environment = firstNonEmpty(os.Getenv("DEPLOYMENT_ENVIRONMENT"), os.Getenv("ENVIRONMENT"), "development")
	}
	configureLogging(config)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") || !exportConfigured() {
		return func(context.Context) error { return nil }, nil
	}

	serviceResource, err := newServiceResource(config)
	if err != nil {
		return nil, fmt.Errorf("build telemetry resource: %w", err)
	}

	var tracerProvider *sdktrace.TracerProvider
	if traceExportConfigured() {
		exporter, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("configure OTLP trace exporter: %w", err)
		}
		tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(serviceResource),
		)
		otel.SetTracerProvider(tracerProvider)
	}

	var meterProvider *metric.MeterProvider
	if metricExportConfigured() {
		exporter, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			if tracerProvider != nil {
				_ = tracerProvider.Shutdown(ctx)
			}
			return nil, fmt.Errorf("configure OTLP metric exporter: %w", err)
		}
		meterProvider = metric.NewMeterProvider(
			metric.WithReader(metric.NewPeriodicReader(exporter)),
			metric.WithResource(serviceResource),
		)
		otel.SetMeterProvider(meterProvider)
		if err := otelruntime.Start(otelruntime.WithMeterProvider(meterProvider)); err != nil {
			_ = meterProvider.Shutdown(ctx)
			if tracerProvider != nil {
				_ = tracerProvider.Shutdown(ctx)
			}
			return nil, fmt.Errorf("register Go runtime metrics: %w", err)
		}
	}

	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Error("telemetry pipeline error", "error", err)
	}))
	return func(ctx context.Context) error {
		var errs []error
		if meterProvider != nil {
			errs = append(errs, meterProvider.Shutdown(ctx))
		}
		if tracerProvider != nil {
			errs = append(errs, tracerProvider.Shutdown(ctx))
		}
		return errors.Join(errs...)
	}, nil
}

// newServiceResource describes the process to every exporter: the SDK's
// default resource (the SDK itself, and OTEL_RESOURCE_ATTRIBUTES) with the
// service's name, version and environment on top. Those carry no schema URL
// of their own. The default names the semantic conventions of the SDK's own
// release, and two resources with different schema URLs do not merge at all,
// which failed every start with export configured.
func newServiceResource(config Config) (*resource.Resource, error) {
	return resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(config.ServiceName),
		semconv.ServiceVersion(config.Version),
		semconv.DeploymentEnvironmentName(config.Environment),
	))
}

func configureLogging(config Config) {
	level := new(slog.LevelVar)
	level.Set(logLevel(os.Getenv("LOG_LEVEL")))
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     level,
		AddSource: strings.EqualFold(strings.TrimSpace(os.Getenv("LOG_ADD_SOURCE")), "true"),
	}).WithAttrs([]slog.Attr{
		slog.String("service", config.ServiceName),
		slog.String("service_version", config.Version),
		slog.String("environment", config.Environment),
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)
	// Preserve existing package-level log calls while emitting the same JSON
	// envelope. New code should use slog with typed attributes.
	// Remaining legacy calls are failure paths in background consumers and
	// fatal startup paths. Informational lifecycle logs use slog directly.
	legacy := slog.NewLogLogger(handler, slog.LevelError)
	log.SetFlags(0)
	log.SetOutput(legacy.Writer())
}

func logLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func exportConfigured() bool { return traceExportConfigured() || metricExportConfigured() }

func traceExportConfigured() bool {
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" ||
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != ""
}

func metricExportConfigured() bool {
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" ||
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")) != ""
}

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "development"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

package usage

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/adk/v2/model"
)

// metricsReader collects everything modelCallDuration and modelCallTokens
// ever record for the whole of this package's test binary.
//
// Both are package-level vars, created once at package load against
// whatever MeterProvider is global then (see modelCallDuration's doc for why
// that is safe in production). The corollary for testing is that the global
// MeterProvider can only usefully be set once per process: the OTel Go SDK's
// global package delegates an instrument to the first real MeterProvider
// registered after it, not to whichever was registered most recently, so a
// second otel.SetMeterProvider call in a later test would not be seen by
// instruments resolved before it. TestMain sets it up exactly once, before
// any test (and so before modelCallDuration/modelCallTokens's own delegate
// is ever resolved), and every test reads the same reader, distinguishing
// its own data points by using a model name of its own.
var metricsReader *metric.ManualReader

func TestMain(m *testing.M) {
	metricsReader = metric.NewManualReader()
	otel.SetMeterProvider(metric.NewMeterProvider(metric.WithReader(metricsReader)))
	os.Exit(m.Run())
}

// collectMetric reads the current, cumulative state of one instrument.
func collectMetric(t *testing.T, name string) (metricdata.Metrics, bool) {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	for _, scope := range data.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name == name {
				return instrument, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// tokensByType reads oryxa.model.tokens's data points for one model, keyed
// by their "type" attribute (input, output or cached).
func tokensByType(t *testing.T, model string) map[string]int64 {
	t.Helper()
	instrument, ok := collectMetric(t, "oryxa.model.tokens")
	if !ok {
		return nil
	}
	sum, ok := instrument.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("oryxa.model.tokens data = %T, want metricdata.Sum[int64]", instrument.Data)
	}
	byType := map[string]int64{}
	for _, point := range sum.DataPoints {
		gotModel, _ := point.Attributes.Value(attribute.Key("model"))
		if gotModel.AsString() != model {
			continue
		}
		tokenType, _ := point.Attributes.Value(attribute.Key("type"))
		byType[tokenType.AsString()] = point.Value
	}
	return byType
}

// durationCountFor reads how many values oryxa.model.call.duration has
// recorded for one model so far.
func durationCountFor(t *testing.T, model string) uint64 {
	t.Helper()
	instrument, ok := collectMetric(t, "oryxa.model.call.duration")
	if !ok {
		return 0
	}
	histogram, ok := instrument.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("oryxa.model.call.duration data = %T, want metricdata.Histogram[float64]", instrument.Data)
	}
	for _, point := range histogram.DataPoints {
		if gotModel, _ := point.Attributes.Value(attribute.Key("model")); gotModel.AsString() == model {
			return point.Count
		}
	}
	return 0
}

func TestRecordModelCallRecordsDurationAndTokensByType(t *testing.T) {
	recordModelCall(context.Background(), "test-record-basic", usageOf(100, 20), 0)

	if got := durationCountFor(t, "test-record-basic"); got != 1 {
		t.Fatalf("duration data points for this model = %d, want 1", got)
	}
	if by := tokensByType(t, "test-record-basic"); by["input"] != 100 || by["output"] != 20 {
		t.Fatalf("tokens by type = %+v, want input=100 output=20", by)
	}
	if by := tokensByType(t, "test-record-basic"); by["cached"] != 0 {
		t.Fatalf("tokens by type = %+v, want no cached tokens reported", by)
	}
}

func TestRecordModelCallReportsCachedTokensSeparately(t *testing.T) {
	metadata := usageOf(100, 20)
	metadata.CachedContentTokenCount = 40
	recordModelCall(context.Background(), "test-record-cached", metadata, 0)

	if by := tokensByType(t, "test-record-cached"); by["cached"] != 40 {
		t.Fatalf("cached tokens = %+v, want 40", by)
	}
}

func TestRecordModelCallOfNoMetadataRecordsNothing(t *testing.T) {
	recordModelCall(context.Background(), "test-record-no-metadata", nil, 0)

	if got := durationCountFor(t, "test-record-no-metadata"); got != 0 {
		t.Fatalf("duration data points = %d, want 0 for a call with no usage metadata", got)
	}
}

func TestCountedRecordsMetricsForANonStreamingCall(t *testing.T) {
	fake := &scriptedLLM{name: "test-counted-non-streaming", script: []scriptedYield{
		{response: &model.LLMResponse{Partial: false, UsageMetadata: usageOf(50, 10)}},
	}}
	ctx := WithCounter(context.Background(), NewCounter())

	if _, errs := drain(Counted(fake).GenerateContent(ctx, &model.LLMRequest{}, false)); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if got := durationCountFor(t, "test-counted-non-streaming"); got != 1 {
		t.Fatalf("duration data points = %d, want 1", got)
	}
	if by := tokensByType(t, "test-counted-non-streaming"); by["input"] != 50 || by["output"] != 10 {
		t.Fatalf("tokens by type = %+v, want input=50 output=10", by)
	}
}

func TestCountedRecordsMetricsOnceForAStreamingCall(t *testing.T) {
	fake := &scriptedLLM{name: "test-counted-streaming", script: []scriptedYield{
		{response: &model.LLMResponse{Partial: true, UsageMetadata: usageOf(10, 1)}},
		{response: &model.LLMResponse{Partial: true, UsageMetadata: usageOf(10, 2)}},
		{response: &model.LLMResponse{Partial: false, UsageMetadata: usageOf(10, 3)}},
	}}
	ctx := WithCounter(context.Background(), NewCounter())

	if _, errs := drain(Counted(fake).GenerateContent(ctx, &model.LLMRequest{}, true)); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Exactly one measurement despite three chunks: the same rule Counter.Add
	// itself follows (see countedGenerateContent), so the metrics and the
	// run's priced usage always agree.
	if got := durationCountFor(t, "test-counted-streaming"); got != 1 {
		t.Fatalf("duration data points = %d, want exactly 1 for the whole streamed call", got)
	}
	if by := tokensByType(t, "test-counted-streaming"); by["output"] != 3 {
		t.Fatalf("tokens by type = %+v, want the final chunk's output count (3), not the sum of every chunk", by)
	}
}

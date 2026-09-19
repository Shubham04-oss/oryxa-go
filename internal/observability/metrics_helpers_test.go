package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// manualMeterProvider registers a MeterProvider backed by a ManualReader as
// the global one, with the same histogramView Setup uses, so a test collects
// exactly what production would export. It restores whatever was global
// before, so this test cannot leak state into another one.
func manualMeterProvider(t *testing.T) (*metric.ManualReader, *metric.MeterProvider) {
	t.Helper()
	previous := otel.GetMeterProvider()
	reader := metric.NewManualReader()
	meterProvider := metric.NewMeterProvider(metric.WithReader(reader), metric.WithView(histogramView))
	otel.SetMeterProvider(meterProvider)
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
		otel.SetMeterProvider(previous)
	})
	return reader, meterProvider
}

func collect(t *testing.T, reader *metric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func findMetric(data metricdata.ResourceMetrics, name string) (metricdata.Metrics, bool) {
	for _, scope := range data.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name == name {
				return instrument, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// assertHistogramRecorded fails the test unless name was recorded exactly
// wantCount times, as a base-2 exponential histogram - the aggregation
// histogramView forces every histogram instrument into, so the collector's
// awsemf exporter can compute percentiles from it instead of a plain
// min/max/sum/count statistic set.
func assertHistogramRecorded(t *testing.T, data metricdata.ResourceMetrics, name string, wantCount uint64) {
	t.Helper()
	instrument, ok := findMetric(data, name)
	if !ok {
		t.Fatalf("metric %s was not recorded", name)
	}
	histogram, ok := instrument.Data.(metricdata.ExponentialHistogram[float64])
	if !ok {
		t.Fatalf("metric %s data = %T, want metricdata.ExponentialHistogram[float64]", name, instrument.Data)
	}
	var total uint64
	for _, point := range histogram.DataPoints {
		total += point.Count
	}
	if total != wantCount {
		t.Fatalf("metric %s recorded %d values across its data points, want %d", name, total, wantCount)
	}
}

func assertCounterTotal(t *testing.T, data metricdata.ResourceMetrics, name string, want int64) {
	t.Helper()
	instrument, ok := findMetric(data, name)
	if !ok {
		t.Fatalf("metric %s was not recorded", name)
	}
	sum, ok := instrument.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %s data = %T, want metricdata.Sum[int64]", name, instrument.Data)
	}
	var total int64
	for _, point := range sum.DataPoints {
		total += point.Value
	}
	if total != want {
		t.Fatalf("metric %s total = %d, want %d", name, total, want)
	}
}

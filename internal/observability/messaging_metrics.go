package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// OutboxMetrics is the standard set of measurements internal/outbox records
// for every table it dispatches, whichever service owns it: one Dispatcher
// keeps one of these and reuses it for every event it publishes.
type OutboxMetrics struct {
	duration metric.Float64Histogram
	lag      metric.Float64Histogram
	failures metric.Int64Counter
}

// NewOutboxMetrics creates the outbox instruments on the shared meter.
func NewOutboxMetrics() (OutboxMetrics, error) {
	meter := Meter()
	duration, err := meter.Float64Histogram("oryxa.outbox.publish.duration",
		metric.WithDescription("Time spent publishing one outbox event to the bus."),
		metric.WithUnit("s"))
	if err != nil {
		return OutboxMetrics{}, fmt.Errorf("create oryxa.outbox.publish.duration: %w", err)
	}
	lag, err := meter.Float64Histogram("oryxa.outbox.publish.lag",
		metric.WithDescription("Time between an event occurring and this attempt to publish it, however long it waited in the outbox."),
		metric.WithUnit("s"))
	if err != nil {
		return OutboxMetrics{}, fmt.Errorf("create oryxa.outbox.publish.lag: %w", err)
	}
	failures, err := meter.Int64Counter("oryxa.outbox.publish.failures",
		metric.WithDescription("Outbox events that failed to publish and were scheduled for retry."),
		metric.WithUnit("{event}"))
	if err != nil {
		return OutboxMetrics{}, fmt.Errorf("create oryxa.outbox.publish.failures: %w", err)
	}
	return OutboxMetrics{duration: duration, lag: lag, failures: failures}, nil
}

// Record logs one publish attempt: how long it took, how long the event had
// waited since it occurred, and, when err is set, a failure. component
// identifies the owning outbox table (such as "teams_outbox"), which is
// enough to tell services apart without an id or any other high-cardinality
// value.
//
// Record is a no-op on the zero OutboxMetrics, which is what a Dispatcher
// falls back to if NewOutboxMetrics ever fails: publishing itself must not
// stop just because its own metrics could not be created.
func (metrics OutboxMetrics) Record(ctx context.Context, component, detailType string, occurredAt, started time.Time, err error) {
	if metrics.duration == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("component", component), attribute.String("detail_type", detailType))
	metrics.duration.Record(ctx, time.Since(started).Seconds(), attrs)
	if !occurredAt.IsZero() {
		metrics.lag.Record(ctx, time.Since(occurredAt).Seconds(), attrs)
	}
	if err != nil {
		metrics.failures.Add(ctx, 1, attrs)
	}
}

// ConsumerMetrics is the standard set of measurements an event consumer
// records for every message it handles, whichever service or queue it is.
type ConsumerMetrics struct {
	duration metric.Float64Histogram
	lag      metric.Float64Histogram
	failures metric.Int64Counter
}

// NewConsumerMetrics creates the consumer instruments on the shared meter.
func NewConsumerMetrics() (ConsumerMetrics, error) {
	meter := Meter()
	duration, err := meter.Float64Histogram("oryxa.consumer.process.duration",
		metric.WithDescription("Time spent handling one message, from decoding to the handler returning."),
		metric.WithUnit("s"))
	if err != nil {
		return ConsumerMetrics{}, fmt.Errorf("create oryxa.consumer.process.duration: %w", err)
	}
	lag, err := meter.Float64Histogram("oryxa.consumer.delivery.lag",
		metric.WithDescription("Time between an event occurring and this consumer receiving it."),
		metric.WithUnit("s"))
	if err != nil {
		return ConsumerMetrics{}, fmt.Errorf("create oryxa.consumer.delivery.lag: %w", err)
	}
	failures, err := meter.Int64Counter("oryxa.consumer.process.failures",
		metric.WithDescription("Messages whose handler returned an error."),
		metric.WithUnit("{message}"))
	if err != nil {
		return ConsumerMetrics{}, fmt.Errorf("create oryxa.consumer.process.failures: %w", err)
	}
	return ConsumerMetrics{duration: duration, lag: lag, failures: failures}, nil
}

// Record logs one handled message: processing duration and outcome, and, when
// occurredAt is known, how stale the message was when received. component
// identifies the consuming service, such as "teams".
//
// Record is a no-op on the zero ConsumerMetrics, which is what a Consumer
// falls back to if NewConsumerMetrics ever fails: handling the message must
// not stop just because its own metrics could not be created.
func (metrics ConsumerMetrics) Record(ctx context.Context, component, detailType string, occurredAt, receivedAt, started time.Time, err error) {
	if metrics.duration == nil {
		return
	}
	base := []attribute.KeyValue{attribute.String("component", component), attribute.String("detail_type", detailType)}
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	durationAttrs := metric.WithAttributes(append(append([]attribute.KeyValue{}, base...), attribute.String("outcome", outcome))...)
	metrics.duration.Record(ctx, time.Since(started).Seconds(), durationAttrs)
	if !occurredAt.IsZero() && !receivedAt.IsZero() {
		metrics.lag.Record(ctx, receivedAt.Sub(occurredAt).Seconds(), metric.WithAttributes(base...))
	}
	if err != nil {
		metrics.failures.Add(ctx, 1, metric.WithAttributes(base...))
	}
}

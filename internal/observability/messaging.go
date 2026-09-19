package observability

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
)

// awsEventBridgeSystem is messaging.system's value for the outbox's producer
// spans. The semantic conventions define aws_sqs and aws_sns but, as of the
// semconv version this module vendors, no registered value for EventBridge;
// this is the same "aws_<service>" shape those use.
const awsEventBridgeSystem = "aws_eventbridge"

// StartProducerSpan starts the span for publishing one outbox event to the
// bus. destination is the event bus's name (never an ARN, which carries an
// account id).
//
// It links to, rather than descends from, the context captured when the
// event was written, on purpose: publishing is decoupled from that write by
// the outbox's flush interval and by retry backoff, which can stretch to
// minutes under backlog, and in a system where handling one event can itself
// write another outbox row, chaining every hop as parent/child would make a
// trace grow for as long as a business process keeps reacting to itself. A
// link keeps the write's trace one click away without merging unrelated time
// ranges into a single, ever-growing one. The hop this span exists for - bus
// to consumer - is a single, short, bounded wait, so that one is still a
// direct child; see StartConsumerSpan.
func StartProducerSpan(ctx context.Context, carrier TraceCarrier, destination, detailType, eventID string) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String(awsEventBridgeSystem),
			semconv.MessagingDestinationName(destination),
			semconv.MessagingOperationTypeSend,
			semconv.MessagingOperationName("publish"),
			semconv.MessagingMessageID(eventID),
			attribute.String("detail_type", detailType),
		),
	}
	if spanContext, ok := carrier.SpanContext(); ok {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: spanContext}))
	}
	return Tracer().Start(ctx, "publish "+destination, opts...)
}

// StartConsumerSpan starts the span for handling one message a consumer
// received off destination (a queue's name, never its URL or ARN). It is a
// child of the producer's span when the message carries a carrier - every
// message published after this rollout - and a fresh root when it does not:
// a message in flight during the rollout, or one that never went through our
// outbox, such as an EventBridge schedule or an SES notification landing
// straight on a queue.
func StartConsumerSpan(ctx context.Context, carrier TraceCarrier, destination, detailType, eventID string) (context.Context, trace.Span) {
	parent := carrier.ContextWith(ctx)
	return Tracer().Start(parent, "process "+destination,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemAWSSQS,
			semconv.MessagingDestinationName(destination),
			semconv.MessagingOperationTypeProcess,
			semconv.MessagingOperationName("process"),
			semconv.MessagingMessageID(eventID),
			attribute.String("detail_type", detailType),
		),
	)
}

// EndSpan finishes span, recording err on it when it is not nil. It is the
// shared tail of every producer and consumer span: on success it just ends,
// leaving the status unset, which is the convention for a span that has
// nothing more to say.
func EndSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// QueueName is the last path segment of an SQS queue URL, for a span or log
// attribute that should not carry the AWS account id embedded earlier in the
// URL. It returns queueURL unchanged if there is no slash to split on.
func QueueName(queueURL string) string {
	if index := strings.LastIndex(queueURL, "/"); index >= 0 {
		return queueURL[index+1:]
	}
	return queueURL
}

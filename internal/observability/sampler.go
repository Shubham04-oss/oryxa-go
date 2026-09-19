package observability

import (
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Sampler is the process's trace sampler: ParentBased, so a non-root span
// always follows its parent's sampling decision, wrapping a root sampler
// that drops one specific shape of root span before falling through to
// whatever OTEL_TRACES_SAMPLER would otherwise choose for a root (see
// rootSamplerFromEnv).
//
// The span it drops is otelaws's own client span around an SQS
// ReceiveMessage call. Every consumer's long poll opens one, whether or not
// it found a message, about every 20 seconds for as long as the process
// runs. Poll runs on a background worker rather than inside a request, so
// that span has no parent and would otherwise become its own root trace: at
// one per consumer per poll interval, every one empty of anything else, it
// would dwarf the traces this rollout exists to produce - the request that
// wrote an event, the message that carried it, the run it woke - in both
// volume and cost. A ReceiveMessage call that does have a parent (nothing in
// this codebase makes one that way today, but a future caller might) is left
// to the ordinary decision: dropping it there would cut whatever span called
// it too, which this carve-out has no business doing.
func Sampler() sdktrace.Sampler {
	return sdktrace.ParentBased(rootSampler{fallback: rootSamplerFromEnv()})
}

// rootSampler drops an SQS ReceiveMessage root span and defers to fallback
// for everything else. It is only ever used as ParentBased's root sampler,
// so ShouldSample only runs for a span with no valid parent.
type rootSampler struct {
	fallback sdktrace.Sampler
}

func (s rootSampler) ShouldSample(parameters sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if isSQSReceiveMessage(parameters.Attributes) {
		return sdktrace.SamplingResult{
			Decision: sdktrace.Drop,
			// Every built-in sampler carries the parent's tracestate forward
			// regardless of its own decision; a dropped span is no reason for
			// this one to be the exception.
			Tracestate: trace.SpanContextFromContext(parameters.ParentContext).TraceState(),
		}
	}
	return s.fallback.ShouldSample(parameters)
}

func (s rootSampler) Description() string {
	return "SQSReceiveMessageFilter{" + s.fallback.Description() + "}"
}

// rpcSystemNameKey and rpcMethodKey are the semconv v1.43.0 attributes
// go.opentelemetry.io/contrib's otelaws v0.71.0 sets on the client span it
// starts around every AWS SDK call (see its otelMiddlewares.
// initializeMiddlewareAfter and attributes.go): rpc.system.name is the
// service-independent "aws-api", and what used to be a separate rpc.service
// attribute is now the first segment of rpc.method, "<ServiceID>/<Operation>".
const (
	rpcSystemNameKey = attribute.Key("rpc.system.name")
	rpcMethodKey     = attribute.Key("rpc.method")
)

// isSQSReceiveMessage recognizes otelaws's span for SQS's ReceiveMessage
// operation from the attributes it is started with.
func isSQSReceiveMessage(attrs []attribute.KeyValue) bool {
	var system, method string
	for _, attr := range attrs {
		switch attr.Key {
		case rpcSystemNameKey:
			system = attr.Value.AsString()
		case rpcMethodKey:
			method = attr.Value.AsString()
		}
	}
	return system == "aws-api" && method == "SQS/ReceiveMessage"
}

// rootSamplerFromEnv mirrors the root-sampler half of what
// go.opentelemetry.io/otel/sdk/trace's own OTEL_TRACES_SAMPLER support
// chooses. That package's version is unexported and returns a sampler
// already wrapped in ParentBased; Sampler above supplies its own ParentBased
// instead, so it needs only the root component here. AlwaysSample is the
// SDK's own default when the variable is unset, unrecognized, or (for a
// ratio-based sampler) its argument does not parse as a fraction in [0,1] -
// see the SDK's ensureValidTracerProviderConfig and samplerFromEnv.
func rootSamplerFromEnv() sdktrace.Sampler {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER"))) {
	case "always_off", "parentbased_always_off":
		return sdktrace.NeverSample()
	case "traceidratio", "parentbased_traceidratio":
		ratio, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG")), 64)
		if err != nil || ratio < 0 || ratio > 1 {
			ratio = 1.0
		}
		return sdktrace.TraceIDRatioBased(ratio)
	default:
		// Covers "always_on", "parentbased_always_on", unset, and anything
		// the SDK itself would not recognize either.
		return sdktrace.AlwaysSample()
	}
}

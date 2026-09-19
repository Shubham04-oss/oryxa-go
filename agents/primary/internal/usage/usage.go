// Package usage counts the model tokens one run spends, across every model
// call it makes — Krude's own, and those of the planner, researcher, helpers
// and dynamic sub-agents it starts — so the run can be priced as a whole.
//
// A run carries a Counter on its context (WithCounter). Every model the
// agents run on is wrapped with Counted, which adds each finished response's
// usage to the counter on the context of the call that produced it. Sub-agents
// and helpers run inside the tool call that started them, on a context
// derived from the run's, so their calls reach the same counter.
package usage

import (
	"context"
	"iter"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// meterName scopes the model-call metrics below to this package, the way
// ADK's own internal/telemetry scopes its generate_content and execute_tool
// spans to "gcp.vertex.agent": both read from the same global providers this
// process configures in internal/observability.Setup, but a viewer should
// still be able to tell which library produced which signal.
const meterName = "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"

// modelCallDuration and modelCallTokens are created once, against the global
// MeterProvider, the same way ADK's own package-level tracer is: both are
// resolved lazily by the otel API, so creating them before
// internal/observability.Setup has registered the real provider is safe -
// see go.opentelemetry.io/otel's own doc for Meter and Tracer. ADK's
// generate_content span already carries a duration (its own start/end) and
// the same token counts as span attributes (see internal/telemetry.
// TraceGenerateContentResult), which is enough to read one call's numbers
// off its trace; these give the same numbers as metrics, which is what a
// dashboard or an alarm needs and a trace does not provide.
var modelCallDuration, modelCallTokens = newModelMetrics()

func newModelMetrics() (metric.Float64Histogram, metric.Int64Counter) {
	meter := otel.Meter(meterName)
	duration, err := meter.Float64Histogram("oryxa.model.call.duration",
		metric.WithDescription("Time spent in one model call, from request to its finished (non-partial) response."),
		metric.WithUnit("s"))
	if err != nil {
		otel.Handle(err)
	}
	tokens, err := meter.Int64Counter("oryxa.model.tokens",
		metric.WithDescription("Tokens spent on model calls, by model and token type (input, output, cached)."),
		metric.WithUnit("{token}"))
	if err != nil {
		otel.Handle(err)
	}
	return duration, tokens
}

// recordModelCall records one finished model call's duration and token
// counts, mirroring Counter.Add's own billing rules (see its doc) so the
// metrics and the run's priced usage never disagree: tool-use-prompt and
// thinking tokens count as input and output respectively, and cached tokens
// are reported as their own type despite already being part of input, since
// that split is the only way to see a cache hit rate from these metrics.
func recordModelCall(ctx context.Context, model string, metadata *genai.GenerateContentResponseUsageMetadata, duration time.Duration) {
	if modelCallDuration == nil || metadata == nil {
		return
	}
	modelAttr := attribute.String("model", model)
	modelCallDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(modelAttr))

	inputTokens := metadata.PromptTokenCount + metadata.ToolUsePromptTokenCount
	outputTokens := metadata.CandidatesTokenCount + metadata.ThoughtsTokenCount
	if inputTokens > 0 {
		modelCallTokens.Add(ctx, int64(inputTokens), metric.WithAttributes(modelAttr, attribute.String("type", "input")))
	}
	if outputTokens > 0 {
		modelCallTokens.Add(ctx, int64(outputTokens), metric.WithAttributes(modelAttr, attribute.String("type", "output")))
	}
	if metadata.CachedContentTokenCount > 0 {
		modelCallTokens.Add(ctx, int64(metadata.CachedContentTokenCount), metric.WithAttributes(modelAttr, attribute.String("type", "cached")))
	}
}

// Counter adds up what each model spent in one run. It is safe for
// concurrent use: sub-agents called in the same model turn run concurrently.
type Counter struct {
	mu     sync.Mutex
	models map[string]agentsv1.Usage
}

// NewCounter returns an empty counter.
func NewCounter() *Counter {
	return &Counter{models: make(map[string]agentsv1.Usage)}
}

// counterKey is the unexported type WithCounter and FromContext key their
// value on, so no other package can collide with or read it directly.
type counterKey struct{}

// WithCounter attaches counter to ctx, for the models a run calls to find.
func WithCounter(ctx context.Context, counter *Counter) context.Context {
	return context.WithValue(ctx, counterKey{}, counter)
}

// FromContext returns the counter WithCounter attached to ctx, or nil.
func FromContext(ctx context.Context) *Counter {
	counter, _ := ctx.Value(counterKey{}).(*Counter)
	return counter
}

// Add records one finished model call's usage against the named model. A nil
// metadata does nothing, since a response can carry none — a filtered chunk,
// say — and there is then nothing to price.
func (counter *Counter) Add(model string, metadata *genai.GenerateContentResponseUsageMetadata) {
	if counter == nil || metadata == nil {
		return
	}

	// Gemini bills tool results fed back to the model as input tokens, and
	// thinking output as output tokens, even though genai reports both in
	// their own fields rather than folding them into the prompt/candidates
	// counts. Cached tokens are already part of the prompt count, not extra.
	added := agentsv1.Usage{
		InputTokens:  metadata.PromptTokenCount + metadata.ToolUsePromptTokenCount,
		OutputTokens: metadata.CandidatesTokenCount + metadata.ThoughtsTokenCount,
		CachedTokens: metadata.CachedContentTokenCount,
	}
	if metadata.TotalTokenCount != 0 {
		added.TotalTokens = metadata.TotalTokenCount
	} else {
		// Whatever backend produced this response left the total unset; bill
		// this call for exactly what it added elsewhere.
		added.TotalTokens = added.InputTokens + added.OutputTokens
	}

	counter.mu.Lock()
	defer counter.mu.Unlock()
	if counter.models == nil {
		counter.models = make(map[string]agentsv1.Usage)
	}
	entry := counter.models[model]
	entry.InputTokens += added.InputTokens
	entry.OutputTokens += added.OutputTokens
	entry.CachedTokens += added.CachedTokens
	entry.TotalTokens += added.TotalTokens
	counter.models[model] = entry
}

// Models returns what each model spent so far, by model name. The map is a
// copy the caller is free to mutate or keep past the next Add.
func (counter *Counter) Models() map[string]agentsv1.Usage {
	if counter == nil {
		return map[string]agentsv1.Usage{}
	}

	counter.mu.Lock()
	defer counter.mu.Unlock()
	models := make(map[string]agentsv1.Usage, len(counter.models))
	for name, spent := range counter.models {
		models[name] = spent
	}
	return models
}

// Total returns the sum of every model's usage.
func (counter *Counter) Total() agentsv1.Usage {
	var total agentsv1.Usage
	if counter == nil {
		return total
	}

	counter.mu.Lock()
	defer counter.mu.Unlock()
	for _, spent := range counter.models {
		total.InputTokens += spent.InputTokens
		total.OutputTokens += spent.OutputTokens
		total.CachedTokens += spent.CachedTokens
		total.TotalTokens += spent.TotalTokens
	}
	return total
}

// googleLLM mirrors ADK's own internal googlellm.GoogleLLM interface. ADK
// type-asserts a model.LLM against this method set to tell a Vertex AI model
// from a Gemini API one (internal/llminternal/googlellm/variant.go in the ADK
// module). Counted must keep exposing the method exactly when the model it
// wraps does, or wrapping a model would silently break that detection.
type googleLLM interface {
	GetGoogleLLMVariant() genai.Backend
}

// countedLLM wraps a model.LLM that does not report a Google LLM variant.
type countedLLM struct {
	llm model.LLM
}

func (counted countedLLM) Name() string { return counted.llm.Name() }

func (counted countedLLM) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return countedGenerateContent(ctx, counted.llm, request, stream)
}

// countedGoogleLLM is a countedLLM that also forwards GetGoogleLLMVariant, for
// wrapping a model.LLM that implements googleLLM. Embedding the interface
// promotes the method with no method body of its own to keep in sync.
type countedGoogleLLM struct {
	countedLLM
	googleLLM
}

// Counted wraps llm so the usage of each response that finishes a model call
// is added to the Counter on that call's context, when there is one.
func Counted(llm model.LLM) model.LLM {
	if llm == nil {
		return nil
	}
	wrapped := countedLLM{llm: llm}
	if variant, ok := llm.(googleLLM); ok {
		return countedGoogleLLM{countedLLM: wrapped, googleLLM: variant}
	}
	return wrapped
}

// countedGenerateContent runs llm's GenerateContent and adds the usage of
// every response that finishes a model call to the Counter on ctx, if one is
// attached. It forwards every response and error from llm unchanged and in
// order, and stops pulling from llm as soon as the consumer stops ranging.
func countedGenerateContent(ctx context.Context, llm model.LLM, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// A call cut short — an error mid-stream, a cancelled run, a member
		// who stopped listening — never gets its finished response, yet its
		// prompt was billed. The newest usage a partial chunk carried is the
		// best measure of it left.
		var partial *genai.GenerateContentResponseUsageMetadata
		counted := false
		started := time.Now()
		defer func() {
			if !counted && partial != nil {
				recordModelCall(ctx, llm.Name(), partial, time.Since(started))
				FromContext(ctx).Add(llm.Name(), partial)
			}
		}()
		for response, err := range llm.GenerateContent(ctx, request, stream) {
			// In streaming mode ADK's Gemini model yields every chunk with
			// Partial = true (each chunk repeats the running usage), then one
			// final aggregated response with Partial = false carrying the
			// call's usage; in non-streaming mode the single response is
			// already non-partial. Counting only non-partial, error-free
			// responses counts each model call exactly once.
			if err == nil && response != nil && response.UsageMetadata != nil {
				if response.Partial {
					partial = response.UsageMetadata
				} else if !counted {
					recordModelCall(ctx, llm.Name(), response.UsageMetadata, time.Since(started))
					FromContext(ctx).Add(llm.Name(), response.UsageMetadata)
					counted = true
				}
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

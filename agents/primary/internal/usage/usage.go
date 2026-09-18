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

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

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
		defer func() {
			if !counted && partial != nil {
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

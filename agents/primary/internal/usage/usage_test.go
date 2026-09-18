package usage

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// scriptedYield is one (response, error) pair a scriptedLLM hands its caller.
type scriptedYield struct {
	response *model.LLMResponse
	err      error
}

// scriptedLLM is a fake model.LLM that yields a fixed script and records how
// many elements it actually produced, so a test can tell whether a consumer
// that stopped early also stopped the fake from running to completion.
type scriptedLLM struct {
	name   string
	script []scriptedYield

	yielded int
}

func (fake *scriptedLLM) Name() string { return fake.name }

func (fake *scriptedLLM) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, step := range fake.script {
			fake.yielded++
			if !yield(step.response, step.err) {
				return
			}
		}
	}
}

// googleVariantLLM is a scriptedLLM that also reports a Google LLM variant,
// for exercising the half of Counted that must forward GetGoogleLLMVariant.
type googleVariantLLM struct {
	*scriptedLLM
	variant genai.Backend
}

func (fake *googleVariantLLM) GetGoogleLLMVariant() genai.Backend { return fake.variant }

// asGoogleLLM is the same method set as ADK's own internal googlellm.GoogleLLM
// interface, restated locally since that package is internal to ADK and this
// module cannot import it.
type asGoogleLLM interface {
	GetGoogleLLMVariant() genai.Backend
}

func usageOf(prompt, candidates int32) *genai.GenerateContentResponseUsageMetadata {
	return &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     prompt,
		CandidatesTokenCount: candidates,
		TotalTokenCount:      prompt + candidates,
	}
}

func drain(seq iter.Seq2[*model.LLMResponse, error]) (responses int, errs []error) {
	for _, err := range seq {
		responses++
		if err != nil {
			errs = append(errs, err)
		}
	}
	return responses, errs
}

func TestCountedStreamingCountsOnceFromFinalResponse(t *testing.T) {
	fake := &scriptedLLM{
		name: "gemini-streaming",
		script: []scriptedYield{
			{response: &model.LLMResponse{Partial: true, UsageMetadata: usageOf(10, 1)}},
			{response: &model.LLMResponse{Partial: true, UsageMetadata: usageOf(10, 2)}},
			{response: &model.LLMResponse{Partial: true, UsageMetadata: usageOf(10, 3)}},
			{response: &model.LLMResponse{Partial: false, UsageMetadata: usageOf(10, 6)}},
		},
	}
	counter := NewCounter()
	ctx := WithCounter(context.Background(), counter)

	responses, errs := drain(Counted(fake).GenerateContent(ctx, &model.LLMRequest{}, true))
	if responses != 4 || len(errs) != 0 {
		t.Fatalf("drain() = %d responses, %v errors; want 4 responses, no errors", responses, errs)
	}

	got := counter.Models()["gemini-streaming"]
	want := agentsv1.Usage{InputTokens: 10, OutputTokens: 6, TotalTokens: 16}
	if got != want {
		t.Fatalf("Models()[gemini-streaming] = %+v, want %+v (only the final, non-partial chunk's numbers)", got, want)
	}
}

func TestCountedNonStreamingSingleResponseCounted(t *testing.T) {
	fake := &scriptedLLM{
		name: "gemini-batch",
		script: []scriptedYield{
			{response: &model.LLMResponse{UsageMetadata: usageOf(5, 7)}},
		},
	}
	counter := NewCounter()
	ctx := WithCounter(context.Background(), counter)

	if _, errs := drain(Counted(fake).GenerateContent(ctx, &model.LLMRequest{}, false)); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	got := counter.Total()
	want := agentsv1.Usage{InputTokens: 5, OutputTokens: 7, TotalTokens: 12}
	if got != want {
		t.Fatalf("Total() = %+v, want %+v", got, want)
	}
}

func TestCountedIgnoresNilAndUsagelessResponses(t *testing.T) {
	fake := &scriptedLLM{
		name: "gemini-mixed",
		script: []scriptedYield{
			{response: nil},
			{response: &model.LLMResponse{}}, // no usage metadata
			{response: &model.LLMResponse{UsageMetadata: usageOf(7, 0)}},
		},
	}
	counter := NewCounter()
	ctx := WithCounter(context.Background(), counter)

	responses, _ := drain(Counted(fake).GenerateContent(ctx, &model.LLMRequest{}, false))
	if responses != 3 {
		t.Fatalf("drain() = %d responses, want 3 (nil and usageless ones still pass through)", responses)
	}
	if total := counter.Total(); total.InputTokens != 7 {
		t.Fatalf("Total().InputTokens = %d, want 7 (only the response carrying usage counted)", total.InputTokens)
	}
}

func TestCountedPassesErrorsThroughWithoutCountingTheirResponse(t *testing.T) {
	boom := errors.New("boom")
	fake := &scriptedLLM{
		name: "gemini-errors",
		script: []scriptedYield{
			// A response with usage paired with an error must not be counted:
			// the call did not finish cleanly.
			{response: &model.LLMResponse{UsageMetadata: usageOf(99, 99)}, err: boom},
			{err: boom},
		},
	}
	counter := NewCounter()
	ctx := WithCounter(context.Background(), counter)

	responses, errs := drain(Counted(fake).GenerateContent(ctx, &model.LLMRequest{}, false))
	if responses != 2 || len(errs) != 2 || errs[0] != boom || errs[1] != boom {
		t.Fatalf("drain() = %d responses, errors %v; want 2 responses, [boom boom] unchanged", responses, errs)
	}
	if total := counter.Total(); total != (agentsv1.Usage{}) {
		t.Fatalf("Total() = %+v, want zero (a response paired with an error is never counted)", total)
	}
}

func TestCountedWithoutCounterOnContextPassesThrough(t *testing.T) {
	fake := &scriptedLLM{
		name: "gemini-no-counter",
		script: []scriptedYield{
			{response: &model.LLMResponse{UsageMetadata: usageOf(3, 4)}},
		},
	}

	responses, errs := drain(Counted(fake).GenerateContent(context.Background(), &model.LLMRequest{}, false))
	if responses != 1 || len(errs) != 0 {
		t.Fatalf("drain() = %d responses, %v errors; want 1 response, no errors, no panic", responses, errs)
	}
}

func TestFromContextWithoutCounterReturnsNil(t *testing.T) {
	if counter := FromContext(context.Background()); counter != nil {
		t.Fatalf("FromContext(background) = %v, want nil", counter)
	}
}

func TestCountedStopsWhenConsumerStops(t *testing.T) {
	fake := &scriptedLLM{
		name: "gemini-stop-early",
		script: []scriptedYield{
			{response: &model.LLMResponse{UsageMetadata: usageOf(1, 0)}},
			{response: &model.LLMResponse{UsageMetadata: usageOf(2, 0)}},
			{response: &model.LLMResponse{UsageMetadata: usageOf(3, 0)}},
		},
	}
	counter := NewCounter()
	ctx := WithCounter(context.Background(), counter)

	seen := 0
	for range Counted(fake).GenerateContent(ctx, &model.LLMRequest{}, false) {
		seen++
		if seen == 1 {
			break
		}
	}

	if fake.yielded != 1 {
		t.Fatalf("fake.yielded = %d, want 1 (breaking out of the consumer loop must stop the inner iteration)", fake.yielded)
	}
	if total := counter.Total(); total.InputTokens != 1 {
		t.Fatalf("Total().InputTokens = %d, want 1 (only the one response actually yielded was counted)", total.InputTokens)
	}
}

func TestModelsAndTotal(t *testing.T) {
	counter := NewCounter()
	counter.Add("model-a", usageOf(10, 5))
	counter.Add("model-b", usageOf(100, 50))

	models := counter.Models()
	if len(models) != 2 {
		t.Fatalf("len(Models()) = %d, want 2", len(models))
	}
	if got, want := models["model-a"], (agentsv1.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}); got != want {
		t.Fatalf("Models()[model-a] = %+v, want %+v", got, want)
	}
	if got, want := models["model-b"], (agentsv1.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}); got != want {
		t.Fatalf("Models()[model-b] = %+v, want %+v", got, want)
	}

	if got, want := counter.Total(), (agentsv1.Usage{InputTokens: 110, OutputTokens: 55, TotalTokens: 165}); got != want {
		t.Fatalf("Total() = %+v, want %+v (sum of every model)", got, want)
	}

	// Models() must hand back a copy: mutating the result must not reach back
	// into the counter.
	models["model-a"] = agentsv1.Usage{InputTokens: 999}
	if got := counter.Models()["model-a"]; got.InputTokens == 999 {
		t.Fatalf("Models() returned a live map, not a copy")
	}
}

func TestAddMapsFieldsPerBillingRule(t *testing.T) {
	counter := NewCounter()
	counter.Add("model", &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:        10,
		ToolUsePromptTokenCount: 3,
		CandidatesTokenCount:    4,
		ThoughtsTokenCount:      2,
		CachedContentTokenCount: 6,
		// TotalTokenCount left zero on purpose, to exercise the fallback.
	})

	got := counter.Total()
	want := agentsv1.Usage{
		InputTokens:  13, // PromptTokenCount + ToolUsePromptTokenCount
		OutputTokens: 6,  // CandidatesTokenCount + ThoughtsTokenCount
		CachedTokens: 6,  // CachedContentTokenCount, already inside InputTokens
		TotalTokens:  19, // fallback: this call's input + output, since TotalTokenCount was 0
	}
	if got != want {
		t.Fatalf("Total() = %+v, want %+v", got, want)
	}
}

func TestAddTotalTokenCountWhenNonZeroIsUsedAsIs(t *testing.T) {
	counter := NewCounter()
	// An inconsistent TotalTokenCount (not literally input+output) must still
	// win over the fallback: the model's own total is authoritative.
	counter.Add("model", &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     10,
		CandidatesTokenCount: 5,
		TotalTokenCount:      999,
	})
	if got := counter.Total().TotalTokens; got != 999 {
		t.Fatalf("Total().TotalTokens = %d, want 999 (TotalTokenCount reported as-is when non-zero)", got)
	}
}

func TestAddConcurrent(t *testing.T) {
	counter := NewCounter()
	const goroutines = 50
	const perGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				counter.Add("shared-model", usageOf(1, 1))
			}
		}()
	}
	wg.Wait()

	const want = int32(goroutines * perGoroutine)
	got := counter.Total()
	if got.InputTokens != want || got.OutputTokens != want || got.TotalTokens != want*2 {
		t.Fatalf("Total() = %+v, want input=%d output=%d total=%d", got, want, want, want*2)
	}
}

func TestNilCounterIsSafe(t *testing.T) {
	var counter *Counter

	counter.Add("model", usageOf(1, 1))

	if models := counter.Models(); models == nil || len(models) != 0 {
		t.Fatalf("Models() on a nil *Counter = %#v, want a non-nil empty map", models)
	}
	if total := counter.Total(); total != (agentsv1.Usage{}) {
		t.Fatalf("Total() on a nil *Counter = %+v, want zero", total)
	}
}

func TestCountedNilReturnsNil(t *testing.T) {
	if got := Counted(nil); got != nil {
		t.Fatalf("Counted(nil) = %v, want nil", got)
	}
}

func TestCountedNamePassesThrough(t *testing.T) {
	fake := &scriptedLLM{name: "my-model"}
	if got := Counted(fake).Name(); got != "my-model" {
		t.Fatalf("Name() = %q, want %q", got, "my-model")
	}
}

func TestCountedForwardsGoogleLLMVariantWhenInnerHasIt(t *testing.T) {
	fake := &googleVariantLLM{scriptedLLM: &scriptedLLM{name: "vertex-model"}, variant: genai.BackendVertexAI}

	variant, ok := Counted(fake).(asGoogleLLM)
	if !ok {
		t.Fatalf("Counted(fake) does not implement GetGoogleLLMVariant, want it to when the inner model does")
	}
	if got := variant.GetGoogleLLMVariant(); got != genai.BackendVertexAI {
		t.Fatalf("GetGoogleLLMVariant() = %v, want %v", got, genai.BackendVertexAI)
	}
}

func TestCountedOmitsGoogleLLMVariantWhenInnerLacksIt(t *testing.T) {
	fake := &scriptedLLM{name: "plain-model"}

	if _, ok := Counted(fake).(asGoogleLLM); ok {
		t.Fatalf("Counted(fake) implements GetGoogleLLMVariant, want it not to when the inner model does not")
	}
}

func TestCountedCountsTheLastPartialUsageOfACallCutShort(t *testing.T) {
	counter := NewCounter()
	ctx := WithCounter(context.Background(), counter)
	cut := &scriptedLLM{name: "gemini-3.8-flash", script: []scriptedYield{
		{response: &model.LLMResponse{Partial: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 900, TotalTokenCount: 900}}},
		{response: &model.LLMResponse{Partial: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 900, CandidatesTokenCount: 30, TotalTokenCount: 930}}},
		{err: errors.New("stream broke")},
	}}
	for range Counted(cut).GenerateContent(ctx, &model.LLMRequest{}, true) {
	}
	if got := counter.Total(); got.InputTokens != 900 || got.OutputTokens != 30 || got.TotalTokens != 930 {
		t.Fatalf("total = %+v, want the last partial's usage", got)
	}

	// A consumer that stops early after the finished response counts it once.
	finished := NewCounter()
	whole := &scriptedLLM{name: "gemini-3.8-flash", script: []scriptedYield{
		{response: &model.LLMResponse{Partial: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, TotalTokenCount: 5}}},
		{response: &model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 2, TotalTokenCount: 12}}},
	}}
	for range Counted(whole).GenerateContent(WithCounter(context.Background(), finished), &model.LLMRequest{}, true) {
	}
	if got := finished.Total(); got.TotalTokens != 12 {
		t.Fatalf("total = %+v, want only the finished response", got)
	}
}

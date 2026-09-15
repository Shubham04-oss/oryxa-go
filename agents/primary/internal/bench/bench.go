// Package bench runs scripted tasks against the agents and measures what they
// cost: latency, time to first text, tokens, cache hits and the priced cost.
package bench

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// Task is one prompt handed to the agents.
type Task struct {
	Name   string
	Prompt string
}

// Usage accumulates token counts across model calls.
type Usage struct {
	InputTokens  int32
	OutputTokens int32
	CachedTokens int32
	ModelCalls   int32
}

// Add folds one model response's usage in. Cached tokens are part of the
// input tokens.
func (usage *Usage) Add(meta *genai.GenerateContentResponseUsageMetadata) {
	if meta == nil {
		return
	}
	usage.InputTokens += meta.PromptTokenCount
	usage.OutputTokens += meta.CandidatesTokenCount
	usage.CachedTokens += meta.CachedContentTokenCount
	usage.ModelCalls++
}

func (usage Usage) TotalTokens() int32 { return usage.InputTokens + usage.OutputTokens }

// ModelFor names the model that produced an event, so usage is priced on the
// right tier.
type ModelFor func(event *session.Event) string

// Pricing prices usage, as pricing.Table does.
type Pricing interface {
	Cost(model string, usage agentsv1.Usage) (agentsv1.Cost, agentsv1.ModelPrices, bool)
}

// Run is one measured task run. A run that failed keeps its task and attempt
// with Error set, so a report survives a quota or network failure.
type Run struct {
	Task        string
	Attempt     int
	Latency     time.Duration
	TimeToFirst time.Duration
	// Models holds the token usage per model, which is what pricing needs.
	Models   map[string]Usage
	Cost     agentsv1.Cost
	Unpriced []string
	Error    string
	// Answer is the root agent's final text, for inspecting a run.
	Answer string
}

// Usage is the sum of the run's models.
func (run Run) Usage() Usage {
	var total Usage
	for _, usage := range run.Models {
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		total.CachedTokens += usage.CachedTokens
		total.ModelCalls += usage.ModelCalls
	}
	return total
}

// Harness runs tasks against one ADK runner and prices what they used.
type Harness struct {
	Turns    *runner.Runner
	Sessions session.Service
	AppName  string
	UserID   string
	State    map[string]any
	ModelFor ModelFor
	Pricing  Pricing
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// OnEvent, when set, sees every event before it is measured.
	OnEvent func(event *session.Event)
}

// RunTask gives the agents one task in a fresh session and measures the run.
func (harness *Harness) RunTask(ctx context.Context, task Task, attempt int) (Run, error) {
	now := harness.Now
	if now == nil {
		now = time.Now
	}
	created, err := harness.Sessions.Create(ctx, &session.CreateRequest{
		AppName: harness.AppName, UserID: harness.UserID, State: harness.State,
	})
	if err != nil {
		return Run{Task: task.Name, Attempt: attempt, Error: err.Error()}, fmt.Errorf("create session: %w", err)
	}

	start := now()
	models := map[string]Usage{}
	var timeToFirst time.Duration
	var answer string
	sawText := false
	for event, err := range harness.Turns.Run(ctx, harness.UserID, created.Session.ID(), genai.NewContentFromText(task.Prompt, genai.RoleUser), agent.RunConfig{StreamingMode: agent.StreamingModeSSE}) {
		if err != nil {
			return Run{Task: task.Name, Attempt: attempt, Latency: now().Sub(start), Error: err.Error()}, fmt.Errorf("run %s: %w", task.Name, err)
		}
		if event == nil {
			continue
		}
		if harness.OnEvent != nil {
			harness.OnEvent(event)
		}
		// Streaming yields partial chunks that repeat the usage metadata; only
		// the non-partial aggregate counts, once per model call.
		if meta := event.UsageMetadata; meta != nil && !event.Partial {
			model := harness.ModelFor(event)
			usage := models[model]
			usage.Add(meta)
			models[model] = usage
		}
		if event.Author == harness.AppName && event.Content != nil {
			if text := contentText(event.Content); text != "" {
				if !sawText {
					timeToFirst = now().Sub(start)
					sawText = true
				}
				if !event.Partial {
					answer = text
				}
			}
		}
	}

	run := Run{Task: task.Name, Attempt: attempt, Latency: now().Sub(start), TimeToFirst: timeToFirst, Models: models, Answer: answer}
	run.Cost, run.Unpriced = harness.price(models)
	return run, nil
}

func (harness *Harness) price(models map[string]Usage) (agentsv1.Cost, []string) {
	var cost agentsv1.Cost
	var unpriced []string
	for model, usage := range models {
		priced, _, ok := harness.Pricing.Cost(model, agentsv1.Usage{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			CachedTokens: usage.CachedTokens,
			TotalTokens:  usage.TotalTokens(),
		})
		if !ok {
			unpriced = append(unpriced, model)
			continue
		}
		cost.InputMicros += priced.InputMicros
		cost.OutputMicros += priced.OutputMicros
		cost.CachedMicros += priced.CachedMicros
		cost.TotalMicros += priced.TotalMicros
	}
	sort.Strings(unpriced)
	return cost, unpriced
}

// Summary compares runs that a cache served with runs it did not.
type Summary struct {
	Runs          int
	FailedRuns    int
	CachedRuns    int
	CachedLatency time.Duration
	ColdRuns      int
	ColdLatency   time.Duration
	Usage         Usage
	Cost          agentsv1.Cost
	// Unpriced names any model the price table did not know.
	Unpriced map[string]bool
}

// Summarize folds the runs into totals and cache-hit versus cold latency.
// Failed runs count but contribute no usage or latency.
func Summarize(runs []Run) Summary {
	summary := Summary{Unpriced: map[string]bool{}}
	var cachedTotal, coldTotal time.Duration
	for _, run := range runs {
		summary.Runs++
		if run.Error != "" {
			summary.FailedRuns++
			continue
		}
		usage := run.Usage()
		summary.Usage.InputTokens += usage.InputTokens
		summary.Usage.OutputTokens += usage.OutputTokens
		summary.Usage.CachedTokens += usage.CachedTokens
		summary.Usage.ModelCalls += usage.ModelCalls
		summary.Cost.InputMicros += run.Cost.InputMicros
		summary.Cost.OutputMicros += run.Cost.OutputMicros
		summary.Cost.CachedMicros += run.Cost.CachedMicros
		summary.Cost.TotalMicros += run.Cost.TotalMicros
		for _, model := range run.Unpriced {
			summary.Unpriced[model] = true
		}
		if usage.CachedTokens > 0 {
			summary.CachedRuns++
			cachedTotal += run.Latency
		} else {
			summary.ColdRuns++
			coldTotal += run.Latency
		}
	}
	if summary.CachedRuns > 0 {
		summary.CachedLatency = cachedTotal / time.Duration(summary.CachedRuns)
	}
	if summary.ColdRuns > 0 {
		summary.ColdLatency = coldTotal / time.Duration(summary.ColdRuns)
	}
	return summary
}

// Dollars renders micros of a US dollar.
func Dollars(micros int64) string {
	return fmt.Sprintf("$%.6f", float64(micros)/1_000_000)
}

// WriteReport writes the per-run table and the summary.
func WriteReport(writer io.Writer, runs []Run, summary Summary, cacheEnabled bool) {
	fmt.Fprintf(writer, "%-8s %3s %10s %10s %8s %8s %7s %11s %s\n",
		"task", "att", "latency", "ttft", "input", "cached", "output", "cost", "models")
	for _, run := range runs {
		if run.Error != "" {
			fmt.Fprintf(writer, "%-8s %3d %10s %10s %8s %8s %7s %11s failed: %s\n",
				run.Task, run.Attempt, run.Latency.Round(time.Millisecond), "-", "-", "-", "-", "-", firstLine(run.Error))
			continue
		}
		usage := run.Usage()
		var models []string
		for model, calls := range run.Models {
			models = append(models, fmt.Sprintf("%s x%d", model, calls.ModelCalls))
		}
		sort.Strings(models)
		fmt.Fprintf(writer, "%-8s %3d %10s %10s %8d %8d %7d %11s %s\n",
			run.Task, run.Attempt, run.Latency.Round(time.Millisecond), run.TimeToFirst.Round(time.Millisecond),
			usage.InputTokens, usage.CachedTokens, usage.OutputTokens, Dollars(run.Cost.TotalMicros), strings.Join(models, ", "))
	}

	fmt.Fprintf(writer, "\nruns %d · tokens in %d (cached %d) out %d · model calls %d · cost %s\n",
		summary.Runs, summary.Usage.InputTokens, summary.Usage.CachedTokens, summary.Usage.OutputTokens,
		summary.Usage.ModelCalls, Dollars(summary.Cost.TotalMicros))
	if summary.FailedRuns > 0 {
		fmt.Fprintf(writer, "failed runs %d (excluded from the averages and cost)\n", summary.FailedRuns)
	}
	if summary.ColdRuns > 0 {
		fmt.Fprintf(writer, "cold runs %d · average %s\n", summary.ColdRuns, summary.ColdLatency.Round(time.Millisecond))
	}
	if summary.CachedRuns > 0 {
		fmt.Fprintf(writer, "cache-hit runs %d · average %s\n", summary.CachedRuns, summary.CachedLatency.Round(time.Millisecond))
	} else if cacheEnabled {
		fmt.Fprintln(writer, "no run reported cached tokens; the prefix may be under the cache floor or the cache was just created")
	}
	if len(summary.Unpriced) > 0 {
		var models []string
		for model := range summary.Unpriced {
			models = append(models, model)
		}
		sort.Strings(models)
		fmt.Fprintf(writer, "unpriced models (cost is incomplete): %s\n", strings.Join(models, ", "))
	}
}

func contentText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var text string
	for _, part := range content.Parts {
		if part != nil && !part.Thought {
			text += part.Text
		}
	}
	return text
}

// firstLine trims an error to one short line for the report.
func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	const limit = 120
	if len(text) > limit {
		text = text[:limit] + "…"
	}
	return text
}

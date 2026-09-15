package bench

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

func TestUsageAddsUp(t *testing.T) {
	var usage Usage
	usage.Add(&genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 20, CachedContentTokenCount: 80})
	usage.Add(&genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 50, CandidatesTokenCount: 10})
	usage.Add(nil)

	if usage.InputTokens != 150 || usage.OutputTokens != 30 || usage.CachedTokens != 80 || usage.ModelCalls != 2 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.TotalTokens() != 180 {
		t.Fatalf("TotalTokens() = %d", usage.TotalTokens())
	}
}

func TestSummarizeSplitsCacheHitsFromColdRuns(t *testing.T) {
	runs := []Run{
		{Task: "chat", Attempt: 1, Latency: 2 * time.Second, Models: map[string]Usage{"main": {InputTokens: 1000, OutputTokens: 100}}, Cost: agentsv1.Cost{TotalMicros: 500}},
		{Task: "chat", Attempt: 2, Latency: time.Second, Models: map[string]Usage{"main": {InputTokens: 1000, CachedTokens: 900, OutputTokens: 100}}, Cost: agentsv1.Cost{TotalMicros: 400}},
		{Task: "plan", Attempt: 1, Latency: 3 * time.Second, Models: map[string]Usage{"lite": {InputTokens: 500, CachedTokens: 400, OutputTokens: 50}}, Cost: agentsv1.Cost{TotalMicros: 100}, Unpriced: []string{"mystery"}},
	}
	summary := Summarize(runs)

	if summary.Runs != 3 || summary.CachedRuns != 2 || summary.ColdRuns != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.ColdLatency != 2*time.Second || summary.CachedLatency != 2*time.Second {
		t.Fatalf("latencies: cold %v, cached %v", summary.ColdLatency, summary.CachedLatency)
	}
	if summary.Usage.InputTokens != 2500 || summary.Usage.CachedTokens != 1300 || summary.Usage.OutputTokens != 250 || summary.Usage.ModelCalls != 0 {
		t.Fatalf("usage = %+v", summary.Usage)
	}
	if summary.Cost.TotalMicros != 1000 {
		t.Fatalf("cost = %+v", summary.Cost)
	}
	if !summary.Unpriced["mystery"] {
		t.Fatalf("unpriced = %v", summary.Unpriced)
	}
}

func TestWriteReportNamesTheNumbers(t *testing.T) {
	runs := []Run{{
		Task: "chat", Attempt: 1, Latency: 1500 * time.Millisecond, TimeToFirst: time.Second,
		Models: map[string]Usage{"gemini-3.8-flash": {InputTokens: 1000, CachedTokens: 800, OutputTokens: 100, ModelCalls: 2}},
		Cost:   agentsv1.Cost{TotalMicros: 1234},
	}}
	var report bytes.Buffer
	WriteReport(&report, runs, Summarize(runs), true)

	for _, want := range []string{"latency", "ttft", "1000", "800", "100", "$0.001234", "gemini-3.8-flash x2", "cache-hit runs 1"} {
		if !strings.Contains(report.String(), want) {
			t.Fatalf("report is missing %q:\n%s", want, report.String())
		}
	}
}

func TestWriteReportWarnsAboutUnpricedModels(t *testing.T) {
	runs := []Run{{Task: "chat", Models: map[string]Usage{"mystery": {InputTokens: 10}}, Unpriced: []string{"mystery"}}}
	var report bytes.Buffer
	WriteReport(&report, runs, Summarize(runs), false)
	if !strings.Contains(report.String(), "unpriced models") {
		t.Fatalf("report does not warn about unpriced models:\n%s", report.String())
	}
}

func TestFailedRunsAreCountedButNotAveraged(t *testing.T) {
	runs := []Run{
		{Task: "chat", Attempt: 1, Latency: 2 * time.Second, Models: map[string]Usage{"main": {InputTokens: 100}}},
		{Task: "chat", Attempt: 2, Latency: 5 * time.Second, Error: "429 quota exceeded"},
	}
	summary := Summarize(runs)
	if summary.Runs != 2 || summary.FailedRuns != 1 || summary.ColdRuns != 1 || summary.ColdLatency != 2*time.Second {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Usage.InputTokens != 100 {
		t.Fatalf("a failed run leaked usage: %+v", summary.Usage)
	}
	var report bytes.Buffer
	WriteReport(&report, runs, summary, true)
	if !strings.Contains(report.String(), "failed: 429 quota exceeded") {
		t.Fatalf("report is missing the failure:\n%s", report.String())
	}
}

func TestContentTextSkipsThoughts(t *testing.T) {
	content := &genai.Content{Parts: []*genai.Part{
		{Text: "thinking", Thought: true},
		{Text: "answer"},
		nil,
	}}
	if got := contentText(content); got != "answer" {
		t.Fatalf("contentText() = %q", got)
	}
}

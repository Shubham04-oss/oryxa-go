package judge

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// TestLiveGeminiClassification is opt-in because it calls a billed Vertex AI
// model. It checks the response schema and category path used by ingestion.
func TestLiveGeminiClassification(t *testing.T) {
	if os.Getenv("KNOWLEDGE_LIVE_JUDGE") != "1" {
		t.Skip("set KNOWLEDGE_LIVE_JUDGE=1 to call Vertex AI")
	}
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		t.Fatal("GOOGLE_CLOUD_PROJECT is required")
	}
	model := os.Getenv("KNOWLEDGE_LIVE_JUDGE_MODEL")
	if model == "" {
		model = "gemini-3.5-flash-lite"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	classifier, err := New(ctx, Config{Project: project, Location: "global", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := classifier.Judge(ctx, domain.Candidate{
		Provider: "gmail", Kind: "message", Title: "Invoice INV-TEST-48213",
		MIME: "text/plain", Text: "Invoice INV-TEST-48213 for September design services. Total due: 128 dollars by October 15.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.Keep || !slices.Contains(categoryNames(), verdict.Category) || verdict.Category == "" {
		t.Fatalf("Gemini verdict = keep=%t category=%q reason=%q", verdict.Keep, verdict.Category, verdict.Reason)
	}
	t.Logf("Gemini classified synthetic Gmail invoice: keep=%t category=%s", verdict.Keep, verdict.Category)
}

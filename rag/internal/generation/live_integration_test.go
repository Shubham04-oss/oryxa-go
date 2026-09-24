package generation

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// TestLiveConverseAnswer is opt-in because it calls a billed Bedrock model.
// It exercises the same grounded prompt and response parser as RAG answers.
func TestLiveConverseAnswer(t *testing.T) {
	if os.Getenv("RAG_LIVE_CONVERSE") != "1" {
		t.Skip("set RAG_LIVE_CONVERSE=1 to call Bedrock Converse")
	}
	model := os.Getenv("RAG_LIVE_CONVERSE_MODEL")
	if model == "" {
		model = "us.amazon.nova-micro-v1:0"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	config, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answerer, err := NewBedrock(bedrockruntime.NewFromConfig(config), model, BedrockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := answerer.Generate(ctx, Request{
		Question:  "What is the approval code?",
		Passages:  []Passage{{Index: 1, Title: "Synthetic approval note", Text: "The approval code is 7319."}},
		MaxTokens: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.Text, "7319") || len(Cited(answer.Text, 1)) == 0 {
		t.Fatalf("Converse did not answer from and cite the passage: %q", answer.Text)
	}
	if answer.Usage.InputTokens <= 0 || answer.Usage.OutputTokens <= 0 {
		t.Fatalf("Converse returned no token usage: %+v", answer.Usage)
	}
	t.Logf("Converse cited the synthetic passage using %d input and %d output tokens", answer.Usage.InputTokens, answer.Usage.OutputTokens)
}

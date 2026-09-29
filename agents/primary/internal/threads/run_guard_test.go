package threads

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
)

func TestPublicationGuardUsesOriginalQuestionAndOnlyMemberContext(t *testing.T) {
	history := []*session.Event{}
	for _, said := range []string{"The launch is Thursday.", "What time?"} {
		event := &session.Event{Author: adkUserAuthor}
		event.Content = genai.NewContentFromText(said, genai.RoleUser)
		history = append(history, event)
	}
	private := &session.Event{Author: "krude"}
	private.Content = genai.NewContentFromText("A private tool result repeated by the agent.", genai.RoleModel)
	history = append(history, private, &session.Event{Author: adkUserAuthor})
	got := publicationGuardInput(history, "")
	if got.Request != "What time?" || !slices.Equal(got.Context, []string{"The launch is Thursday."}) || !slices.Equal(got.Sources, []string{relevance.ThreadHistory}) {
		t.Fatalf("confirmation guard input = %+v", got)
	}
	ctx := primaryagents.WithRunGuard(context.Background(), got)
	if primaryagents.GuardRun(ctx).Request != "What time?" {
		t.Fatal("guard input lost on context")
	}
	fresh := publicationGuardInput(nil, "New question")
	if fresh.Request != "New question" || len(fresh.Sources) != 0 || len(fresh.Context) != 0 {
		t.Fatalf("fresh input = %+v", fresh)
	}
	second := publicationGuardInput(history, "Another question")
	if second.Request != "Another question" || !slices.Equal(second.Context, []string{"The launch is Thursday.", "What time?"}) {
		t.Fatalf("reused input = %+v", second)
	}
}

func TestPublicationContextKeepsOnlyBoundedRecentMemberMessages(t *testing.T) {
	var history []*session.Event
	for i := range 50 {
		event := &session.Event{Author: adkUserAuthor}
		event.Content = genai.NewContentFromText(fmt.Sprintf("%d %s", i, strings.Repeat("é", 2_000)), genai.RoleUser)
		history = append(history, event)
	}
	input := publicationGuardInput(history, "New question")
	if len(input.Context) != relevance.MaxContextLines || !strings.HasPrefix(input.Context[0], "44 ") || !strings.HasPrefix(input.Context[5], "49 ") {
		t.Fatalf("context does not keep its latest six messages")
	}
	for _, line := range input.Context {
		if len([]rune(line)) != relevance.MaxContextLineRunes {
			t.Fatal("context line is not bounded")
		}
	}
}

type guardObservingModel struct {
	model.LLM
	inputs []primaryagents.RunGuardInput
}

func (llm *guardObservingModel) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	llm.inputs = append(llm.inputs, primaryagents.GuardRun(ctx))
	return llm.LLM.GenerateContent(ctx, request, stream)
}

func TestThreadExecutionCarriesItsOwnGuardContext(t *testing.T) {
	llm := &guardObservingModel{LLM: &scriptedModel{respond: func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("A private agent reply must not become request context.", genai.RoleModel), nil
	}}}
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: llm, Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{AppName: "krude", Agent: krude, Sessions: session.InMemoryService(), Runs: newFakeRuns(), Prices: prices})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"First question", "Second question"} {
		run, err := service.StartRun(context.Background(), asha, thread.ID, RunInput{Message: question})
		if err != nil {
			t.Fatal(err)
		}
		run.Execute(context.Background(), func(Item) error { return nil })
	}
	if len(llm.inputs) != 2 {
		t.Fatalf("model calls=%d", len(llm.inputs))
	}
	first, second := llm.inputs[0], llm.inputs[1]
	if first.Request != "First question" || len(first.Sources) != 0 || len(first.Context) != 0 {
		t.Fatalf("first=%+v", first)
	}
	if second.Request != "Second question" || !slices.Equal(second.Sources, []string{relevance.ThreadHistory}) || !slices.Equal(second.Context, []string{"First question"}) {
		t.Fatalf("second=%+v", second)
	}
}

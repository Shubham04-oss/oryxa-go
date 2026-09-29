package agents

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
)

// functionResults is the turn ADK stores for the results of function calls.
func functionResults(results ...*genai.FunctionResponse) *genai.Content {
	content := &genai.Content{Role: genai.RoleUser}
	for _, result := range results {
		content.Parts = append(content.Parts, &genai.Part{FunctionResponse: result})
	}
	return content
}

// assertInterrupted fails the test unless content is a user turn holding one
// interrupted response for each of the named functions, in order.
func assertInterrupted(t *testing.T, content *genai.Content, names ...string) {
	t.Helper()
	if content == nil || content.Role != genai.RoleUser || len(content.Parts) != len(names) {
		t.Fatalf("turn = %+v, want a user turn of %d interrupted responses", content, len(names))
	}
	for i, name := range names {
		response := content.Parts[i].FunctionResponse
		if response == nil || response.Name != name || response.Response["error"] != interruptedResult {
			t.Fatalf("part %d = %+v, want an interrupted response for %q", i, content.Parts[i], name)
		}
	}
}

// snapshot is contents as JSON, to compare a history before and after.
func snapshot(t *testing.T, contents []*genai.Content) string {
	t.Helper()
	encoded, err := json.Marshal(contents)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestAnswerDanglingCallsAnswersACallNobodyAnswered(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("send it", genai.RoleUser),
		functionCalls(&genai.FunctionCall{Name: "send_message", Args: map[string]any{"to": "sam"}}),
		genai.NewContentFromText("are you there?", genai.RoleUser),
	}

	repaired := answerDanglingCalls(contents)

	if len(repaired) != 4 || repaired[0] != contents[0] || repaired[1] != contents[1] || repaired[3] != contents[2] {
		t.Fatalf("repaired = %v, want the turns as they were with one added after the call", repaired)
	}
	assertInterrupted(t, repaired[2], "send_message")
}

func TestAnswerDanglingCallsAnswersACallEndingTheHistory(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("send it", genai.RoleUser),
		functionCalls(&genai.FunctionCall{Name: "send_message"}),
	}

	repaired := answerDanglingCalls(contents)

	if len(repaired) != 3 || repaired[1] != contents[1] {
		t.Fatalf("repaired = %v, want the response added after the last call", repaired)
	}
	assertInterrupted(t, repaired[2], "send_message")
}

func TestAnswerDanglingCallsAnswersEveryCallOfATurn(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("check both", genai.RoleUser),
		functionCalls(&genai.FunctionCall{Name: "get_weather"}, &genai.FunctionCall{Name: "get_time"}),
		genai.NewContentFromText("hello?", genai.RoleUser),
	}

	repaired := answerDanglingCalls(contents)

	if len(repaired) != 4 {
		t.Fatalf("repaired = %v, want one turn added", repaired)
	}
	assertInterrupted(t, repaired[2], "get_weather", "get_time")
}

func TestAnswerDanglingCallsAnswersEveryTurnOfCalls(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("go", genai.RoleUser),
		functionCalls(&genai.FunctionCall{Name: "first"}),
		functionCalls(&genai.FunctionCall{Name: "second"}),
		genai.NewContentFromText("again", genai.RoleUser),
	}

	repaired := answerDanglingCalls(contents)

	if len(repaired) != 6 || repaired[1] != contents[1] || repaired[3] != contents[2] || repaired[5] != contents[3] {
		t.Fatalf("repaired = %v, want a response after each turn of calls", repaired)
	}
	assertInterrupted(t, repaired[2], "first")
	assertInterrupted(t, repaired[4], "second")
}

func TestAnswerDanglingCallsLeavesAnsweredCallsAlone(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("check both", genai.RoleUser),
		functionCalls(&genai.FunctionCall{Name: "get_weather"}, &genai.FunctionCall{Name: "get_time"}),
		functionResults(
			&genai.FunctionResponse{Name: "get_weather", Response: map[string]any{"output": "rain"}},
			&genai.FunctionResponse{Name: "get_time", Response: map[string]any{"output": "noon"}},
		),
		genai.NewContentFromText("Rain at noon.", genai.RoleModel),
	}

	if repaired := answerDanglingCalls(contents); &repaired[0] != &contents[0] {
		t.Fatalf("a history with every call answered was copied: %v", repaired)
	}
}

// Vertex counts a turn's responses against its calls, so a turn with as many
// as there are calls is not touched even when they are named differently.
func TestAnswerDanglingCallsCountsResponsesAgainstCalls(t *testing.T) {
	contents := []*genai.Content{
		functionCalls(&genai.FunctionCall{Name: "get_weather"}),
		functionResults(&genai.FunctionResponse{Name: "weather", Response: map[string]any{"output": "rain"}}),
	}

	if repaired := answerDanglingCalls(contents); &repaired[0] != &contents[0] {
		t.Fatalf("a turn with a response for each call was changed: %v", repaired)
	}
}

func TestAnswerDanglingCallsCompletesAPartlyAnsweredTurnByNameAndOrder(t *testing.T) {
	weather := &genai.FunctionResponse{Name: "get_weather", Response: map[string]any{"output": "rain"}}
	answers := functionResults(weather)
	contents := []*genai.Content{
		functionCalls(&genai.FunctionCall{Name: "get_time"}, &genai.FunctionCall{Name: "get_weather"}),
		answers,
		genai.NewContentFromText("done?", genai.RoleUser),
	}

	repaired := answerDanglingCalls(contents)

	if len(repaired) != 3 || repaired[0] != contents[0] || repaired[2] != contents[2] {
		t.Fatalf("repaired = %v, want the same turns with the answers completed", repaired)
	}
	completed := repaired[1]
	if completed.Role != genai.RoleUser || len(completed.Parts) != 2 {
		t.Fatalf("turn = %+v, want a response for each of the two calls", completed)
	}
	if response := completed.Parts[0].FunctionResponse; response.Name != "get_time" || response.Response["error"] != interruptedResult {
		t.Fatalf("first response = %+v, want the interrupted one for the first call", response)
	}
	if completed.Parts[1].FunctionResponse != weather {
		t.Fatalf("second response = %+v, want the real one kept as it was", completed.Parts[1].FunctionResponse)
	}
	if len(answers.Parts) != 1 {
		t.Fatalf("the stored turn changed: %+v", answers.Parts)
	}
}

func TestAnswerDanglingCallsMatchesByIDWhereThereIsOne(t *testing.T) {
	second := &genai.FunctionResponse{ID: "call-2", Name: "lookup", Response: map[string]any{"output": "b"}}
	contents := []*genai.Content{
		functionCalls(&genai.FunctionCall{ID: "call-1", Name: "lookup"}, &genai.FunctionCall{ID: "call-2", Name: "lookup"}),
		functionResults(second),
	}

	repaired := answerDanglingCalls(contents)

	parts := repaired[1].Parts
	if len(repaired) != 2 || len(parts) != 2 {
		t.Fatalf("repaired = %v, want one turn of two responses", repaired)
	}
	if response := parts[0].FunctionResponse; response.ID != "call-1" || response.Response["error"] != interruptedResult {
		t.Fatalf("first response = %+v, want the interrupted one for call-1, which carries its id", response)
	}
	if parts[1].FunctionResponse != second {
		t.Fatalf("second response = %+v, want the one for call-2 kept", parts[1].FunctionResponse)
	}
}

func TestAnswerDanglingCallsTakesCallsOfOneNameInOrder(t *testing.T) {
	real := &genai.FunctionResponse{Name: "lookup", Response: map[string]any{"output": "a"}}
	contents := []*genai.Content{
		functionCalls(&genai.FunctionCall{Name: "lookup"}, &genai.FunctionCall{Name: "lookup"}),
		functionResults(real),
	}

	parts := answerDanglingCalls(contents)[1].Parts

	if len(parts) != 2 || parts[0].FunctionResponse != real || parts[1].FunctionResponse.Response["error"] != interruptedResult {
		t.Fatalf("parts = %+v, want the response for the first call and an interrupted one for the second", parts)
	}
}

func TestAnswerDanglingCallsDoesNotChangeWhatItIsGiven(t *testing.T) {
	contents := []*genai.Content{
		genai.NewContentFromText("go", genai.RoleUser),
		functionCalls(&genai.FunctionCall{Name: "first"}),
		genai.NewContentFromText("then", genai.RoleUser),
		functionCalls(&genai.FunctionCall{Name: "get_time"}, &genai.FunctionCall{Name: "get_weather"}),
		functionResults(&genai.FunctionResponse{Name: "get_weather", Response: map[string]any{"output": "rain"}}),
	}
	before := snapshot(t, contents)
	held := slices.Clone(contents)

	repaired := answerDanglingCalls(contents)

	if len(repaired) == len(contents) {
		t.Fatal("nothing was repaired, so this test proves nothing")
	}
	if after := snapshot(t, contents); after != before {
		t.Fatalf("the history changed:\nbefore %s\nafter  %s", before, after)
	}
	if !slices.Equal(contents, held) {
		t.Fatal("the slice of turns changed")
	}
}

func TestAnswerDanglingCallsToleratesEmptyTurnsAndParts(t *testing.T) {
	contents := []*genai.Content{
		nil,
		{Role: genai.RoleModel},
		{Role: genai.RoleModel, Parts: []*genai.Part{nil, {FunctionCall: &genai.FunctionCall{Name: "lookup"}}}},
		nil,
	}

	repaired := answerDanglingCalls(contents)

	if len(repaired) != 5 {
		t.Fatalf("repaired = %v, want a response after the one call", repaired)
	}
	assertInterrupted(t, repaired[3], "lookup")
	if got := answerDanglingCalls(nil); got != nil {
		t.Fatalf("answerDanglingCalls(nil) = %v", got)
	}
}

func TestRepairedHistoryAnswersDanglingCalls(t *testing.T) {
	recorder := &requestRecorder{}
	llm := repairedHistory{recorder}
	req := &model.LLMRequest{Contents: []*genai.Content{
		genai.NewContentFromText("hi", genai.RoleUser),
		streamedAnswer("Hi.", "sig"),
		functionCalls(&genai.FunctionCall{Name: "send_message"}),
		genai.NewContentFromText("still there?", genai.RoleUser),
	}}

	for range llm.GenerateContent(context.Background(), req, true) {
	}

	sent := recorder.requests[0].Contents
	if len(sent) != 5 {
		t.Fatalf("sent %d turns, want the history with a response added", len(sent))
	}
	assertInterrupted(t, sent[3], "send_message")
	if len(sent[1].Parts) != 1 || string(sent[1].Parts[0].ThoughtSignature) != "sig" {
		t.Fatalf("answer = %+v, want its signature folded onto its text as well", sent[1].Parts)
	}
}

// refusesDanglingCalls answers a request as Vertex does one whose function
// call turn is not followed by a response for each call: with a 400.
type refusesDanglingCalls struct {
	model.LLM
}

func (m refusesDanglingCalls) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	for index, content := range req.Contents {
		calls := callParts(content.Parts)
		if len(calls) == 0 {
			continue
		}
		responses := 0
		if index+1 < len(req.Contents) {
			responses = len(responseParts(req.Contents[index+1].Parts))
		}
		if responses != len(calls) {
			return func(yield func(*model.LLMResponse, error) bool) {
				yield(nil, errors.New("400 INVALID_ARGUMENT: the number of function response parts must equal the number of function call parts of the function call turn"))
			}
		}
	}
	return m.LLM.GenerateContent(ctx, req, stream)
}

// A run cut off between a model turn's function call and its response leaves
// the thread ending in a call. The member's next message must still get an
// answer, through ADK's own history handling, which keeps such a call as it is.
func TestAThreadStillAnswersAfterARunWasInterruptedMidCall(t *testing.T) {
	ctx := context.Background()
	tree := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Yes, I am here.", genai.RoleModel)
	})
	h := newHarness(t, repairedHistory{refusesDanglingCalls{tree}})
	created, err := h.sessions.Create(ctx, &session.CreateRequest{AppName: RootName, UserID: "member_1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range []struct {
		author  string
		content *genai.Content
	}{
		{"user", genai.NewContentFromText("Send Sam the notes.", genai.RoleUser)},
		{RootName, functionCalls(&genai.FunctionCall{ID: "adk-1", Name: "list_tools"})},
	} {
		event := session.NewEvent(ctx, "interrupted-run")
		event.Author, event.Content = stored.author, stored.content
		if err := h.sessions.AppendEvent(ctx, created.Session, event); err != nil {
			t.Fatal(err)
		}
	}

	var answer strings.Builder
	for event, err := range h.turns.Run(ctx, "member_1", created.Session.ID(), genai.NewContentFromText("Are you there?", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if event.Author == RootName && event.Content != nil {
			answer.WriteString(text(event.Content))
		}
	}

	if answer.String() != "Yes, I am here." {
		t.Fatalf("answer = %q", answer.String())
	}
	contents := tree.requestsFor(RootName)[0].Contents
	if len(contents) != 4 {
		t.Fatalf("the model was sent %d turns, want the stored two, the interrupted call's response and the new message", len(contents))
	}
	assertInterrupted(t, contents[2], "list_tools")
	if got := text(contents[3]); got != "Are you there?" {
		t.Fatalf("last turn = %q, want the member's new message", got)
	}
}

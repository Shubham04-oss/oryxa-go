package generation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/auth"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

func testMatches(count int) []domain.Match {
	matches := make([]domain.Match, 0, count)
	for index := range count {
		matches = append(matches, domain.Match{
			Chunk: domain.Chunk{
				ID:          "chk_" + string(rune('a'+index)),
				DocumentID:  "doc_" + string(rune('a'+index)),
				Text:        "Passage number " + string(rune('1'+index)) + ".",
				HeadingPath: []string{"Security", "Credentials"},
				StartByte:   index * 100,
				EndByte:     index*100 + 20,
			},
			Document: domain.Ref{
				ID: "doc_" + string(rune('a'+index)), Title: "Doc " + string(rune('A'+index)),
				Source: domain.Source{Kind: domain.SourceInline},
			},
			Score: 1 - float64(index)/10,
		})
	}
	return matches
}

// stubGenerator answers with whatever a test set up, and records the prompt.
type stubGenerator struct {
	answer  string
	err     error
	system  string
	user    string
	request Request
	calls   int
}

func (stub *stubGenerator) Model() string { return "test-model" }

func (stub *stubGenerator) Generate(_ context.Context, request Request) (Response, error) {
	stub.calls++
	stub.request = request
	stub.system, stub.user = Prompt(request)
	if stub.err != nil {
		return Response{}, stub.err
	}
	return Response{Text: stub.answer, Usage: domain.Usage{InputTokens: 100, OutputTokens: 20}}, nil
}

func TestAnswerReturnsOnlyTheSourcesItUsed(t *testing.T) {
	t.Parallel()
	stub := &stubGenerator{answer: "Credentials rotate every ninety days [2]. A failure pages on-call [2][4]."}
	answer, err := Answer(context.Background(), stub, "how often do credentials rotate?", testMatches(5), "")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !answer.Grounded {
		t.Fatal("an answer with citations is not marked grounded")
	}
	// Listing every retrieved passage would make the answer look better
	// supported than it is.
	if len(answer.Citations) != 2 {
		t.Fatalf("got %d citations, want the 2 the answer used", len(answer.Citations))
	}
	if answer.Citations[0].ChunkID != "chk_b" || answer.Citations[1].ChunkID != "chk_d" {
		t.Fatalf("citations point at %s and %s", answer.Citations[0].ChunkID, answer.Citations[1].ChunkID)
	}
	// A citation has to be checkable against the document it came from.
	if answer.Citations[0].StartByte != 100 || answer.Citations[0].EndByte != 120 {
		t.Fatalf("citation lost its byte range: %+v", answer.Citations[0])
	}
	if answer.Citations[0].Title != "Doc B" {
		t.Fatalf("citation title = %q", answer.Citations[0].Title)
	}
	if answer.Model != "test-model" || answer.Usage.OutputTokens != 20 {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestAnswerDropsAnInventedCitation(t *testing.T) {
	t.Parallel()
	// Three sources were supplied; the model cited a seventh. That one is
	// dropped rather than rendered as a link to a passage nobody saw.
	stub := &stubGenerator{answer: "The policy is ninety days [1], reviewed annually [7]."}
	answer, err := Answer(context.Background(), stub, "q", testMatches(3), "")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if len(answer.Citations) != 1 || answer.Citations[0].Index != 1 {
		t.Fatalf("citations = %+v, want only the real one", answer.Citations)
	}
	// The answer is still worth showing: it was right about the source it did
	// have, and the invention is simply not rendered.
	if !strings.Contains(answer.Text, "ninety days") {
		t.Fatalf("the answer text was lost: %q", answer.Text)
	}
}

func TestAnswerWithNothingRetrievedCallsNoModel(t *testing.T) {
	t.Parallel()
	// A model given no sources and asked a question answers from memory,
	// which is the exact failure this package exists to prevent.
	stub := &stubGenerator{answer: "I happen to know the answer anyway."}
	answer, err := Answer(context.Background(), stub, "q", nil, "")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if stub.calls != 0 {
		t.Fatal("a model was called with no sources")
	}
	if answer.Grounded {
		t.Fatal("an answer with no sources is marked grounded")
	}
	if len(answer.Citations) != 0 {
		t.Fatalf("citations = %+v", answer.Citations)
	}
}

func TestAnswerWithNoCitationsIsNotGrounded(t *testing.T) {
	t.Parallel()
	// The model was given sources and answered without citing any. That is
	// either a refusal or an answer from memory; either way it is not
	// grounded, and saying so is the difference between a usable product and
	// a confident one.
	stub := &stubGenerator{answer: "The sources do not say."}
	answer, err := Answer(context.Background(), stub, "q", testMatches(3), "")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if answer.Grounded {
		t.Fatal("an uncited answer is marked grounded")
	}
}

func TestAnswerReportsAGeneratorFailure(t *testing.T) {
	t.Parallel()
	stub := &stubGenerator{err: errors.New("throttled")}
	if _, err := Answer(context.Background(), stub, "q", testMatches(2), ""); err == nil {
		t.Fatal("a failing generator was swallowed")
	}
}

func TestPromptNumbersAndDelimitsEverySource(t *testing.T) {
	t.Parallel()
	stub := &stubGenerator{answer: "[1]"}
	if _, err := Answer(context.Background(), stub, "how often?", testMatches(3), ""); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	for index := 1; index <= 3; index++ {
		if !strings.Contains(stub.user, `<source id="`+string(rune('0'+index))+`"`) {
			t.Fatalf("source %d is not delimited in the prompt:\n%s", index, stub.user)
		}
	}
	if !strings.Contains(stub.user, "Question: how often?") {
		t.Fatalf("the question is missing from the prompt:\n%s", stub.user)
	}
	// The heading trail travels with the passage, because a passage read
	// alone is often missing the subject it is about.
	if !strings.Contains(stub.user, `section="Security > Credentials"`) {
		t.Fatalf("the heading trail is missing:\n%s", stub.user)
	}
	for _, rule := range []string{"only what the sources say", "square brackets", "do not contain the answer"} {
		if !strings.Contains(stub.system, rule) {
			t.Fatalf("the grounding rules are missing %q:\n%s", rule, stub.system)
		}
	}
}

func TestOrganizationInstructionsAddToTheRulesRatherThanReplacingThem(t *testing.T) {
	t.Parallel()
	stub := &stubGenerator{answer: "[1]"}
	if _, err := Answer(context.Background(), stub, "q", testMatches(1), "Always answer in British English."); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !strings.Contains(stub.system, "Always answer in British English.") {
		t.Fatalf("the organization's instructions are missing:\n%s", stub.system)
	}
	// The grounding rules are not negotiable, so they must survive whatever
	// an organization adds — including an instruction that tries to undo them.
	if !strings.Contains(stub.system, "Use only what the sources say.") {
		t.Fatalf("organization instructions displaced the grounding rules:\n%s", stub.system)
	}
	if !strings.HasPrefix(stub.system, "You answer questions using only the numbered sources") {
		t.Fatalf("the grounding contract is no longer first:\n%s", stub.system)
	}
}

func TestCited(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		answer   string
		supplied int
		want     []int
	}{
		{name: "none", answer: "no citations here", supplied: 3},
		{name: "one", answer: "a claim [2].", supplied: 3, want: []int{2}},
		{name: "several in order of appearance", answer: "[3] then [1] then [3] again", supplied: 3, want: []int{3, 1}},
		{name: "adjacent", answer: "both say so [1][2].", supplied: 3, want: []int{1, 2}},
		{name: "out of range is dropped", answer: "[1] and [9]", supplied: 3, want: []int{1}},
		{name: "zero is not a source", answer: "[0]", supplied: 3},
		{name: "not a citation", answer: "an array index a[0] and [x]", supplied: 3},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := Cited(testCase.answer, testCase.supplied)
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("Cited(%q) = %v, want %v", testCase.answer, got, testCase.want)
			}
		})
	}
}

// fakeConverse stands in for Bedrock Runtime.
type fakeConverse struct {
	input  *bedrockruntime.ConverseInput
	output *bedrockruntime.ConverseOutput
	err    error
}

func (fake *fakeConverse) Converse(_ context.Context, input *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	fake.input = input
	return fake.output, fake.err
}

func bedrockAnswer(text string) *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{
		Output: &bedrocktypes.ConverseOutputMemberMessage{
			Value: bedrocktypes.Message{
				Role:    bedrocktypes.ConversationRoleAssistant,
				Content: []bedrocktypes.ContentBlock{&bedrocktypes.ContentBlockMemberText{Value: text}},
			},
		},
		Usage: &bedrocktypes.TokenUsage{InputTokens: aws.Int32(120), OutputTokens: aws.Int32(30)},
	}
}

func TestBedrockGenerate(t *testing.T) {
	t.Parallel()
	fake := &fakeConverse{output: bedrockAnswer("Ninety days [1].")}
	generator, err := NewBedrock(fake, "anthropic.claude-opus-5", BedrockOptions{})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	response, err := generator.Generate(context.Background(), Request{
		Question: "how often?", Passages: PassagesFrom(testMatches(1)),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if response.Text != "Ninety days [1]." {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Usage.InputTokens != 120 || response.Usage.OutputTokens != 30 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	// The same sources should give the same answer; sampling would make a
	// wrong one harder to reproduce.
	if aws.ToFloat32(fake.input.InferenceConfig.Temperature) != 0 {
		t.Fatalf("temperature = %v, want 0", aws.ToFloat32(fake.input.InferenceConfig.Temperature))
	}
	if len(fake.input.System) != 1 {
		t.Fatal("the grounding rules were not sent as a system block")
	}
}

func TestBedrockReportsAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	// A guardrail intervention returns a message with no text. Saying so
	// beats an empty answer that reads as though the documents held nothing.
	fake := &fakeConverse{output: &bedrockruntime.ConverseOutput{
		Output:     &bedrocktypes.ConverseOutputMemberMessage{Value: bedrocktypes.Message{}},
		StopReason: bedrocktypes.StopReasonGuardrailIntervened,
	}}
	generator, _ := NewBedrock(fake, "anthropic.claude-opus-5", BedrockOptions{})
	_, err := generator.Generate(context.Background(), Request{Question: "q", Passages: PassagesFrom(testMatches(1))})
	if err == nil {
		t.Fatal("an empty answer was returned as success")
	}
	if !strings.Contains(err.Error(), "guardrail") {
		t.Fatalf("error = %v, want it to name the stop reason", err)
	}
}

func TestBedrockGuardrailConfiguration(t *testing.T) {
	t.Parallel()
	fake := &fakeConverse{output: bedrockAnswer("ok [1]")}
	generator, err := NewBedrock(fake, "anthropic.claude-opus-5", BedrockOptions{
		GuardrailIdentifier: "gr-abc", GuardrailVersion: "3",
	})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	if _, err := generator.Generate(context.Background(), Request{Question: "q", Passages: PassagesFrom(testMatches(1))}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if fake.input.GuardrailConfig == nil || aws.ToString(fake.input.GuardrailConfig.GuardrailIdentifier) != "gr-abc" {
		t.Fatalf("guardrail config = %+v", fake.input.GuardrailConfig)
	}

	// A DRAFT version means what ran is not what was reviewed.
	if _, err := NewBedrock(fake, "m", BedrockOptions{GuardrailIdentifier: "gr-abc", GuardrailVersion: "DRAFT"}); err == nil {
		t.Fatal("a DRAFT guardrail version was accepted")
	}
	if _, err := NewBedrock(fake, "m", BedrockOptions{GuardrailIdentifier: "gr-abc"}); err == nil {
		t.Fatal("a guardrail with no version was accepted")
	}
}

func TestBedrockNeedsAModel(t *testing.T) {
	t.Parallel()
	// No default: the id that works depends on the region and on whether a
	// cross-region inference profile is in use, so a guess would fail at the
	// first question rather than at startup.
	if _, err := NewBedrock(&fakeConverse{}, "  ", BedrockOptions{}); err == nil {
		t.Fatal("a generator was built with no model")
	}
	if _, err := NewBedrock(nil, "anthropic.claude-opus-5", BedrockOptions{}); err == nil {
		t.Fatal("a generator was built with no client")
	}
}

func TestVertexGenerate(t *testing.T) {
	t.Parallel()
	var (
		gotPath string
		gotBody vertexRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		_ = json.NewDecoder(request.Body).Decode(&gotBody)
		_, _ = writer.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"Ninety days [1]."}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":140,"candidatesTokenCount":12}}`))
	}))
	defer server.Close()

	generator, err := NewVertex(context.Background(), VertexConfig{
		Project: "oryxa-dev", Location: "asia-south1", Model: "gemini-3.8-flash",
		HTTPClient: server.Client(), Endpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	response, err := generator.Generate(context.Background(), Request{
		Question: "how often?", Passages: PassagesFrom(testMatches(2)),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if response.Text != "Ninety days [1]." {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Usage.InputTokens != 140 || response.Usage.OutputTokens != 12 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if want := "/v1/projects/oryxa-dev/locations/asia-south1/publishers/google/models/gemini-3.8-flash:generateContent"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotBody.SystemInstruction == nil || !strings.Contains(gotBody.SystemInstruction.Parts[0].Text, "only the numbered sources") {
		t.Fatal("the grounding rules were not sent as a system instruction")
	}
	if gotBody.GenerationConfig.Temperature != 0 {
		t.Fatalf("temperature = %v, want 0", gotBody.GenerationConfig.Temperature)
	}
}

// fixedToken is credentials that always hold the same token.
type fixedToken string

func (token fixedToken) Token(context.Context) (*auth.Token, error) {
	return &auth.Token{Value: string(token), Type: "Bearer"}, nil
}

// Without a client of its own, a request carries the token of the credentials
// it was given, such as those internal/gcpauth federates from AWS.
func TestVertexSendsTheCredentialsToken(t *testing.T) {
	t.Parallel()
	authorization := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization <- request.Header.Get("Authorization")
		_, _ = writer.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Ninety days [1]."}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	generator, err := NewVertex(context.Background(), VertexConfig{
		Project: "oryxa-dev", Model: "gemini-3.5-flash-lite", Endpoint: server.URL,
		Credentials: auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: fixedToken("service-account-token")}),
	})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	if _, err := generator.Generate(context.Background(), Request{
		Question: "how often?", Passages: PassagesFrom(testMatches(2)),
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := <-authorization; got != "Bearer service-account-token" {
		t.Fatalf("Authorization = %q, want the credentials' token", got)
	}
}

func TestVertexReportsABlockedAnswer(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY"}]}`))
	}))
	defer server.Close()
	generator, _ := NewVertex(context.Background(), VertexConfig{
		Project: "p", Model: "gemini-3.8-flash", HTTPClient: server.Client(), Endpoint: server.URL,
	})
	_, err := generator.Generate(context.Background(), Request{Question: "q", Passages: PassagesFrom(testMatches(1))})
	if err == nil {
		t.Fatal("a blocked answer was returned as success")
	}
	if !strings.Contains(err.Error(), "SAFETY") {
		t.Fatalf("error = %v, want it to name the finish reason", err)
	}
}

func TestVertexDoesNotEchoTheProviderBody(t *testing.T) {
	t.Parallel()
	// A Vertex error can quote the prompt, and the prompt here is an
	// organization's own document text.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"status":"INVALID_ARGUMENT","message":"the document said something private"}}`))
	}))
	defer server.Close()
	generator, _ := NewVertex(context.Background(), VertexConfig{
		Project: "p", Model: "gemini-3.8-flash", HTTPClient: server.Client(), Endpoint: server.URL,
	})
	_, err := generator.Generate(context.Background(), Request{Question: "q", Passages: PassagesFrom(testMatches(1))})
	if err == nil {
		t.Fatal("a 400 was returned as success")
	}
	if strings.Contains(err.Error(), "said something private") {
		t.Fatalf("the error echoed the provider's message: %v", err)
	}
}

func TestVertexNeedsAProjectAndModel(t *testing.T) {
	t.Parallel()
	if _, err := NewVertex(context.Background(), VertexConfig{Model: "gemini-3.8-flash"}); err == nil {
		t.Fatal("a generator was built with no project")
	}
	if _, err := NewVertex(context.Background(), VertexConfig{Project: "p", HTTPClient: http.DefaultClient}); err == nil {
		t.Fatal("a generator was built with no model")
	}
}

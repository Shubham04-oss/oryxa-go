package interactions

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
)

// No live scoring takes place: the fixture captures the actual SDK input and
// supplies explicit scores to characterize the ledger/guard contract.
type consentBedrockFixture struct {
	mu     sync.Mutex
	inputs []*bedrockruntime.ApplyGuardrailInput
	output *bedrockruntime.ApplyGuardrailOutput
}

func (fixture *consentBedrockFixture) ApplyGuardrail(_ context.Context, input *bedrockruntime.ApplyGuardrailInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.inputs = append(fixture.inputs, input)
	return fixture.output, nil
}

func consentGroundingOutput(score, threshold float64, blocked bool) *bedrockruntime.ApplyGuardrailOutput {
	action := types.GuardrailContextualGroundingPolicyActionNone
	if blocked {
		action = types.GuardrailContextualGroundingPolicyActionBlocked
	}
	return &bedrockruntime.ApplyGuardrailOutput{Assessments: []types.GuardrailAssessment{{
		ContextualGroundingPolicy: &types.GuardrailContextualGroundingPolicyAssessment{Filters: []types.GuardrailContextualGroundingFilter{
			{Type: types.GuardrailContextualGroundingFilterTypeGrounding, Score: aws.Float64(score), Threshold: aws.Float64(threshold), Action: action, Detected: aws.Bool(blocked)},
			{Type: types.GuardrailContextualGroundingFilterTypeRelevance, Score: aws.Float64(0.87), Threshold: aws.Float64(0.3), Action: types.GuardrailContextualGroundingPolicyActionNone, Detected: aws.Bool(false)},
		}},
	}}}
}

func consentTaggedTexts(t *testing.T, input *bedrockruntime.ApplyGuardrailInput) map[types.GuardrailContentQualifier]string {
	t.Helper()
	if input.Source != types.GuardrailContentSourceOutput || input.OutputScope != types.GuardrailOutputScopeFull ||
		aws.ToString(input.GuardrailIdentifier) != "fixture-consent" || aws.ToString(input.GuardrailVersion) != "1" || len(input.Content) != 3 {
		t.Fatalf("unexpected guardrail request: %+v", input)
	}
	texts := map[types.GuardrailContentQualifier]string{}
	for _, block := range input.Content {
		text, ok := block.(*types.GuardrailContentBlockMemberText)
		if !ok || len(text.Value.Qualifiers) != 1 {
			t.Fatalf("expected a singly qualified text block, got %#v", block)
		}
		qualifier := text.Value.Qualifiers[0]
		if _, duplicate := texts[qualifier]; duplicate {
			t.Fatalf("duplicate qualifier %s", qualifier)
		}
		texts[qualifier] = aws.ToString(text.Value.Text)
	}
	return texts
}

func TestSlackConsentChoicesForwardTheSameSourcesAndAnswerToBedrock(t *testing.T) {
	const question = "When does Project Falcon launch?"
	const note = "Project Falcon launches on 14 October. The v2 pricing plan's codename is Tern."
	populated := []agents.ToolResult{{Tool: "knowledge_search", Parts: []string{
		"passages.collection_id: private_falcon", "passages.document_title: Falcon", "passages.score: 0.91",
		"passages.source: note:falcon", "passages.text: " + note, "searched: 1",
	}}}
	answer := agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "Project Falcon launches on 14 October.",
		Content: "It is related to the newest AI launch.", Artifacts: []string{"falcon-note.md"},
	}
	for _, testCase := range []struct {
		name      string
		results   []agents.ToolResult
		score     float64
		threshold float64
		blocked   bool
		minimum   float64
		status    string
		kind      string
	}{
		{"reported scores are held", populated, 0.23, 0.5, true, 0, StatusReview, consentguard.KindUngrounded},
		{"high synthetic score control", populated, 0.95, 0.5, false, 0, StatusCompleted, ""},
		{"empty search metadata still reaches Bedrock", []agents.ToolResult{{Tool: "knowledge_search", Parts: []string{"searched: 1"}}}, 0.23, 0.5, true, 0, StatusReview, consentguard.KindUngrounded},
		{"no result skips Bedrock", nil, 0.95, 0.5, false, 0, StatusReview, consentguard.KindNoSources},
		// NONE/undetected isolates the threshold floor from provider blocking.
		{"lower Go minimum still respects provider floor", populated, 0.23, 0.5, false, 0.2, StatusReview, consentguard.KindUngrounded},
		{"provider blocking still holds above both minima", populated, 0.23, 0.1, true, 0.2, StatusReview, consentguard.KindUngrounded},
		{"lower Go minimum passes above provider floor", populated, 0.6, 0.5, false, 0.2, StatusCompleted, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, choice := range []string{ConsentSummary, ConsentAccess} {
				t.Run(choice, func(t *testing.T) {
					fixture := &consentBedrockFixture{output: consentGroundingOutput(testCase.score, testCase.threshold, testCase.blocked)}
					grounder, err := consentguard.NewBedrockGrounder(fixture, "fixture-consent", "1")
					if err != nil {
						t.Fatal(err)
					}
					consent := consentguard.New(grounder, consentguard.Config{MinGrounding: testCase.minimum})
					service, repo, request := slackService(t, &fakeGuard{}, consent, choice)
					request.Task = question
					repo.requests[request.ID] = request
					answered, err := service.Respond(withResults(testCase.results), target, request.ID, answer)
					if err != nil || answered.Status != testCase.status {
						t.Fatalf("answer = %+v, %v; want status %s", answered, err, testCase.status)
					}
					if testCase.kind != "" && (answered.Guard == nil || !slices.Equal(answered.Guard.Kinds, []string{testCase.kind})) {
						t.Fatalf("guard = %+v, want only %s", answered.Guard, testCase.kind)
					}
					if testCase.status == StatusReview && answered.Result != nil {
						t.Fatal("a held answer was published")
					}
					if len(testCase.results) == 0 {
						if len(fixture.inputs) != 0 {
							t.Fatal("an answer with no sources called Bedrock")
						}
						return
					}
					if len(fixture.inputs) != 1 {
						t.Fatalf("Bedrock calls = %d, want one", len(fixture.inputs))
					}
					texts := consentTaggedTexts(t, fixture.inputs[0])
					want := map[types.GuardrailContentQualifier]string{
						types.GuardrailContentQualifierGroundingSource: strings.Join(testCase.results[0].Parts, "\n"),
						types.GuardrailContentQualifierQuery:           question,
						types.GuardrailContentQualifierGuardContent:    answer.Summary + "\n\n" + answer.Content + "\n\nfalcon-note.md",
					}
					for qualifier, expected := range want {
						if texts[qualifier] != expected {
							t.Errorf("%s = %q, want %q", qualifier, texts[qualifier], expected)
						}
					}
				})
			}
		})
	}
}

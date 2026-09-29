package consentguard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// guardrail is a Bedrock that answers each call with reply, and keeps what it
// was asked, and by how many calls at once.
type guardrail struct {
	mu      sync.Mutex
	inputs  []*bedrockruntime.ApplyGuardrailInput
	reply   func(call int, piece string) (*bedrockruntime.ApplyGuardrailOutput, error)
	pending int
	peak    int
}

func (fake *guardrail) ApplyGuardrail(_ context.Context, input *bedrockruntime.ApplyGuardrailInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	fake.mu.Lock()
	fake.inputs = append(fake.inputs, input)
	call := len(fake.inputs)
	fake.mu.Unlock()
	return fake.reply(call, pieceOf(input))
}

// blocks are the three texts of a call, by qualifier.
func blocks(t *testing.T, input *bedrockruntime.ApplyGuardrailInput) map[types.GuardrailContentQualifier]string {
	t.Helper()
	found := map[types.GuardrailContentQualifier]string{}
	for _, block := range input.Content {
		text, ok := block.(*types.GuardrailContentBlockMemberText)
		if !ok || len(text.Value.Qualifiers) != 1 {
			t.Fatalf("a content block is %#v, want a text with one qualifier", block)
		}
		found[text.Value.Qualifiers[0]] = aws.ToString(text.Value.Text)
	}
	return found
}

func pieceOf(input *bedrockruntime.ApplyGuardrailInput) string {
	for _, block := range input.Content {
		if text, ok := block.(*types.GuardrailContentBlockMemberText); ok && slices.Contains(text.Value.Qualifiers, types.GuardrailContentQualifierGuardContent) {
			return aws.ToString(text.Value.Text)
		}
	}
	return ""
}

// scored is a response that scores both filters, as a guardrail set up for it does.
func scored(grounded, relevance float64) *bedrockruntime.ApplyGuardrailOutput {
	return &bedrockruntime.ApplyGuardrailOutput{Assessments: []types.GuardrailAssessment{{
		ContextualGroundingPolicy: &types.GuardrailContextualGroundingPolicyAssessment{Filters: []types.GuardrailContextualGroundingFilter{
			filter(types.GuardrailContextualGroundingFilterTypeGrounding, grounded, 0.75),
			filter(types.GuardrailContextualGroundingFilterTypeRelevance, relevance, 0.5),
		}},
	}}}
}

func filter(kind types.GuardrailContextualGroundingFilterType, score, threshold float64) types.GuardrailContextualGroundingFilter {
	action, detected := types.GuardrailContextualGroundingPolicyActionNone, score < threshold
	if detected {
		action = types.GuardrailContextualGroundingPolicyActionBlocked
	}
	return types.GuardrailContextualGroundingFilter{
		Type: kind, Score: aws.Float64(score), Threshold: aws.Float64(threshold), Action: action, Detected: aws.Bool(detected),
	}
}

func always(output *bedrockruntime.ApplyGuardrailOutput, err error) func(int, string) (*bedrockruntime.ApplyGuardrailOutput, error) {
	return func(int, string) (*bedrockruntime.ApplyGuardrailOutput, error) { return output, err }
}

func grounderOf(t *testing.T, fake *guardrail) *BedrockGrounder {
	t.Helper()
	grounder, err := NewBedrockGrounder(fake, "gr-consent", "3")
	if err != nil {
		t.Fatal(err)
	}
	return grounder
}

func TestTheGuardrailIsAskedWithTheSourceTheQuestionAndTheAnswerInTheirRoles(t *testing.T) {
	fake := &guardrail{reply: always(scored(0.9, 0.8), nil)}
	grounding, err := grounderOf(t, fake).Ground(context.Background(), GroundingInput{
		Sources: []string{"first result", "second result"}, Question: "  When do deployments run? ", Answer: "On Tuesdays.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.inputs) != 1 {
		t.Fatalf("the guardrail was called %d times", len(fake.inputs))
	}
	input := fake.inputs[0]
	if aws.ToString(input.GuardrailIdentifier) != "gr-consent" || aws.ToString(input.GuardrailVersion) != "3" {
		t.Errorf("guardrail %s version %s", aws.ToString(input.GuardrailIdentifier), aws.ToString(input.GuardrailVersion))
	}
	if input.Source != types.GuardrailContentSourceOutput {
		t.Errorf("source = %s, want OUTPUT: it is an answer being read", input.Source)
	}
	if input.OutputScope != types.GuardrailOutputScopeFull {
		t.Errorf("output scope = %q, want FULL: without it a filter that found nothing is left out with its score", input.OutputScope)
	}
	want := map[types.GuardrailContentQualifier]string{
		types.GuardrailContentQualifierGroundingSource: "first result\n\nsecond result",
		types.GuardrailContentQualifierQuery:           "When do deployments run?",
		types.GuardrailContentQualifierGuardContent:    "On Tuesdays.",
	}
	got := blocks(t, input)
	for qualifier, text := range want {
		if got[qualifier] != text {
			t.Errorf("%s = %q, want %q", qualifier, got[qualifier], text)
		}
	}
	if len(input.Content) != 3 {
		t.Errorf("%d content blocks, want the source, the query and the answer", len(input.Content))
	}
	if grounding.Grounded.Value != 0.9 || grounding.Grounded.Threshold != 0.75 || grounding.Grounded.Failed ||
		grounding.Relevant.Value != 0.8 || grounding.Relevant.Threshold != 0.5 || len(grounding.Topics) != 0 {
		t.Errorf("grounding = %+v", grounding)
	}
}

func TestWhatTheGuardrailDetectedIsFailedAndItsTopicsAreNamed(t *testing.T) {
	detected := scored(0.3, 0.9)
	detected.Assessments[0].TopicPolicy = &types.GuardrailTopicPolicyAssessment{Topics: []types.GuardrailTopic{
		{Name: aws.String("Compensation"), Type: types.GuardrailTopicTypeDeny, Action: types.GuardrailTopicPolicyActionBlocked, Detected: aws.Bool(true)},
		{Name: aws.String("Credentials"), Type: types.GuardrailTopicTypeDeny, Action: types.GuardrailTopicPolicyActionBlocked},
		{Name: aws.String("Legal"), Type: types.GuardrailTopicTypeDeny, Action: types.GuardrailTopicPolicyActionNone, Detected: aws.Bool(false)},
	}}
	grounding, err := grounderOf(t, &guardrail{reply: always(detected, nil)}).Ground(context.Background(),
		GroundingInput{Sources: []string{"s"}, Question: "q", Answer: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !grounding.Grounded.Failed || grounding.Relevant.Failed || grounding.Grounded.Value != 0.3 {
		t.Errorf("grounding = %+v, want grounding failed and relevance not", grounding)
	}
	if !slices.Equal(grounding.Topics, []string{"Compensation", "Credentials"}) {
		t.Errorf("topics = %v, want the two detected, one that said nothing of it and one not detected left out", grounding.Topics)
	}
}

func TestAResponseThatDoesNotScoreBothFiltersSaysNothing(t *testing.T) {
	onlyGrounding := scored(0.9, 0.9)
	onlyGrounding.Assessments[0].ContextualGroundingPolicy.Filters = onlyGrounding.Assessments[0].ContextualGroundingPolicy.Filters[:1]
	noScore := scored(0.9, 0.9)
	noScore.Assessments[0].ContextualGroundingPolicy.Filters[1].Score = nil
	noThreshold := scored(0.9, 0.9)
	noThreshold.Assessments[0].ContextualGroundingPolicy.Filters[0].Threshold = nil
	for name, output := range map[string]*bedrockruntime.ApplyGuardrailOutput{
		"nothing":                    nil,
		"no assessments":             {},
		"an empty one":               {Assessments: []types.GuardrailAssessment{{}}},
		"only grounding":             onlyGrounding,
		"a filter with no score":     noScore,
		"a filter with no threshold": noThreshold,
	} {
		_, err := grounderOf(t, &guardrail{reply: always(output, nil)}).Ground(context.Background(), GroundingInput{Sources: []string{"s"}, Question: "q", Answer: "a"})
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// What went wrong is said without repeating what Bedrock said, which can quote
// the text it was sent.
func TestAFailedCallIsExplainedWithoutQuotingAnything(t *testing.T) {
	quoting := "ValidationException: the text 'GIRAFFEANSWER' is too long"
	for name, testCase := range map[string]struct {
		err  error
		want string
	}{
		"an api error":  {&smithy.GenericAPIError{Code: "ThrottlingException", Message: quoting}, "the guardrail answered ThrottlingException"},
		"a timeout":     {context.DeadlineExceeded, "the guardrail did not answer in time"},
		"a cancel":      {fmt.Errorf("wrapped: %w", context.Canceled), "the check was cancelled"},
		"anything else": {errors.New(quoting), "the guardrail could not be reached"},
	} {
		_, err := grounderOf(t, &guardrail{reply: always(nil, testCase.err)}).Ground(context.Background(),
			GroundingInput{Sources: []string{"s"}, Question: "q", Answer: "GIRAFFEANSWER"})
		if err == nil || err.Error() != testCase.want {
			t.Errorf("%s: err = %v, want %q", name, err, testCase.want)
		}
		if err != nil && strings.Contains(err.Error(), "GIRAFFEANSWER") {
			t.Errorf("%s: the error quotes the answer", name)
		}
	}
}

func TestThereIsNothingToAskAboutWithoutASourceAQuestionOrAnAnswer(t *testing.T) {
	fake := &guardrail{reply: always(scored(1, 1), nil)}
	for name, input := range map[string]GroundingInput{
		"no sources":                 {Question: "q", Answer: "a"},
		"blank sources":              {Sources: []string{"  ", ""}, Question: "q", Answer: "a"},
		"no question":                {Sources: []string{"s"}, Question: " ", Answer: "a"},
		"no answer":                  {Sources: []string{"s"}, Question: "q", Answer: "\n "},
		"an answer too long to read": {Sources: []string{"s"}, Question: "q", Answer: strings.Repeat("word ", chunkChars*maxChunks/5+chunkChars)},
	} {
		if _, err := grounderOf(t, fake).Ground(context.Background(), input); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(fake.inputs) != 0 {
		t.Errorf("the guardrail was called %d times about nothing", len(fake.inputs))
	}
}

func TestALongAnswerIsReadInPiecesAndIsAsGroundedAsItsWorstPiece(t *testing.T) {
	answer := strings.Repeat("Deployments run every Tuesday at nine. ", 250) // about 9,500 characters
	fake := &guardrail{}
	fake.reply = func(call int, piece string) (*bedrockruntime.ApplyGuardrailOutput, error) {
		if utf8.RuneCountInString(piece) > chunkChars {
			t.Errorf("a piece of %d characters is over the %d asked for", utf8.RuneCountInString(piece), chunkChars)
		}
		switch {
		case strings.HasSuffix(piece, "nine.") && call == 2:
			// The second piece to be asked: the worst grounded, and the most relevant.
			output := scored(0.4, 0.95)
			output.Assessments[0].TopicPolicy = &types.GuardrailTopicPolicyAssessment{Topics: []types.GuardrailTopic{{Name: aws.String("Legal")}}}
			return output, nil
		default:
			return scored(0.9, 0.6), nil
		}
	}
	grounding, err := grounderOf(t, fake).Ground(context.Background(), GroundingInput{Sources: []string{"s"}, Question: "q", Answer: answer})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.inputs) < 3 {
		t.Fatalf("an answer of %d characters was read in %d pieces", utf8.RuneCountInString(answer), len(fake.inputs))
	}
	// Every piece is read against the same source and question.
	for _, input := range fake.inputs {
		got := blocks(t, input)
		if got[types.GuardrailContentQualifierGroundingSource] != "s" || got[types.GuardrailContentQualifierQuery] != "q" {
			t.Errorf("a piece was read against %v", got)
		}
	}
	if grounding.Grounded.Value != 0.4 || !grounding.Grounded.Failed {
		t.Errorf("grounded = %+v, want the worst piece's", grounding.Grounded)
	}
	if grounding.Relevant.Value != 0.95 {
		t.Errorf("relevant = %+v, want the best piece's", grounding.Relevant)
	}
	if !slices.Equal(grounding.Topics, []string{"Legal"}) {
		t.Errorf("topics = %v, want the one any piece was about", grounding.Topics)
	}
}

func TestOnePieceThatCannotBeReadFailsTheWholeAnswer(t *testing.T) {
	answer := strings.Repeat("Deployments run every Tuesday at nine. ", 250)
	fake := &guardrail{reply: func(call int, _ string) (*bedrockruntime.ApplyGuardrailOutput, error) {
		if call == 2 {
			return nil, &smithy.GenericAPIError{Code: "ServiceUnavailableException"}
		}
		return scored(0.9, 0.9), nil
	}}
	if _, err := grounderOf(t, fake).Ground(context.Background(), GroundingInput{Sources: []string{"s"}, Question: "q", Answer: answer}); err == nil ||
		err.Error() != "the guardrail answered ServiceUnavailableException" {
		t.Errorf("err = %v", err)
	}
}

func TestTheSourceKeepsTheNewestResultsWithinWhatBedrockTakes(t *testing.T) {
	oldest := strings.Repeat("o", 60_000)
	middle := strings.Repeat("m", 30_000)
	newest := strings.Repeat("n", 20_000)
	source := fitSources([]string{oldest, "  ", middle, newest}, maxSourceChars)
	if got := utf8.RuneCountInString(source); got > maxSourceChars {
		t.Fatalf("the source is %d characters, over the %d Bedrock takes", got, maxSourceChars)
	}
	if !strings.HasSuffix(source, newest) || !strings.Contains(source, middle+"\n\n"+newest) {
		t.Errorf("the newest results are not whole at the end of the source")
	}
	// What is left of the oldest is its start, and comes first.
	if !strings.HasPrefix(source, "o") || strings.Count(source, "o") != maxSourceChars-30_000-20_000-2-2 {
		t.Errorf("the oldest kept %d characters", strings.Count(source, "o"))
	}
	if whole := fitSources([]string{"a", "b"}, maxSourceChars); whole != "a\n\nb" {
		t.Errorf("sources that fit = %q", whole)
	}
	if none := fitSources([]string{"", " "}, maxSourceChars); none != "" {
		t.Errorf("nothing = %q", none)
	}
}

func TestClipKeepsTheStartOfWhatIsTooLong(t *testing.T) {
	if got := clip("abcdef", 3); got != "abc" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("héllo wörld", 5); got != "héllo" {
		t.Errorf("clip counted bytes, not characters: %q", got)
	}
	if got := clip("short", 10); got != "short" {
		t.Errorf("clip = %q", got)
	}
}

func TestChunksBreakWhereABreakReadsBest(t *testing.T) {
	paragraph := func(letter string) string { return strings.Repeat(letter+" ", 40) + letter + "." }
	text := paragraph("a") + "\n\n" + paragraph("b") + "\n\n" + paragraph("c")
	pieces := chunks(text, 100)
	if len(pieces) != 3 || pieces[0] != paragraph("a") || pieces[1] != paragraph("b") || pieces[2] != paragraph("c") {
		t.Errorf("chunks broke a text of three paragraphs into %q", pieces)
	}
	if one := chunks("  short text \n", 100); !slices.Equal(one, []string{"short text"}) {
		t.Errorf("a text that fits = %q", one)
	}
	if none := chunks(" \n\t", 100); len(none) != 0 {
		t.Errorf("nothing = %q", none)
	}
	if sentences := chunks(strings.Repeat("One sentence here. ", 30), 100); len(sentences) < 5 || slices.ContainsFunc(sentences, func(piece string) bool { return !strings.HasSuffix(piece, ".") }) {
		t.Errorf("sentences were cut in the middle: %q", sentences)
	}
	// Without a break at all, at the limit.
	unbroken := strings.Repeat("x", 250)
	if pieces := chunks(unbroken, 100); len(pieces) != 3 || utf8.RuneCountInString(pieces[0]) != 100 || strings.Join(pieces, "") != unbroken {
		t.Errorf("unbroken text was cut into %d pieces", len(pieces))
	}
	// Whatever the text, every piece fits and nothing but space is lost.
	for _, limit := range []int{1, 2, 7, 50} {
		text := "Some words, some more words.\n\nA new paragraph with 日本語 in it.\nAnd a line. " + strings.Repeat("x", 30)
		var kept string
		for _, piece := range chunks(text, limit) {
			if utf8.RuneCountInString(piece) > limit {
				t.Errorf("limit %d: a piece of %d characters", limit, utf8.RuneCountInString(piece))
			}
			kept += strings.Join(strings.Fields(piece), "")
		}
		if want := strings.Join(strings.Fields(text), ""); kept != want {
			t.Errorf("limit %d: kept %q, want %q", limit, kept, want)
		}
	}
}

func TestABedrockGrounderNeedsAClientAGuardrailAndAVersion(t *testing.T) {
	fake := &guardrail{}
	for name, build := range map[string]func() (*BedrockGrounder, error){
		"no client":    func() (*BedrockGrounder, error) { return NewBedrockGrounder(nil, "g", "1") },
		"no guardrail": func() (*BedrockGrounder, error) { return NewBedrockGrounder(fake, " ", "1") },
		"no version":   func() (*BedrockGrounder, error) { return NewBedrockGrounder(fake, "g", "") },
	} {
		if _, err := build(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if grounder, err := NewBedrockGrounder(fake, " g ", " 2 "); err != nil || grounder.identifier != "g" || grounder.version != "2" {
		t.Errorf("grounder = %+v, %v", grounder, err)
	}
}

// End to end through the guard: the guardrail's findings hold an answer.
func TestAGuardOverBedrockHoldsAnUngroundedAnswer(t *testing.T) {
	fake := &guardrail{reply: always(scored(0.3, 0.9), nil)}
	result := New(grounderOf(t, fake), Config{}).Check(context.Background(), Input{
		Level: LevelAccess, Question: theQuestion, Texts: []string{"Deployments run daily."}, Sources: sourceOf("Deployments run every Tuesday."),
	})
	if result.Action != ActionHold || !slices.Equal(reasonKinds(result), []string{KindUngrounded}) {
		t.Errorf("result = %+v", result)
	}
	good := &guardrail{reply: always(scored(0.95, 0.9), nil)}
	if result := New(grounderOf(t, good), Config{}).Check(context.Background(), Input{
		Level: LevelAccess, Question: theQuestion, Texts: []string{"Deployments run on Tuesdays."}, Sources: sourceOf("Deployments run every Tuesday."),
	}); result.Action != ActionPass {
		t.Errorf("result = %+v", result)
	}
}

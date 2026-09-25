package answerguard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// fakeBedrock answers every text with what answer makes of it, and keeps
// what it was sent.
type fakeBedrock struct {
	mu     sync.Mutex
	answer func(ctx context.Context, text string) (*bedrockruntime.ApplyGuardrailOutput, error)
	sent   []*bedrockruntime.ApplyGuardrailInput
}

func (fake *fakeBedrock) ApplyGuardrail(ctx context.Context, input *bedrockruntime.ApplyGuardrailInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	fake.mu.Lock()
	fake.sent = append(fake.sent, input)
	fake.mu.Unlock()
	text := aws.ToString(input.Content[0].(*types.GuardrailContentBlockMemberText).Value.Text)
	return fake.answer(ctx, text)
}

func entity(kind, match string, action types.GuardrailSensitiveInformationPolicyAction) types.GuardrailPiiEntityFilter {
	return types.GuardrailPiiEntityFilter{Type: types.GuardrailPiiEntityType(kind), Match: aws.String(match), Action: action, Detected: aws.Bool(true)}
}

// response is the guardrail's answer: action, the masked output when there
// is one, and the entities it found.
func response(action types.GuardrailAction, output string, entities ...types.GuardrailPiiEntityFilter) *bedrockruntime.ApplyGuardrailOutput {
	answer := &bedrockruntime.ApplyGuardrailOutput{Action: action, Outputs: []types.GuardrailOutputContent{}}
	if output != "" {
		answer.Outputs = append(answer.Outputs, types.GuardrailOutputContent{Text: aws.String(output)})
	}
	if len(entities) > 0 {
		answer.Assessments = []types.GuardrailAssessment{{SensitiveInformationPolicy: &types.GuardrailSensitiveInformationPolicyAssessment{PiiEntities: entities}}}
	}
	return answer
}

// answering is a Bedrock that answers each text it knows as told, and
// anything else as clear.
func answering(answers map[string]*bedrockruntime.ApplyGuardrailOutput) *fakeBedrock {
	return &fakeBedrock{answer: func(_ context.Context, text string) (*bedrockruntime.ApplyGuardrailOutput, error) {
		if answer, ok := answers[text]; ok {
			return answer, nil
		}
		return response(types.GuardrailActionNone, ""), nil
	}}
}

func testGuard(t *testing.T, client Client, logs io.Writer) *Guard {
	t.Helper()
	if logs == nil {
		logs = io.Discard
	}
	guard, err := New(client, Config{Identifier: "gr_answers", Version: "1", Timeout: 200 * time.Millisecond,
		Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

// TestNothingFoundIsClear: an answer the guardrail finds nothing in is
// delivered as given, each of its texts read on its own, from the output
// side, with the guardrail named; an empty text is not sent.
func TestNothingFoundIsClear(t *testing.T) {
	bedrock := answering(nil)
	texts := []string{"The launch is on Thursday.", "", "Here is the plan in full."}
	verdict := testGuard(t, bedrock, nil).Check(context.Background(), texts)
	if verdict.State != StateClear || len(verdict.Kinds) != 0 || !slices.Equal(verdict.Texts, texts) || verdict.Masked != nil {
		t.Fatalf("verdict=%+v, want clear with the texts as given", verdict)
	}
	if len(bedrock.sent) != 2 {
		t.Fatalf("sent %d texts, want the two that are not empty", len(bedrock.sent))
	}
	for _, input := range bedrock.sent {
		if input.Source != types.GuardrailContentSourceOutput || aws.ToString(input.GuardrailIdentifier) != "gr_answers" ||
			aws.ToString(input.GuardrailVersion) != "1" || len(input.Content) != 1 {
			t.Fatalf("sent %+v, want one text from the output side to gr_answers version 1", input)
		}
	}
}

// TestOnlyAlwaysMaskedKindsAreDeliveredMasked: an answer with nothing but
// kinds the guardrail anonymizes is delivered as the guardrail masked it,
// naming the kinds.
func TestOnlyAlwaysMaskedKindsAreDeliveredMasked(t *testing.T) {
	bedrock := answering(map[string]*bedrockruntime.ApplyGuardrailOutput{
		"The password is hunter2 and the key is AKIAABCDEFGHIJKLMNOP.": response(types.GuardrailActionGuardrailIntervened,
			"The password is {PASSWORD} and the key is {AWS_ACCESS_KEY}.",
			entity("PASSWORD", "hunter2", types.GuardrailSensitiveInformationPolicyActionAnonymized),
			entity("AWS_ACCESS_KEY", "AKIAABCDEFGHIJKLMNOP", types.GuardrailSensitiveInformationPolicyActionAnonymized)),
	})
	verdict := testGuard(t, bedrock, nil).Check(context.Background(), []string{"The password is hunter2 and the key is AKIAABCDEFGHIJKLMNOP.", "Nothing here."})
	want := []string{"The password is {PASSWORD} and the key is {AWS_ACCESS_KEY}.", "Nothing here."}
	if verdict.State != StateMasked || !slices.Equal(verdict.Kinds, []string{"AWS_ACCESS_KEY", "PASSWORD"}) || !slices.Equal(verdict.Texts, want) || verdict.Masked != nil {
		t.Fatalf("verdict=%+v, want masked as the guardrail masked it", verdict)
	}
}

// TestAHeldKindHoldsTheAnswerWhateverTheTopLevelAction: email, phone and
// address are only detected, so an answer with nothing else comes back with
// action NONE and no outputs; the finding still holds it. With a kind that
// is always masked beside them, the held answer is the guardrail's masked
// output, and its fully masked version replaces the held values too.
func TestAHeldKindHoldsTheAnswerWhateverTheTopLevelAction(t *testing.T) {
	detectedOnly := "Write to priya@example.com or call +1 415 555 0100."
	mixed := "The PIN is 4921; Priya is at 100 Main Street, Springfield."
	bedrock := answering(map[string]*bedrockruntime.ApplyGuardrailOutput{
		detectedOnly: response(types.GuardrailActionNone, "",
			entity("EMAIL", "priya@example.com", types.GuardrailSensitiveInformationPolicyActionNone),
			entity("PHONE", "+1 415 555 0100", types.GuardrailSensitiveInformationPolicyActionNone)),
		mixed: response(types.GuardrailActionGuardrailIntervened, "The PIN is {PIN}; Priya is at 100 Main Street, Springfield.",
			entity("PIN", "4921", types.GuardrailSensitiveInformationPolicyActionAnonymized),
			entity("ADDRESS", "100 Main Street, Springfield", types.GuardrailSensitiveInformationPolicyActionNone)),
	})
	verdict := testGuard(t, bedrock, nil).Check(context.Background(), []string{detectedOnly, mixed})
	if verdict.State != StateHeld || !slices.Equal(verdict.Kinds, []string{"ADDRESS", "EMAIL", "PHONE"}) {
		t.Fatalf("verdict=%+v, want held for the address, email and phone", verdict)
	}
	if !slices.Equal(verdict.Texts, []string{detectedOnly, "The PIN is {PIN}; Priya is at 100 Main Street, Springfield."}) {
		t.Fatalf("the held texts = %q, want the guardrail's output, its PIN masked", verdict.Texts)
	}
	if !slices.Equal(verdict.Masked, []string{"Write to {EMAIL} or call {PHONE}.", "The PIN is {PIN}; Priya is at {ADDRESS}."}) {
		t.Fatalf("the fully masked texts = %q", verdict.Masked)
	}
}

// TestAKindLeftUnmaskedIsHeld: a kind that is always masked, found but not
// anonymized (the guardrail no longer set up as it should be), is still in
// the text, so the answer is held and its masked version replaces it.
func TestAKindLeftUnmaskedIsHeld(t *testing.T) {
	text := "Her SSN is 123-45-6789."
	bedrock := answering(map[string]*bedrockruntime.ApplyGuardrailOutput{
		text: response(types.GuardrailActionNone, "", entity("US_SOCIAL_SECURITY_NUMBER", "123-45-6789", types.GuardrailSensitiveInformationPolicyActionNone)),
	})
	verdict := testGuard(t, bedrock, nil).Check(context.Background(), []string{text})
	if verdict.State != StateHeld || !slices.Equal(verdict.Kinds, []string{"US_SOCIAL_SECURITY_NUMBER"}) || !slices.Equal(verdict.Masked, []string{"Her SSN is {US_SOCIAL_SECURITY_NUMBER}."}) {
		t.Fatalf("verdict=%+v, want it held with the number masked in its masked version", verdict)
	}
}

// TestAKindTheGuardIsNotSetUpForIsLeftAsItIs: a kind configured for
// neither, found and left as it is (detect only), neither masks nor holds.
func TestAKindTheGuardIsNotSetUpForIsLeftAsItIs(t *testing.T) {
	text := "Ask Priya."
	bedrock := answering(map[string]*bedrockruntime.ApplyGuardrailOutput{
		text: response(types.GuardrailActionNone, "", entity("NAME", "Priya", types.GuardrailSensitiveInformationPolicyActionNone)),
	})
	if verdict := testGuard(t, bedrock, nil).Check(context.Background(), []string{text}); verdict.State != StateClear || !slices.Equal(verdict.Texts, []string{text}) {
		t.Fatalf("verdict=%+v, want clear", verdict)
	}
}

// TestHeldValuesAreMaskedWhateverTheirCaseAndSpacing: the masked version
// finds a held value however its case and spaces differ from what the
// guardrail reported, and a value it cannot find at all turns that text
// into its kinds, so no held value survives in it.
func TestHeldValuesAreMaskedWhateverTheirCaseAndSpacing(t *testing.T) {
	spaced := "Mail PRIYA@Example.com at 100  Main\nStreet."
	lost := "Reach her the usual way."
	bedrock := answering(map[string]*bedrockruntime.ApplyGuardrailOutput{
		spaced: response(types.GuardrailActionNone, "",
			entity("EMAIL", "priya@example.com", types.GuardrailSensitiveInformationPolicyActionNone),
			entity("ADDRESS", "100 Main Street", types.GuardrailSensitiveInformationPolicyActionNone)),
		lost: response(types.GuardrailActionNone, "", entity("PHONE", "555 0100", types.GuardrailSensitiveInformationPolicyActionNone)),
	})
	verdict := testGuard(t, bedrock, nil).Check(context.Background(), []string{spaced, lost})
	if !slices.Equal(verdict.Masked, []string{"Mail {EMAIL} at {ADDRESS}.", "{PHONE}"}) {
		t.Fatalf("the fully masked texts = %q", verdict.Masked)
	}
}

// TestAnythingBlockedOrUnexpectedIsHeldUnchecked: nothing is set up to
// block, so a block, another policy acting, a custom pattern acting, an
// intervention with nothing masked, and masking without intervening are
// all taken as an answer that could not be checked: held, as UNCHECKED,
// with no masked version.
func TestAnythingBlockedOrUnexpectedIsHeldUnchecked(t *testing.T) {
	blockedTopic := response(types.GuardrailActionGuardrailIntervened, "Sorry.", entity("PIN", "4921", types.GuardrailSensitiveInformationPolicyActionAnonymized))
	blockedTopic.Assessments = append(blockedTopic.Assessments, types.GuardrailAssessment{TopicPolicy: &types.GuardrailTopicPolicyAssessment{
		Topics: []types.GuardrailTopic{{Name: aws.String("Finance"), Action: types.GuardrailTopicPolicyActionBlocked}},
	}})
	pattern := response(types.GuardrailActionGuardrailIntervened, "Ticket {ticket}.")
	pattern.Assessments = []types.GuardrailAssessment{{SensitiveInformationPolicy: &types.GuardrailSensitiveInformationPolicyAssessment{
		Regexes: []types.GuardrailRegexFilter{{Name: aws.String("ticket"), Match: aws.String("T-1"), Action: types.GuardrailSensitiveInformationPolicyActionAnonymized}},
	}}}
	for name, answer := range map[string]*bedrockruntime.ApplyGuardrailOutput{
		"a blocked entity":             response(types.GuardrailActionGuardrailIntervened, "Sorry.", entity("EMAIL", "priya@example.com", types.GuardrailSensitiveInformationPolicyActionBlocked)),
		"a blocked topic":              blockedTopic,
		"a custom pattern":             pattern,
		"intervening, masking nothing": response(types.GuardrailActionGuardrailIntervened, "Sorry."),
		"masking, not intervening":     response(types.GuardrailActionNone, "", entity("PIN", "4921", types.GuardrailSensitiveInformationPolicyActionAnonymized)),
		"an action nobody knows":       response(types.GuardrailAction("SOMETHING_NEW"), ""),
	} {
		text := "Priya's PIN is 4921, ticket T-1, priya@example.com."
		verdict := testGuard(t, answering(map[string]*bedrockruntime.ApplyGuardrailOutput{text: answer}), nil).Check(context.Background(), []string{text, "Fine."})
		if verdict.State != StateHeld || !slices.Equal(verdict.Kinds, []string{Unchecked}) || verdict.Masked != nil {
			t.Errorf("%s: verdict=%+v, want held as UNCHECKED with no masked version", name, verdict)
		}
		if !slices.Equal(verdict.Texts, []string{text, "Fine."}) {
			t.Errorf("%s: held texts %q, want what could not be read as given", name, verdict.Texts)
		}
	}
}

// TestAFailedOrSlowGuardrailHoldsTheAnswerUnchecked: an error, and a
// guardrail that does not answer within the guard's bound, hold the answer
// as UNCHECKED, in time; what was read before the failure is kept as the
// guardrail left it.
func TestAFailedOrSlowGuardrailHoldsTheAnswerUnchecked(t *testing.T) {
	masked := "The PIN is 4921."
	failures := map[string]func(context.Context, string) (*bedrockruntime.ApplyGuardrailOutput, error){
		"an error": func(_ context.Context, text string) (*bedrockruntime.ApplyGuardrailOutput, error) {
			if text == masked {
				return response(types.GuardrailActionGuardrailIntervened, "The PIN is {PIN}.", entity("PIN", "4921", types.GuardrailSensitiveInformationPolicyActionAnonymized)), nil
			}
			return nil, &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Too many requests"}
		},
		"a timeout": func(ctx context.Context, text string) (*bedrockruntime.ApplyGuardrailOutput, error) {
			if text == masked {
				return response(types.GuardrailActionGuardrailIntervened, "The PIN is {PIN}.", entity("PIN", "4921", types.GuardrailSensitiveInformationPolicyActionAnonymized)), nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
		"nothing at all": func(context.Context, string) (*bedrockruntime.ApplyGuardrailOutput, error) { return nil, nil },
	}
	for name, answer := range failures {
		started := time.Now()
		verdict := testGuard(t, &fakeBedrock{answer: answer}, nil).Check(context.Background(), []string{"Priya is at priya@example.com.", masked})
		if took := time.Since(started); took > 2*time.Second {
			t.Errorf("%s: checking took %s", name, took)
		}
		if verdict.State != StateHeld || !slices.Equal(verdict.Kinds, []string{Unchecked}) || verdict.Masked != nil {
			t.Errorf("%s: verdict=%+v, want held as UNCHECKED", name, verdict)
		}
		if name != "nothing at all" && !slices.Equal(verdict.Texts, []string{"Priya is at priya@example.com.", "The PIN is {PIN}."}) {
			t.Errorf("%s: held texts %q", name, verdict.Texts)
		}
	}
}

// TestTheGuardNeverLogsWhatItFound: whatever the guardrail finds or fails
// with, the guard's log names reasons and kinds, never a value it found or
// any text it read, even when the failure's own message quotes it.
func TestTheGuardNeverLogsWhatItFound(t *testing.T) {
	secrets := []string{"priya@example.com", "+1 415 555 0100", "hunter2", "100 Main Street", "Write to"}
	text := "Write to priya@example.com, call +1 415 555 0100, password hunter2, 100 Main Street."
	answers := []func(context.Context, string) (*bedrockruntime.ApplyGuardrailOutput, error){
		func(context.Context, string) (*bedrockruntime.ApplyGuardrailOutput, error) {
			return response(types.GuardrailActionGuardrailIntervened, "Write to priya@example.com, call +1 415 555 0100, password {PASSWORD}, 100 Main Street.",
				entity("EMAIL", "priya@example.com", types.GuardrailSensitiveInformationPolicyActionNone),
				entity("PHONE", "+1 415 555 0100", types.GuardrailSensitiveInformationPolicyActionNone),
				entity("PASSWORD", "hunter2", types.GuardrailSensitiveInformationPolicyActionAnonymized),
				entity("ADDRESS", "100 Main Street", types.GuardrailSensitiveInformationPolicyActionNone)), nil
		},
		func(context.Context, string) (*bedrockruntime.ApplyGuardrailOutput, error) {
			return response(types.GuardrailActionGuardrailIntervened, "Sorry.", entity("EMAIL", "priya@example.com", types.GuardrailSensitiveInformationPolicyActionBlocked)), nil
		},
		func(context.Context, string) (*bedrockruntime.ApplyGuardrailOutput, error) {
			return nil, &smithy.GenericAPIError{Code: "ValidationException", Message: "Value '" + text + "' at 'content' failed to satisfy constraint"}
		},
		func(context.Context, string) (*bedrockruntime.ApplyGuardrailOutput, error) {
			return nil, fmt.Errorf("post %q: connection refused", text)
		},
	}
	var logs bytes.Buffer
	for _, answer := range answers {
		testGuard(t, &fakeBedrock{answer: answer}, &logs).Check(context.Background(), []string{text})
	}
	if logs.Len() == 0 {
		t.Fatal("the guard logged nothing about the answers it could not check")
	}
	for _, secret := range secrets {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the guard's log holds %q:\n%s", secret, logs.String())
		}
	}
	for _, reason := range []string{"ValidationException", "blocked EMAIL", "could not be reached"} {
		if !strings.Contains(logs.String(), reason) {
			t.Errorf("the guard's log does not say %q:\n%s", reason, logs.String())
		}
	}
}

// TestTheGuardReadsBedrocksOwnResponse drives the SDK's own client against
// a local stand-in for Bedrock, as AWS_ENDPOINT_URL_BEDROCK_RUNTIME points
// dev at one: the request is ApplyGuardrail on the output side for the
// guardrail named, and a response in Bedrock's own JSON is read as Bedrock
// means it.
func TestTheGuardReadsBedrocksOwnResponse(t *testing.T) {
	var asked struct {
		path string
		body map[string]any
	}
	standIn := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		asked.path = request.URL.Path
		_ = json.NewDecoder(request.Body).Decode(&asked.body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"action": "GUARDRAIL_INTERVENED",
			"outputs": [{"text": "Call 415 555 0100? No: the PIN is {PIN}."}],
			"assessments": [{"sensitiveInformationPolicy": {"piiEntities": [
				{"type": "PHONE", "match": "415 555 0100", "action": "NONE", "detected": true},
				{"type": "PIN", "match": "4921", "action": "ANONYMIZED", "detected": true}
			], "regexes": []}}],
			"usage": {"topicPolicyUnits": 0, "contentPolicyUnits": 0, "wordPolicyUnits": 0, "sensitiveInformationPolicyUnits": 1, "sensitiveInformationPolicyFreeUnits": 0, "contextualGroundingPolicyUnits": 0}
		}`)
	}))
	defer standIn.Close()
	client := bedrockruntime.New(bedrockruntime.Options{
		Region: "us-west-2", BaseEndpoint: aws.String(standIn.URL), RetryMaxAttempts: 1,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	guard, err := New(client, Config{Identifier: "gr_answers", Version: "3"})
	if err != nil {
		t.Fatal(err)
	}
	verdict := guard.Check(context.Background(), []string{"Call 415 555 0100? No: the PIN is 4921."})
	if asked.path != "/guardrail/gr_answers/version/3/apply" || asked.body["source"] != "OUTPUT" {
		t.Fatalf("asked %s with %v, want ApplyGuardrail on gr_answers version 3 from the output side", asked.path, asked.body)
	}
	if verdict.State != StateHeld || !slices.Equal(verdict.Kinds, []string{"PHONE"}) ||
		!slices.Equal(verdict.Texts, []string{"Call 415 555 0100? No: the PIN is {PIN}."}) ||
		!slices.Equal(verdict.Masked, []string{"Call {PHONE}? No: the PIN is {PIN}."}) {
		t.Fatalf("verdict=%+v", verdict)
	}
}

// TestAGuardNeedsItsGuardrail: a guard without a client, an identifier or a
// version is refused.
func TestAGuardNeedsItsGuardrail(t *testing.T) {
	for name, build := range map[string]func() (*Guard, error){
		"no client":     func() (*Guard, error) { return New(nil, Config{Identifier: "gr", Version: "1"}) },
		"no identifier": func() (*Guard, error) { return New(answering(nil), Config{Version: "1"}) },
		"no version":    func() (*Guard, error) { return New(answering(nil), Config{Identifier: "gr"}) },
	} {
		if _, err := build(); err == nil {
			t.Errorf("%s: built a guard", name)
		}
	}
	if _, err := New(answering(nil), Config{Identifier: "gr", Version: "1"}); err != nil {
		t.Fatal(err)
	}
}

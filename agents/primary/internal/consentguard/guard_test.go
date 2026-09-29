package consentguard

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// scoring is a grounder that gives every answer the same findings, and keeps
// what it was asked.
type scoring struct {
	grounding Grounding
	err       error
	asked     []GroundingInput
}

func (grounder *scoring) Ground(_ context.Context, in GroundingInput) (Grounding, error) {
	grounder.asked = append(grounder.asked, in)
	return grounder.grounding, grounder.err
}

// good is a finding an answer passes on.
func good() Grounding {
	return Grounding{Grounded: Score{Value: 0.95, Threshold: 0.75}, Relevant: Score{Value: 0.9, Threshold: 0.5}}
}

// theQuestion is what the asker wrote in the tests below, and passage what the
// run read: sixty words the answers copy from, or do not.
const theQuestion = "When do deployments run?"

var passage = numbered("w", 60)

func checked(t *testing.T, grounder Grounder, in Input) Result {
	t.Helper()
	if in.Question == "" {
		in.Question = theQuestion
	}
	return New(grounder, Config{}).Check(context.Background(), in)
}

func reasonKinds(result Result) []string {
	kinds := make([]string, len(result.Reasons))
	for index, reason := range result.Reasons {
		kinds[index] = reason.Kind
	}
	return kinds
}

func TestAnAnswerThatStandsOnItsSourcesPasses(t *testing.T) {
	grounder := &scoring{grounding: good()}
	texts := []string{"Deployments run on Tuesdays.", "They start at nine.", ""}
	for _, level := range []Level{LevelSummary, LevelAccess} {
		result := checked(t, grounder, Input{Level: level, Texts: texts, Sources: sourceOf("Deployments run every Tuesday at nine.")})
		if result.Action != ActionPass || len(result.Reasons) != 0 || len(result.Masked) != 0 || !slices.Equal(result.Texts, texts) {
			t.Errorf("%s: result = %+v, want the texts passed as they were", level, result)
		}
	}
}

func TestTheGrounderIsToldTheSourcesTheQuestionAndTheAnswerAsItWouldBeShown(t *testing.T) {
	grounder := &scoring{grounding: good()}
	sources := []Source{
		{Tool: "knowledge_search", Parts: []string{"first passage", "", "second passage"}},
		{Tool: "apps_read", Parts: nil},
		{Tool: "directory_find_people", Parts: []string{"a person"}},
	}
	token := join("xo", "xb-", "123456789012-", "1234567890123-", "abcdefghijklmnopqrstuvwx")
	checked(t, grounder, Input{Texts: []string{"The summary " + token, "  ", "The content"}, Sources: sources})
	if len(grounder.asked) != 1 {
		t.Fatalf("the grounder was asked %d times", len(grounder.asked))
	}
	asked := grounder.asked[0]
	if want := []string{"first passage\nsecond passage", "a person"}; !slices.Equal(asked.Sources, want) {
		t.Errorf("sources = %q, want one text for each result that returned something: %q", asked.Sources, want)
	}
	if asked.Question != theQuestion {
		t.Errorf("question = %q", asked.Question)
	}
	// It reads the answer as the asker would read it: credentials already out.
	if want := "The summary {SLACK_TOKEN}\n\nThe content"; asked.Answer != want {
		t.Errorf("answer = %q, want %q", asked.Answer, want)
	}
}

func TestCredentialsAreMaskedAtEveryLevelAndTheAnswerIsDeliveredMasked(t *testing.T) {
	token := join("xo", "xb-", "123456789012-", "1234567890123-", "abcdefghijklmnopqrstuvwx")
	for _, level := range []Level{LevelSummary, LevelAccess} {
		texts := []string{"Use " + token + " to post.", "Deployments run on Tuesdays."}
		result := checked(t, &scoring{grounding: good()}, Input{Level: level, Texts: texts, Sources: sourceOf("Deployments run every Tuesday.")})
		if result.Action != ActionMask || len(result.Reasons) != 0 || !slices.Equal(result.Masked, []string{KindSlackToken}) ||
			!slices.Equal(result.Texts, []string{"Use {SLACK_TOKEN} to post.", "Deployments run on Tuesdays."}) {
			t.Errorf("%s: result = %+v, want the token masked and the answer delivered", level, result)
		}
		if !slices.Equal(result.Kinds(), []string{KindSlackToken}) {
			t.Errorf("%s: kinds = %v", level, result.Kinds())
		}
	}
}

// TestASummaryHasNoTwentyWordCopyOfWhatTheRunRead is the owner's rule read
// through Check, at its edge.
func TestASummaryHasNoTwentyWordCopyOfWhatTheRunRead(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		level  Level
		answer string
		want   []string
	}{
		{"nineteen words copied", LevelSummary, "In short: " + firstWords(passage, 19), nil},
		{"twenty words copied", LevelSummary, "In short: " + firstWords(passage, 20), []string{KindCopied}},
		{"thirty words copied", LevelSummary, firstWords(passage, 30), []string{KindCopied}},
		{"twenty words copied, at access", LevelAccess, firstWords(passage, 20), nil},
		{"the whole passage, at access", LevelAccess, passage, nil},
		{"twenty words copied, at a level nobody knows", Level("everything"), firstWords(passage, 20), []string{KindCopied}},
		{"twenty words copied, at no level", "", firstWords(passage, 20), []string{KindCopied}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := checked(t, &scoring{grounding: good()}, Input{Level: testCase.level, Texts: []string{testCase.answer}, Sources: sourceOf(passage)})
			if got := reasonKinds(result); !slices.Equal(got, testCase.want) {
				t.Fatalf("held for %v, want %v (%+v)", got, testCase.want, result.Reasons)
			}
			if len(testCase.want) > 0 && (result.Action != ActionHold || !strings.Contains(result.Reasons[0].Detail, "words")) {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

// A copy is found across the answer's texts, in the order they are read: a
// summary and a content that continue one another are one run.
func TestACopyRunsOnFromOneTextIntoTheNext(t *testing.T) {
	result := checked(t, &scoring{grounding: good()}, Input{
		Level: LevelSummary, Sources: sourceOf(passage),
		Texts: []string{firstWords(passage, 10), strings.Join(strings.Fields(passage)[10:22], " ")},
	})
	if got := reasonKinds(result); !slices.Equal(got, []string{KindCopied}) {
		t.Errorf("held for %v, want a copy across two texts of 10 and 12 words", got)
	}
}

func TestASummaryHasNoLink(t *testing.T) {
	for _, testCase := range []struct {
		level Level
		want  []string
	}{{LevelSummary, []string{KindLink}}, {LevelAccess, nil}, {"", []string{KindLink}}} {
		result := checked(t, &scoring{grounding: good()}, Input{
			Level: testCase.level, Sources: sourceOf("The plan is in the wiki."),
			Texts: []string{"Deployments run on Tuesdays.", "The plan: https://wiki.example.com/deploy"},
		})
		if got := reasonKinds(result); !slices.Equal(got, testCase.want) {
			t.Errorf("at %q held for %v, want %v", testCase.level, got, testCase.want)
		}
	}
	// An address that is not a link does not hold it.
	result := checked(t, &scoring{grounding: good()}, Input{Sources: sourceOf("Ask Raya."), Texts: []string{"Ask raya@example.com about deploy.sh."}})
	if len(result.Reasons) != 0 {
		t.Errorf("held for %v for an email address and a file name", result.Reasons)
	}
}

func TestAnAnswerCanBeHeldForACopyAndALinkAtOnce(t *testing.T) {
	result := checked(t, &scoring{grounding: good()}, Input{
		Level: LevelSummary, Sources: sourceOf(passage),
		Texts: []string{firstWords(passage, 25) + " https://example.com/doc"},
	})
	if got := reasonKinds(result); !slices.Equal(got, []string{KindCopied, KindLink}) || !slices.Equal(result.Kinds(), []string{KindCopied, KindLink}) {
		t.Errorf("held for %v, kinds %v", got, result.Kinds())
	}
}

// The owner's decision 4a: an answer from a run that called no tool is held,
// at either level, and nobody is asked whether it is grounded.
func TestAnAnswerFromARunThatReadNothingIsHeld(t *testing.T) {
	for name, sources := range map[string][]Source{
		"no results":              nil,
		"results with no values":  {{Tool: "knowledge_search"}},
		"results of blank values": sourceOf("", "  \n "),
	} {
		for _, level := range []Level{LevelSummary, LevelAccess} {
			grounder := &scoring{grounding: good()}
			result := checked(t, grounder, Input{Level: level, Texts: []string{"Deployments run on Tuesdays."}, Sources: sources})
			if result.Action != ActionHold || !slices.Equal(reasonKinds(result), []string{KindNoSources}) {
				t.Errorf("%s at %s: result = %+v, want held for no sources", name, level, result)
			}
			if len(grounder.asked) != 0 {
				t.Errorf("%s at %s: the grounder was asked about an answer with nothing to stand on", name, level)
			}
		}
	}
}

func TestAGrounderThatCannotTellHoldsTheAnswerAsUnchecked(t *testing.T) {
	for name, grounder := range map[string]Grounder{
		"an error":       &scoring{err: errors.New("the guardrail answered ThrottlingException")},
		"no grounder":    nil,
		"a nil pointer":  (*BedrockGrounder)(nil),
		"a nil function": GrounderFunc(nil),
	} {
		result := checked(t, grounder, Input{Texts: []string{"Deployments run on Tuesdays."}, Sources: sourceOf("Deployments run every Tuesday.")})
		if result.Action != ActionHold || !slices.Equal(reasonKinds(result), []string{KindUnchecked}) || !slices.Equal(result.Kinds(), []string{KindUnchecked}) {
			t.Errorf("%s: result = %+v, want held as unchecked", name, result)
		}
	}
	result := checked(t, &scoring{err: errors.New("the guardrail answered ThrottlingException")}, Input{Texts: []string{"x"}, Sources: sourceOf("y")})
	if !strings.Contains(result.Reasons[0].Detail, "ThrottlingException") {
		t.Errorf("the reason does not say why: %+v", result.Reasons)
	}
}

func TestAnAnswerWithNoQuestionIsHeldAsUnchecked(t *testing.T) {
	grounder := &scoring{grounding: good()}
	result := New(grounder, Config{}).Check(context.Background(), Input{Question: "  ", Texts: []string{"An answer."}, Sources: sourceOf("Something.")})
	if !slices.Equal(reasonKinds(result), []string{KindUnchecked}) || len(grounder.asked) != 0 {
		t.Errorf("result = %+v, asked %d", result, len(grounder.asked))
	}
}

func TestAGrounderThatIsSlowHoldsTheAnswerAsUnchecked(t *testing.T) {
	slow := GrounderFunc(func(ctx context.Context, _ GroundingInput) (Grounding, error) {
		<-ctx.Done()
		return Grounding{}, ctx.Err()
	})
	started := time.Now()
	result := New(slow, Config{Timeout: 20 * time.Millisecond}).Check(context.Background(), Input{
		Question: theQuestion, Texts: []string{"An answer."}, Sources: sourceOf("Something."),
	})
	if !slices.Equal(reasonKinds(result), []string{KindUnchecked}) || time.Since(started) > 5*time.Second {
		t.Errorf("result = %+v after %v", result, time.Since(started))
	}
}

func TestWhatTheGrounderFindsHoldsTheAnswer(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		grounding Grounding
		want      []string
	}{
		{"nothing", good(), nil},
		{"ungrounded", Grounding{Grounded: Score{Value: 0.4, Threshold: 0.75}, Relevant: Score{Value: 0.9}}, []string{KindUngrounded}},
		{"just under the grounding minimum", Grounding{Grounded: Score{Value: 0.7499}, Relevant: Score{Value: 0.9}}, []string{KindUngrounded}},
		{"exactly the grounding minimum", Grounding{Grounded: Score{Value: 0.75}, Relevant: Score{Value: 0.9}}, nil},
		{"irrelevant", Grounding{Grounded: Score{Value: 0.95}, Relevant: Score{Value: 0.2, Threshold: 0.5}}, []string{KindIrrelevant}},
		{"just under the relevance minimum", Grounding{Grounded: Score{Value: 0.95}, Relevant: Score{Value: 0.4999}}, []string{KindIrrelevant}},
		{"both", Grounding{Grounded: Score{Value: 0.1}, Relevant: Score{Value: 0.1}}, []string{KindUngrounded, KindIrrelevant}},
		{"a denied topic", Grounding{Grounded: good().Grounded, Relevant: good().Relevant, Topics: []string{"Compensation"}}, []string{KindDeniedTopic}},
		{"a grounder that asks for more than the guard", Grounding{Grounded: Score{Value: 0.8, Threshold: 0.9}, Relevant: good().Relevant}, []string{KindUngrounded}},
		{"a grounder that found it wanting itself", Grounding{Grounded: Score{Value: 0.99, Failed: true}, Relevant: good().Relevant}, []string{KindUngrounded}},
		{"a score that is not a number", Grounding{Grounded: Score{Value: math.NaN()}, Relevant: good().Relevant}, []string{KindUngrounded}},
		{"a threshold that is not a number", Grounding{Grounded: Score{Value: 0.99, Threshold: math.NaN()}, Relevant: good().Relevant}, []string{KindUngrounded}},
		{"nothing scored", Grounding{}, []string{KindUngrounded, KindIrrelevant}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := checked(t, &scoring{grounding: testCase.grounding}, Input{Texts: []string{"An answer."}, Sources: sourceOf("Something it says.")})
			if got := reasonKinds(result); !slices.Equal(got, testCase.want) {
				t.Fatalf("held for %v, want %v (%+v)", got, testCase.want, result.Reasons)
			}
			if wantAction := map[bool]Action{true: ActionHold, false: ActionPass}[len(testCase.want) > 0]; result.Action != wantAction {
				t.Fatalf("action = %s, want %s", result.Action, wantAction)
			}
		})
	}
}

func TestTheMinimumsCanBeChanged(t *testing.T) {
	grounding := Grounding{Grounded: Score{Value: 0.8}, Relevant: Score{Value: 0.6}}
	input := Input{Question: theQuestion, Texts: []string{"An answer."}, Sources: sourceOf("Something.")}
	for _, testCase := range []struct {
		name   string
		config Config
		want   []string
	}{
		{"the defaults", Config{}, nil},
		{"asking more of grounding", Config{MinGrounding: 0.9}, []string{KindUngrounded}},
		{"asking more of relevance", Config{MinRelevance: 0.7}, []string{KindIrrelevant}},
		{"asking less", Config{MinGrounding: 0.3, MinRelevance: 0.3}, nil},
		{"numbers that mean nothing are the defaults", Config{MinGrounding: -1, MinRelevance: 7}, nil},
	} {
		result := New(&scoring{grounding: grounding}, testCase.config).Check(context.Background(), input)
		if got := reasonKinds(result); !slices.Equal(got, testCase.want) {
			t.Errorf("%s: held for %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// A held answer is not sent to the grounder: it can add nothing but a cost.
func TestAnAnswerHeldAlreadyIsNotSentToTheGrounder(t *testing.T) {
	grounder := &scoring{grounding: good()}
	result := checked(t, grounder, Input{Level: LevelSummary, Texts: []string{firstWords(passage, 22)}, Sources: sourceOf(passage)})
	if !slices.Equal(reasonKinds(result), []string{KindCopied}) || len(grounder.asked) != 0 {
		t.Errorf("held for %v, and the grounder was asked %d times", reasonKinds(result), len(grounder.asked))
	}
}

func TestAnAnswerThatReportsAFailureNeedsNothingToStandOn(t *testing.T) {
	grounder := &scoring{grounding: good()}
	token := join("xo", "xb-", "123456789012-", "1234567890123-", "abcdefghijklmnopqrstuvwx")
	result := checked(t, grounder, Input{Failed: true, Texts: []string{"I could not find anything about that."}, Sources: nil})
	if result.Action != ActionPass || len(grounder.asked) != 0 {
		t.Errorf("a failure with no sources: %+v, asked %d", result, len(grounder.asked))
	}
	// It is still read for credentials, and, as a summary, for a copy and a link.
	result = checked(t, grounder, Input{Failed: true, Level: LevelSummary, Sources: sourceOf(passage),
		Texts: []string{"Failed with " + token + " after " + firstWords(passage, 21) + " see https://example.com"}})
	if result.Action != ActionHold || !slices.Equal(reasonKinds(result), []string{KindCopied, KindLink}) || !slices.Equal(result.Masked, []string{KindSlackToken}) {
		t.Errorf("a failure with a copy: %+v", result)
	}
	if len(grounder.asked) != 0 {
		t.Errorf("the grounder was asked about a failure")
	}
}

func TestAnAnswerItsOwnerWroteHasOnlyItsCredentialsTakenOut(t *testing.T) {
	grounder := &scoring{err: errors.New("never asked")}
	token := join("xo", "xb-", "123456789012-", "1234567890123-", "abcdefghijklmnopqrstuvwx")
	result := checked(t, grounder, Input{
		WrittenByOwner: true, Level: LevelSummary, Sources: nil,
		Texts: []string{"See https://example.com/doc: " + firstWords(passage, 30) + " " + token},
	})
	if result.Action != ActionMask || len(result.Reasons) != 0 || strings.Contains(result.Texts[0], token) ||
		!strings.Contains(result.Texts[0], "https://example.com/doc") || len(grounder.asked) != 0 {
		t.Errorf("result = %+v", result)
	}
}

func TestAnAnswerOfNoTextPasses(t *testing.T) {
	for _, texts := range [][]string{nil, {}, {"", "  ", "\n"}} {
		grounder := &scoring{grounding: good()}
		result := checked(t, grounder, Input{Texts: texts})
		if result.Action != ActionPass || len(result.Texts) != len(texts) || len(grounder.asked) != 0 {
			t.Errorf("texts %q: result = %+v, asked %d", texts, result, len(grounder.asked))
		}
	}
}

func TestTheResultHasAsManyTextsAsWereGiven(t *testing.T) {
	texts := []string{"one", "", "three"}
	result := checked(t, &scoring{grounding: good()}, Input{Texts: texts, Sources: sourceOf("Something.")})
	if len(result.Texts) != 3 {
		t.Fatalf("texts = %q", result.Texts)
	}
	result.Texts[0] = "changed"
	if texts[0] != "one" {
		t.Errorf("the texts given were changed")
	}
}

func TestKindsAreSortedAndNotRepeated(t *testing.T) {
	result := Result{Masked: []string{KindPassword, KindSlackToken}, Reasons: []Reason{{Kind: KindUngrounded}, {Kind: KindCopied}, {Kind: KindUngrounded}}}
	if got := result.Kinds(); !slices.Equal(got, []string{KindCopied, KindPassword, KindSlackToken, KindUngrounded}) {
		t.Errorf("kinds = %v", got)
	}
	if kinds := (Result{}).Kinds(); len(kinds) != 0 {
		t.Errorf("kinds of nothing = %v", kinds)
	}
}

// Nothing the guard logs is a text: not the answer, nor what the run read, nor
// the question, nor a credential; only what was decided, by kind and by score.
func TestNothingIsLoggedButWhatWasDecided(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	token := join("xo", "xb-", "123456789012-", "1234567890123-", "abcdefghijklmnopqrstuvwx")
	sensitive := []string{"ZEBRAQUESTION", "GIRAFFEANSWER", "OKAPISOURCE", token}
	guard := New(&scoring{grounding: Grounding{Grounded: Score{Value: 0.2}, Relevant: Score{Value: 0.9}, Topics: []string{"Compensation"}}}, Config{Logger: logger})
	guard.Check(context.Background(), Input{
		RequestID: "irq_1", OrganizationID: "org_1", Question: "ZEBRAQUESTION ok?",
		Texts: []string{"GIRAFFEANSWER " + token}, Sources: sourceOf("OKAPISOURCE and more"),
	})
	guard.Check(context.Background(), Input{Question: "ZEBRAQUESTION", Texts: []string{"GIRAFFEANSWER"}, Sources: sourceOf(passage)})
	New(&scoring{err: errors.New("the guardrail did not answer in time")}, Config{Logger: logger}).Check(context.Background(), Input{
		Question: "ZEBRAQUESTION", Texts: []string{"GIRAFFEANSWER"}, Sources: sourceOf("OKAPISOURCE"),
	})
	text := logs.String()
	if text == "" {
		t.Fatal("nothing was logged")
	}
	for _, secret := range sensitive {
		if strings.Contains(text, secret) {
			t.Errorf("the log has %q: %s", secret, text)
		}
	}
	for _, want := range []string{"irq_1", "org_1", "UNGROUNDED", "DENIED_TOPIC", "SLACK_TOKEN", "UNCHECKED", "the grounding check could not tell"} {
		if !strings.Contains(text, want) {
			t.Errorf("the log does not say %q: %s", want, text)
		}
	}
}

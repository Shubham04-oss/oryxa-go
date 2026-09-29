package answerguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
)

// fakeJudge decides every answer it is asked about with decide, and keeps
// every input it was given.
type fakeJudge struct {
	mu     sync.Mutex
	inputs []relevance.Input
	decide func(relevance.Input) relevance.Result
}

func (fake *fakeJudge) Judge(_ context.Context, in relevance.Input) relevance.Result {
	fake.mu.Lock()
	fake.inputs = append(fake.inputs, in)
	fake.mu.Unlock()
	return fake.decide(in)
}

func (fake *fakeJudge) calls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.inputs)
}

// saying is a judge that makes the same of every answer.
func saying(result relevance.Result) *fakeJudge {
	return &fakeJudge{decide: func(relevance.Input) relevance.Result { return result }}
}

// What a judge can make of an answer.
var (
	judgedClean    = relevance.Result{Judged: true, Model: "fake-judge", Certainty: 0.95}
	judgedOffTopic = relevance.Result{
		Judged: true, Hold: true, Extras: []relevance.Category{relevance.Health, relevance.HR}, Certainty: 0.8, Model: "fake-judge",
	}
	judgeFailed = relevance.Result{Judged: true, Hold: true, Err: relevance.ErrTimeout, Model: "fake-judge"}
)

// privateSources are tools that may have read something private, and so have
// their answers judged.
var privateSources = []string{"apps_gmail_search_messages"}

// judged is a guard over the intensity tests' guardrail (a password, an email
// address, and text that cannot be read) that also has judge to ask.
func judged(t *testing.T, judge relevance.Judge) *Guard {
	t.Helper()
	guard := intensityGuard(t)
	WithRelevance(judge)(guard)
	return guard
}

// asking is a RelevanceInput for an answer that may carry something private.
func asking(mode string) *RelevanceInput {
	return &RelevanceInput{Request: "Is Priya free on Thursday?", Sources: privateSources, Mode: mode}
}

// sameButRelevance is whether two verdicts are the same in everything the
// personal data read decides.
func sameButRelevance(a, b Verdict) bool {
	a.Relevance, b.Relevance = nil, nil
	return reflect.DeepEqual(a, b)
}

// TestTheRelevanceCheckRunsInTheModeItIsAskedIn is the table of what a judge's
// finding comes to in each mode: off never asks the judge; shadow asks it and
// records what it would have done, and changes nothing; hold holds an answer
// the judge would hold, for what it found, and for a judge that could not tell
// as unchecked, and shares nothing of it at any intensity.
func TestTheRelevanceCheckRunsInTheModeItIsAskedIn(t *testing.T) {
	texts := []string{intensityFine, "Thursday at three."}
	type want struct {
		asked     bool
		judged    bool
		wouldHold bool
		held      bool
		extras    []string
		err       string
		state     string
		kinds     []string
	}
	for _, testCase := range []struct {
		name   string
		mode   string
		result relevance.Result
		want   want
	}{
		{"off, a clean answer", RelevanceOff, judgedClean, want{state: StateClear}},
		{"off, an off-topic one", RelevanceOff, judgedOffTopic, want{state: StateClear}},
		{"off, a judge that would fail", RelevanceOff, judgeFailed, want{state: StateClear}},

		{"shadow, a clean answer", RelevanceShadow, judgedClean, want{asked: true, judged: true, extras: []string{}, state: StateClear}},
		{"shadow, an off-topic one", RelevanceShadow, judgedOffTopic, want{asked: true, judged: true, wouldHold: true, extras: []string{"health", "hr"}, state: StateClear}},
		{"shadow, a judge that could not tell", RelevanceShadow, judgeFailed, want{asked: true, judged: true, wouldHold: true, extras: []string{}, err: "timeout", state: StateClear}},

		{"hold, a clean answer", RelevanceHold, judgedClean, want{asked: true, judged: true, extras: []string{}, state: StateClear}},
		{
			"hold, an off-topic one", RelevanceHold, judgedOffTopic,
			want{asked: true, judged: true, wouldHold: true, held: true, extras: []string{"health", "hr"}, state: StateHeld, kinds: []string{OffTopic, "health", "hr"}},
		},
		{
			"hold, a judge that could not tell", RelevanceHold, judgeFailed,
			want{asked: true, judged: true, wouldHold: true, held: true, extras: []string{}, err: "timeout", state: StateHeld, kinds: []string{OffTopic, Unchecked}},
		},

		// A mode this code does not know never checks less than it was asked to.
		{"a mode nobody knows, a clean answer", "Hold", judgedClean, want{asked: true, judged: true, extras: []string{}, state: StateClear}},
		{
			"no mode, an off-topic answer", "", judgedOffTopic,
			want{asked: true, judged: true, wouldHold: true, held: true, extras: []string{"health", "hr"}, state: StateHeld, kinds: []string{OffTopic, "health", "hr"}},
		},
		{
			"a mode nobody knows, an off-topic answer", "strict", judgedOffTopic,
			want{asked: true, judged: true, wouldHold: true, held: true, extras: []string{"health", "hr"}, state: StateHeld, kinds: []string{OffTopic, "health", "hr"}},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			judge := saying(testCase.result)
			guard := judged(t, judge)
			verdict := guard.CheckWith(context.Background(), texts, asking(testCase.mode))

			if asked := judge.calls() == 1; asked != testCase.want.asked || (!asked && judge.calls() != 0) {
				t.Fatalf("the judge was asked %d times, want asked=%v", judge.calls(), testCase.want.asked)
			}
			outcome := verdict.Relevance
			if outcome == nil {
				t.Fatal("a check that asked for the relevance check has no outcome")
			}
			if outcome.Judged != testCase.want.judged || outcome.WouldHold != testCase.want.wouldHold || outcome.Held != testCase.want.held || outcome.Err != testCase.want.err ||
				!slices.Equal(outcome.Extras, testCase.want.extras) || (outcome.Extras == nil) != (testCase.want.extras == nil) {
				t.Errorf("outcome = %+v, want %+v", *outcome, testCase.want)
			}
			if verdict.State != testCase.want.state || !slices.Equal(verdict.Kinds, testCase.want.kinds) {
				t.Errorf("verdict = state %q kinds %v, want %q %v", verdict.State, verdict.Kinds, testCase.want.state, testCase.want.kinds)
			}
			if !slices.Equal(verdict.Given, texts) {
				t.Errorf("Given = %q, want the texts as handed over", verdict.Given)
			}
			for _, level := range []Intensity{IntensityOff, IntensitySecrets, IntensityStrict, "nobody knows"} {
				got := verdict.At(len(texts), level)
				if testCase.want.held {
					if got.State != StateHeld || got.Shareable != nil || got.Kinds == nil || !slices.Equal(got.Kinds, testCase.want.kinds) {
						t.Errorf("At(%s) = %+v, want held for %v and nothing shared", level, got, testCase.want.kinds)
					}
				} else if got.State != StateClear || !slices.Equal(got.Shareable, texts) {
					t.Errorf("At(%s) = %+v, want the texts shared, as nothing was held", level, got)
				}
			}
			if shared := verdict.Shareable(len(texts)); testCase.want.held != (shared == nil) || (shared != nil && !slices.Equal(shared, texts)) {
				t.Errorf("Shareable = %q, want held=%v", shared, testCase.want.held)
			}
		})
	}
}

// TestACheckWithNothingToAskIsExactlyCheck: with no RelevanceInput the verdict
// is Check's, with no Relevance, and a judge, however it would decide, is
// never asked; and Check itself never asks one.
func TestACheckWithNothingToAskIsExactlyCheck(t *testing.T) {
	judge := saying(judgedOffTopic)
	guard := judged(t, judge)
	for _, texts := range [][]string{
		{intensityFine},
		{intensityFine, intensityContact},
		{intensityBoth},
		{intensityFine, intensityUnread},
		{},
		nil,
	} {
		want := guard.Check(context.Background(), texts)
		got := guard.CheckWith(context.Background(), texts, nil)
		if !reflect.DeepEqual(got, want) || got.Relevance != nil || want.Relevance != nil {
			t.Errorf("CheckWith(%q, nil) = %+v, want exactly Check's %+v", texts, got, want)
		}
	}
	if judge.calls() != 0 {
		t.Fatalf("the judge was asked %d times by checks that did not ask for relevance", judge.calls())
	}
}

// TestOnlyAnAnswerWhoseSourcesMayHaveReadSomethingPrivateIsJudged: the gate is
// relevance.NeedsJudging: an answer from no source, or from sources known to
// read nothing private, costs no model call; a source that may have read
// something private, one nobody has classified, and the two things that put
// private material in a run without a tool, do. Whatever the judge would say.
func TestOnlyAnAnswerWhoseSourcesMayHaveReadSomethingPrivateIsJudged(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		sources []string
		judged  bool
	}{
		{"no sources", nil, false},
		{"an empty list", []string{}, false},
		{"blank names", []string{"", "  "}, false},
		{"tools that read nothing private", []string{"directory_find_people", "list_tools", "sandbox_execute_code"}, false},
		{"one of the connected apps", []string{"apps_gmail_search_messages"}, true},
		{"knowledge", []string{"knowledge_search"}, true},
		{"memory", []string{"load_memory"}, true},
		{"a tool nobody has heard of", []string{"some_new_tool"}, true},
		{"memories placed in the run's context", []string{relevance.MemoryPreload}, true},
		{"the member's earlier turns in the thread", []string{relevance.ThreadHistory}, true},
		{"a harmless tool beside a private one", []string{"directory_find_people", "apps_gmail_search_messages"}, true},
		{"names in any case", []string{"  APPS_Gmail_Search_Messages "}, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, mode := range []string{RelevanceShadow, RelevanceHold} {
				judge := saying(judgedOffTopic)
				rel := asking(mode)
				rel.Sources = testCase.sources
				verdict := judged(t, judge).CheckWith(context.Background(), []string{intensityFine}, rel)
				if asked := judge.calls() == 1; asked != testCase.judged {
					t.Errorf("%s: the judge was asked %d times, want judged=%v", mode, judge.calls(), testCase.judged)
				}
				if verdict.Relevance == nil || verdict.Relevance.Judged != testCase.judged || verdict.Relevance.WouldHold != testCase.judged {
					t.Errorf("%s: outcome = %+v, want judged=%v", mode, verdict.Relevance, testCase.judged)
				}
				if held := verdict.State == StateHeld; held != (testCase.judged && mode == RelevanceHold) {
					t.Errorf("%s: state %q", mode, verdict.State)
				}
				if !testCase.judged && (verdict.Relevance.Held || verdict.State != StateClear || len(verdict.Kinds) != 0) {
					t.Errorf("%s: an answer that was not judged was held: %+v", mode, verdict)
				}
			}
		})
	}
}

// TestWithNoJudgeTheRelevanceCheckDoesNotRunWhateverItIsAskedFor: a guard that
// was given none, or nil, or a judge that was never built, is asked for the
// check and answers as Check does: not judged, not held, not unchecked.
func TestWithNoJudgeTheRelevanceCheckDoesNotRunWhateverItIsAskedFor(t *testing.T) {
	var neverBuilt *fakeJudge
	for name, build := range map[string]func(t *testing.T) *Guard{
		"no option":                      func(t *testing.T) *Guard { return intensityGuard(t) },
		"nil":                            func(t *testing.T) *Guard { return judged(t, nil) },
		"a nil pointer in the interface": func(t *testing.T) *Guard { return judged(t, neverBuilt) },
	} {
		t.Run(name, func(t *testing.T) {
			guard := build(t)
			for _, mode := range []string{RelevanceOff, RelevanceShadow, RelevanceHold, "", "nobody knows"} {
				verdict := guard.CheckWith(context.Background(), []string{intensityFine, intensityContact}, asking(mode))
				if verdict.Relevance == nil || !reflect.DeepEqual(*verdict.Relevance, RelevanceOutcome{}) {
					t.Errorf("%q: outcome = %+v, want the zero outcome", mode, verdict.Relevance)
				}
				if !sameButRelevance(verdict, guard.Check(context.Background(), []string{intensityFine, intensityContact})) {
					t.Errorf("%q: the verdict is not Check's: %+v", mode, verdict)
				}
			}
		})
	}
}

// TestARegulatedOrganizationHoldsWhatWasAskedForToo: the rule is applied in
// the guard, from what the judge found: an answer that carries nothing extra
// but was asked for health, HR or pay is held when the organization is
// regulated, and is not otherwise; asking for something else is never enough.
// The judge is told the organization is regulated either way.
func TestARegulatedOrganizationHoldsWhatWasAskedForToo(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		asked     []relevance.Category
		regulated bool
		wouldHold bool
	}{
		{"health, unregulated", []relevance.Category{relevance.Health}, false, false},
		{"health, regulated", []relevance.Category{relevance.Health}, true, true},
		{"HR, regulated", []relevance.Category{relevance.HR}, true, true},
		{"pay, regulated", []relevance.Category{relevance.Compensation}, true, true},
		{"health among others, regulated", []relevance.Category{relevance.PersonalLife, relevance.Health}, true, true},
		{"all three, unregulated", []relevance.Category{relevance.Health, relevance.HR, relevance.Compensation}, false, false},
		{"personal life, regulated", []relevance.Category{relevance.PersonalLife}, true, false},
		{"credentials, regulated", []relevance.Category{relevance.Credentials}, true, false},
		{"someone else's private data, regulated", []relevance.Category{relevance.OthersPrivate}, true, false},
		{"nothing private, regulated", nil, true, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// The judge does not set Hold: the guard decides, in code.
			judge := saying(relevance.Result{Judged: true, Asked: testCase.asked, Model: "fake-judge"})
			rel := asking(RelevanceHold)
			rel.Regulated = testCase.regulated
			verdict := judged(t, judge).CheckWith(context.Background(), []string{intensityFine}, rel)

			if len(judge.inputs) != 1 || judge.inputs[0].Regulated != testCase.regulated {
				t.Fatalf("the judge was given %+v, want Regulated=%v", judge.inputs, testCase.regulated)
			}
			outcome := verdict.Relevance
			if outcome.WouldHold != testCase.wouldHold || outcome.Held != testCase.wouldHold || len(outcome.Extras) != 0 {
				t.Fatalf("outcome = %+v, want would-hold and held = %v with no extras", *outcome, testCase.wouldHold)
			}
			if testCase.wouldHold {
				// Nothing was extra, so nothing is named but the kind.
				if verdict.State != StateHeld || !slices.Equal(verdict.Kinds, []string{OffTopic}) || verdict.Shareable(1) != nil {
					t.Errorf("verdict = %+v, want it held as off topic and nothing shared", verdict)
				}
			} else if verdict.State != StateClear {
				t.Errorf("verdict = %+v, want it clear", verdict)
			}

			// In shadow mode the same finding is only recorded.
			rel.Mode = RelevanceShadow
			shadow := judged(t, judge).CheckWith(context.Background(), []string{intensityFine}, rel)
			if shadow.Relevance.WouldHold != testCase.wouldHold || shadow.Relevance.Held || shadow.State != StateClear {
				t.Errorf("shadow: %+v, want would-hold=%v, nothing held", shadow, testCase.wouldHold)
			}
		})
	}
}

// TestAJudgeThatSaysHoldOrFailsHoldsWhateverElseItSays: the guard believes a
// judge that holds for a reason of its own, and one that failed holds however
// it set its fields: an error is a would-hold even from a judge that forgot to
// hold, and one that is not judged but says hold is believed.
func TestAJudgeThatSaysHoldOrFailsHoldsWhateverElseItSays(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		result  relevance.Result
		judged  bool
		hold    bool
		errKind string
		kinds   []string
	}{
		{"an error and no hold", relevance.Result{Err: errors.New("boom")}, true, true, "other", []string{OffTopic, Unchecked}},
		{"an error, judged, and no hold", relevance.Result{Judged: true, Err: relevance.ErrUnparseable}, true, true, "unparseable", []string{OffTopic, Unchecked}},
		{"a hold and nothing found", relevance.Result{Judged: true, Hold: true}, true, true, "", []string{OffTopic}},
		{"a hold that was not judged", relevance.Result{Hold: true}, false, true, "", []string{OffTopic}},
		{"nothing judged and nothing said", relevance.Result{}, false, false, "", nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			verdict := judged(t, saying(testCase.result)).CheckWith(context.Background(), []string{intensityFine}, asking(RelevanceHold))
			outcome := verdict.Relevance
			if outcome.Judged != testCase.judged || outcome.WouldHold != testCase.hold || outcome.Held != testCase.hold || outcome.Err != testCase.errKind {
				t.Errorf("outcome = %+v, want judged=%v hold=%v err=%q", *outcome, testCase.judged, testCase.hold, testCase.errKind)
			}
			if !slices.Equal(verdict.Kinds, testCase.kinds) || (testCase.hold && verdict.State != StateHeld) {
				t.Errorf("verdict = state %q kinds %v, want kinds %v", verdict.State, verdict.Kinds, testCase.kinds)
			}
		})
	}
}

// TestThePersonalDataReadRunsFirst: the guardrail has read every text before
// the judge is asked about any of them.
func TestThePersonalDataReadRunsFirst(t *testing.T) {
	var sequence atomic.Int64
	var read, asked atomic.Int64
	bedrock := &fakeBedrock{answer: func(context.Context, string) (*bedrockruntime.ApplyGuardrailOutput, error) {
		read.Store(sequence.Add(1))
		return response(types.GuardrailActionNone, ""), nil
	}}
	judge := &fakeJudge{decide: func(relevance.Input) relevance.Result {
		asked.Store(sequence.Add(1))
		return judgedClean
	}}
	guard := testGuard(t, bedrock, nil)
	WithRelevance(judge)(guard)
	guard.CheckWith(context.Background(), []string{intensityFine}, asking(RelevanceHold))
	if read.Load() == 0 || asked.Load() == 0 || read.Load() > asked.Load() {
		t.Fatalf("the guardrail was read at %d and the judge asked at %d, want the read first", read.Load(), asked.Load())
	}
}

// TestWhatThePersonalDataReadFoundSurvivesTheRelevanceCheck: in hold mode the
// answer is held whatever the read made of it, and what the read found stays:
// the held kinds beside the relevance ones, the kinds masked in SecretKinds,
// the texts as the guardrail delivered them, and the fully masked version,
// which is never shared. In shadow mode nothing about the verdict changes but
// its Relevance.
func TestWhatThePersonalDataReadFoundSurvivesTheRelevanceCheck(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		texts       []string
		kinds       []string
		secretKinds []string
		masked      bool
	}{
		{"nothing found", []string{intensityFine}, []string{OffTopic, "health", "hr"}, nil, false},
		{"only a kind that is always masked", []string{intensityPassword}, []string{OffTopic, "health", "hr"}, []string{"PASSWORD"}, false},
		{"only a held kind", []string{intensityFine, intensityContact}, []string{"EMAIL", OffTopic, "health", "hr"}, nil, true},
		{"both, in one text", []string{intensityBoth}, []string{"EMAIL", OffTopic, "health", "hr"}, []string{"PASSWORD"}, true},
		{"a secret the guardrail left in the text", []string{intensityLeftSSN}, []string{"EMAIL", OffTopic, "US_SOCIAL_SECURITY_NUMBER", "health", "hr"}, []string{"US_SOCIAL_SECURITY_NUMBER"}, true},
		{"an answer that could not be read", []string{intensityFine, intensityUnread}, []string{OffTopic, Unchecked, "health", "hr"}, nil, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			guard := judged(t, saying(judgedOffTopic))
			read := guard.Check(context.Background(), testCase.texts)

			held := guard.CheckWith(context.Background(), testCase.texts, asking(RelevanceHold))
			if held.State != StateHeld || !slices.Equal(held.Kinds, testCase.kinds) || !held.Relevance.Held {
				t.Fatalf("held = state %q kinds %v, want held for %v", held.State, held.Kinds, testCase.kinds)
			}
			if !slices.Equal(held.SecretKinds, testCase.secretKinds) || !slices.Equal(held.SecretKinds, read.SecretKinds) {
				t.Errorf("SecretKinds = %v, want the read's %v", held.SecretKinds, testCase.secretKinds)
			}
			if !slices.Equal(held.Texts, read.Texts) || !slices.Equal(held.Given, testCase.texts) || !slices.Equal(held.Masked, read.Masked) || (held.Masked != nil) != testCase.masked {
				t.Errorf("texts = %q masked = %q given = %q, want the read's %q, %q", held.Texts, held.Masked, held.Given, read.Texts, read.Masked)
			}
			// What the read would have let through is not shared, masked or not.
			if held.Shareable(len(testCase.texts)) != nil {
				t.Errorf("Shareable = %q, want nothing", held.Shareable(len(testCase.texts)))
			}
			for _, level := range []Intensity{IntensityOff, IntensitySecrets, IntensityStrict} {
				if got := held.At(len(testCase.texts), level); got.Shareable != nil || got.State != StateHeld || !slices.Equal(got.Kinds, testCase.kinds) {
					t.Errorf("At(%s) = %+v, want held for %v and nothing shared", level, got, testCase.kinds)
				}
			}

			shadow := guard.CheckWith(context.Background(), testCase.texts, asking(RelevanceShadow))
			if !sameButRelevance(shadow, read) || !shadow.Relevance.WouldHold || shadow.Relevance.Held {
				t.Errorf("shadow = %+v, want Check's verdict %+v with the would-hold recorded", shadow, read)
			}
			for _, level := range []Intensity{IntensityOff, IntensitySecrets, IntensityStrict} {
				if got, want := shadow.At(len(testCase.texts), level), read.At(len(testCase.texts), level); !reflect.DeepEqual(got, want) {
					t.Errorf("shadow At(%s) = %+v, want what the read makes of it: %+v", level, got, want)
				}
			}
		})
	}
}

// TestTheJudgeIsHandedTheAnswerAsGivenAndWhatWasAskedAlone: the judge reads
// the texts as they were handed over (what the agent said, secrets included),
// beside the request, the context, the sources and whether the organization
// is regulated, and nothing else; and the guard neither keeps nor changes
// what it was given.
func TestTheJudgeIsHandedTheAnswerAsGivenAndWhatWasAskedAlone(t *testing.T) {
	judge := saying(judgedClean)
	guard := judged(t, judge)
	texts := []string{intensityBoth, "", intensityFine}
	rel := &RelevanceInput{
		Request: "What is the staging password?", Context: []string{"earlier line", "the line before it"},
		Sources: []string{"apps_gmail_search_messages", relevance.ThreadHistory}, Regulated: true, Mode: RelevanceShadow,
		OrganizationID: "org_1", UserID: "user_1",
	}
	before := *rel
	before.Context, before.Sources = slices.Clone(rel.Context), slices.Clone(rel.Sources)

	verdict := guard.CheckWith(context.Background(), texts, rel)
	if len(judge.inputs) != 1 {
		t.Fatalf("the judge was asked %d times", len(judge.inputs))
	}
	got := judge.inputs[0]
	want := relevance.Input{
		Request: rel.Request, Context: rel.Context, Answer: []string{intensityBoth, "", intensityFine},
		Sources: rel.Sources, Regulated: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the judge was given %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(*rel, before) {
		t.Errorf("the guard changed what it was asked: %+v, was %+v", *rel, before)
	}
	// The verdict does not alias the input, and the answer the judge was
	// given is its own copy.
	got.Answer[0] = "changed by the judge"
	if verdict.Given[0] != intensityBoth || texts[0] != intensityBoth {
		t.Errorf("the judge's copy of the answer changed the verdict's or the caller's: %q %q", verdict.Given, texts)
	}
}

// TestAVerdictHeldForRelevanceSharesNothingWhateverItCarries: the two ways to
// share read the flag, not the state or the kinds, so a verdict that says it
// was held for relevance shares nothing however it was made, and one that only
// would have been held shares as its read says.
func TestAVerdictHeldForRelevanceSharesNothingWhateverItCarries(t *testing.T) {
	held := &RelevanceOutcome{Judged: true, WouldHold: true, Held: true}
	for _, testCase := range []struct {
		name    string
		verdict Verdict
		kinds   []string
	}{
		{"marked held, and clear", Verdict{State: StateClear, Texts: []string{"a"}, Relevance: held}, []string{OffTopic}},
		{"marked held, and masked", Verdict{State: StateMasked, Kinds: []string{"PIN"}, Texts: []string{"a"}, Relevance: held}, []string{OffTopic, "PIN"}},
		{"marked held, with a masked version", Verdict{State: StateHeld, Kinds: []string{"EMAIL", OffTopic}, Texts: []string{"a"}, Masked: []string{"a"}, Relevance: held}, []string{"EMAIL", OffTopic}},
		{"marked held, and held for nothing", Verdict{State: StateHeld, Texts: []string{"a"}, Masked: []string{"a"}, Relevance: held}, []string{OffTopic}},
		{"marked held, with a state nobody knows", Verdict{State: "quarantined", Texts: []string{"a"}, Relevance: held}, []string{OffTopic}},
		{"marked held, the zero verdict", Verdict{Relevance: held}, []string{OffTopic}},
		{"marked held, its kinds unsorted and repeated", Verdict{State: StateHeld, Kinds: []string{"health", OffTopic, "EMAIL", "health"}, Texts: []string{"a"}, Relevance: held}, []string{"EMAIL", OffTopic, "health"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.verdict.Shareable(1); got != nil {
				t.Errorf("Shareable = %q, want nothing", got)
			}
			for _, level := range []Intensity{IntensityOff, IntensitySecrets, IntensityStrict, "nobody knows"} {
				got := testCase.verdict.At(1, level)
				if got.State != StateHeld || got.Shareable != nil || got.Kinds == nil || !slices.Equal(got.Kinds, testCase.kinds) {
					t.Errorf("At(%s) = %+v, want held for %v and nothing shared", level, got, testCase.kinds)
				}
				if got := testCase.verdict.ShareableAt(1, level); got != nil {
					t.Errorf("ShareableAt(%s) = %q, want nothing", level, got)
				}
			}
		})
	}

	// One that would have been held, in shadow mode, shares as it always did.
	shadow := Verdict{State: StateHeld, Kinds: []string{"EMAIL"}, Texts: []string{"a priya@example.com"}, Masked: []string{"a {EMAIL}"},
		Relevance: &RelevanceOutcome{Judged: true, WouldHold: true}}
	if got := shadow.Shareable(1); !slices.Equal(got, []string{"a {EMAIL}"}) {
		t.Errorf("a shadow verdict shares %q, want its masked version", got)
	}
	if got := shadow.At(1, IntensitySecrets); got.State != StateClear || !slices.Equal(got.Shareable, shadow.Texts) {
		t.Errorf("a shadow verdict at secrets = %+v, want it as the read makes it", got)
	}
	// And a verdict the relevance check never saw is unchanged by the field.
	plain := Verdict{State: StateClear, Texts: []string{"a"}}
	if got := plain.Shareable(1); !slices.Equal(got, []string{"a"}) {
		t.Errorf("a plain verdict shares %q", got)
	}
}

// relevanceLog is the lines a guard logged about the relevance check.
func relevanceLog(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if message, _ := record["msg"].(string); strings.Contains(message, "relevance check") {
			lines = append(lines, record)
		}
	}
	return lines
}

// TestTheRelevanceCheckLogsItsDecisionOnceAndNeverAText: one line for each
// answer a judge read, with the judge's fields and the mode, whether it was
// held, and whose answer it was when the caller said; nothing for an answer no
// judge read; and no text, request, context or value in any of it, whatever
// the judge found.
func TestTheRelevanceCheckLogsItsDecisionOnceAndNeverAText(t *testing.T) {
	const (
		secretAnswer  = "Priya starts chemotherapy on Monday; the vault password is hunter2 (priya@example.com)."
		secretRequest = "What is the salary of Priya Raman?"
		secretContext = "an earlier line about the acquisition of Northwind"
	)
	var logs bytes.Buffer
	judge := saying(judgedOffTopic)
	guard := testGuard(t, answering(nil), &logs)
	WithRelevance(judge)(guard)
	rel := func(mode string) *RelevanceInput {
		return &RelevanceInput{
			Request: secretRequest, Context: []string{secretContext}, Sources: privateSources, Mode: mode,
			OrganizationID: "org_1", UserID: "user_1",
		}
	}
	texts := []string{secretAnswer}

	guard.CheckWith(context.Background(), texts, rel(RelevanceHold))
	lines := relevanceLog(t, &logs)
	if len(lines) != 1 {
		t.Fatalf("logged %d relevance lines for one judged answer, want 1:\n%s", len(lines), logs.String())
	}
	line := lines[0]
	for key, want := range map[string]any{
		"level": "INFO", "mode": "hold", "held": true, "judged": true, "hold": true, "model": "fake-judge", "err_kind": "none",
		"organization_id": "org_1", "user_id": "user_1", "truncated": false,
	} {
		if line[key] != want {
			t.Errorf("%s = %v, want %v in %v", key, line[key], want, line)
		}
	}
	if fmt.Sprint(line["extras"]) != "[health hr]" || fmt.Sprint(line["asked"]) != "[]" || line["certainty"] != 0.8 {
		t.Errorf("the line does not carry the judge's findings: %v", line)
	}
	if strings.Count(logs.String(), `"level":`) != len(strings.Split(strings.TrimSpace(logs.String()), "\n")) {
		t.Errorf("a line names its level other than once:\n%s", logs.String())
	}

	// Shadow, with nobody named: the fields say so, and are absent.
	logs.Reset()
	anonymous := rel(RelevanceShadow)
	anonymous.OrganizationID, anonymous.UserID = "", ""
	guard.CheckWith(context.Background(), texts, anonymous)
	lines = relevanceLog(t, &logs)
	if len(lines) != 1 || lines[0]["mode"] != "shadow" || lines[0]["held"] != false {
		t.Fatalf("shadow lines = %v", lines)
	}
	for _, key := range []string{"organization_id", "user_id"} {
		if _, named := lines[0][key]; named {
			t.Errorf("a check that named nobody logged %s: %v", key, lines[0])
		}
	}

	// Nothing for an answer no judge read: off, no private source, nothing said.
	logs.Reset()
	guard.CheckWith(context.Background(), texts, rel(RelevanceOff))
	harmless := rel(RelevanceHold)
	harmless.Sources = []string{"directory_find_people"}
	guard.CheckWith(context.Background(), texts, harmless)
	guard.Check(context.Background(), texts)
	if lines := relevanceLog(t, &logs); len(lines) != 0 {
		t.Errorf("logged for answers no judge read: %v", lines)
	}
	unjudged := &Guard{client: answering(nil), identifier: "gr", version: "1", timeout: guard.timeout, logger: guard.logger}
	unjudged.CheckWith(context.Background(), texts, rel(RelevanceHold))
	if lines := relevanceLog(t, &logs); len(lines) != 0 {
		t.Errorf("a guard with no judge logged about the relevance check: %v", lines)
	}

	// A judge that could not tell is a warning, by why and never by what.
	logs.Reset()
	failing := testGuard(t, answering(nil), &logs)
	WithRelevance(saying(relevance.Result{
		Judged: true, Hold: true, Err: fmt.Errorf("the model said: %s %s %w", secretAnswer, secretRequest, relevance.ErrUnavailable),
	}))(failing)
	verdict := failing.CheckWith(context.Background(), texts, rel(RelevanceHold))
	lines = relevanceLog(t, &logs)
	if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["err_kind"] != "unavailable" || lines[0]["held"] != true {
		t.Fatalf("a judge that could not tell logged %v", lines)
	}
	if verdict.Relevance.Err != "unavailable" || strings.Contains(fmt.Sprint(*verdict.Relevance), "Priya") {
		t.Errorf("the outcome names its error as %q: %+v", verdict.Relevance.Err, *verdict.Relevance)
	}

	// Whatever happened, none of what was read is anywhere in what was logged.
	logs.Reset()
	guard.CheckWith(context.Background(), texts, rel(RelevanceHold))
	failing.CheckWith(context.Background(), texts, rel(RelevanceHold))
	for _, secret := range []string{"chemotherapy", "hunter2", "priya@example.com", "Priya", "salary", "Raman", "Northwind", "acquisition", "gmail"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log holds %q:\n%s", secret, logs.String())
		}
	}
}

// TestACheckMayBeMadeFromManyGoroutinesAtOnce: a guard is shared by every
// request, so what it does for one, with its own input and its own judge
// results, is independent of the rest.
func TestACheckMayBeMadeFromManyGoroutinesAtOnce(t *testing.T) {
	judge := &fakeJudge{decide: func(in relevance.Input) relevance.Result {
		if strings.Contains(in.Request, "leaky") {
			return judgedOffTopic
		}
		return judgedClean
	}}
	guard := judged(t, judge)
	var wait sync.WaitGroup
	failures := make(chan string, 200)
	for count := range 100 {
		wait.Go(func() {
			leaky := count%2 == 0
			mode := []string{RelevanceOff, RelevanceShadow, RelevanceHold}[count%3]
			request := "clean request"
			if leaky {
				request = "leaky request"
			}
			rel := &RelevanceInput{Request: request, Sources: privateSources, Mode: mode}
			verdict := guard.CheckWith(context.Background(), []string{intensityFine, intensityContact}, rel)
			held := leaky && mode == RelevanceHold
			if verdict.Relevance.Held != held || (verdict.State == StateHeld && verdict.Relevance.Held) != held {
				failures <- fmt.Sprintf("%s %s: %+v", mode, request, verdict.Relevance)
			}
			if shared := verdict.Shareable(2); held != (shared == nil) {
				failures <- fmt.Sprintf("%s %s shared %q", mode, request, shared)
			}
		})
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

// TestACertaintyThatIsNotANumberCanStillBeEncoded: an outcome carries the
// judge's certainty clamped to 0 through 1, and never NaN or infinity, which
// JSON cannot hold.
func TestACertaintyThatIsNotANumberCanStillBeEncoded(t *testing.T) {
	for value, want := range map[float64]float64{0.3: 0.3, 0: 0, 1: 1, 1.7: 1, -2: 0, math.Inf(1): 1, math.Inf(-1): 0, math.NaN(): 0} {
		result := judgedClean
		result.Certainty = value
		verdict := judged(t, saying(result)).CheckWith(context.Background(), []string{intensityFine}, asking(RelevanceShadow))
		if got := verdict.Relevance.Certainty; got != want {
			t.Errorf("certainty %v became %v, want %v", value, got, want)
		}
		if _, err := json.Marshal(verdict.Relevance); err != nil {
			t.Errorf("certainty %v: the outcome cannot be encoded: %v", value, err)
		}
	}
}

// TestTheRealJudgeIsAskedThroughTheGuard drives the guard with the relevance
// package's own Gemini judge against a stand-in for Vertex AI, which is what
// production builds: an answer from no private source is never sent to the
// model, one from a private source is, and what the model says is what holds
// or lets the answer through, or holds it as unchecked when the model is down.
func TestTheRealJudgeIsAskedThroughTheGuard(t *testing.T) {
	var asked atomic.Int64
	var respond atomic.Value
	respond.Store(func() (int, string) {
		return http.StatusOK, modelSays(`{"asked":[],"extras":["health"],"certainty":0.9}`)
	})
	vertex := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		asked.Add(1)
		_, _ = io.Copy(io.Discard, request.Body)
		status, body := respond.Load().(func() (int, string))()
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}))
	defer vertex.Close()
	for _, name := range []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_VERTEX_BASE_URL", "GOOGLE_GENAI_USE_VERTEXAI"} {
		t.Setenv(name, "")
	}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend: genai.BackendVertexAI, Project: "oryxa-test", Location: "global",
		HTTPClient: vertex.Client(), HTTPOptions: genai.HTTPOptions{BaseURL: vertex.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	guard := intensityGuard(t)
	WithRelevance(relevance.NewGemini(client, "gemini-3.5-flash-lite", 0))(guard)
	texts := []string{"No, she has a doctor's appointment."}

	verdict := guard.CheckWith(context.Background(), texts, &RelevanceInput{Request: "Is Priya free Thursday?", Mode: RelevanceHold, Sources: []string{"directory_find_people"}})
	if asked.Load() != 0 || verdict.Relevance.Judged || verdict.State != StateClear {
		t.Fatalf("an answer from a directory lookup reached the model %d times: %+v", asked.Load(), verdict)
	}

	held := guard.CheckWith(context.Background(), texts, &RelevanceInput{Request: "Is Priya free Thursday?", Mode: RelevanceHold, Sources: privateSources})
	if asked.Load() != 1 || !held.Relevance.Judged || !held.Relevance.Held || !slices.Equal(held.Relevance.Extras, []string{"health"}) ||
		held.State != StateHeld || !slices.Equal(held.Kinds, []string{OffTopic, "health"}) || held.Shareable(1) != nil || held.Relevance.Certainty != 0.9 {
		t.Fatalf("an off-topic answer from a private source = %+v after %d calls, want it held for health", held, asked.Load())
	}

	respond.Store(func() (int, string) { return http.StatusOK, modelSays(`{"asked":[],"extras":[],"certainty":0.9}`) })
	if clean := guard.CheckWith(context.Background(), texts, &RelevanceInput{Request: "Is Priya free Thursday?", Mode: RelevanceHold, Sources: privateSources}); clean.State != StateClear || clean.Relevance.WouldHold {
		t.Fatalf("a clean answer = %+v, want it let through", clean)
	}

	respond.Store(func() (int, string) {
		return http.StatusServiceUnavailable, `{"error":{"code":503,"message":"down: Priya has a doctor's appointment"}}`
	})
	down := guard.CheckWith(context.Background(), texts, &RelevanceInput{Request: "Is Priya free Thursday?", Mode: RelevanceHold, Sources: privateSources})
	if down.State != StateHeld || !slices.Equal(down.Kinds, []string{OffTopic, Unchecked}) || down.Relevance.Err != "unavailable" || down.Shareable(1) != nil {
		t.Fatalf("a model that is down = %+v, want the answer held as unchecked", down)
	}
	// In shadow mode the same failure is recorded and holds nothing.
	shadow := guard.CheckWith(context.Background(), texts, &RelevanceInput{Request: "Is Priya free Thursday?", Mode: RelevanceShadow, Sources: privateSources})
	if shadow.State != StateClear || !shadow.Relevance.WouldHold || shadow.Relevance.Held || shadow.Relevance.Err != "unavailable" {
		t.Fatalf("a model that is down, in shadow mode = %+v", shadow)
	}
}

// modelSays is the response of a model that answered with text.
func modelSays(text string) string {
	encoded, _ := json.Marshal(text)
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":` + string(encoded) + `}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`
}

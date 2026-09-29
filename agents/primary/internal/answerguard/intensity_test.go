package answerguard

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// The texts the intensity tests hand the guard, and what the fake guardrail
// makes of each.
const (
	intensityFine     = "The launch is on Thursday."
	intensityPassword = "The password is hunter2."
	intensityContact  = "Write to priya@example.com."
	intensityBoth     = "The password is hunter2; write to priya@example.com."
	intensityLeftSSN  = "Her SSN is 123-45-6789, write to priya@example.com."
	intensityUnread   = "Nobody can read this one."
)

// intensityGuard is a guard over a guardrail that finds a password, an email
// address, and, in intensityLeftSSN, a social security number it was set up
// to mask and did not, and that cannot read intensityUnread.
func intensityGuard(t *testing.T) *Guard {
	t.Helper()
	return testGuard(t, &fakeBedrock{answer: func(_ context.Context, text string) (*bedrockruntime.ApplyGuardrailOutput, error) {
		none := types.GuardrailSensitiveInformationPolicyActionNone
		anonymized := types.GuardrailSensitiveInformationPolicyActionAnonymized
		switch text {
		case intensityPassword:
			return response(types.GuardrailActionGuardrailIntervened, "The password is {PASSWORD}.",
				entity("PASSWORD", "hunter2", anonymized)), nil
		case intensityContact:
			return response(types.GuardrailActionNone, "", entity("EMAIL", "priya@example.com", none)), nil
		case intensityBoth:
			return response(types.GuardrailActionGuardrailIntervened, "The password is {PASSWORD}; write to priya@example.com.",
				entity("PASSWORD", "hunter2", anonymized), entity("EMAIL", "priya@example.com", none)), nil
		case intensityLeftSSN:
			return response(types.GuardrailActionNone, "",
				entity("US_SOCIAL_SECURITY_NUMBER", "123-45-6789", none), entity("EMAIL", "priya@example.com", none)), nil
		case intensityUnread:
			return nil, context.DeadlineExceeded
		}
		return response(types.GuardrailActionNone, ""), nil
	}}, nil)
}

// TestTheIntensityDecidesHowMuchOfWhatWasFoundIsActedOn is the table of what
// the guard's own verdicts come to at each level: off shares what was given,
// secrets masks only the kinds that are always masked and leaves an email
// address, phone number or address as written, and strict is the rule for
// everybody else. What could not be checked is shared as given at off, and
// nowhere else. The state and the kinds are what was acted on at that level.
func TestTheIntensityDecidesHowMuchOfWhatWasFoundIsActedOn(t *testing.T) {
	guard := intensityGuard(t)
	type outcome struct {
		state     string
		kinds     []string
		shareable []string
	}
	for _, testCase := range []struct {
		name  string
		texts []string
		off   outcome
		// secrets and strict
		secrets, strict outcome
	}{
		{
			name:    "nothing found",
			texts:   []string{intensityFine, ""},
			off:     outcome{StateClear, nil, []string{intensityFine, ""}},
			secrets: outcome{StateClear, nil, []string{intensityFine, ""}},
			strict:  outcome{StateClear, nil, []string{intensityFine, ""}},
		},
		{
			name:    "only a kind that is always masked",
			texts:   []string{intensityFine, intensityPassword},
			off:     outcome{StateClear, nil, []string{intensityFine, intensityPassword}},
			secrets: outcome{StateMasked, []string{"PASSWORD"}, []string{intensityFine, "The password is {PASSWORD}."}},
			strict:  outcome{StateMasked, []string{"PASSWORD"}, []string{intensityFine, "The password is {PASSWORD}."}},
		},
		{
			name:    "only a held kind",
			texts:   []string{intensityFine, intensityContact},
			off:     outcome{StateClear, nil, []string{intensityFine, intensityContact}},
			secrets: outcome{StateClear, nil, []string{intensityFine, intensityContact}},
			strict:  outcome{StateHeld, []string{"EMAIL"}, []string{intensityFine, "Write to {EMAIL}."}},
		},
		{
			name:    "a held kind and a kind that is always masked in one text",
			texts:   []string{intensityBoth},
			off:     outcome{StateClear, nil, []string{intensityBoth}},
			secrets: outcome{StateMasked, []string{"PASSWORD"}, []string{"The password is {PASSWORD}; write to priya@example.com."}},
			strict:  outcome{StateHeld, []string{"EMAIL"}, []string{"The password is {PASSWORD}; write to {EMAIL}."}},
		},
		{
			name:  "a kind that is always masked left in the text by the guardrail",
			texts: []string{intensityLeftSSN},
			off:   outcome{StateClear, nil, []string{intensityLeftSSN}},
			// Only the fully masked version is known to be without the number,
			// so the email address is masked there as well.
			secrets: outcome{StateMasked, []string{"US_SOCIAL_SECURITY_NUMBER"}, []string{"Her SSN is {US_SOCIAL_SECURITY_NUMBER}, write to {EMAIL}."}},
			strict:  outcome{StateHeld, []string{"EMAIL", "US_SOCIAL_SECURITY_NUMBER"}, []string{"Her SSN is {US_SOCIAL_SECURITY_NUMBER}, write to {EMAIL}."}},
		},
		{
			name:    "an answer that could not be checked",
			texts:   []string{intensityFine, intensityUnread},
			off:     outcome{StateClear, nil, []string{intensityFine, intensityUnread}},
			secrets: outcome{StateHeld, []string{Unchecked}, nil},
			strict:  outcome{StateHeld, []string{Unchecked}, nil},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			verdict := guard.Check(context.Background(), testCase.texts)
			for level, want := range map[Intensity]outcome{
				IntensityOff: testCase.off, IntensitySecrets: testCase.secrets, IntensityStrict: testCase.strict,
			} {
				got := verdict.At(len(testCase.texts), level)
				if got.State != want.state || got.Kinds == nil || !slices.Equal(got.Kinds, want.kinds) ||
					(got.Shareable == nil) != (want.shareable == nil) || !slices.Equal(got.Shareable, want.shareable) {
					t.Errorf("at %s: state=%q kinds=%#v shareable=%#v, want state=%q kinds=%#v shareable=%#v",
						level, got.State, got.Kinds, got.Shareable, want.state, want.kinds, want.shareable)
				}
				if shareable := verdict.ShareableAt(len(testCase.texts), level); (shareable == nil) != (want.shareable == nil) || !slices.Equal(shareable, want.shareable) {
					t.Errorf("ShareableAt(%s) = %#v, want %#v", level, shareable, want.shareable)
				}
			}
			if strict := verdict.Shareable(len(testCase.texts)); (strict == nil) != (testCase.strict.shareable == nil) || !slices.Equal(strict, testCase.strict.shareable) {
				t.Errorf("Shareable = %#v, want the strict outcome %#v", strict, testCase.strict.shareable)
			}
		})
	}
}

// TestALevelNobodyKnowsIsTheStrictOne: an intensity this code does not know
// is read as the strictest, so a mistyped or later one never shares more than
// the guard ever did.
func TestALevelNobodyKnowsIsTheStrictOne(t *testing.T) {
	verdict := intensityGuard(t).Check(context.Background(), []string{intensityContact})
	for _, level := range []Intensity{"", "lenient", "STRICT", "Secrets"} {
		got := verdict.At(1, level)
		if got.State != StateHeld || !slices.Equal(got.Shareable, []string{"Write to {EMAIL}."}) {
			t.Errorf("level %q: %+v, want what strict shares", level, got)
		}
	}
}

// TestAVerdictThatDoesNotAddUpSharesNothingBeyondOff: whatever else is wrong
// with a verdict, secrets and strict share nothing from it and say it could
// not be checked, or what it is held for: a text too few or too many, a state
// nobody knows, and a held answer for a secret with no masked version to share
// instead. Off does not depend on the verdict's findings, only on holding the
// n texts.
func TestAVerdictThatDoesNotAddUpSharesNothingBeyondOff(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		verdict Verdict
		n       int
	}{
		{"a text too few", Verdict{State: StateClear, Texts: []string{"a"}}, 2},
		{"a text too many", Verdict{State: StateMasked, Kinds: []string{"PIN"}, Texts: []string{"a", "b"}}, 1},
		{"a state nobody knows", Verdict{State: "quarantined", Texts: []string{"a"}}, 1},
		{"the zero verdict", Verdict{}, 1},
		{"held for a secret with no masked version", Verdict{State: StateHeld, Kinds: []string{"PASSWORD"}, Texts: []string{"a"}}, 1},
		{"held for a secret with a masked version of another length", Verdict{State: StateHeld, Kinds: []string{"PASSWORD"}, Texts: []string{"a"}, Masked: []string{"a", "b"}}, 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, level := range []Intensity{IntensitySecrets, IntensityStrict} {
				got := testCase.verdict.At(testCase.n, level)
				if got.Shareable != nil || got.State != StateHeld || len(got.Kinds) == 0 {
					t.Errorf("at %s: %+v, want nothing shared and a held state that says why", level, got)
				}
			}
		})
	}
}

// TestSecretsHoldsAHeldVerdictToWhatItSaysItIsHeldFor: at secrets a held
// verdict is shared from only when it names what it is held for and every
// name is one the guard makes: held kinds, or an always-masked kind the
// guardrail left in the text. One that names nothing, a kind nobody knows, or
// that it could not be checked, shares nothing, whatever masked version it
// carries.
func TestSecretsHoldsAHeldVerdictToWhatItSaysItIsHeldFor(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		verdict Verdict
	}{
		{"held for nothing", Verdict{State: StateHeld, Texts: []string{"a"}, Masked: []string{"a"}}},
		{"held for a kind nobody knows", Verdict{State: StateHeld, Kinds: []string{"SHOE_SIZE"}, Texts: []string{"a"}, Masked: []string{"a"}}},
		{"held for a kind nobody knows beside a known one", Verdict{State: StateHeld, Kinds: []string{"EMAIL", "SHOE_SIZE"}, Texts: []string{"a"}, Masked: []string{"a"}}},
		{"held as unchecked, whatever it carries", Verdict{State: StateHeld, Kinds: []string{Unchecked}, Texts: []string{"a"}, Masked: []string{"a"}}},
		{"held as unchecked beside a known kind", Verdict{State: StateHeld, Kinds: []string{"EMAIL", Unchecked}, Texts: []string{"a"}, Masked: []string{"a"}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := testCase.verdict.At(1, IntensitySecrets)
			if got.Shareable != nil || got.State != StateHeld || len(got.Kinds) == 0 {
				t.Errorf("%+v, want nothing shared and a held state that says why", got)
			}
		})
	}
}

// TestASecretsReadingNeedsOnlyWhatAVerdictSays covers the verdicts that were
// not made by Check, which know nothing of the texts as given or the kinds
// masked beside a held one: they are read from their Kinds and their Texts
// alone, and never shared beyond what the guard would.
func TestASecretsReadingNeedsOnlyWhatAVerdictSays(t *testing.T) {
	held := Verdict{State: StateHeld, Kinds: []string{"EMAIL", "PHONE"}, Texts: []string{"call 415 555 0100, write to priya@example.com"}, Masked: []string{"call {PHONE}, write to {EMAIL}"}}
	if got := held.At(1, IntensitySecrets); got.State != StateClear || len(got.Kinds) != 0 || got.Kinds == nil ||
		!slices.Equal(got.Shareable, held.Texts) {
		t.Errorf("only held kinds at secrets = %+v, want clear, no kinds, and the texts as written", got)
	}
	masked := Verdict{State: StateMasked, Kinds: []string{"PIN"}, Texts: []string{"the PIN is {PIN}"}}
	if got := masked.At(1, IntensitySecrets); got.State != StateMasked || !slices.Equal(got.Kinds, []string{"PIN"}) || !slices.Equal(got.Shareable, masked.Texts) {
		t.Errorf("only secrets at secrets = %+v", got)
	}
	// Off shares what it can tell was given: the texts, when the verdict does
	// not keep them.
	if got := held.At(1, IntensityOff); got.State != StateClear || !slices.Equal(got.Shareable, held.Texts) {
		t.Errorf("off without the texts as given = %+v, want the verdict's texts", got)
	}
	if got := (Verdict{State: StateHeld, Kinds: []string{Unchecked}, Texts: []string{"a", "b"}, Given: []string{"x", "y"}}).At(2, IntensityOff); !slices.Equal(got.Shareable, []string{"x", "y"}) {
		t.Errorf("off with the texts as given = %+v, want them", got)
	}
	if got := (Verdict{State: StateClear, Texts: []string{"a"}}).At(2, IntensityOff); got.Shareable != nil || got.State != StateHeld {
		t.Errorf("off with a text too few = %+v, want nothing shared", got)
	}
}

// TestWhatAVerdictCarriesForTheLevelsBelowStrict: the guard records the texts
// as given, and every kind that is always masked it found beside the held
// ones, since a held verdict's kinds name only what it is held for. A checked
// answer keeps its own copies of both.
func TestWhatAVerdictCarriesForTheLevelsBelowStrict(t *testing.T) {
	guard := intensityGuard(t)
	for _, testCase := range []struct {
		name        string
		texts       []string
		state       string
		kinds       []string
		secretKinds []string
	}{
		{"nothing found", []string{intensityFine}, StateClear, nil, nil},
		{"only a kind that is always masked", []string{intensityPassword}, StateMasked, []string{"PASSWORD"}, []string{"PASSWORD"}},
		{"only a held kind", []string{intensityContact}, StateHeld, []string{"EMAIL"}, nil},
		{"both in one text", []string{intensityBoth}, StateHeld, []string{"EMAIL"}, []string{"PASSWORD"}},
		{"both in two texts", []string{intensityPassword, intensityContact}, StateHeld, []string{"EMAIL"}, []string{"PASSWORD"}},
		{"a secret left in the text", []string{intensityLeftSSN}, StateHeld, []string{"EMAIL", "US_SOCIAL_SECURITY_NUMBER"}, []string{"US_SOCIAL_SECURITY_NUMBER"}},
		{"could not be checked", []string{intensityFine, intensityUnread}, StateHeld, []string{Unchecked}, nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			given := slices.Clone(testCase.texts)
			verdict := guard.Check(context.Background(), given)
			if verdict.State != testCase.state || !slices.Equal(verdict.Kinds, testCase.kinds) || !slices.Equal(verdict.SecretKinds, testCase.secretKinds) {
				t.Fatalf("verdict = state %q kinds %v secret kinds %v, want %q %v %v",
					verdict.State, verdict.Kinds, verdict.SecretKinds, testCase.state, testCase.kinds, testCase.secretKinds)
			}
			if !slices.Equal(verdict.Given, testCase.texts) {
				t.Fatalf("Given = %q, want the texts as they were handed over: %q", verdict.Given, testCase.texts)
			}
			given[0] = "changed by the caller"
			if slices.Equal(verdict.Given, given) {
				t.Errorf("the verdict's Given follows the caller's slice: %q", verdict.Given)
			}
		})
	}
}

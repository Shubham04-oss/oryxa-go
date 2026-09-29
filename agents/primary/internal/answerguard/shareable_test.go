package answerguard

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// TestAVerdictAllowsSharingOnlyWhatTheRuleSays: clear and masked verdicts
// share their Texts, a held one shares its fully masked version when it has
// one for every text, and everything else shares nothing, which is nil and
// not an empty list.
func TestAVerdictAllowsSharingOnlyWhatTheRuleSays(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		verdict Verdict
		n       int
		want    []string
	}{
		{
			name:    "nothing found shares the texts as given",
			verdict: Verdict{State: StateClear, Texts: []string{"a", "b"}},
			n:       2, want: []string{"a", "b"},
		},
		{
			name:    "kinds that are always masked share the masked texts",
			verdict: Verdict{State: StateMasked, Kinds: []string{"PASSWORD"}, Texts: []string{"The password is {PASSWORD}."}},
			n:       1, want: []string{"The password is {PASSWORD}."},
		},
		{
			name:    "a held kind shares the fully masked version, never the texts",
			verdict: Verdict{State: StateHeld, Kinds: []string{"EMAIL"}, Texts: []string{"Write to priya@example.com."}, Masked: []string{"Write to {EMAIL}."}},
			n:       1, want: []string{"Write to {EMAIL}."},
		},
		{
			name: "a held kind with a text of each kind shares every masked text",
			verdict: Verdict{State: StateHeld, Kinds: []string{"EMAIL"}, Texts: []string{"x", "y@example.com", ""},
				Masked: []string{"x", "{EMAIL}", ""}},
			n: 3, want: []string{"x", "{EMAIL}", ""},
		},
		{
			name:    "a held answer with no masked version shares nothing",
			verdict: Verdict{State: StateHeld, Kinds: []string{"EMAIL"}, Texts: []string{"priya@example.com"}},
			n:       1, want: nil,
		},
		{
			name:    "an answer that could not be checked shares nothing",
			verdict: Verdict{State: StateHeld, Kinds: []string{Unchecked}, Texts: []string{"whatever it said"}},
			n:       1, want: nil,
		},
		{
			name:    "a clear verdict with a text too few shares nothing",
			verdict: Verdict{State: StateClear, Texts: []string{"a"}},
			n:       2, want: nil,
		},
		{
			name:    "a masked verdict with a text too many shares nothing",
			verdict: Verdict{State: StateMasked, Kinds: []string{"PIN"}, Texts: []string{"a", "b"}},
			n:       1, want: nil,
		},
		{
			name: "a held verdict whose masked version is short shares nothing",
			verdict: Verdict{State: StateHeld, Kinds: []string{"PHONE"}, Texts: []string{"a", "b"},
				Masked: []string{"a"}},
			n: 2, want: nil,
		},
		{
			name: "a held verdict whose masked version is long shares nothing",
			verdict: Verdict{State: StateHeld, Kinds: []string{"PHONE"}, Texts: []string{"a"},
				Masked: []string{"a", "b"}},
			n: 1, want: nil,
		},
		{
			name: "a held verdict whose texts are short shares nothing though its masked version fits",
			verdict: Verdict{State: StateHeld, Kinds: []string{"PHONE"}, Texts: []string{"a"},
				Masked: []string{"a", "b"}},
			n: 2, want: nil,
		},
		{
			name:    "a state nobody knows shares nothing",
			verdict: Verdict{State: "quarantined", Texts: []string{"a"}},
			n:       1, want: nil,
		},
		{
			name:    "the zero verdict shares nothing",
			verdict: Verdict{},
			n:       1, want: nil,
		},
		{
			name:    "a negative count shares nothing",
			verdict: Verdict{State: StateClear, Texts: []string{"a"}},
			n:       -1, want: nil,
		},
		{
			name:    "no texts to share is an empty list, not nil",
			verdict: Verdict{State: StateClear},
			n:       0, want: []string{},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := testCase.verdict.Shareable(testCase.n)
			if (got == nil) != (testCase.want == nil) || !slices.Equal(got, testCase.want) {
				t.Fatalf("Shareable(%d) = %#v, want %#v", testCase.n, got, testCase.want)
			}
		})
	}
}

// TestSharingHandsBackACopyOfWhatItMayShare: a caller that edits what it was
// allowed to share does not edit the verdict it came from.
func TestSharingHandsBackACopyOfWhatItMayShare(t *testing.T) {
	clear := Verdict{State: StateClear, Texts: []string{"a", "b"}}
	shared := clear.Shareable(2)
	shared[0] = "changed"
	if !slices.Equal(clear.Texts, []string{"a", "b"}) {
		t.Fatalf("the verdict's texts became %q", clear.Texts)
	}
	held := Verdict{State: StateHeld, Kinds: []string{"EMAIL"}, Texts: []string{"x@example.com"}, Masked: []string{"{EMAIL}"}}
	shared = held.Shareable(1)
	shared[0] = "changed"
	if !slices.Equal(held.Masked, []string{"{EMAIL}"}) {
		t.Fatalf("the verdict's masked version became %q", held.Masked)
	}
}

// TestWhatTheGuardFindsIsSharedAsTheRuleSays drives the rule with the
// verdicts the guard itself makes, one of each kind, rather than with ones
// written by hand: what a room, the ledger and the check route are handed is
// what Check returns.
func TestWhatTheGuardFindsIsSharedAsTheRuleSays(t *testing.T) {
	const (
		fine     = "The launch is on Thursday."
		password = "The password is hunter2."
		contact  = "Write to priya@example.com."
		unread   = "Nobody can read this one."
	)
	bedrock := &fakeBedrock{answer: func(_ context.Context, text string) (*bedrockruntime.ApplyGuardrailOutput, error) {
		switch text {
		case password:
			return response(types.GuardrailActionGuardrailIntervened, "The password is {PASSWORD}.",
				entity("PASSWORD", "hunter2", types.GuardrailSensitiveInformationPolicyActionAnonymized)), nil
		case contact:
			return response(types.GuardrailActionNone, "",
				entity("EMAIL", "priya@example.com", types.GuardrailSensitiveInformationPolicyActionNone)), nil
		case unread:
			return nil, context.DeadlineExceeded
		}
		return response(types.GuardrailActionNone, ""), nil
	}}
	guard := testGuard(t, bedrock, nil)

	for _, testCase := range []struct {
		name  string
		texts []string
		want  []string
	}{
		{"nothing found", []string{fine, ""}, []string{fine, ""}},
		{"a kind that is always masked", []string{fine, password}, []string{fine, "The password is {PASSWORD}."}},
		{"a held kind", []string{fine, contact}, []string{fine, "Write to {EMAIL}."}},
		{"a held kind beside one that is always masked", []string{password, contact}, []string{"The password is {PASSWORD}.", "Write to {EMAIL}."}},
		{"an answer that could not be checked", []string{fine, unread}, nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := guard.Check(context.Background(), testCase.texts).Shareable(len(testCase.texts))
			if (got == nil) != (testCase.want == nil) || !slices.Equal(got, testCase.want) {
				t.Fatalf("shareable = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

package interactions

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// fakeGuardPolicy says how the guard treats one owner's answers, and keeps
// whose it was asked about.
type fakeGuardPolicy struct {
	effective guardpolicy.Effective
	err       error
	asked     [][2]string
}

func (policy *fakeGuardPolicy) Effective(_ context.Context, organizationID, userID string) (guardpolicy.Effective, error) {
	policy.asked = append(policy.asked, [2]string{organizationID, userID})
	return policy.effective, policy.err
}

// strictlyHolding is what a member who chose nothing gets where the organization
// holds off-topic answers.
func strictlyHolding() guardpolicy.Effective {
	return guardpolicy.Effective{Owner: guardpolicy.LevelSecrets, Peer: guardpolicy.LevelStrict, Relevance: guardpolicy.RelevanceHold}
}

// relevanceHeld is the verdict of an answer the relevance check held for
// carrying health details its request did not ask for: held, with what it was
// held for, and a masked version the guard made for the owner's review that
// must not be offered.
func relevanceHeld() answerguard.Verdict {
	return answerguard.Verdict{
		State: answerguard.StateHeld, Kinds: []string{answerguard.OffTopic, "health"}, Texts: textsGiven, Masked: textsMasked,
		Relevance: &answerguard.RelevanceOutcome{Judged: true, WouldHold: true, Held: true, Extras: []string{"health"}},
	}
}

// TestTheGuardIsToldWhatWasAskedAndWhatTheRunCalled: the relevance check reads
// the request in its asker's words, the tools the run called from the run's own
// record, and the mode and the regulated preset of its owner's organization,
// and is asked about the answer's owner.
func TestTheGuardIsToldWhatWasAskedAndWhatTheRunCalled(t *testing.T) {
	guard := &fakeGuard{}
	service, _, approved := guardedService(t, guard, nil)
	settings := &fakeGuardPolicy{effective: guardpolicy.Effective{
		Owner: guardpolicy.LevelSecrets, Peer: guardpolicy.LevelStrict, Relevance: guardpolicy.RelevanceShadow, Regulated: true,
	}}
	service.guardPolicy = settings

	ctx := agents.WithRunSources(context.Background(), []string{"interactions_get", "load_memory"})
	if _, err := service.Respond(ctx, target, approved.ID, answerGiven); err != nil {
		t.Fatal(err)
	}
	if len(guard.relevance) != 1 {
		t.Fatalf("the guard was asked %d times", len(guard.relevance))
	}
	got := guard.relevance[0]
	if got.Request != "How do I reach Priya?" || !slices.Equal(got.Sources, []string{"interactions_get", "load_memory"}) ||
		got.Mode != answerguard.RelevanceShadow || !got.Regulated || got.OrganizationID != "org_1" || got.UserID != "user_b" {
		t.Fatalf("the guard was told %+v", got)
	}
	// The owner's settings, and no other member's, decide.
	if !slices.Equal(settings.asked[0][:], []string{"org_1", "user_b"}) || len(settings.asked) != 1 {
		t.Fatalf("settings were asked for %v, want the answering owner's", settings.asked)
	}
}

// An answer that does not come from a run has no tools to name, so nothing is
// judged; the mode is what the settings say, hold when they say something this
// code does not know, and off where no settings are kept.
func TestTheRelevanceModeIsTheSettingsAndOffWhereNoneAreKept(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		settings *fakeGuardPolicy
		want     string
	}{
		{"none kept", nil, answerguard.RelevanceOff},
		{"hold", &fakeGuardPolicy{effective: strictlyHolding()}, answerguard.RelevanceHold},
		{"shadow", &fakeGuardPolicy{effective: guardpolicy.Effective{Relevance: guardpolicy.RelevanceShadow}}, answerguard.RelevanceShadow},
		{"off", &fakeGuardPolicy{effective: guardpolicy.Effective{Relevance: guardpolicy.RelevanceOff}}, answerguard.RelevanceOff},
		{"a mode nobody knows", &fakeGuardPolicy{effective: guardpolicy.Effective{Relevance: "sometimes"}}, answerguard.RelevanceHold},
		{"none chosen", &fakeGuardPolicy{}, answerguard.RelevanceHold},
	} {
		guard := &fakeGuard{}
		service, _, approved := guardedService(t, guard, nil)
		if testCase.settings != nil {
			service.guardPolicy = testCase.settings
		}
		if _, err := service.Respond(context.Background(), target, approved.ID, answerGiven); err != nil {
			t.Fatal(err)
		}
		if got := guard.relevance[0]; got.Mode != testCase.want || len(got.Sources) != 0 {
			t.Errorf("%s: the guard was told %+v, want mode %s and no sources", testCase.name, got, testCase.want)
		}
	}
}

// TestAnAnswerHeldForRelevanceWaitsForItsOwnersReview: the relevance check's
// hold is a hold like any other, in the flow the owner already has: in review,
// nothing delivered, what it found kept by kind, the guardrail's masked version
// not offered since what is wrong with the answer is what it says, and the
// owner sends it as it was, or withholds it.
func TestAnAnswerHeldForRelevanceWaitsForItsOwnersReview(t *testing.T) {
	for _, level := range []guardpolicy.Level{guardpolicy.LevelStrict, guardpolicy.LevelSecrets} {
		t.Run(string(level), func(t *testing.T) {
			verdict := relevanceHeld()
			service, repo, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
			service.guardPolicy = &fakeGuardPolicy{effective: guardpolicy.Effective{Owner: level, Peer: level, Relevance: guardpolicy.RelevanceHold}}
			answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
			if err != nil || answered.Status != StatusReview || answered.Result != nil {
				t.Fatalf("answered = %+v, %v; want held for review", answered, err)
			}
			held := asTheOwner(t, service, approved.ID)
			if held.Held == nil || held.Held.MaskedResult != nil || held.Held.Result.Summary != textsAsHeld[0] || held.Guard == nil ||
				held.Guard.State != answerguard.StateHeld || !slices.Equal(held.Guard.Kinds, []string{answerguard.OffTopic, "health"}) {
				t.Fatalf("the owner reads %+v, guard %+v; want the answer held with no masked version, by relevance", held, held.Guard)
			}
			// What the member who asked reads of it is its status alone.
			asked, err := service.Get(context.Background(), requester, approved.ID)
			if err != nil || asked.Status != StatusReview || asked.Result != nil || asked.Held != nil || asked.Guard != nil {
				t.Fatalf("the asker reads %+v, %v", asked, err)
			}
			if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", approved.ID, agents.InteractionReviewInput{Action: ReviewSendMasked}); !errors.Is(err, ErrConflict) {
				t.Fatalf("sending it masked: %v, want ErrConflict, it has no masked version", err)
			}
			sent, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", approved.ID, agents.InteractionReviewInput{Action: ReviewSend})
			if err != nil || sent.Status != StatusCompleted || repo.reviews != 1 {
				t.Fatalf("the owner's review = %+v, %v; want it sent as held", sent, err)
			}
		})
	}
}

// What the owner's level takes out is theirs to choose: at the strictest a held
// kind holds the answer for review, and at the level that only masks secrets it
// is delivered as written; the secrets are masked at both.
func TestAnAnswerIsReadAtItsOwnersLevel(t *testing.T) {
	secretsAndEmail := answerguard.Verdict{
		State: answerguard.StateHeld, Kinds: []string{"EMAIL"}, Texts: textsAsHeld, Masked: textsMasked,
		SecretKinds: []string{"PIN"}, Given: textsGiven,
	}
	for _, testCase := range []struct {
		name       string
		verdict    answerguard.Verdict
		level      guardpolicy.Level
		wantStatus string
		wantTexts  []string
		wantState  string
		wantKinds  []string
	}{
		{"an email at the strictest level", heldVerdict, guardpolicy.LevelStrict, StatusReview, nil, answerguard.StateHeld, []string{"EMAIL", "PHONE"}},
		{"an email at the level that masks secrets", answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{"EMAIL", "PHONE"}, Texts: textsGiven, Masked: textsMasked, Given: textsGiven},
			guardpolicy.LevelSecrets, StatusCompleted, textsGiven, answerguard.StateClear, []string{}},
		{"an email and a pin at the level that masks secrets", secretsAndEmail, guardpolicy.LevelSecrets, StatusCompleted, textsAsHeld, answerguard.StateMasked, []string{"PIN"}},
		{"a pin at the strictest level", answerguard.Verdict{State: answerguard.StateMasked, Kinds: []string{"PIN"}, Texts: textsAsHeld, SecretKinds: []string{"PIN"}},
			guardpolicy.LevelStrict, StatusCompleted, textsAsHeld, answerguard.StateMasked, []string{"PIN"}},
		{"nothing found at the level that masks secrets", answerguard.Verdict{State: answerguard.StateClear, Texts: textsGiven, Given: textsGiven},
			guardpolicy.LevelSecrets, StatusCompleted, textsGiven, answerguard.StateClear, []string{}},
		{"an answer that could not be checked at any level", uncheckedOne, guardpolicy.LevelSecrets, StatusReview, nil, answerguard.StateHeld, []string{answerguard.Unchecked}},
		{"a level nobody knows is the strictest", heldVerdict, "lax", StatusReview, nil, answerguard.StateHeld, []string{"EMAIL", "PHONE"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			verdict := testCase.verdict
			service, _, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
			service.guardPolicy = &fakeGuardPolicy{effective: guardpolicy.Effective{Owner: testCase.level, Peer: testCase.level, Relevance: guardpolicy.RelevanceOff}}
			answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
			if err != nil || answered.Status != testCase.wantStatus {
				t.Fatalf("answered = %+v, %v; want %s", answered, err, testCase.wantStatus)
			}
			if testCase.wantTexts != nil {
				got := []string{answered.Result.Summary, answered.Result.Content, joinLines(answered.Result.Artifacts)}
				if !slices.Equal(got, testCase.wantTexts) {
					t.Errorf("delivered %q, want %q", got, testCase.wantTexts)
				}
			}
			owner := asTheOwner(t, service, approved.ID)
			if owner.Guard == nil || owner.Guard.State != testCase.wantState || !slices.Equal(nonNilStrings(owner.Guard.Kinds), testCase.wantKinds) {
				t.Errorf("guard = %+v, want %s %v", owner.Guard, testCase.wantState, testCase.wantKinds)
			}
		})
	}
}

func joinLines(lines []string) string {
	joined := ""
	for index, line := range lines {
		if index > 0 {
			joined += "\n"
		}
		joined += line
	}
	return joined
}

// In shadow mode the relevance check is recorded and never holds: the answer is
// delivered as the read left it.
func TestShadowRelevanceNeverHoldsAnAnswer(t *testing.T) {
	verdict := answerguard.Verdict{
		State: answerguard.StateClear, Texts: textsGiven,
		Relevance: &answerguard.RelevanceOutcome{Judged: true, WouldHold: true, Extras: []string{"hr"}},
	}
	service, _, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
	service.guardPolicy = &fakeGuardPolicy{effective: guardpolicy.Effective{Peer: guardpolicy.LevelStrict, Relevance: guardpolicy.RelevanceShadow}}
	answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
	if err != nil || answered.Status != StatusCompleted || answered.Result == nil || answered.Held != nil {
		t.Fatalf("answered = %+v, %v; want it delivered", answered, err)
	}
}

// Where the owner's own levels cannot be read, the answer is read as strictly as
// it can be, with the settings' own strictest reading, and the log says so
// without a text.
func TestAnUnreadablePolicyReadsAnAnswerStrictly(t *testing.T) {
	verdict := heldVerdict
	guard := &fakeGuard{verdict: &verdict}
	service, _, approved := guardedService(t, guard, nil)
	service.guardPolicy = &fakeGuardPolicy{effective: strictlyHolding(), err: errors.New("settings storage unavailable")}
	answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
	if err != nil || answered.Status != StatusReview {
		t.Fatalf("answered = %+v, %v; want it held", answered, err)
	}
	if guard.relevance[0].Mode != answerguard.RelevanceHold {
		t.Fatalf("the relevance mode = %s, want the strictest reading's hold", guard.relevance[0].Mode)
	}
}

// TestTheWorkspacePolicyStillHoldsEveryAnswerForItsOwner: HoldForReview holds an
// answer the guard delivered, as it did, and one it already held stays as it
// is, with the masked version it may have, or has none.
func TestTheWorkspacePolicyStillHoldsEveryAnswerForItsOwner(t *testing.T) {
	t.Run("an answer the guard delivered", func(t *testing.T) {
		verdict := answerguard.Verdict{State: answerguard.StateClear, Texts: textsGiven}
		service, _, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
		service.policy = fakeWorkspacePolicy{settings: policy.Settings{HoldForReview: true}}
		service.guardPolicy = &fakeGuardPolicy{effective: strictlyHolding()}
		answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
		if err != nil || answered.Status != StatusReview {
			t.Fatalf("answered = %+v, %v; want it held", answered, err)
		}
		if held := asTheOwner(t, service, approved.ID); held.Held == nil || held.Held.MaskedResult != nil || held.Held.Result.Summary != answerGiven.Summary {
			t.Fatalf("the owner reads %+v", held)
		}
	})
	t.Run("an answer the relevance check held", func(t *testing.T) {
		verdict := relevanceHeld()
		service, _, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
		service.policy = fakeWorkspacePolicy{settings: policy.Settings{HoldForReview: true}}
		service.guardPolicy = &fakeGuardPolicy{effective: strictlyHolding()}
		answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
		if err != nil || answered.Status != StatusReview {
			t.Fatalf("answered = %+v, %v; want it held", answered, err)
		}
		held := asTheOwner(t, service, approved.ID)
		if held.Held == nil || held.Held.MaskedResult != nil || !slices.Equal(held.Guard.Kinds, []string{answerguard.OffTopic, "health"}) {
			t.Fatalf("the owner reads %+v, guard %+v; want it held for relevance still, with no masked version", held, held.Guard)
		}
	})
}

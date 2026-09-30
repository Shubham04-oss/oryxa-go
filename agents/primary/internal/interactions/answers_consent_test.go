package interactions

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// fakeConsentGuard answers every answer with what result makes of it, or passes
// it as it is, and keeps what it was asked.
type fakeConsentGuard struct {
	result func(consentguard.Input) consentguard.Result
	inputs []consentguard.Input
}

func (guard *fakeConsentGuard) Check(_ context.Context, input consentguard.Input) consentguard.Result {
	guard.inputs = append(guard.inputs, input)
	if guard.result != nil {
		return guard.result(input)
	}
	return consentguard.Result{Action: consentguard.ActionPass, Texts: slices.Clone(input.Texts)}
}

// holding is a consent guard that holds every answer for kind, with its texts
// as it was given them.
func holding(kind string) *fakeConsentGuard {
	return &fakeConsentGuard{result: func(input consentguard.Input) consentguard.Result {
		return consentguard.Result{Action: consentguard.ActionHold, Texts: slices.Clone(input.Texts), Reasons: []consentguard.Reason{{Kind: kind, Detail: "for a reason"}}}
	}}
}

// The answer an owner's agent gives a question asked in Slack in these tests,
// and the texts it comes to.
var (
	slackAnswer = agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "The keys live in the vault.", Content: "Ask for the dev vault path.",
	}
	slackTexts = []string{"The keys live in the vault.", "Ask for the dev vault path.", ""}
	// whatTheRunRead is what the run behind the answer read, as the respond tool
	// hands it to the ledger.
	whatTheRunRead = []agents.ToolResult{{Tool: "knowledge_search", Parts: []string{"passages.text: The keys live in the vault.", "total: 1"}}}
)

// slackService is a ledger with the answer guard and the consent guard reading
// every answer, and an approved request in it made from Slack, its owner having
// consented as consent says ("summary", "access", or none).
func slackService(t *testing.T, guard Guard, consent ConsentGuard, consented string) (*Service, *fakeRepository, agents.Interaction) {
	t.Helper()
	service, repo, approved := guardedService(t, guard, nil)
	service.consentGuard = consent
	request := repo.requests[approved.ID]
	request.Origin = OriginSlack
	request.Decision.Consent = consented
	repo.requests[approved.ID] = request
	return service, repo, request
}

func withResults(results []agents.ToolResult) context.Context {
	return agents.WithRunResults(context.Background(), results)
}

// TestTheConsentGuardReadsTheAnswerAfterTheAnswerGuardWithWhatItNeeds: it is
// told the texts the answer guard left to be shown, what the run's tools
// returned, the request in its asker's words and how far its owner let the
// answer go.
func TestTheConsentGuardReadsTheAnswerAfterTheAnswerGuardWithWhatItNeeds(t *testing.T) {
	// The answer guard masks a PIN in the content; the consent guard sees that.
	afterTheAnswerGuard := []string{"The keys live in the vault.", "Ask for the dev vault path. The PIN is {PIN}.", ""}
	masking := &fakeGuard{verdict: &answerguard.Verdict{
		State: answerguard.StateMasked, Kinds: []string{"PIN"}, Texts: afterTheAnswerGuard, SecretKinds: []string{"PIN"},
	}}
	for _, testCase := range []struct {
		consented string
		want      consentguard.Level
	}{
		{ConsentSummary, consentguard.LevelSummary},
		{ConsentAccess, consentguard.LevelAccess},
		{"", consentguard.LevelSummary},
		{"everything", consentguard.LevelSummary},
	} {
		consent := &fakeConsentGuard{}
		service, _, request := slackService(t, masking, consent, testCase.consented)
		input := slackAnswer
		input.Content = "Ask for the dev vault path. The PIN is 4921."
		if _, err := service.Respond(withResults(whatTheRunRead), target, request.ID, input); err != nil {
			t.Fatal(err)
		}
		if len(consent.inputs) != 1 {
			t.Fatalf("consent %q: the consent guard was asked %d times", testCase.consented, len(consent.inputs))
		}
		got := consent.inputs[0]
		if got.Level != testCase.want || got.Question != "How do I reach Priya?" || !slices.Equal(got.Texts, afterTheAnswerGuard) ||
			got.Failed || got.WrittenByOwner || got.RequestID != request.ID || got.OrganizationID != "org_1" {
			t.Errorf("consent %q: the consent guard was told %+v", testCase.consented, got)
		}
		wantSources := []consentguard.Source{{Tool: "knowledge_search", Parts: []string{"passages.text: The keys live in the vault.", "total: 1"}}}
		if len(got.Sources) != 1 || got.Sources[0].Tool != wantSources[0].Tool || !slices.Equal(got.Sources[0].Parts, wantSources[0].Parts) {
			t.Errorf("consent %q: the sources were %+v, want %+v", testCase.consented, got.Sources, wantSources)
		}
	}
}

func TestAnAnswerTheConsentGuardPassesIsDeliveredAsItWas(t *testing.T) {
	consent := &fakeConsentGuard{}
	service, _, request := slackService(t, &fakeGuard{}, consent, ConsentSummary)
	answered, err := service.Respond(withResults(whatTheRunRead), target, request.ID, slackAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if answered.Status != StatusCompleted || answered.Result == nil || answered.Result.Summary != slackAnswer.Summary || answered.Result.Content != slackAnswer.Content ||
		answered.Held != nil || answered.Guard == nil || answered.Guard.State != answerguard.StateClear || len(answered.Guard.Kinds) != 0 {
		t.Fatalf("answered = %+v, guard %+v; want it completed as given, clear", answered, answered.Guard)
	}
}

func TestAnAnswerTheConsentGuardOnlyMasksIsDeliveredMasked(t *testing.T) {
	consent := &fakeConsentGuard{result: func(input consentguard.Input) consentguard.Result {
		return consentguard.Result{
			Action: consentguard.ActionMask, Masked: []string{consentguard.KindSlackToken},
			Texts: []string{input.Texts[0], "Use {SLACK_TOKEN} to post.", ""},
		}
	}}
	service, _, request := slackService(t, &fakeGuard{}, consent, ConsentAccess)
	answered, err := service.Respond(withResults(whatTheRunRead), target, request.ID, slackAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if answered.Status != StatusCompleted || answered.Result == nil || answered.Result.Summary != slackAnswer.Summary || answered.Result.Content != "Use {SLACK_TOKEN} to post." ||
		answered.Guard == nil || answered.Guard.State != answerguard.StateMasked || !slices.Equal(answered.Guard.Kinds, []string{consentguard.KindSlackToken}) {
		t.Fatalf("answered = %+v, guard %+v; want it delivered with the token masked, and masked", answered.Result, answered.Guard)
	}
	// What is kept says nothing of the token, and what is logged never does.
	if encoded := answered.Result.Content + answered.Result.Summary; strings.Contains(encoded, "xoxb") {
		t.Fatalf("the token is in %q", encoded)
	}
}

// TestAnAnswerTheConsentGuardHoldsWaitsForItsOwnersReview: the hold is a hold
// like any other, in the flow the owner already has: in review, nothing
// delivered, the asker told its status alone, the owner sending it as it was
// held (with its credentials masked) or withholding it.
func TestAnAnswerTheConsentGuardHoldsWaitsForItsOwnersReview(t *testing.T) {
	consent := &fakeConsentGuard{result: func(input consentguard.Input) consentguard.Result {
		return consentguard.Result{
			Action: consentguard.ActionHold, Masked: []string{consentguard.KindAPIKey},
			Texts:   []string{input.Texts[0], "Use {API_KEY} to read it.", ""},
			Reasons: []consentguard.Reason{{Kind: consentguard.KindCopied, Detail: "a run of 31 words is copied"}, {Kind: consentguard.KindLink, Detail: "a link"}},
		}
	}}
	service, repo, request := slackService(t, &fakeGuard{}, consent, ConsentSummary)
	answered, err := service.Respond(withResults(whatTheRunRead), target, request.ID, slackAnswer)
	if err != nil || answered.Status != StatusReview || answered.Result != nil {
		t.Fatalf("answered = %+v, %v; want it held for review", answered, err)
	}
	held := asTheOwner(t, service, request.ID)
	if held.Held == nil || held.Held.Result.Content != "Use {API_KEY} to read it." || held.Held.Result.Summary != slackAnswer.Summary || held.Held.MaskedResult != nil ||
		held.Guard == nil || held.Guard.State != answerguard.StateHeld ||
		!slices.Equal(held.Guard.Kinds, []string{consentguard.KindAPIKey, consentguard.KindCopied, consentguard.KindLink}) {
		t.Fatalf("the owner reads %+v, guard %+v; want the masked answer held, for what it was held for", held.Held, held.Guard)
	}
	asked, err := service.Get(context.Background(), requester, request.ID)
	if err != nil || asked.Status != StatusReview || asked.Result != nil || asked.Held != nil || asked.Guard != nil {
		t.Fatalf("the asker reads %+v, %v; want its status alone", asked, err)
	}
	if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", request.ID, agents.InteractionReviewInput{Action: ReviewSendMasked}); !errors.Is(err, ErrConflict) {
		t.Fatalf("sending it masked: %v, want ErrConflict, it has no masked version", err)
	}
	sent, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", request.ID, agents.InteractionReviewInput{Action: ReviewSend})
	if err != nil || sent.Status != StatusCompleted || repo.reviews != 1 || sent.Result == nil || strings.Contains(sent.Result.Content, "vault path") {
		t.Fatalf("the owner's review = %+v, %v; want it sent as held, masked", sent, err)
	}
}

func TestTheOwnerMayWithholdWhatTheConsentGuardHeld(t *testing.T) {
	service, _, request := slackService(t, &fakeGuard{}, holding(consentguard.KindNoSources), ConsentAccess)
	if answered, err := service.Respond(context.Background(), target, request.ID, slackAnswer); err != nil || answered.Status != StatusReview {
		t.Fatalf("answered = %+v, %v; want it held for having no sources", answered, err)
	}
	withheld, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", request.ID, agents.InteractionReviewInput{Action: ReviewWithhold})
	if err != nil || withheld.Status != StatusWithheld || withheld.Result != nil {
		t.Fatalf("withheld = %+v, %v", withheld, err)
	}
}

func TestOnlyAnAnswerToASlackRequestIsReadByTheConsentGuard(t *testing.T) {
	// One an agent filed, in the ordinary way.
	consent := &fakeConsentGuard{}
	service, repo, request := slackService(t, &fakeGuard{}, consent, ConsentSummary)
	agentMade := repo.requests[request.ID]
	agentMade.Origin = OriginAgent
	repo.requests[request.ID] = agentMade
	if _, err := service.Respond(context.Background(), target, request.ID, slackAnswer); err != nil || len(consent.inputs) != 0 {
		t.Errorf("an agent's request: %v, the consent guard was asked %d times", err, len(consent.inputs))
	}
	// One that names no origin, as an older build stored it.
	consent = &fakeConsentGuard{}
	service, repo, request = slackService(t, &fakeGuard{}, consent, ConsentSummary)
	old := repo.requests[request.ID]
	old.Origin = ""
	repo.requests[request.ID] = old
	if _, err := service.Respond(context.Background(), target, request.ID, slackAnswer); err != nil || len(consent.inputs) != 0 {
		t.Errorf("a request with no origin: %v, the consent guard was asked %d times", err, len(consent.inputs))
	}
	// A member's own answer to themself is theirs to read.
	consent = &fakeConsentGuard{}
	service, repo, request = slackService(t, &fakeGuard{}, consent, ConsentSummary)
	yours := repo.requests[request.ID]
	yours.Requester.UserID = yours.Target.UserID
	repo.requests[request.ID] = yours
	if _, err := service.Respond(context.Background(), target, request.ID, slackAnswer); err != nil || len(consent.inputs) != 0 {
		t.Errorf("a member asking themself: %v, the consent guard was asked %d times", err, len(consent.inputs))
	}
}

func TestWithNoConsentGuardASlackAnswerIsReadByTheAnswerGuardAlone(t *testing.T) {
	for name, consent := range map[string]ConsentGuard{
		"none":                     nil,
		"a nil pointer to a guard": (*consentguard.Guard)(nil),
	} {
		guard := &fakeGuard{}
		service, _, request := slackService(t, guard, nil, ConsentSummary)
		configured, err := New(Config{Repository: service.repository, Directory: service.directory, Guard: guard, ConsentGuard: consent, Now: service.now})
		if err != nil {
			t.Fatal(err)
		}
		if configured.consentGuard != nil {
			t.Errorf("%s: the ledger kept %v as a consent guard", name, configured.consentGuard)
		}
		answered, err := configured.Respond(context.Background(), target, request.ID, slackAnswer)
		if err != nil || answered.Status != StatusCompleted || len(guard.checked) != 1 {
			t.Errorf("%s: answered = %+v, %v; the answer guard read %d answers", name, answered, err, len(guard.checked))
		}
	}
}

// The consent guard reads with no answer guard as well: what it holds does not
// depend on another guard being there.
func TestTheConsentGuardReadsAnAnswerWhereThereIsNoAnswerGuard(t *testing.T) {
	consent := holding(consentguard.KindUngrounded)
	service, _, request := slackService(t, nil, consent, ConsentAccess)
	answered, err := service.Respond(withResults(whatTheRunRead), target, request.ID, slackAnswer)
	if err != nil || answered.Status != StatusReview || len(consent.inputs) != 1 || !slices.Equal(consent.inputs[0].Texts, slackTexts) {
		t.Fatalf("answered = %+v, %v; the consent guard read %v", answered, err, consent.inputs)
	}
}

// An answer the answer guard held stays held, beside what the consent guard
// found: the credentials are masked in every version the owner may send, the
// fully masked one included, and it is held for both kinds' reasons.
func TestAnAnswerTheAnswerGuardHeldStaysHeldAndLosesItsCredentials(t *testing.T) {
	token := "xo" + "xb-" + "123456789012-" + "1234567890123-" + "abcdefghijklmnopqrstuvwx"
	held := answerguard.Verdict{
		State: answerguard.StateHeld, Kinds: []string{"EMAIL"}, Given: textsGiven,
		Texts:  []string{"Mail priya@example.com.", "Use " + token + " for it.", ""},
		Masked: []string{"Mail {EMAIL}.", "Use " + token + " for it.", ""},
	}
	consent := &fakeConsentGuard{result: func(input consentguard.Input) consentguard.Result {
		masked, kinds := consentguard.Strip(input.Texts)
		return consentguard.Result{Action: consentguard.ActionMask, Texts: masked, Masked: kinds}
	}}
	service, _, request := slackService(t, &fakeGuard{verdict: &held}, consent, ConsentAccess)
	answered, err := service.Respond(withResults(whatTheRunRead), target, request.ID, slackAnswer)
	if err != nil || answered.Status != StatusReview {
		t.Fatalf("answered = %+v, %v; want it held", answered, err)
	}
	if !slices.Equal(consent.inputs[0].Texts, held.Texts) {
		t.Errorf("the consent guard read %q, want the texts the answer guard held", consent.inputs[0].Texts)
	}
	read := asTheOwner(t, service, request.ID)
	if read.Held == nil || read.Held.Result.Summary != "Mail priya@example.com." || read.Held.Result.Content != "Use {SLACK_TOKEN} for it." {
		t.Fatalf("what the owner may send as held = %+v", read.Held)
	}
	if read.Held.MaskedResult == nil || read.Held.MaskedResult.Summary != "Mail {EMAIL}." || read.Held.MaskedResult.Content != "Use {SLACK_TOKEN} for it." {
		t.Fatalf("what the owner may send masked = %+v, want the credentials out of it as well", read.Held.MaskedResult)
	}
	if read.Guard == nil || read.Guard.State != answerguard.StateHeld || !slices.Equal(read.Guard.Kinds, []string{"EMAIL", consentguard.KindSlackToken}) {
		t.Fatalf("guard = %+v, want held for the email, and the token named", read.Guard)
	}
}

func TestAnAnswerTheAnswerGuardHeldIsNotLoosenedByAConsentGuardThatPasses(t *testing.T) {
	consent := &fakeConsentGuard{}
	service, _, request := slackService(t, &fakeGuard{verdict: &heldVerdict}, consent, ConsentAccess)
	answered, err := service.Respond(withResults(whatTheRunRead), target, request.ID, answerGiven)
	if err != nil || answered.Status != StatusReview {
		t.Fatalf("answered = %+v, %v; want it held all the same", answered, err)
	}
	read := asTheOwner(t, service, request.ID)
	if read.Held == nil || read.Held.MaskedResult == nil || read.Held.MaskedResult.Summary != textsMasked[0] ||
		read.Guard == nil || !slices.Equal(read.Guard.Kinds, []string{"EMAIL", "PHONE"}) {
		t.Fatalf("the owner reads %+v, guard %+v", read.Held, read.Guard)
	}
}

func TestAGuardThatDoesNotGiveBackEveryTextHoldsTheAnswerAsUncheckedWithItsCredentialsMasked(t *testing.T) {
	consent := &fakeConsentGuard{result: func(consentguard.Input) consentguard.Result {
		return consentguard.Result{Action: consentguard.ActionPass, Texts: []string{"only one"}}
	}}
	service, _, request := slackService(t, &fakeGuard{}, consent, ConsentAccess)
	token := "xo" + "xb-" + "123456789012-" + "1234567890123-" + "abcdefghijklmnopqrstuvwx"
	answered, err := service.Respond(context.Background(), target, request.ID, agents.InteractionResponseInput{Status: StatusCompleted, Summary: "Use " + token, Content: "ok"})
	if err != nil || answered.Status != StatusReview {
		t.Fatalf("answered = %+v, %v; want it held", answered, err)
	}
	read := asTheOwner(t, service, request.ID)
	if read.Held == nil || strings.Contains(read.Held.Result.Summary, token) || !strings.Contains(read.Held.Result.Summary, "{SLACK_TOKEN}") ||
		read.Guard == nil || !slices.Equal(read.Guard.Kinds, []string{consentguard.KindSlackToken, consentguard.KindUnchecked}) {
		t.Fatalf("the owner reads %+v, guard %+v", read.Held, read.Guard)
	}
}

func TestTheConsentGuardIsToldWhoWroteTheAnswerAndWhetherItFailed(t *testing.T) {
	consent := &fakeConsentGuard{}
	service, _, request := slackService(t, &fakeGuard{}, consent, ConsentSummary)
	if _, err := service.Respond(context.Background(), inOwnSession(targetPrimary), request.ID, slackAnswer); err != nil {
		t.Fatal(err)
	}
	if got := consent.inputs[0]; !got.WrittenByOwner || got.Failed || len(got.Sources) != 0 {
		t.Errorf("an answer the owner wrote in their session was described as %+v", got)
	}

	consent = &fakeConsentGuard{}
	service, _, request = slackService(t, &fakeGuard{}, consent, ConsentSummary)
	failed := agents.InteractionResponseInput{Status: StatusFailed, Summary: "I could not find it.", Content: "Nothing matched."}
	answered, err := service.Respond(context.Background(), target, request.ID, failed)
	if err != nil || answered.Status != StatusFailed || answered.Result == nil {
		t.Fatalf("answered = %+v, %v", answered, err)
	}
	if got := consent.inputs[0]; got.WrittenByOwner || !got.Failed {
		t.Errorf("a failure was described as %+v", got)
	}
}

// A failure the guard holds is held like an answer: in review, and it wins
// nothing.
func TestAFailureTheConsentGuardHoldsIsHeldAsAFailure(t *testing.T) {
	service, _, request := slackService(t, &fakeGuard{}, holding(consentguard.KindLink), ConsentSummary)
	failed := agents.InteractionResponseInput{Status: StatusFailed, Summary: "It failed.", Content: "See https://example.com/logs."}
	answered, err := service.Respond(context.Background(), target, request.ID, failed)
	if err != nil || answered.Status != StatusReview {
		t.Fatalf("answered = %+v, %v; want it held", answered, err)
	}
	if read := asTheOwner(t, service, request.ID); read.Held == nil || read.Held.Result.Status != StatusFailed {
		t.Fatalf("held = %+v, want the failure", read.Held)
	}
}

func TestAWorkspaceThatHoldsEveryAnswerHoldsTheMaskedOne(t *testing.T) {
	consent := &fakeConsentGuard{result: func(input consentguard.Input) consentguard.Result {
		return consentguard.Result{Action: consentguard.ActionMask, Masked: []string{consentguard.KindPassword}, Texts: []string{input.Texts[0], "the password is {PASSWORD}", ""}}
	}}
	service, _, request := slackService(t, &fakeGuard{}, consent, ConsentAccess)
	service.policy = fakeWorkspacePolicy{settings: policy.Settings{HoldForReview: true}}
	answered, err := service.Respond(context.Background(), target, request.ID, slackAnswer)
	if err != nil || answered.Status != StatusReview {
		t.Fatalf("answered = %+v, %v; want it held", answered, err)
	}
	if read := asTheOwner(t, service, request.ID); read.Held == nil || read.Held.Result.Content != "the password is {PASSWORD}" {
		t.Fatalf("held = %+v, want the masked answer", read.Held)
	}
}

// The real consent guard fits the hook: nothing to stand on holds an answer,
// what the run read and a grounder that agrees deliver it, a summary that copies
// the run's words is held, and a token is masked.
func TestTheConsentGuardItselfReadsAnAnswerToASlackRequest(t *testing.T) {
	grounded := consentguard.GrounderFunc(func(context.Context, consentguard.GroundingInput) (consentguard.Grounding, error) {
		return consentguard.Grounding{Grounded: consentguard.Score{Value: 0.9}, Relevant: consentguard.Score{Value: 0.9}}, nil
	})
	token := "xo" + "xb-" + "123456789012-" + "1234567890123-" + "abcdefghijklmnopqrstuvwx"
	copied := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa quebec romeo sierra tango"
	for _, testCase := range []struct {
		name      string
		consented string
		results   []agents.ToolResult
		input     agents.InteractionResponseInput
		status    string
		content   string
		kinds     []string
	}{
		{"nothing to stand on", ConsentAccess, nil, slackAnswer, StatusReview, "", []string{consentguard.KindNoSources}},
		{"standing on what the run read", ConsentSummary, whatTheRunRead, slackAnswer, StatusCompleted, slackAnswer.Content, nil},
		{"a summary that copies the run's words", ConsentSummary, []agents.ToolResult{{Tool: "knowledge_search", Parts: []string{copied}}},
			agents.InteractionResponseInput{Status: StatusCompleted, Summary: "As it says:", Content: copied}, StatusReview, "", []string{consentguard.KindCopied}},
		{"the same words, as excerpts the owner allowed", ConsentAccess, []agents.ToolResult{{Tool: "knowledge_search", Parts: []string{copied}}},
			agents.InteractionResponseInput{Status: StatusCompleted, Summary: "As it says:", Content: copied}, StatusCompleted, copied, nil},
		{"a token", ConsentAccess, whatTheRunRead, agents.InteractionResponseInput{Status: StatusCompleted, Summary: "Done.", Content: "Use " + token + " to post."},
			StatusCompleted, "Use {SLACK_TOKEN} to post.", []string{consentguard.KindSlackToken}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, _, request := slackService(t, &fakeGuard{}, consentguard.New(grounded, consentguard.Config{}), testCase.consented)
			answered, err := service.Respond(withResults(testCase.results), target, request.ID, testCase.input)
			if err != nil || answered.Status != testCase.status {
				t.Fatalf("answered = %+v, %v; want %s", answered, err, testCase.status)
			}
			if testCase.status == StatusCompleted && answered.Result.Content != testCase.content {
				t.Errorf("content = %q, want %q", answered.Result.Content, testCase.content)
			}
			if testCase.status == StatusCompleted && strings.Contains(answered.Result.Content, token) {
				t.Errorf("the token was delivered")
			}
			read := asTheOwner(t, service, request.ID)
			if len(testCase.kinds) > 0 && (read.Guard == nil || !slices.Equal(read.Guard.Kinds, testCase.kinds)) {
				t.Errorf("guard = %+v, want kinds %v", read.Guard, testCase.kinds)
			}
		})
	}
}

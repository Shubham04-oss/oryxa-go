package interactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
)

// fakeGuard answers every answer with its verdict, or finds nothing when it
// has none, and keeps the texts it was given.
type fakeGuard struct {
	verdict *answerguard.Verdict
	checked [][]string
}

func (guard *fakeGuard) Check(_ context.Context, texts []string) answerguard.Verdict {
	guard.checked = append(guard.checked, slices.Clone(texts))
	if guard.verdict == nil {
		return answerguard.Verdict{State: answerguard.StateClear, Texts: slices.Clone(texts)}
	}
	return *guard.verdict
}

// The answer B's agent gives A in these tests, and what a guard makes of it.
var (
	answerGiven = agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "Reach Priya at priya@example.com.", Content: "Her number is +1 415 555 0100; the PIN is 4921.",
		Artifacts: []string{"notes-for-priya@example.com.md", "plan.md"},
	}
	textsGiven   = []string{"Reach Priya at priya@example.com.", "Her number is +1 415 555 0100; the PIN is 4921.", "notes-for-priya@example.com.md\nplan.md"}
	textsAsHeld  = []string{"Reach Priya at priya@example.com.", "Her number is +1 415 555 0100; the PIN is {PIN}.", "notes-for-priya@example.com.md\nplan.md"}
	textsMasked  = []string{"Reach Priya at {EMAIL}.", "Her number is {PHONE}; the PIN is {PIN}.", "notes-for-{EMAIL}.md\nplan.md"}
	heldVerdict  = answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{"EMAIL", "PHONE"}, Texts: textsAsHeld, Masked: textsMasked}
	uncheckedOne = answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{answerguard.Unchecked}, Texts: textsGiven}
)

// guardedService is the ledger with guard reading every answer, logging to
// logs, and an approved request from A to B's Engineering agent in it.
func guardedService(t *testing.T, guard Guard, logs io.Writer) (*Service, *fakeRepository, agents.Interaction) {
	t.Helper()
	if logs == nil {
		logs = io.Discard
	}
	repo := newFakeRepository()
	config := Config{
		Repository: repo, Directory: fakeDirectory{directory: agents.OrganizationDirectory{
			People: []agents.DirectoryPerson{{UserID: "user_b", Email: "b@example.com", DisplayName: "B", Agents: []agents.DirectoryAgent{{
				ID: "agt_engineering", OwnerUserID: "user_b", OwnerEmail: "b@example.com", OwnerDisplayName: "B", Kind: "specialist", Name: "Engineering",
			}}}},
		}},
		Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:    func() time.Time { return testNow },
	}
	if guard != nil {
		config.Guard = guard
	}
	service, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), requester, "token", "key", agents.InteractionCreateInput{
		TargetAgentID: "agt_engineering", Purpose: "Contact", Task: "How do I reach Priya?",
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.Decide(context.Background(), target, created.ID, agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "Approved"})
	if err != nil {
		t.Fatal(err)
	}
	return service, repo, approved
}

// asTheOwner is request as its addressed member reads it in their own
// session, the one view that holds the texts the guard held.
func asTheOwner(t *testing.T, service *Service, id string) agents.Interaction {
	t.Helper()
	read, err := service.Get(context.Background(), inOwnSession(targetPrimary), id)
	if err != nil {
		t.Fatal(err)
	}
	return read
}

// TestAnAnswerNothingIsFoundInIsDeliveredAsGiven: the guard reads every text
// the member who asked would, the summary, the content and the artifact
// references, and an answer it finds nothing in completes the request as
// it was given, saying the guard found it clear.
func TestAnAnswerNothingIsFoundInIsDeliveredAsGiven(t *testing.T) {
	guard := &fakeGuard{}
	service, _, approved := guardedService(t, guard, nil)
	answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
	if err != nil {
		t.Fatal(err)
	}
	if len(guard.checked) != 1 || !slices.Equal(guard.checked[0], textsGiven) {
		t.Fatalf("the guard read %q, want the summary, the content and the artifacts", guard.checked)
	}
	if answered.Status != StatusCompleted || answered.Result == nil || answered.Result.Summary != answerGiven.Summary ||
		answered.Held != nil || answered.Guard == nil || answered.Guard.State != answerguard.StateClear || len(answered.Guard.Kinds) != 0 {
		t.Fatalf("answered=%+v guard=%+v, want completed as given, clear", answered, answered.Guard)
	}
	if encoded, _ := json.Marshal(answered.Guard); string(encoded) != `{"state":"clear"}` {
		t.Fatalf("guard reads %s, want {\"state\":\"clear\"}", encoded)
	}
}

// TestAnAnswerWithOnlyMaskedKindsIsDeliveredMasked: the member who asked
// reads the guardrail's masked texts, told which kinds were masked.
func TestAnAnswerWithOnlyMaskedKindsIsDeliveredMasked(t *testing.T) {
	masked := []string{"Reach Priya.", "The PIN is {PIN}.", "plan.md"}
	service, _, approved := guardedService(t, &fakeGuard{verdict: &answerguard.Verdict{State: answerguard.StateMasked, Kinds: []string{"PIN"}, Texts: masked}}, nil)
	answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
	if err != nil {
		t.Fatal(err)
	}
	if answered.Status != StatusCompleted || answered.Result == nil || answered.Result.Summary != "Reach Priya." || answered.Result.Content != "The PIN is {PIN}." ||
		!slices.Equal(answered.Result.Artifacts, []string{"plan.md"}) || answered.Held != nil {
		t.Fatalf("answered=%+v, want completed with the masked texts", answered.Result)
	}
	if encoded, _ := json.Marshal(answered.Guard); string(encoded) != `{"state":"masked","kinds":["PIN"]}` {
		t.Fatalf("guard reads %s", encoded)
	}
}

// TestAHeldAnswerWaitsInReview: an answer with a held kind leaves the
// request in review with no result, holding the guardrail's output and its
// fully masked version for the addressed member.
func TestAHeldAnswerWaitsInReview(t *testing.T) {
	verdict := heldVerdict
	service, repo, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
	answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
	if err != nil {
		t.Fatal(err)
	}
	if answered.Status != StatusReview || answered.Result != nil || answered.Held != nil || answered.Guard == nil {
		t.Fatalf("answered=%+v, want in review with no result, and nothing held shown to the agent that answered", answered)
	}
	answered = asTheOwner(t, service, approved.ID)
	if answered.Held == nil || answered.Held.MaskedResult == nil {
		t.Fatalf("the owner reads %+v, want both versions held", answered)
	}
	if held := answered.Held.Result; held.Status != StatusCompleted || held.Content != textsAsHeld[1] || !slices.Equal(held.Artifacts, []string{"notes-for-priya@example.com.md", "plan.md"}) {
		t.Fatalf("held=%+v, want the guardrail's output", held)
	}
	if masked := answered.Held.MaskedResult; masked.Summary != textsMasked[0] || masked.Content != textsMasked[1] || !slices.Equal(masked.Artifacts, []string{"notes-for-{EMAIL}.md", "plan.md"}) {
		t.Fatalf("masked=%+v, want every held value replaced", masked)
	}
	if encoded, _ := json.Marshal(answered.Guard); string(encoded) != `{"state":"held","kinds":["EMAIL","PHONE"]}` {
		t.Fatalf("guard reads %s", encoded)
	}
	if repo.requests[approved.ID].Status != StatusReview {
		t.Fatalf("the ledger holds %s", repo.requests[approved.ID].Status)
	}
}

// TestAnAnswerThatCouldNotBeCheckedIsHeldWithNothingToSendMasked: held as
// UNCHECKED, it has no masked version, so only sending it as it is or
// withholding it is possible.
func TestAnAnswerThatCouldNotBeCheckedIsHeldWithNothingToSendMasked(t *testing.T) {
	verdict := uncheckedOne
	service, repo, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
	if _, err := service.Respond(context.Background(), target, approved.ID, answerGiven); err != nil {
		t.Fatal(err)
	}
	answered := asTheOwner(t, service, approved.ID)
	if answered.Status != StatusReview || answered.Held == nil || answered.Held.MaskedResult != nil ||
		answered.Guard == nil || !slices.Equal(answered.Guard.Kinds, []string{answerguard.Unchecked}) {
		t.Fatalf("answered=%+v guard=%+v, want held as UNCHECKED with no masked version", answered, answered.Guard)
	}
	if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), approved.ID, agents.InteractionReviewInput{Action: ReviewSendMasked}); !errors.Is(err, ErrConflict) {
		t.Fatalf("sending masked an unchecked answer: error=%v, want ErrConflict", err)
	}
	if repo.requests[approved.ID].Status != StatusReview || repo.reviews != 0 {
		t.Fatal("the refused review changed the request")
	}
	if encoded, _ := json.Marshal(answered.Held); strings.Contains(string(encoded), "masked_result") {
		t.Fatalf("held reads %s, want no masked_result", encoded)
	}
}

// TestAGuardVerdictThatDoesNotAddUpIsHeldUnchecked: a verdict with the wrong
// number of texts, or a state nobody knows, fails closed.
func TestAGuardVerdictThatDoesNotAddUpIsHeldUnchecked(t *testing.T) {
	for _, verdict := range []answerguard.Verdict{
		{State: answerguard.StateClear, Texts: []string{"only one"}},
		{State: "fine", Texts: textsGiven},
		{State: answerguard.StateHeld, Kinds: []string{"EMAIL"}, Texts: textsGiven, Masked: []string{"short"}},
	} {
		service, _, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
		if _, err := service.Respond(context.Background(), target, approved.ID, answerGiven); err != nil {
			t.Fatal(err)
		}
		answered := asTheOwner(t, service, approved.ID)
		if answered.Status != StatusReview || answered.Guard == nil || !slices.Equal(answered.Guard.Kinds, []string{answerguard.Unchecked}) ||
			answered.Held == nil || answered.Held.Result.Summary != answerGiven.Summary {
			t.Errorf("verdict %+v: answered=%+v, want held as UNCHECKED, as given", verdict, answered)
		}
	}
}

// TestWithoutAGuardAnswersAreDeliveredUnchecked: with no guardrail named, an
// answer completes the request as given and says nothing of a guard, as
// before.
func TestWithoutAGuardAnswersAreDeliveredUnchecked(t *testing.T) {
	service, _, approved := guardedService(t, nil, nil)
	answered, err := service.Respond(context.Background(), target, approved.ID, answerGiven)
	if err != nil || answered.Status != StatusCompleted || answered.Result == nil || answered.Result.Content != answerGiven.Content || answered.Guard != nil {
		t.Fatalf("answered=%+v err=%v, want completed as given with no guard", answered, err)
	}
}

// TestTheOwnerSendsSendsMaskedOrWithholds: in their own session, the
// addressed member sends the held answer as it was held, sends its masked
// version, or withholds it; the first two complete the request with that
// result, the last ends it withheld with none; and the review records what
// they did, who, and when.
func TestTheOwnerSendsSendsMaskedOrWithholds(t *testing.T) {
	for action, want := range map[string]struct {
		status  string
		summary string
	}{
		ReviewSend:       {StatusCompleted, textsAsHeld[0]},
		ReviewSendMasked: {StatusCompleted, textsMasked[0]},
		ReviewWithhold:   {StatusWithheld, ""},
	} {
		verdict := heldVerdict
		service, repo, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
		if _, err := service.Respond(context.Background(), target, approved.ID, answerGiven); err != nil {
			t.Fatal(err)
		}
		reviewed, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), approved.ID, agents.InteractionReviewInput{Action: " " + strings.ToUpper(action) + " "})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if reviewed.Status != want.status || reviewed.Held != nil {
			t.Fatalf("%s: reviewed=%+v, want %s with nothing held", action, reviewed, want.status)
		}
		if want.summary == "" && reviewed.Result != nil || want.summary != "" && (reviewed.Result == nil || reviewed.Result.Summary != want.summary) {
			t.Fatalf("%s: result=%+v, want summary %q", action, reviewed.Result, want.summary)
		}
		review := reviewed.Review
		if review == nil || review.Action != action || review.DecidedBy.Kind != DecidedByMember || review.DecidedBy.UserID != "user_b" || !review.DecidedAt.Equal(testNow) {
			t.Fatalf("%s: review=%+v, want the member's own, now", action, review)
		}
		if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), approved.ID, agents.InteractionReviewInput{Action: ReviewSend}); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s, reviewed again: error=%v, want ErrConflict", action, err)
		}
		if repo.reviews != 1 {
			t.Fatalf("%s: %d reviews recorded, want one", action, repo.reviews)
		}
	}
}

// TestAHeldFailureIsSentAsAFailure: a failed answer is read and held like
// any other, and sending it fails the request, as the answer did.
func TestAHeldFailureIsSentAsAFailure(t *testing.T) {
	verdict := heldVerdict
	service, _, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
	failed := answerGiven
	failed.Status = StatusFailed
	if _, err := service.Respond(context.Background(), target, approved.ID, failed); err != nil {
		t.Fatal(err)
	}
	if held := asTheOwner(t, service, approved.ID); held.Status != StatusReview || held.Held == nil || held.Held.Result.Status != StatusFailed {
		t.Fatalf("held=%+v, want a failure held in review", held)
	}
	sent, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), approved.ID, agents.InteractionReviewInput{Action: ReviewSendMasked})
	if err != nil || sent.Status != StatusFailed || sent.Result == nil || sent.Result.Status != StatusFailed {
		t.Fatalf("sent=%+v err=%v, want the request failed with the masked failure", sent, err)
	}
}

// TestOnlyTheOwnersOwnSessionReviewsAnAnswerInReview: the member who asked,
// even in their own session, cannot review; nobody else can read the
// request; no agent reviews, the addressed member's own included; an action
// nobody knows is refused; and only an answer in review, before its request
// expires, is reviewed.
func TestOnlyTheOwnersOwnSessionReviewsAnAnswerInReview(t *testing.T) {
	verdict := heldVerdict
	service, repo, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
	if _, err := service.Respond(context.Background(), target, approved.ID, answerGiven); err != nil {
		t.Fatal(err)
	}
	send := agents.InteractionReviewInput{Action: ReviewSend}
	for name, attempt := range map[string]struct {
		actor agents.InteractionActor
		input agents.InteractionReviewInput
		want  error
	}{
		"the member who asked":            {inOwnSession(requester), send, ErrForbidden},
		"another member":                  {inOwnSession(otherMember), send, ErrNotFound},
		"the addressed member's agent":    {targetPrimary, send, ErrForbidden},
		"the addressed specialist itself": {target, send, ErrForbidden},
		"an action nobody knows":          {inOwnSession(targetPrimary), agents.InteractionReviewInput{Action: "forward"}, ErrInvalid},
	} {
		if _, err := service.ReviewAsOwner(context.Background(), attempt.actor, approved.ID, attempt.input); !errors.Is(err, attempt.want) {
			t.Errorf("%s: error=%v, want %v", name, err, attempt.want)
		}
	}
	if repo.requests[approved.ID].Status != StatusReview || repo.reviews != 0 {
		t.Fatal("a refused review changed the request")
	}

	// A request not in review, and one whose expiry passed, are not reviewed.
	pending, err := service.Create(context.Background(), requester, "token", "other", agents.InteractionCreateInput{TargetAgentID: "agt_engineering", Purpose: "P", Task: "T"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), pending.ID, send); !errors.Is(err, ErrConflict) {
		t.Fatalf("reviewing a pending request: error=%v, want ErrConflict", err)
	}
	expired := repo.requests[approved.ID]
	expired.ExpiresAt = testNow.Add(-time.Second)
	repo.requests[approved.ID] = expired
	if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), approved.ID, send); !errors.Is(err, ErrExpired) {
		t.Fatalf("reviewing past the request's expiry: error=%v, want ErrExpired", err)
	}
}

// TestWhatEachMemberSeesOfAHeldAnswer: while it is in review, the member who
// asked sees its status and nothing of the answer, nor what the guard
// found, reading it or listing it, and neither does their agent; the
// addressed member sees both versions and the kinds. Withheld, the member
// who asked sees that, and the review, and no result; sent, the answer as
// sent, what the guard found and the review, and never the held versions.
func TestWhatEachMemberSeesOfAHeldAnswer(t *testing.T) {
	verdict := heldVerdict
	service, _, approved := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
	if _, err := service.Respond(context.Background(), target, approved.ID, answerGiven); err != nil {
		t.Fatal(err)
	}
	asker := func() agents.Interaction {
		t.Helper()
		read, err := service.Get(context.Background(), inOwnSession(requester), approved.ID)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := service.List(context.Background(), requester, Query{Box: BoxSent})
		if err != nil || len(listed) != 1 {
			t.Fatalf("listed=%+v err=%v", listed, err)
		}
		readJSON, _ := json.Marshal(read)
		listedJSON, _ := json.Marshal(listed[0])
		if string(readJSON) != string(listedJSON) {
			t.Fatalf("the member who asked reads %s but lists %s", readJSON, listedJSON)
		}
		return read
	}
	inReview := asker()
	if inReview.Status != StatusReview || inReview.Result != nil || inReview.Held != nil || inReview.Guard != nil {
		t.Fatalf("the member who asked sees %+v, want the status alone", inReview)
	}
	encoded, _ := json.Marshal(inReview)
	for _, private := range []string{"priya@example.com", "{EMAIL}", "EMAIL", "held"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("the member who asked reads %q in %s", private, encoded)
		}
	}
	owner, err := service.Get(context.Background(), inOwnSession(targetPrimary), approved.ID)
	if err != nil || owner.Held == nil || owner.Held.MaskedResult == nil || owner.Guard == nil || !slices.Equal(owner.Guard.Kinds, []string{"EMAIL", "PHONE"}) {
		t.Fatalf("the addressed member sees %+v (%v), want both versions and the kinds", owner, err)
	}
	for _, agent := range []agents.InteractionActor{target, targetPrimary} {
		byAgent, err := service.Get(context.Background(), agent, approved.ID)
		if err != nil || byAgent.Held != nil || byAgent.Guard == nil || byAgent.Status != StatusReview {
			t.Fatalf("the addressed member's agent %s sees %+v (%v), want that it is held, and what for, and no held text", agent.AgentID, byAgent, err)
		}
	}

	if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), approved.ID, agents.InteractionReviewInput{Action: ReviewSendMasked}); err != nil {
		t.Fatal(err)
	}
	sent := asker()
	if sent.Status != StatusCompleted || sent.Result == nil || sent.Result.Summary != textsMasked[0] || sent.Held != nil ||
		sent.Guard == nil || sent.Guard.State != answerguard.StateHeld || sent.Review == nil || sent.Review.Action != ReviewSendMasked {
		t.Fatalf("once sent masked the member who asked sees %+v, want the masked answer, the guard and the review", sent)
	}

	verdict = heldVerdict
	withholding, _, second := guardedService(t, &fakeGuard{verdict: &verdict}, nil)
	if _, err := withholding.Respond(context.Background(), target, second.ID, answerGiven); err != nil {
		t.Fatal(err)
	}
	if _, err := withholding.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), second.ID, agents.InteractionReviewInput{Action: ReviewWithhold}); err != nil {
		t.Fatal(err)
	}
	withheld, err := withholding.Get(context.Background(), requester, second.ID)
	if err != nil || withheld.Status != StatusWithheld || withheld.Result != nil || withheld.Held != nil || withheld.Guard != nil ||
		withheld.Review == nil || withheld.Review.Action != ReviewWithhold || withheld.Review.DecidedBy.UserID != "user_b" {
		t.Fatalf("withheld, the member who asked sees %+v (%v), want the status and the review alone", withheld, err)
	}
}

// fakeBedrock finds, in every text it is given, the email address and phone
// number of answerGiven, held, and its PIN, masked.
type fakeBedrock struct{}

func (fakeBedrock) ApplyGuardrail(_ context.Context, input *bedrockruntime.ApplyGuardrailInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	text := aws.ToString(input.Content[0].(*types.GuardrailContentBlockMemberText).Value.Text)
	var entities []types.GuardrailPiiEntityFilter
	for kind, value := range map[string]string{"EMAIL": "priya@example.com", "PHONE": "+1 415 555 0100"} {
		if strings.Contains(text, value) {
			entities = append(entities, types.GuardrailPiiEntityFilter{Type: types.GuardrailPiiEntityType(kind), Match: aws.String(value), Action: types.GuardrailSensitiveInformationPolicyActionNone})
		}
	}
	output := &bedrockruntime.ApplyGuardrailOutput{Action: types.GuardrailActionNone}
	if strings.Contains(text, "4921") {
		entities = append(entities, types.GuardrailPiiEntityFilter{Type: "PIN", Match: aws.String("4921"), Action: types.GuardrailSensitiveInformationPolicyActionAnonymized})
		output.Action = types.GuardrailActionGuardrailIntervened
		output.Outputs = []types.GuardrailOutputContent{{Text: aws.String(strings.ReplaceAll(text, "4921", "{PIN}"))}}
	}
	output.Assessments = []types.GuardrailAssessment{{SensitiveInformationPolicy: &types.GuardrailSensitiveInformationPolicyAssessment{PiiEntities: entities}}}
	return output, nil
}

// TestNothingTheGuardFindsIsLogged: through the real guard, holding an
// answer and then sending it logs what the guard found by kind, and never a
// value it matched or any text of the answer.
func TestNothingTheGuardFindsIsLogged(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	guard, err := answerguard.New(fakeBedrock{}, answerguard.Config{Identifier: "gr_answers", Version: "1", Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	service, _, approved := guardedService(t, guard, &logs)
	if _, err := service.Respond(context.Background(), target, approved.ID, answerGiven); err != nil {
		t.Fatal(err)
	}
	held := asTheOwner(t, service, approved.ID)
	if held.Status != StatusReview || !slices.Equal(held.Guard.Kinds, []string{"EMAIL", "PHONE"}) ||
		held.Held.MaskedResult.Content != "Her number is {PHONE}; the PIN is {PIN}." {
		t.Fatalf("held=%+v", held)
	}
	if _, err := service.ReviewAsOwner(context.Background(), inOwnSession(targetPrimary), approved.ID, agents.InteractionReviewInput{Action: ReviewSend}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"kinds":["EMAIL","PHONE"]`) {
		t.Fatalf("the log does not say what the guard found:\n%s", logs.String())
	}
	for _, private := range []string{"priya@example.com", "415 555 0100", "4921", "Reach Priya", "Her number", "notes-for"} {
		if strings.Contains(logs.String(), private) {
			t.Errorf("the log holds %q:\n%s", private, logs.String())
		}
	}
}

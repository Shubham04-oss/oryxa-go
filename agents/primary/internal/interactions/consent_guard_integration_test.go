package interactions

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
)

// steadyGrounder finds every answer as grounded and relevant as scores say.
func steadyGrounder(grounded, relevant float64) consentguard.Grounder {
	return consentguard.GrounderFunc(func(context.Context, consentguard.GroundingInput) (consentguard.Grounding, error) {
		return consentguard.Grounding{Grounded: consentguard.Score{Value: grounded}, Relevant: consentguard.Score{Value: relevant}}, nil
	})
}

// ledgerWithConsentGuard is a ledger over the real store, whose consent guard is
// the real one, over a grounder that scores every answer as given.
func ledgerWithConsentGuard(t *testing.T, grounded float64) *slackLedger {
	t.Helper()
	return newSlackLedger(t, func(config *Config) {
		config.ConsentGuard = consentguard.New(steadyGrounder(grounded, 0.9), consentguard.Config{})
	})
}

// respond is an owner's agent answering, having read what results say.
func (ledger *slackLedger) respond(request agents.Interaction, results []agents.ToolResult, input agents.InteractionResponseInput) (agents.Interaction, error) {
	ledger.t.Helper()
	return ledger.service.Respond(agents.WithRunResults(context.Background(), results), ledger.agentOf(request.Target.UserID), request.ID, input)
}

// The source the runs of these tests read, and answers to it.
var (
	vaultSource = []agents.ToolResult{{Tool: "knowledge_search", Parts: []string{"passages.text: The keys live in the vault, and the vault path is on the wiki."}}}
	vaultAnswer = agents.InteractionResponseInput{Status: StatusCompleted, Summary: "The keys live in the vault.", Content: "Ask for the dev vault path."}
)

// TestAnAnswerTheConsentGuardHoldsWinsNothingUntilItsOwnerSendsIt is the loop's
// hold, end to end on the real ledger: an ungrounded answer is held in review, a
// held answer wins nothing so another owner may still answer the ask, and when
// the owner sends what was held, with its credential masked, it wins as a
// delivered answer does.
func TestAnAnswerTheConsentGuardHoldsWinsNothingUntilItsOwnerSendsIt(t *testing.T) {
	ledger := ledgerWithConsentGuard(t, 0.2)
	result := ledger.ask("1790000000.000100", raya, maya)
	forRaya := ledger.decide(raya, requestTo(t, result, raya), ChoiceSummary).Request
	ledger.decide(maya, requestTo(t, result, maya), ChoiceAccess)

	token := "xo" + "xb-" + "123456789012-" + "1234567890123-" + "abcdefghijklmnopqrstuvwx"
	answered, err := ledger.respond(forRaya, vaultSource, agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "The keys live in the vault.", Content: "Post with " + token + ".",
	})
	if err != nil || answered.Status != StatusReview {
		t.Fatalf("Raya's answer = %+v, %v; want it held in review", answered, err)
	}
	held := ledger.read(forRaya.ID)
	if held.Held == nil || strings.Contains(held.Held.Result.Content, token) || !strings.Contains(held.Held.Result.Content, "{SLACK_TOKEN}") ||
		held.Guard == nil || held.Guard.State != answerguard.StateHeld || !slices.Equal(held.Guard.Kinds, []string{consentguard.KindSlackToken, consentguard.KindUngrounded}) {
		t.Fatalf("what is kept = %+v, guard %+v; want the masked answer, held as ungrounded", held.Held, held.Guard)
	}
	// The asker is told its status and nothing more.
	if asked, err := ledger.service.Get(context.Background(), ledger.agentOf(asker), forRaya.ID); err != nil || asked.Status != StatusReview || asked.Result != nil || asked.Held != nil {
		t.Fatalf("the asker reads %+v, %v", asked, err)
	}
	if ask, err := ledger.service.Ask(context.Background(), result.Ask.ID); err != nil || ask.WinnerRequestID != "" || !ask.Open() {
		t.Fatalf("the ask = %+v, %v; a held answer wins nothing", ask, err)
	}

	sent, err := ledger.service.ReviewAsOwner(context.Background(), ledger.owner(raya), "member-token", forRaya.ID, agents.InteractionReviewInput{Action: ReviewSend})
	if err != nil || sent.Status != StatusCompleted || sent.Result == nil || strings.Contains(sent.Result.Content, token) || !strings.Contains(sent.Result.Content, "{SLACK_TOKEN}") {
		t.Fatalf("the owner's review = %+v, %v; want it sent, with the token masked", sent, err)
	}
	if ask, err := ledger.service.Ask(context.Background(), result.Ask.ID); err != nil || ask.WinnerRequestID != forRaya.ID || ask.Open() {
		t.Fatalf("the ask = %+v, %v; want Raya's request, sent, the winner", ask, err)
	}
}

// The level is the one the owner chose, read back from the ledger: the same
// words copied out of what the run read are held under a summary and delivered
// under access.
func TestTheSameExcerptIsHeldForASummaryAndDeliveredForAccess(t *testing.T) {
	ledger := ledgerWithConsentGuard(t, 0.95)
	result := ledger.ask("1790000000.000100", raya, maya)
	forRaya := ledger.decide(raya, requestTo(t, result, raya), ChoiceSummary).Request
	forMaya := ledger.decide(maya, requestTo(t, result, maya), ChoiceAccess).Request

	passage := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa quebec romeo sierra tango uniform"
	source := []agents.ToolResult{{Tool: "knowledge_search", Parts: []string{"passages.text: " + passage}}}
	excerpt := agents.InteractionResponseInput{Status: StatusCompleted, Summary: "It says:", Content: passage}

	summary, err := ledger.respond(forRaya, source, excerpt)
	if err != nil || summary.Status != StatusReview {
		t.Fatalf("the summary owner's answer = %+v, %v; want it held for copying the source", summary, err)
	}
	if held := ledger.read(forRaya.ID); held.Guard == nil || !slices.Equal(held.Guard.Kinds, []string{consentguard.KindCopied}) {
		t.Fatalf("guard = %+v, want it held for a copy", held.Guard)
	}
	access, err := ledger.respond(forMaya, source, excerpt)
	if err != nil || access.Status != StatusCompleted || access.Result == nil || access.Result.Content != passage {
		t.Fatalf("the access owner's answer = %+v, %v; want the excerpt delivered", access, err)
	}
}

// With no tool called there is nothing to stand on, and the answer waits, as the
// owner decided; and a run whose record was lost is the same.
func TestAnAnswerFromARunThatCalledNoToolWaitsForItsOwner(t *testing.T) {
	ledger := ledgerWithConsentGuard(t, 0.95)
	request := ledger.decide(raya, requestTo(t, ledger.ask("1790000000.000100", raya), raya), ChoiceAccess).Request
	answered, err := ledger.respond(request, nil, vaultAnswer)
	if err != nil || answered.Status != StatusReview {
		t.Fatalf("answer = %+v, %v; want it held", answered, err)
	}
	if held := ledger.read(request.ID); held.Guard == nil || !slices.Equal(held.Guard.Kinds, []string{consentguard.KindNoSources}) {
		t.Fatalf("guard = %+v, want it held for having nothing to stand on", held.Guard)
	}
}

// An answer that stands on its sources is delivered as it was given, and wins the
// ask, with a guard record that says it was clear.
func TestAnAnswerThatStandsOnItsSourcesWinsTheAsk(t *testing.T) {
	ledger := ledgerWithConsentGuard(t, 0.95)
	result := ledger.ask("1790000000.000100", raya)
	request := ledger.decide(raya, requestTo(t, result, raya), ChoiceSummary).Request
	answered, err := ledger.respond(request, vaultSource, vaultAnswer)
	if err != nil || answered.Status != StatusCompleted || answered.Result == nil || answered.Result.Content != vaultAnswer.Content {
		t.Fatalf("answer = %+v, %v; want it delivered as given", answered, err)
	}
	if ask, err := ledger.service.Ask(context.Background(), result.Ask.ID); err != nil || ask.WinnerRequestID != request.ID {
		t.Fatalf("the ask = %+v, %v; want it won", ask, err)
	}
}

package slackrouter

import (
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
)

// stripCredentials is what the service hands the router as its Redact.
func stripCredentials(text string) string {
	stripped, _ := consentguard.Strip([]string{text})
	return stripped[0]
}

// A key an asker pastes into their question stops at Slack: the screen, whose
// model is another company's, and the owner's agent, whose task the ledger keeps,
// are given the words with it masked, and the ask is filed with them.
func TestACredentialInAnAsksWordsIsRedactedBeforeTheScreenAndTheAsk(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.router.deps.Redact = stripCredentials
	const secret = "xoxb-123456789012-abcdefghijklmnopqrstuvwx"
	h.mustRun(h.mention(toBot("ask <@URAYA> why " + secret + " stopped working")))

	screened := h.screen.calls()
	if len(screened) != 1 || strings.Contains(screened[0].Question, secret) || !strings.Contains(screened[0].Question, "{SLACK_TOKEN}") {
		t.Fatalf("the screen was given %+v, want the token masked", screened)
	}
	ask := h.ask()
	if strings.Contains(ask.Question, secret) || ask.Question != screened[0].Question {
		t.Fatalf("the ask was filed with %q, want the words the screen read, with the token masked", ask.Question)
	}
}

// With nothing to redact with, the words go as they are, which is what a router
// built before the hook did.
func TestWithNoRedactionTheWordsGoAsTheyAre(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.mustRun(h.mention(toBot("ask <@URAYA> for the Q3 numbers")))
	if ask := h.ask(); ask.Question != "ask @Raya for the Q3 numbers" {
		t.Fatalf("question = %q", ask.Question)
	}
}

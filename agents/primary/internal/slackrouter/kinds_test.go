package slackrouter

import (
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

// TestTheKindsOfAskAreTheConsentCoresOwn: the router does not import the
// consent core, so the kinds it files an ask under are its own copies of the
// core's, and this is the one place the two are put side by side. A kind the
// core does not know is an ask it refuses for good ("an ask is a question or a
// secret"), which would drop every ask of that kind, so a change on either
// side has to break here first.
func TestTheKindsOfAskAreTheConsentCoresOwn(t *testing.T) {
	if AskQuestion != interactions.AskQuestion || AskSecret != interactions.AskSecret {
		t.Fatalf("the router files %q and %q, the consent core knows %q and %q",
			AskQuestion, AskSecret, interactions.AskQuestion, interactions.AskSecret)
	}
}

package appsclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

func TestRefusalCarriesItsVerifiedMessageBindingWithoutCallerEmoji(t *testing.T) {
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		ok(w, map[string]any{"channel_id": "C1", "ts": "1780000000.000100", "ephemeral": true})
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	if err := client.Post(context.Background(), slackrouter.Post{
		OrganizationID: "org_1", IdempotencyKey: "slackrouter:asi_1:refusal", RefusalInboundID: "asi_1",
		Target:   slackrouter.Target{ChannelID: "C1", ThreadTS: "1780000000.000100", UserID: "U1"},
		Template: slackrouter.TemplateRefusal, Params: slackrouter.RefusalParams{Reason: consentpolicy.NoOwner, Hint: "self"},
	}); err != nil {
		t.Fatal(err)
	}
	body := fake.requests()[0].json(t)
	if body["refusal_inbound_id"] != "asi_1" || body["emoji"] != nil || body["reaction"] != nil {
		t.Fatalf("refusal binding=%v", body)
	}
	params, _ := body["params"].(map[string]any)
	if params["reason"] != "no_owner" || params["hint"] != "self" {
		t.Fatalf("refusal params=%v", params)
	}
}

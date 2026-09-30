package appsclient

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

// TestInboundReadsAReplyAsTheRouterDescribesIt: a reply sent from the modal a
// Reply yourself button opened is the sender, the message the button was on,
// the button's ticket and the words, and nothing of a click or a mention.
func TestInboundReadsAReplyAsTheRouterDescribesIt(t *testing.T) {
	fake := newFakeApps(t, func(_ int, r recorded, w http.ResponseWriter) {
		if !strings.HasSuffix(r.Path, "/asi_reply") {
			refuse(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		ok(w, map[string]any{
			"id": "asi_reply", "organization_id": "org_1", "team_id": "T1", "kind": "reply",
			"received_at": "2026-10-01T10:02:00Z", "channel_id": "D1", "user_id": "U2", "message_ts": "1700000001.000200",
			"thread_ts": "1700000001.000200", "action_id": "reply", "action_value": "t1.reply.sig", "text": "We ship on the 14th.",
		})
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	reply, err := client.Inbound(context.Background(), "asi_reply")
	if err != nil {
		t.Fatal(err)
	}
	want := &slackrouter.Reply{UserID: "U2", ChannelID: "D1", MessageTS: "1700000001.000200", Value: "t1.reply.sig", Text: "We ship on the 14th."}
	if reply.Kind != slackrouter.KindReply || reply.Click != nil || reply.Mention != nil || reply.Reply == nil || *reply.Reply != *want {
		t.Fatalf("reply = %+v, %+v", reply, reply.Reply)
	}
}

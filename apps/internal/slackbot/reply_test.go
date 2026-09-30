package slackbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

const (
	replyTicket = "t1.eyJrIjoicmVwbHkifQ.cmVwbHktc2lnbmF0dXJl"
	// noticeWithReply is the owner's notice as agents' surfaces post it: the
	// three choices and Reply yourself.
	noticeWithReply = `{"asker_user_id":"U0ASKER","question":"When is the launch?\nAnd who signs it off?","tickets":{"summary":"t1.a.b","access":"t1.c.d","deny":"t1.e.f","reply":"` + replyTicket + `"}}`
	rayasWords      = "We ship on the 14th, and I sign it off."
)

// postedNotice posts Raya the owner's notice, with Reply yourself, as agents'
// surfaces would, and answers where it went.
func (h *harness) postedNotice() PostResult {
	h.t.Helper()
	h.installFor(testOrg)
	h.seenThread(slackfake.Mention{Text: askText})
	notice, err := h.post("key-notice-reply", TemplateOwnerNotice, noticeWithReply, Target{UserID: "U0RAYA"})
	if err != nil {
		h.t.Fatal(err)
	}
	return notice
}

// clickReply is Raya clicking Reply yourself on a message.
func (h *harness) clickReply(channel, ts, ticket string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.serve(h.fake.Click("", slackfake.Click{User: "U0RAYA", Channel: channel, MessageTS: ts, ActionID: ActionReply, Value: ticket}))
}

// submit is Send on the last modal opened, with text in its box.
func (h *harness) submit(text string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.serve(h.fake.Submit("", slackfake.Submission{Values: map[string]map[string]string{replyBlockID: {replyInputID: text}}}))
}

// replyRow is the one reply kept, and fails unless there is exactly one: the
// records of a test all arrive at the moment its clock says.
func (h *harness) replyRow() Inbound {
	h.t.Helper()
	var found []Inbound
	for _, row := range h.store.inboundRows() {
		if row.Kind == InboundReply {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		h.t.Fatalf("replies kept = %+v, want one", found)
	}
	return found[0]
}

// openedModal is the one modal the fake opened, and fails unless there is one.
func (h *harness) openedModal() slackfake.View {
	h.t.Helper()
	views := h.fake.Views()
	if len(views) != 1 {
		h.t.Fatalf("modals opened = %+v, want one", views)
	}
	return views[0]
}

// TestReplyYourselfOpensItsModalAtOnceAndRecordsNothing: the click is answered
// with the modal opened, with the bot's token and the click's trigger: who
// asked what, as the notice said it, a box for the reply, Send and Cancel, and
// the ticket and the notice in its metadata. The click decides nothing, so it is
// kept nowhere and announced to nobody.
func TestReplyYourselfOpensItsModalAtOnceAndRecordsNothing(t *testing.T) {
	h := newHarness(t)
	notice := h.postedNotice()
	rows, events := len(h.store.inboundRows()), len(h.store.published())

	started := time.Now()
	response := h.clickReply(notice.ChannelID, notice.TS, replyTicket)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("the click = %d %q, want an empty 200", response.Code, response.Body)
	}
	if took := time.Since(started); took > time.Second {
		t.Errorf("the click took %s to answer, and a trigger lasts three seconds", took)
	}
	opened := h.openedModal()
	if calls := h.fake.Calls("views.open"); len(calls) != 1 || calls[0].Token != slackfake.DefaultBotToken || calls[0].Args["trigger_id"] != opened.TriggerID {
		t.Fatalf("views.open calls = %+v", calls)
	}
	view := opened.View
	title, _ := view["title"].(map[string]any)
	submit, _ := view["submit"].(map[string]any)
	closer, _ := view["close"].(map[string]any)
	if view["type"] != "modal" || view["callback_id"] != replyCallbackID || title["text"] != "Reply yourself" || submit["text"] != "Send" || closer["text"] != "Cancel" {
		t.Fatalf("the modal = %v", view)
	}
	var metadata replyMetadata
	if err := json.Unmarshal([]byte(fmt.Sprint(view["private_metadata"])), &metadata); err != nil ||
		metadata != (replyMetadata{Ticket: replyTicket, ChannelID: notice.ChannelID, MessageTS: notice.TS}) {
		t.Fatalf("the modal carries %v (%v), want the ticket and the notice", view["private_metadata"], err)
	}
	blocks, _ := view["blocks"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("blocks = %v", blocks)
	}
	if got := markup(blocks[0].(map[string]any)); got != "Reply to <@U0ASKER>, who asked:\n> When is the launch?\n> And who signs it off?" {
		t.Fatalf("the modal says %q", got)
	}
	input := blocks[1].(map[string]any)
	element, _ := input["element"].(map[string]any)
	if input["type"] != "input" || input["block_id"] != replyBlockID || element["type"] != "plain_text_input" || element["action_id"] != replyInputID ||
		element["multiline"] != true || element["max_length"] != float64(replyBoxRunes) || input["optional"] != nil {
		t.Fatalf("the box = %v", input)
	}
	if len(h.store.inboundRows()) != rows || len(h.store.published()) != events {
		t.Fatal("a click on Reply yourself was kept or announced")
	}
	h.assertLogsLack(replyTicket, "When is the launch", slackfake.DefaultBotToken)
}

// TestAModalThatCannotOpenIsAClickThatFailed: a trigger that ran out, or Slack
// not answering, fails the click, which Slack shows beside the button, and a
// click whose ticket or trigger is of no shape the surface takes is dropped; none
// keeps anything.
func TestAModalThatCannotOpenIsAClickThatFailed(t *testing.T) {
	h := newHarness(t)
	notice := h.postedNotice()
	rows := len(h.store.inboundRows())

	stale := h.fake.Click("", slackfake.Click{User: "U0RAYA", Channel: notice.ChannelID, MessageTS: notice.TS, ActionID: ActionReply, Value: replyTicket})
	h.clock.Advance(4 * time.Second)
	if response := h.serve(stale); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("a click whose trigger ran out = %d %s, want 503", response.Code, response.Body)
	}
	h.fake.Down(true)
	if response := h.clickReply(notice.ChannelID, notice.TS, replyTicket); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("a click while Slack is down = %d %s, want 503", response.Code, response.Body)
	}
	h.fake.Down(false)
	for name, ticket := range map[string]string{"a ticket with a space": "t1 reply", "no ticket": ""} {
		if response := h.clickReply(notice.ChannelID, notice.TS, ticket); response.Code != http.StatusOK {
			t.Errorf("%s = %d, want it dropped", name, response.Code)
		}
	}
	if len(h.fake.Views()) != 0 || len(h.store.inboundRows()) != rows {
		t.Fatalf("modals = %+v, rows = %d, want neither", h.fake.Views(), len(h.store.inboundRows()))
	}
	// A message the surface cannot read the question from still opens a modal,
	// which says less.
	if response := h.clickReply(notice.ChannelID, "1780000999.000999", replyTicket); response.Code != http.StatusOK {
		t.Fatalf("a click on a message the fake does not know = %d %s", response.Code, response.Body)
	}
	blocks, _ := h.openedModal().View["blocks"].([]any)
	if got := markup(blocks[0].(map[string]any)); got != "Reply to the question in the message you clicked, in your own words." {
		t.Fatalf("the modal says %q", got)
	}
}

// TestSendingAReplyKeepsItAndClosesTheModal: the reply is kept as its own kind
// of record, with the words, the ticket and the notice, announced by reference
// as a click is, and read back whole through the internal API; the modal is
// closed with an empty 200. The same submission again is one reply.
func TestSendingAReplyKeepsItAndClosesTheModal(t *testing.T) {
	h := newHarness(t)
	notice := h.postedNotice()
	h.clickReply(notice.ChannelID, notice.TS, replyTicket)
	events := len(h.store.published())

	request := h.fake.Submit("", slackfake.Submission{Values: map[string]map[string]string{replyBlockID: {replyInputID: "  " + rayasWords + "  "}}})
	body := requestBody(t, request)
	response := h.serve(request)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("Send = %d %q, want an empty 200, which closes the modal", response.Code, response.Body)
	}
	row := h.replyRow()
	if row.Kind != InboundReply || row.OrganizationID != testOrg || row.TeamID != slackfake.DefaultTeamID || row.UserID != "U0RAYA" ||
		row.ChannelID != notice.ChannelID || row.MessageTS != notice.TS || row.ThreadTS != notice.TS ||
		row.ActionID != ActionReply || row.ActionValue != replyTicket || row.Text != "  "+rayasWords+"  " {
		t.Fatalf("record = %+v", row)
	}
	published := h.store.published()
	if len(published) != events+1 || published[len(published)-1].DetailType != appsv1.SlackActionReceivedDetailType {
		t.Fatalf("events = %+v, want one more, an action's", published)
	}
	data := dataOf(t, published[len(published)-1].Data)
	if want := []string{"inbound_id", "organization_id", "received_at", "team_id"}; !reflect.DeepEqual(keysOf(data), want) || data["inbound_id"] != row.ID {
		t.Fatalf("event = %v", data)
	}
	if strings.Contains(string(published[len(published)-1].Data), "14th") || strings.Contains(string(published[len(published)-1].Data), replyTicket) {
		t.Fatal("the event carries the reply or its ticket")
	}
	view, err := h.service.Inbound(context.Background(), row.ID)
	if err != nil || view.Kind != InboundReply || view.Text != row.Text || view.ActionValue != replyTicket || view.ChannelID != notice.ChannelID || view.UserID != "U0RAYA" {
		t.Fatalf("the internal view = %+v, %v", view, err)
	}
	h.assertLogsLack(rayasWords, "14th", replyTicket)

	// Slack sending the same submission again is one reply.
	again, err := http.NewRequest(http.MethodPost, request.URL.String(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	again.Header = request.Header.Clone()
	if response := h.serve(again); response.Code != http.StatusOK {
		t.Fatalf("the same submission again = %d", response.Code)
	}
	replies := 0
	for _, kept := range h.store.inboundRows() {
		if kept.Kind == InboundReply {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("%d replies kept, want one", replies)
	}
}

// requestBody reads a request's body without spending it.
func requestBody(t *testing.T, request *http.Request) string {
	t.Helper()
	copied, err := request.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	body, err := io.ReadAll(copied)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestAReplyTheLedgerWouldRefuseIsAnsweredInTheModal: a reply that is empty once
// trimmed, or longer than the ledger takes, keeps the modal open with the error
// under the box, and nothing is kept; the most the ledger takes is a reply.
func TestAReplyTheLedgerWouldRefuseIsAnsweredInTheModal(t *testing.T) {
	for name, test := range map[string]struct {
		text, message string
	}{
		"nothing":         {"", "Write your reply first."},
		"spaces":          {"   \n\t ", "Write your reply first."},
		"4001 characters": {strings.Repeat("a", MaxReplyRunes+1), "A reply holds at most 4000 characters."},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			notice := h.postedNotice()
			h.clickReply(notice.ChannelID, notice.TS, replyTicket)
			rows := len(h.store.inboundRows())
			response := h.submit(test.text)
			if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("Send = %d %s %q", response.Code, response.Header().Get("Content-Type"), response.Body)
			}
			var answer struct {
				Action string            `json:"response_action"`
				Errors map[string]string `json:"errors"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil || answer.Action != "errors" || len(answer.Errors) != 1 || answer.Errors[replyBlockID] != test.message {
				t.Fatalf("Send answered %s (%v), want the error under the box", response.Body, err)
			}
			if len(h.store.inboundRows()) != rows {
				t.Fatal("a reply the ledger would refuse was kept")
			}
		})
	}
	h := newHarness(t)
	notice := h.postedNotice()
	h.clickReply(notice.ChannelID, notice.TS, replyTicket)
	if response := h.submit(" " + strings.Repeat("क", MaxReplyRunes) + " "); response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("a reply of the most the ledger takes = %d %q", response.Code, response.Body)
	}
	if row := h.replyRow(); row.Text != " "+strings.Repeat("क", MaxReplyRunes)+" " {
		t.Fatalf("the longest reply was kept as %q", row.Text)
	}
}

// TestASubmissionTheSurfaceDidNotOpenIsDropped: a modal that is not the
// reply's, metadata that names no ticket or no message of Slack's shape, and a
// workspace with no install each close the modal and keep nothing.
func TestASubmissionTheSurfaceDidNotOpenIsDropped(t *testing.T) {
	h := newHarness(t)
	notice := h.postedNotice()
	h.clickReply(notice.ChannelID, notice.TS, replyTicket)
	rows, events := len(h.store.inboundRows()), len(h.store.published())
	forged := func(metadata string) *http.Request {
		return h.fake.Submit("", slackfake.Submission{
			PrivateMetadata: &metadata, Values: map[string]map[string]string{replyBlockID: {replyInputID: rayasWords}},
		})
	}
	other := func(callback, team string) *http.Request {
		payload, _ := json.Marshal(map[string]any{
			"type": "view_submission", "team": map[string]any{"id": team}, "user": map[string]any{"id": "U0RAYA", "team_id": team},
			"view": map[string]any{
				"callback_id": callback, "private_metadata": `{"t":"` + replyTicket + `","c":"` + notice.ChannelID + `","m":"` + notice.TS + `"}`,
				"state": map[string]any{"values": map[string]any{replyBlockID: map[string]any{replyInputID: map[string]any{"type": "plain_text_input", "value": rayasWords}}}},
			},
		})
		return h.fake.Signed(testBaseURL+InteractivityPath, "application/x-www-form-urlencoded", []byte(url.Values{"payload": {string(payload)}}.Encode()))
	}
	for name, request := range map[string]*http.Request{
		"metadata that is not JSON":        forged("not json"),
		"metadata with no ticket":          forged(`{"c":"` + notice.ChannelID + `","m":"` + notice.TS + `"}`),
		"metadata with a ticket of spaces": forged(`{"t":"t1 x","c":"` + notice.ChannelID + `","m":"` + notice.TS + `"}`),
		"metadata with no channel":         forged(`{"t":"` + replyTicket + `","m":"` + notice.TS + `"}`),
		"metadata with a bad timestamp":    forged(`{"t":"` + replyTicket + `","c":"` + notice.ChannelID + `","m":"soon"}`),
		"another modal":                    other("someone_elses_modal", slackfake.DefaultTeamID),
		"a workspace with no install":      other(replyCallbackID, "T0NOBODY1"),
	} {
		response := h.serve(request)
		if response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Errorf("%s = %d %q, want the modal closed", name, response.Code, response.Body)
		}
	}
	if len(h.store.inboundRows()) != rows || len(h.store.published()) != events {
		t.Fatal("a submission the surface did not open was kept")
	}
}

// TestTheReplysModalKeepsSlacksLimits: whatever the question, the modal the
// surface builds is one Slack takes: a section under 3000 characters, and
// metadata under 3000 with the longest ticket a button carries.
func TestTheReplysModalKeepsSlacksLimits(t *testing.T) {
	long := "> " + strings.Repeat("Why is this so long? ", 300)
	view, err := renderer{}.replyModal("<@U0ASKER>", long, replyMetadata{Ticket: strings.Repeat("t", maxActionValueBytes), ChannelID: "D0RAYA", MessageTS: "1780000000.000100"})
	if err != nil {
		t.Fatal(err)
	}
	blocks := view["blocks"].([]any)
	if text := markup(blocks[0].(map[string]any)); len([]rune(text)) > maxSectionRunes || !strings.HasSuffix(text, "…") {
		t.Fatalf("the modal's question is %d characters", len([]rune(text)))
	}
	if metadata := view["private_metadata"].(string); len([]rune(metadata)) > 3000 {
		t.Fatalf("the metadata is %d characters", len([]rune(metadata)))
	}
	for _, name := range []string{"title", "submit", "close"} {
		if text := view[name].(map[string]any)["text"].(string); len([]rune(text)) > 24 {
			t.Errorf("the %s is %q, longer than Slack takes", name, text)
		}
	}
}

// TestTheAskIsReadOnlyFromTheBotsOwnMessage: who asked what comes from the
// first block of the message the button was on, as the surface rendered it, and
// from nothing else.
func TestTheAskIsReadOnlyFromTheBotsOwnMessage(t *testing.T) {
	message := func(user, text string) clickedMessage {
		var clicked clickedMessage
		raw, _ := json.Marshal(map[string]any{"user": user, "blocks": []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}}}})
		_ = json.Unmarshal(raw, &clicked)
		return clicked
	}
	for name, test := range map[string]struct {
		message      clickedMessage
		asker, asked string
	}{
		"a notice":                   {message("UBOT", "<@U0ASKER> is asking you through Oryxa:\n> When?"), "<@U0ASKER>", "> When?"},
		"a review":                   {message("UBOT", "*Alex Kim* asked you through Oryxa:\n> When?"), "*Alex Kim*", "> When?"},
		"a message with no user":     {message("", "<@U0ASKER> is asking you through Oryxa:\n> When?"), "<@U0ASKER>", "> When?"},
		"someone else's message":     {message("U0EVIL", "<@U0ASKER> is asking you through Oryxa:\n> When?"), "", ""},
		"a message in other words":   {message("UBOT", "Hello there"), "", ""},
		"an asker that is no one's":  {message("UBOT", "<!channel> is asking you through Oryxa:\n> When?"), "", ""},
		"a message with no blocks":   {clickedMessage{User: "UBOT"}, "", ""},
		"a question with no newline": {message("UBOT", "<@U0ASKER> is asking you through Oryxa:"), "", ""},
	} {
		asker, asked := askOf(test.message, "UBOT")
		if asker != test.asker || asked != test.asked {
			t.Errorf("%s: askOf = %q, %q; want %q, %q", name, asker, asked, test.asker, test.asked)
		}
	}
}

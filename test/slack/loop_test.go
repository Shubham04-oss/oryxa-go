// Package slack is the Slack consent loop, end to end: apps and agents in one
// oryxa process, the binary as it ships, on a PostgreSQL database of its own,
// with everything outside the two services faked, and a fake Slack that signs
// and sends what Slack sends.
//
// A mention travels the way it does in production. apps verifies and records it
// and puts its id on the bus; agents' consumer reads it from its queue, and the
// router turns it into an ask, after a screen by Jev; the ledger's events say
// so, and the surfaces worker has apps post the owner's notice; a signed click,
// or a choice on the web card, decides it, the first winning; the owner's agent,
// run on a fake model, searches a fake knowledge base as the owner and answers;
// the answer guard and the consent guard, on a fake Bedrock, read what it says;
// and apps posts what survives in the thread. The stand-ins are AWS (KMS, the
// bus, SQS, Bedrock), Google's model, Jev, Slack, auth, Teams and rag, each
// answering only what the loop asks of it. What is under test is the wiring.
//
// It needs a PostgreSQL server, and creates a database on it for its run and
// drops it after, so it is skipped without one:
//
//	SLACK_TEST_DATABASE_URL='postgres://oryxa@127.0.0.1:5432/postgres?sslmode=disable' go test -count=1 -timeout 15m ./test/slack
//
// SLACK_E2E_DEBUG=1 has the fake model say what it was asked.
package slack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

const channelGeneral = "C0GENERAL"

// asked is an ask as its owner sees it in Slack, and the request it made.
type asked struct {
	thread    string
	notice    slackfake.Message
	tickets   map[string]string
	requestID string
}

// ---- Reading what Slack was told ----

// blocksText is the words in a message's blocks, in order, whatever block they
// are in.
func blocksText(blocks []any) string {
	var words []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if text, ok := typed["text"].(string); ok {
				words = append(words, text)
			}
			for _, key := range []string{"text", "elements", "fields"} {
				if child, ok := typed[key]; ok {
					if _, isString := child.(string); !isString {
						walk(child)
					}
				}
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(blocks)
	return strings.Join(words, "\n")
}

// wording is everything a message says: its text and its blocks.
func wording(message slackfake.Message) string {
	return message.Text + "\n" + blocksText(message.Blocks)
}

type button struct{ ActionID, Value, Label string }

// buttons are the buttons in a message's actions blocks, in order.
func buttons(message slackfake.Message) []button {
	var found []button
	for _, block := range message.Blocks {
		object, _ := block.(map[string]any)
		if object["type"] != "actions" {
			continue
		}
		elements, _ := object["elements"].([]any)
		for _, element := range elements {
			item, _ := element.(map[string]any)
			label, _ := item["text"].(map[string]any)
			found = append(found, button{
				ActionID: fmt.Sprint(item["action_id"]), Value: fmt.Sprint(item["value"]), Label: fmt.Sprint(label["text"]),
			})
		}
	}
	return found
}

// tickets are the values of a notice's buttons, by the button's action.
func ticketsOf(message slackfake.Message) map[string]string {
	tickets := map[string]string{}
	for _, one := range buttons(message) {
		tickets[one.ActionID] = one.Value
	}
	return tickets
}

// requestOf is the request a ticket is for: a ticket is t1.<claims>.<signature>,
// and the claims say which request, whose choice and for which notice.
func requestOf(t testing.TB, ticket string) string {
	t.Helper()
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 || parts[0] != "t1" {
		t.Fatalf("%q is not a ticket", ticket)
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("a ticket's claims: %v", err)
	}
	var decoded struct {
		Request string `json:"r"`
	}
	if err := json.Unmarshal(claims, &decoded); err != nil || decoded.Request == "" {
		t.Fatalf("a ticket's claims %s name no request: %v", claims, err)
	}
	return decoded.Request
}

// claimsOf is what a ticket says, as the JSON it carries: which request, whose,
// for which message, which choice, of which kind and until when.
func claimsOf(t testing.TB, ticket string) string {
	t.Helper()
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 {
		t.Fatalf("%q is not a ticket", ticket)
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("a ticket's claims: %v", err)
	}
	return string(claims)
}

// fromIndex is the messages Slack was told from the nth on.
func (s *stack) fromIndex(index int) []slackfake.Message {
	all := s.slack.Messages()
	if index >= len(all) {
		return nil
	}
	return all[index:]
}

// count is how many messages Slack has been told so far.
func (s *stack) count() int { return len(s.slack.Messages()) }

// inThread is what was said in a channel's thread, ephemeral and not.
func (s *stack) inThread(thread string) []slackfake.Message {
	var found []slackfake.Message
	for _, message := range s.slack.Messages() {
		if message.Channel == channelGeneral && message.ThreadTS == thread {
			found = append(found, message)
		}
	}
	return found
}

// theAnswer is the message in the thread that says what the owner's agent found:
// the one that is not ephemeral and carries a context line saying whose it is.
func (s *stack) theAnswer(thread string) (slackfake.Message, bool) {
	for _, message := range s.inThread(thread) {
		if !message.Ephemeral && strings.Contains(wording(message), "'s agent") {
			return message, true
		}
	}
	return slackfake.Message{}, false
}

// ---- Doing what people do ----

var tsSequence int

// nextTS makes the ts of a message, apart from the ones the fake Slack posts.
func nextTS() string {
	tsSequence++
	return fmt.Sprintf("1800000000.%06d", tsSequence)
}

// mention has who write text in the channel, mentioning the bot, in a thread
// when one is named, and returns the message's own ts. Slack's delivery is
// answered within three seconds, whatever becomes of it.
func (s *stack) mention(who person, text, thread string) string {
	s.t.Helper()
	ts := nextTS()
	request := s.slack.Mention(s.baseURL, slackfake.Mention{
		Channel: channelGeneral, User: who.SlackID, TS: ts, ThreadTS: thread, Text: "<@" + slackfake.DefaultBotUserID + "> " + text,
	})
	started := time.Now()
	response, err := slackfake.Send(nil, request)
	if err != nil {
		s.t.Fatalf("deliver a mention: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		s.t.Fatalf("a mention was answered %d, not 200", response.StatusCode)
	}
	if took := time.Since(started); took > 3*time.Second {
		s.t.Errorf("a mention took %s to answer, and Slack waits three seconds", took)
	}
	return ts
}

// sendClick has an owner click a button of a notice, and reports how Slack's
// delivery was answered, which is at once. It never stops the test, so that a
// goroutine may make it.
func (s *stack) sendClick(owner person, notice slackfake.Message, action, ticket string) error {
	request := s.slack.Click(s.baseURL, slackfake.Click{
		User: owner.SlackID, Channel: notice.Channel, MessageTS: notice.TS, ActionID: action, Value: ticket,
	})
	started := time.Now()
	response, err := slackfake.Send(nil, request)
	if err != nil {
		return fmt.Errorf("deliver a click: %w", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("a click was answered %d, not 200", response.StatusCode)
	}
	if took := time.Since(started); took > 3*time.Second {
		return fmt.Errorf("a click took %s to answer, and Slack waits three seconds", took)
	}
	return nil
}

// clickOn is sendClick for the test's own goroutine.
func (s *stack) clickOn(owner person, notice slackfake.Message, action, ticket string) {
	s.t.Helper()
	if err := s.sendClick(owner, notice, action, ticket); err != nil {
		s.t.Fatal(err)
	}
}

// ask has who ask owner, and waits for what an ask does at once: the asker is
// told who was asked, and the owner has a notice in their direct message.
func (s *stack) ask(who, owner person, question, thread string) asked {
	s.t.Helper()
	before := s.count()
	ts := s.mention(who, "ask <@"+owner.SlackID+"> "+question, thread)
	root := thread
	if root == "" {
		root = ts
	}
	var found asked
	found.thread = root
	s.eventually("the owner's notice", 40*time.Second, func() bool {
		for _, message := range s.fromIndex(before) {
			if message.Channel == slackfake.DMChannel(owner.SlackID) && !message.Ephemeral && len(buttons(message)) > 0 {
				found.notice = message
				return true
			}
		}
		return false
	})
	found.tickets = ticketsOf(found.notice)
	found.requestID = requestOf(s.t, found.tickets["consent_summary"])
	for _, ticket := range found.tickets {
		s.mustNotBeLogged(ticket)
	}
	s.mustNotBeLogged(question)
	return found
}

// mustNotBeLogged notes something the process must never write to its log: a
// ticket, a question, a credential. The last subtest reads the log for them.
func (s *stack) mustNotBeLogged(values ...string) {
	s.sensitive = append(s.sensitive, values...)
}

// request reads a request as its owner, who is the one it is addressed to.
func (s *stack) request(who person, id string) map[string]any {
	s.t.Helper()
	status, request := s.api(who, http.MethodGet, "/v1/organizations/"+organizationID+"/agent-requests/"+id, nil)
	if status != http.StatusOK {
		s.t.Fatalf("read request %s as %s: %d %v", id, who.Name, status, request)
	}
	return request
}

// at follows keys down through nested objects, to nil where the way ends.
func at(value any, keys ...string) any {
	for _, key := range keys {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}

// waitForStatus waits until a request, read as its owner, is in the status.
func (s *stack) waitForStatus(who person, id, status string) map[string]any {
	s.t.Helper()
	var last map[string]any
	s.eventually("request "+id+" to be "+status, 60*time.Second, func() bool {
		_, last = s.api(who, http.MethodGet, "/v1/organizations/"+organizationID+"/agent-requests/"+id, nil)
		return at(last, "status") == status
	})
	return last
}

// noticeSettled waits for the owner's notice to be replaced by what became of
// it, with no buttons, and says what it says.
func (s *stack) noticeSettled(notice slackfake.Message) slackfake.Message {
	s.t.Helper()
	var settled slackfake.Message
	s.eventually("the owner's notice to be settled", 40*time.Second, func() bool {
		message, ok := s.slack.Message(notice.Channel, notice.TS)
		if ok && message.Updates > 0 && len(buttons(message)) == 0 {
			settled = message
			return true
		}
		return false
	})
	return settled
}

// decisions counts the decided events published for a request.
func (s *stack) decisions(requestID string) int {
	count := 0
	for _, event := range s.aws.published("agents.interaction.decided.v1") {
		var detail struct {
			Data struct {
				RequestID string `json:"request_id"`
			} `json:"data"`
		}
		if json.Unmarshal(event.Detail, &detail) == nil && detail.Data.RequestID == requestID {
			count++
		}
	}
	return count
}

// nothingSaid fails if what was said from the nth message on holds text.
func (s *stack) nothingSaid(from int, text, why string) {
	s.t.Helper()
	for _, message := range s.fromIndex(from) {
		if strings.Contains(wording(message), text) {
			s.t.Errorf("%s: Slack was told %q in %+v", why, text, message)
		}
	}
}

// nothingSaidBut is nothingSaid in every conversation but one: the owner's
// direct message, where their review of an answer held for them shows it to
// them, and nobody else.
func (s *stack) nothingSaidBut(from int, channel, text, why string) {
	s.t.Helper()
	for _, message := range s.fromIndex(from) {
		if message.Channel != channel && strings.Contains(wording(message), text) {
			s.t.Errorf("%s: Slack was told %q outside the owner's review, in %+v", why, text, message)
		}
	}
}

// review waits for the owner's review of an answer held for them, sent to their
// direct message from the nth message on, and returns it as it is then.
func (s *stack) review(owner person, from int) slackfake.Message {
	s.t.Helper()
	var found slackfake.Message
	s.eventually("the owner's review of the held answer", 40*time.Second, func() bool {
		for _, message := range s.fromIndex(from) {
			if message.Channel == slackfake.DMChannel(owner.SlackID) && !message.Ephemeral && strings.Contains(wording(message), "held for your review") {
				found = message
				return true
			}
		}
		return false
	})
	return found
}

// agentAnswers is how many answers the fake model has had an agent give, with
// interactions_respond: a reply its owner writes is not one of them.
func (s *stack) agentAnswers() int {
	answers := 0
	for _, call := range s.vertex.recorded() {
		if call == "call interactions_respond" {
			answers++
		}
	}
	return answers
}

// submit has Send pressed on the modal the fake Slack opened last, and reports
// how Slack's delivery was answered: its status and its body, which is empty for
// a modal that closes.
func (s *stack) submit(submission slackfake.Submission) (int, string) {
	s.t.Helper()
	started := time.Now()
	response, err := slackfake.Send(nil, s.slack.Submit(s.baseURL, submission))
	if err != nil {
		s.t.Fatalf("deliver a modal's submission: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if took := time.Since(started); took > 3*time.Second {
		s.t.Errorf("a submission took %s to answer, and Slack waits three seconds", took)
	}
	return response.StatusCode, string(body)
}

// ownReply waits for the answer an owner wrote themself in a thread: the one
// that is not ephemeral and says whose words they are.
func (s *stack) ownReply(thread, owner string) slackfake.Message {
	s.t.Helper()
	var found slackfake.Message
	s.eventually("the owner's own reply in the thread", 60*time.Second, func() bool {
		for _, message := range s.inThread(thread) {
			if !message.Ephemeral && strings.Contains(wording(message), owner+" replied:") {
				found = message
				return true
			}
		}
		return false
	})
	return found
}

// answeredInOwnWords fails the test unless each person's activity marks the
// request's answer as its owner's own words.
func (s *stack) answeredInOwnWords(requestID string, people ...person) {
	s.t.Helper()
	for _, who := range people {
		_, activity := s.api(who, http.MethodGet, "/v1/organizations/"+organizationID+"/agent-requests/activity?limit=100", nil)
		answered := 0
		for _, entry := range asSlice(activity["activity"]) {
			if at(entry, "kind") == "answered" && at(entry, "request_id") == requestID {
				answered++
				if at(entry, "written_by_owner") != true {
					s.t.Errorf("%s's activity reads the answer as %v, not written by its owner", who.Name, entry)
				}
			}
		}
		if answered != 1 {
			s.t.Errorf("%s's activity has %d answers for %s, want the one", who.Name, answered, requestID)
		}
	}
}

// ---- The loop ----

func TestSlackConsentLoop(t *testing.T) {
	server := os.Getenv("SLACK_TEST_DATABASE_URL")
	if server == "" {
		t.Skip("SLACK_TEST_DATABASE_URL is not set")
	}
	s := bootStack(t, server)
	dm := slackfake.DMChannel(raya.SlackID)
	var summary asked

	t.Run("a mention is a notice with four ticketed buttons to the owner, and an acknowledgement to the asker alone", func(t *testing.T) {
		before := s.count()
		summary = s.ask(ada, raya, "for the Q3 numbers and the launch date", "")
		notice := summary.notice

		got := buttons(notice)
		if len(got) != 4 || got[0].ActionID != "consent_summary" || got[1].ActionID != "consent_access" || got[2].ActionID != "consent_deny" ||
			got[3].ActionID != "reply" || got[3].Label != "Reply yourself" {
			t.Fatalf("the notice's buttons = %+v, want summary, access, deny and Reply yourself", got)
		}
		requests := map[string]bool{}
		for _, one := range got {
			if !strings.HasPrefix(one.Value, "t1.") || strings.Count(one.Value, ".") != 2 {
				t.Errorf("button %s carries %q, not a ticket", one.ActionID, one.Value)
			}
			requests[requestOf(t, one.Value)] = true
		}
		if len(requests) != 1 {
			t.Errorf("the four tickets name %d requests, want the one", len(requests))
		}
		// Reply yourself's ticket is of its own kind: no choice's, and no review's.
		if claims := claimsOf(t, summary.tickets["reply"]); !strings.Contains(claims, `"k":"reply"`) {
			t.Errorf("Reply yourself carries %s, not a reply's ticket", claims)
		}
		words := wording(notice)
		for _, want := range []string{"<@" + ada.SlackID + ">", "Q3 numbers"} {
			if !strings.Contains(words, want) {
				t.Errorf("the notice does not say %q: %s", want, words)
			}
		}
		if notice.UnfurlLinks || notice.UnfurlMedia {
			t.Errorf("the notice unfurls links: %+v", notice)
		}
		if notice.Channel != dm {
			t.Errorf("the notice went to %s, not the owner's direct message %s", notice.Channel, dm)
		}

		// The asker, and only the asker, is told who was asked, in the thread, with
		// a link to the request in the web app.
		var ack *slackfake.Message
		for _, message := range s.fromIndex(before) {
			if message.Channel == channelGeneral {
				if !message.Ephemeral || message.User != ada.SlackID || message.ThreadTS != summary.thread {
					t.Errorf("the channel was told %+v, which is not an ephemeral note to the asker in the thread", message)
					continue
				}
				ack = &message
			}
		}
		if ack == nil {
			t.Fatal("the asker was not told who was asked")
		}
		for _, want := range []string{"Asked <@" + raya.SlackID + ">", webOrigin + "/?org=" + organizationID, "view=requests", "request=" + summary.requestID} {
			if !strings.Contains(wording(*ack), want) {
				t.Errorf("the acknowledgement does not say %q: %s", want, wording(*ack))
			}
		}
		// The ledger holds it: pending, from Slack, addressed to Raya.
		request := s.request(raya, summary.requestID)
		if at(request, "status") != "pending" || at(request, "origin") != "slack" || at(request, "requester", "user_id") != ada.UserID ||
			at(request, "target", "user_id") != raya.UserID || at(request, "ask_id") == "" || at(request, "approval") != "owner" {
			t.Errorf("the request = %v", request)
		}
		if got := s.count() - before; got != 2 {
			t.Errorf("%d messages were posted, want the acknowledgement and the notice", got)
		}
	})

	t.Run("the owner's click decides, the notice settles, and the answer is posted in the thread by the owner's agent, as a summary", func(t *testing.T) {
		if summary.requestID == "" {
			t.Skip("no ask to click on")
		}
		before := s.count()
		s.clickOn(raya, summary.notice, "consent_summary", summary.tickets["consent_summary"])
		settled := s.noticeSettled(summary.notice)
		if words := wording(settled); !strings.Contains(words, "Approved by <@"+raya.SlackID+">") || !strings.Contains(words, "answer shared") {
			t.Errorf("the settled notice says %s", words)
		}
		if strings.Contains(wording(settled), "Q3 numbers") == false {
			t.Errorf("the settled notice lost the question it was about: %s", wording(settled))
		}

		var answer slackfake.Message
		s.eventually("the answer in the thread", 90*time.Second, func() bool {
			message, ok := s.theAnswer(summary.thread)
			answer = message
			return ok
		})
		words := wording(answer)
		for _, want := range []string{"Revenue grew by about a fifth in Q3", "Raya's agent · approved by Raya", "AI summary only"} {
			if !strings.Contains(words, want) {
				t.Errorf("the answer does not say %q: %s", want, words)
			}
		}
		// A summary is not the source: nothing of the passage is copied out of it.
		if strings.Contains(words, "4.2 million") {
			t.Errorf("a summary quotes the source: %s", words)
		}
		// It went to the thread, as a reply everyone in the channel reads.
		if answer.Channel != channelGeneral || answer.ThreadTS != summary.thread || answer.Ephemeral {
			t.Errorf("the answer went to %+v", answer)
		}
		// The ledger says how it was decided and how it ended.
		request := s.waitForStatus(raya, summary.requestID, "completed")
		if at(request, "decision", "decided_by", "surface") != "slack" || at(request, "decision", "consent") != "summary" ||
			at(request, "decision", "decided_by", "user_id") != raya.UserID {
			t.Errorf("the decision = %v", at(request, "decision"))
		}
		// The owner's agent read the knowledge as the owner, and no one else.
		for _, reader := range s.rag.readers() {
			if reader != "Bearer agent:"+raya.UserID {
				t.Errorf("the knowledge was read with %q, want Raya's agent token", reader)
			}
		}
		if len(s.rag.readers()) == 0 {
			t.Error("the owner's agent answered without reading anything")
		}
		if got := s.decisions(summary.requestID); got != 1 {
			t.Errorf("%d decisions were published for the request, want one", got)
		}
		_ = before
	})

	t.Run("access is a lease: the asker's follow-up in the thread is answered with no click, from the lease", func(t *testing.T) {
		access := s.ask(ada, raya, "about the Q3 plan", "")
		s.clickOn(raya, access.notice, "consent_access", access.tickets["consent_access"])
		if words := wording(s.noticeSettled(access.notice)); !strings.Contains(words, "access for 1 hour") {
			t.Errorf("the settled notice says %s", words)
		}
		var first slackfake.Message
		s.eventually("the first answer in the thread", 90*time.Second, func() bool {
			message, ok := s.theAnswer(access.thread)
			first = message
			return ok
		})
		if words := wording(first); !strings.Contains(words, "approved by Raya") || strings.Contains(words, "AI summary only") || strings.Contains(words, "1-hour") {
			t.Errorf("the first answer, approved with access, is attributed %s", words)
		}
		s.waitForStatus(raya, access.requestID, "completed")

		// A follow-up in the same thread, within the hour: no notice, no click.
		before := s.count()
		followUp := s.mention(ada, "and <@"+raya.SlackID+"> what about the Q4 plan?", access.thread)
		var answer slackfake.Message
		s.eventually("the follow-up's answer in the thread", 90*time.Second, func() bool {
			for _, message := range s.fromIndex(before) {
				if message.Channel == channelGeneral && message.ThreadTS == access.thread && !message.Ephemeral && strings.Contains(wording(message), "1-hour access") {
					answer = message
					return true
				}
			}
			return false
		})
		if words := wording(answer); !strings.Contains(words, "Raya's agent · approved by Raya") {
			t.Errorf("the follow-up's answer is attributed %s", words)
		}
		for _, message := range s.fromIndex(before) {
			if message.Channel == dm && len(buttons(message)) > 0 {
				t.Errorf("the owner was asked again, in %+v, for a follow-up her lease covers", message)
			}
		}
		// The ledger says the lease decided it, as the owner gave it, and names it.
		_, listed := s.api(raya, http.MethodGet, "/v1/organizations/"+organizationID+"/agent-requests?box=inbox&origin=slack&limit=50", nil)
		found := false
		for _, item := range asSlice(listed["requests"]) {
			if at(item, "ask_id") == at(s.request(raya, access.requestID), "ask_id") {
				continue
			}
			if at(item, "decision", "decided_by", "kind") == "lease" {
				found = true
				if at(item, "decision", "decided_by", "surface") != "lease" || at(item, "decision", "decided_by", "user_id") != raya.UserID {
					t.Errorf("the follow-up was decided as %v", at(item, "decision", "decided_by"))
				}
			}
		}
		if !found {
			t.Errorf("no request was decided by a lease among %v", listed["requests"])
		}
		_ = followUp

		// The owner ends the lease, in Oryxa, and the asker's next follow-up asks
		// again: a notice with its buttons, and no answer until it is decided.
		_, activity := s.api(raya, http.MethodGet, "/v1/organizations/"+organizationID+"/agent-requests/activity?limit=100", nil)
		leaseID := ""
		for _, entry := range asSlice(activity["activity"]) {
			if at(entry, "kind") == "lease_given" && at(entry, "lease", "request_id") == access.requestID {
				leaseID, _ = at(entry, "lease", "id").(string)
			}
		}
		if leaseID == "" {
			t.Fatalf("the owner's activity does not show the lease they gave: %v", activity)
		}
		if status, body := s.api(raya, http.MethodDelete, "/v1/organizations/"+organizationID+"/agent-leases/"+leaseID, nil); status != http.StatusOK || at(body, "lease", "revoked_at") == nil {
			t.Fatalf("revoke the lease: %d %v", status, body)
		}
		before = s.count()
		s.mention(ada, "and <@"+raya.SlackID+"> what about the Q1 plan?", access.thread)
		var again slackfake.Message
		s.eventually("a new notice, since the lease was revoked", 40*time.Second, func() bool {
			for _, message := range s.fromIndex(before) {
				if message.Channel == dm && len(buttons(message)) > 0 {
					again = message
					return true
				}
			}
			return false
		})
		s.quiet()
		for _, message := range s.fromIndex(before) {
			if message.Channel == channelGeneral && !message.Ephemeral && strings.Contains(wording(message), "1-hour access") {
				t.Errorf("a follow-up was answered under a lease its owner had revoked: %+v", message)
			}
		}
		// Settle it, so that it is not left open.
		s.clickOn(raya, again, "consent_deny", ticketsOf(again)["consent_deny"])
		s.noticeSettled(again)
	})

	raceOnTheCard(t, s)

	t.Run("an answer that cannot be grounded is held for its owner, and the thread says only that it is held", func(t *testing.T) {
		restore := s.aws.setGuardrail(func(call guardrailCall) guardrailAnswer {
			if call.Grounding {
				return failGuardrail(call)
			}
			return defaultGuardrail(call)
		})
		defer restore()
		held := s.ask(ada, raya, "about the org chart", "")
		before := s.count()
		s.clickOn(raya, held.notice, "consent_summary", held.tickets["consent_summary"])
		s.waitForStatus(raya, held.requestID, "review")

		var status slackfake.Message
		s.eventually("the held status in the thread", 40*time.Second, func() bool {
			for _, message := range s.fromIndex(before) {
				if message.Channel == channelGeneral && message.ThreadTS == held.thread && strings.Contains(wording(message), "held") {
					status = message
					return true
				}
			}
			return false
		})
		if words := wording(status); !strings.Contains(words, "The answer is held for Raya's review.") {
			t.Errorf("the thread was told %s", words)
		}
		// Its owner is asked to review it, alone, in her direct message: why it
		// was held, the answer, and a button for each review. There is no masked
		// version of an answer that could not be checked, and no button for one.
		review := s.review(raya, before)
		if words := wording(review); !strings.Contains(words, "Oryxa couldn't check it automatically.") || !strings.Contains(words, "Revenue grew") {
			t.Errorf("the owner's review says %s", words)
		}
		if got := buttons(review); len(got) != 3 || got[0].ActionID != "review_send" || got[1].ActionID != "review_withhold" || got[2].ActionID != "reply" {
			t.Errorf("the review's buttons = %+v, want Send, Don't send and Reply yourself", got)
		}
		reviewTickets := ticketsOf(review)
		for _, ticket := range reviewTickets {
			s.mustNotBeLogged(ticket)
		}
		// Nothing of the answer, or of what it would have said, was said in Slack
		// anywhere but there.
		s.nothingSaidBut(before, review.Channel, "Revenue grew", "an answer that was held")
		s.nothingSaidBut(before, review.Channel, "enterprise tier", "an answer that was held")
		if _, ok := s.theAnswer(held.thread); ok {
			t.Error("an answer was posted in the thread while it was held")
		}
		// In Oryxa, the one who asked sees only that it waits; its owner sees why,
		// and the answer, to send or to keep back.
		asker := s.request(ada, held.requestID)
		if at(asker, "status") != "review" || at(asker, "result") != nil || at(asker, "held") != nil {
			t.Errorf("the asker reads the held request as %v", asker)
		}
		owner := s.request(raya, held.requestID)
		if at(owner, "held", "result", "content") == nil || at(owner, "guard", "state") != "held" || !strings.Contains(fmt.Sprint(at(owner, "guard", "kinds")), "UNCHECKED") {
			t.Errorf("the owner reads the held request as %v", owner)
		}
		// The owner keeps it back, in Oryxa, and the asker is told alone that they chose not to share.
		if status, body := s.api(raya, http.MethodPost, "/v1/organizations/"+organizationID+"/agent-requests/"+held.requestID+"/review", map[string]string{"action": "withhold"}); status != http.StatusOK {
			t.Fatalf("the review: %d %v", status, body)
		}
		s.waitForStatus(raya, held.requestID, "withheld")
		s.eventually("the asker to be told the owner chose not to share", 40*time.Second, func() bool {
			for _, message := range s.fromIndex(before) {
				if message.Ephemeral && message.User == ada.SlackID && strings.Contains(wording(message), "chose not to share") {
					return true
				}
			}
			return false
		})
		// Her review in Slack says it was decided in Oryxa, and shows the answer no
		// more; a click on its stale buttons sends nothing.
		settled := s.noticeSettled(review)
		if words := wording(settled); !strings.Contains(words, "Already decided in Oryxa") {
			t.Errorf("the settled review says %s", words)
		}
		s.nothingSaid(before, "Revenue grew", "an answer that was withheld")
		s.clickOn(raya, review, "review_send", reviewTickets["review_send"])
		s.quiet()
		if request := s.request(raya, held.requestID); at(request, "status") != "withheld" || at(request, "review", "decided_by", "surface") != nil {
			t.Errorf("a click after the web's review changed the request to %v", request)
		}
		if _, ok := s.theAnswer(held.thread); ok {
			t.Error("an answer withheld on the web was posted by a click in Slack")
		}
	})

	t.Run("an answer that does not answer the question is held, its owner is asked in Slack, and her Send posts it in the thread", func(t *testing.T) {
		// The consent guard scores the answer's relevance at 0.04, well under the
		// 0.50 it needs, and holds it as IRRELEVANT, as it held one live.
		restore := s.aws.setGuardrail(func(call guardrailCall) guardrailAnswer {
			if call.Grounding {
				return guardrailAnswer{Status: http.StatusOK, Body: groundingBody(0.95, 0.04, nil)}
			}
			return defaultGuardrail(call)
		})
		defer restore()
		held := s.ask(ada, raya, "about the travel budget", "")
		before := s.count()
		s.clickOn(raya, held.notice, "consent_summary", held.tickets["consent_summary"])
		s.waitForStatus(raya, held.requestID, "review")

		// The owner is shown, alone, who asked what, why it was held in plain
		// words, and the answer as it would be sent.
		review := s.review(raya, before)
		if review.Channel != slackfake.DMChannel(raya.SlackID) || review.Ephemeral {
			t.Fatalf("the review went to %+v, want Raya's direct message", review)
		}
		words := wording(review)
		for _, want := range []string{"<@" + ada.SlackID + ">", "about the travel budget", "held for your review",
			"The answer may not address the question.", "Revenue grew by about a fifth in Q3"} {
			if !strings.Contains(words, want) {
				t.Errorf("the review does not say %q: %s", want, words)
			}
		}
		got := buttons(review)
		if len(got) != 3 || got[0].ActionID != "review_send" || got[0].Label != "Send" || got[1].ActionID != "review_withhold" || got[1].Label != "Don't send" ||
			got[2].ActionID != "reply" || got[2].Label != "Reply yourself" {
			t.Fatalf("the review's buttons = %+v, want Send, Don't send and Reply yourself: an answer held for relevance has no masked version", got)
		}
		reviewTickets := ticketsOf(review)
		for action, ticket := range reviewTickets {
			kind := `"k":"review"`
			if action == "reply" {
				kind = `"k":"reply"`
			}
			if requestOf(t, ticket) != held.requestID || !strings.Contains(claimsOf(t, ticket), kind) {
				t.Errorf("the %s button carries %s, not a ticket of its kind for the request", action, claimsOf(t, ticket))
			}
			s.mustNotBeLogged(ticket)
		}
		// Everyone else is told only that it is held.
		s.nothingSaidBut(before, review.Channel, "Revenue grew", "an answer that was held")
		if _, ok := s.theAnswer(held.thread); ok {
			t.Error("an answer was posted in the thread while it was held")
		}

		// A click by anyone but its owner, or a consent's ticket on a review's
		// button, sends nothing.
		s.clickOn(ada, review, "review_send", reviewTickets["review_send"])
		s.clickOn(raya, review, "review_send", held.tickets["consent_access"])
		s.quiet()
		if request := s.request(raya, held.requestID); at(request, "status") != "review" || at(request, "review") != nil {
			t.Fatalf("a click that should have sent nothing left the request as %v", request)
		}

		// Her own Send posts it in the thread, and her review says it was sent.
		s.clickOn(raya, review, "review_send", reviewTickets["review_send"])
		request := s.waitForStatus(raya, held.requestID, "completed")
		if at(request, "review", "action") != "send" || at(request, "review", "decided_by", "surface") != "slack" ||
			at(request, "review", "decided_by", "user_id") != raya.UserID {
			t.Errorf("the review is recorded as %v", at(request, "review"))
		}
		var answer slackfake.Message
		s.eventually("the answer in the thread", 60*time.Second, func() bool {
			message, ok := s.theAnswer(held.thread)
			answer = message
			return ok
		})
		for _, want := range []string{"Revenue grew by about a fifth in Q3", "Raya's agent · approved by Raya"} {
			if !strings.Contains(wording(answer), want) {
				t.Errorf("the answer does not say %q: %s", want, wording(answer))
			}
		}
		if answer.Channel != channelGeneral || answer.ThreadTS != held.thread || answer.Ephemeral {
			t.Errorf("the answer went to %+v", answer)
		}
		settled := s.noticeSettled(review)
		if words := wording(settled); !strings.Contains(words, "Sent") || strings.Contains(words, "Revenue grew") {
			t.Errorf("the settled review says %s", words)
		}

		// The review is over: her review on the web, and a second click, change nothing.
		if status, body := s.api(raya, http.MethodPost, "/v1/organizations/"+organizationID+"/agent-requests/"+held.requestID+"/review", map[string]string{"action": "withhold"}); status != http.StatusConflict {
			t.Errorf("a review on the web after Slack's: %d %v, want a conflict", status, body)
		}
		s.clickOn(raya, review, "review_withhold", reviewTickets["review_withhold"])
		s.quiet()
		if request := s.request(raya, held.requestID); at(request, "status") != "completed" || at(request, "review", "action") != "send" {
			t.Errorf("a second review changed the request to %v", request)
		}
		answers := 0
		for _, message := range s.inThread(held.thread) {
			if !message.Ephemeral && strings.Contains(wording(message), "'s agent") {
				answers++
			}
		}
		if answers != 1 {
			t.Errorf("the thread has %d answers, want the one sent", answers)
		}
	})

	t.Run("Reply yourself opens a modal, and her own words, sent from it, answer in the thread as hers; her agent never runs", func(t *testing.T) {
		const words = "Yes, the offsite is on Friday; I booked the room myself."
		s.mustNotBeLogged(words)
		own := s.ask(ada, raya, "whether the offsite is on Friday", "")
		ticket := own.tickets["reply"]
		if ticket == "" || requestOf(t, ticket) != own.requestID || !strings.Contains(claimsOf(t, ticket), `"k":"reply"`) {
			t.Fatalf("the notice's Reply yourself carries %q, not a reply's ticket for the request", ticket)
		}
		s.quiet()
		readers, answers, opened := len(s.rag.readers()), s.agentAnswers(), len(s.slack.Views())

		// The click opens the modal, to her, before Slack is answered, and decides nothing.
		s.clickOn(raya, own.notice, "reply", ticket)
		views := s.slack.Views()
		if len(views) != opened+1 {
			t.Fatalf("the click opened %d modals, want one", len(views)-opened)
		}
		modal := views[len(views)-1]
		title, _ := at(modal.View, "title", "text").(string)
		blocks, _ := modal.View["blocks"].([]any)
		said := blocksText(blocks)
		if modal.User != raya.SlackID || modal.View["callback_id"] != "oryxa_reply" || title != "Reply yourself" ||
			at(modal.View, "submit", "text") != "Send" || at(modal.View, "close", "text") != "Cancel" {
			t.Fatalf("the modal opened = %+v", modal)
		}
		for _, want := range []string{"Reply to <@" + ada.SlackID + ">, who asked:", "whether the offsite is on Friday", "Your agent does not answer it."} {
			if !strings.Contains(said, want) {
				t.Errorf("the modal does not say %q: %s", want, said)
			}
		}
		if metadata, _ := modal.View["private_metadata"].(string); !strings.Contains(metadata, ticket) {
			t.Errorf("the modal does not carry the button's ticket: %q", metadata)
		}
		if request := s.request(raya, own.requestID); at(request, "status") != "pending" || s.decisions(own.requestID) != 0 {
			t.Fatalf("opening the modal changed the request to %v", request)
		}

		// Sending nothing keeps the modal open with the reason, and records nothing.
		status, answer := s.submit(slackfake.Submission{Values: map[string]map[string]string{"reply": {"text": "   "}}})
		if status != http.StatusOK || !strings.Contains(answer, `"response_action":"errors"`) || !strings.Contains(answer, "Write your reply first.") {
			t.Fatalf("an empty reply was answered %d %s, want the error in the modal", status, answer)
		}
		// Her words close it, and become the answer.
		status, answer = s.submit(slackfake.Submission{Values: map[string]map[string]string{"reply": {"text": "  " + words + "\n"}}})
		if status != http.StatusOK || answer != "" {
			t.Fatalf("the reply was answered %d %q, want an empty 200, which closes the modal", status, answer)
		}
		request := s.waitForStatus(raya, own.requestID, "completed")
		if at(request, "decision", "decision") != "approved" || at(request, "decision", "consent") != "reply" ||
			at(request, "decision", "decided_by", "surface") != "slack" || at(request, "decision", "decided_by", "user_id") != raya.UserID ||
			at(request, "result", "written_by_owner") != true || at(request, "result", "summary") != words {
			t.Errorf("the request = %v", request)
		}
		if asker := s.request(ada, own.requestID); at(asker, "result", "written_by_owner") != true || at(asker, "result", "summary") != words {
			t.Errorf("the asker reads the answer as %v", at(asker, "result"))
		}

		// The thread hears her words, as hers, and nothing of an agent's.
		reply := s.ownReply(own.thread, "Raya")
		if said := wording(reply); !strings.Contains(said, words) || strings.Contains(said, "'s agent") || strings.Contains(said, "AI summary") {
			t.Errorf("the thread was told %s", said)
		}
		if reply.Channel != channelGeneral || reply.ThreadTS != own.thread || reply.Ephemeral {
			t.Errorf("her reply went to %+v", reply)
		}
		// Her notice says she replied, and what.
		if said := wording(s.noticeSettled(own.notice)); !strings.Contains(said, "You replied") || !strings.Contains(said, words) {
			t.Errorf("the settled notice says %s", said)
		}
		s.quiet()
		// Her agent never ran: nothing was searched and no model answered.
		if len(s.rag.readers()) != readers || s.agentAnswers() != answers {
			t.Errorf("her agent ran for a reply: %d reads and %d answers, were %d and %d", len(s.rag.readers()), s.agentAnswers(), readers, answers)
		}
		if got := s.decisions(own.requestID); got != 1 {
			t.Errorf("%d decisions were published for the request, want one", got)
		}
		s.answeredInOwnWords(own.requestID, raya, ada)

		// Afterwards a choice on the web, a reply on the web and a stale click
		// change nothing, and the web is told what won.
		base := "/v1/organizations/" + organizationID + "/agent-requests/" + own.requestID
		if status, body := s.api(raya, http.MethodPost, base+"/consent", map[string]string{"choice": "access"}); status != http.StatusConflict ||
			at(body, "error", "code") != "already_decided" || at(body, "winner", "consent") != "reply" {
			t.Errorf("a choice on the web after her reply = %d %v, want a refusal naming the reply", status, body)
		}
		if status, body := s.api(raya, http.MethodPost, base+"/reply", map[string]string{"text": "Something else."}); status != http.StatusConflict ||
			at(body, "error", "code") != "already_decided" || at(body, "winner", "consent") != "reply" {
			t.Errorf("a second reply = %d %v, want a refusal naming the first", status, body)
		}
		s.clickOn(raya, own.notice, "consent_access", own.tickets["consent_access"])
		s.quiet()
		if request := s.request(raya, own.requestID); at(request, "decision", "consent") != "reply" || at(request, "result", "summary") != words {
			t.Errorf("what came after her reply changed the request to %v", request)
		}
		if got := s.decisions(own.requestID); got != 1 {
			t.Errorf("%d decisions were published for the request, want one", got)
		}
		replies := 0
		for _, message := range s.inThread(own.thread) {
			if !message.Ephemeral && strings.Contains(wording(message), "replied:") {
				replies++
			}
		}
		if replies != 1 {
			t.Errorf("the thread has %d replies, want the one", replies)
		}
	})

	t.Run("a held answer she replaces with her own words, on the web, is never sent, and her review in Slack says she replied", func(t *testing.T) {
		const words = "The travel budget is on hold until the board meets."
		s.mustNotBeLogged(words)
		restore := s.aws.setGuardrail(func(call guardrailCall) guardrailAnswer {
			if call.Grounding {
				return failGuardrail(call)
			}
			return defaultGuardrail(call)
		})
		defer restore()
		held := s.ask(ada, raya, "about the offsite travel budget", "")
		before := s.count()
		s.clickOn(raya, held.notice, "consent_summary", held.tickets["consent_summary"])
		s.waitForStatus(raya, held.requestID, "review")
		review := s.review(raya, before)
		got := buttons(review)
		if len(got) != 3 || got[2].ActionID != "reply" || got[2].Label != "Reply yourself" {
			t.Fatalf("the review's buttons = %+v, want Reply yourself last", got)
		}
		reviewTickets := ticketsOf(review)
		for _, ticket := range reviewTickets {
			s.mustNotBeLogged(ticket)
		}
		if claims := claimsOf(t, reviewTickets["reply"]); !strings.Contains(claims, `"k":"reply"`) {
			t.Errorf("the review's Reply yourself carries %s, not a reply's ticket", claims)
		}

		status, body := s.api(raya, http.MethodPost, "/v1/organizations/"+organizationID+"/agent-requests/"+held.requestID+"/reply", map[string]string{"text": "  " + words + "  "})
		if status != http.StatusOK || at(body, "request", "status") != "completed" || at(body, "request", "review", "action") != "reply" ||
			at(body, "request", "review", "decided_by", "surface") != "web" || at(body, "request", "result", "written_by_owner") != true ||
			at(body, "request", "result", "summary") != words || at(body, "request", "held") != nil {
			t.Fatalf("the reply: %d %v", status, body)
		}
		if asker := s.request(ada, held.requestID); at(asker, "result", "summary") != words || at(asker, "result", "written_by_owner") != true || at(asker, "held") != nil {
			t.Errorf("the asker reads the request as %v", asker)
		}

		// The thread hears her words; the held answer is said nowhere, and her
		// review becomes her reply, showing the held answer no more.
		reply := s.ownReply(held.thread, "Raya")
		if said := wording(reply); !strings.Contains(said, words) || strings.Contains(said, "Revenue grew") {
			t.Errorf("the thread was told %s", said)
		}
		if _, ok := s.theAnswer(held.thread); ok {
			t.Error("the held answer was posted in the thread")
		}
		settled := s.noticeSettled(review)
		if said := wording(settled); !strings.Contains(said, "You replied") || !strings.Contains(said, words) || strings.Contains(said, "Revenue grew") {
			t.Errorf("the settled review says %s", said)
		}
		s.nothingSaid(before, "Revenue grew", "an answer that was replaced")

		// A Send on the stale review sends nothing.
		s.clickOn(raya, review, "review_send", reviewTickets["review_send"])
		s.quiet()
		if request := s.request(raya, held.requestID); at(request, "status") != "completed" || at(request, "review", "action") != "reply" || at(request, "result", "summary") != words {
			t.Errorf("a click after her reply changed the request to %v", request)
		}
		s.nothingSaid(before, "Revenue grew", "an answer that was replaced")
		s.answeredInOwnWords(held.requestID, raya, ada)
	})

	t.Run("a mention by a guest is refused, to the guest alone, and nobody is asked", func(t *testing.T) {
		s.quiet()
		inbox := func() int {
			_, listed := s.api(raya, http.MethodGet, "/v1/organizations/"+organizationID+"/agent-requests?box=inbox&origin=slack&limit=100", nil)
			return len(asSlice(listed["requests"]))
		}
		requests, before := inbox(), s.count()
		thread := s.mention(gus, "ask <@"+raya.SlackID+"> for the Q3 numbers", "")
		var refusal slackfake.Message
		s.eventually("the refusal", 40*time.Second, func() bool {
			for _, message := range s.fromIndex(before) {
				if message.Channel == channelGeneral && message.ThreadTS == thread {
					refusal = message
					return true
				}
			}
			return false
		})
		if !refusal.Ephemeral || refusal.User != gus.SlackID || !strings.Contains(wording(refusal), "Only members of Acme in Oryxa can ask me.") {
			t.Errorf("the refusal is %+v", refusal)
		}
		s.quiet()
		if got := s.count() - before; got != 1 {
			t.Errorf("%d messages were posted for a guest's mention, want the refusal alone", got)
		}
		if got := inbox(); got != requests {
			t.Errorf("the owner has %d requests from Slack, was %d: a guest's mention asked her", got, requests)
		}
	})

	t.Run("each refusal of the screen and the department check says what it means, in apps' own words, to the asker alone", func(t *testing.T) {
		s.quiet()
		for _, test := range []struct {
			name   string
			asker  person
			text   string
			setup  func() (restore func())
			expect string
		}{
			{"words that are not work", ada, "ask <@" + raya.SlackID + "> to sing me a song", nil, "I only pass on work questions."},
			{"a screen that could not finish", ada, "ask <@" + raya.SlackID + "> for the Q3 numbers", func() func() {
				s.jev.setDown(true)
				return func() { s.jev.setDown(false) }
			}, "I couldn't check that just now, so I didn't ask anyone. Try again in a moment."},
			{"an asker who has not let agents act for them", nina, "ask <@" + raya.SlackID + "> for the Q3 numbers", nil,
				"Give Oryxa's agent access to act for you in Settings, then ask again."},
			{"an owner whose department is plainly the wrong one", ada, "ask <@" + raya.SlackID + "> about the zoning permit", nil,
				"I didn't ask <@" + raya.SlackID + ">: this looks like a question for another team."},
		} {
			t.Run(test.name, func(t *testing.T) {
				if test.setup != nil {
					defer test.setup()()
				}
				before := s.count()
				thread := s.mention(test.asker, test.text, "")
				var refusal slackfake.Message
				s.eventually("the refusal", 40*time.Second, func() bool {
					for _, message := range s.fromIndex(before) {
						if message.Channel == channelGeneral && message.ThreadTS == thread {
							refusal = message
							return true
						}
					}
					return false
				})
				if !refusal.Ephemeral || refusal.User != test.asker.SlackID || !strings.Contains(wording(refusal), test.expect) {
					t.Errorf("the refusal is %+v, want an ephemeral note to %s saying %q", refusal, test.asker.Name, test.expect)
				}
				s.quiet()
				if got := s.count() - before; got != 1 {
					t.Errorf("%d messages were posted, want the refusal alone: %+v", got, s.fromIndex(before))
				}
			})
		}
	})

	t.Run("Slack delivering a mention twice, as it does when it is not answered in time, is one ask and one notice", func(t *testing.T) {
		s.quiet()
		before := s.count()
		ts := nextTS()
		deliver := func() {
			request := s.slack.Mention(s.baseURL, slackfake.Mention{
				Channel: channelGeneral, User: ada.SlackID, TS: ts, EventID: "EvRETRIED0001",
				Text: "<@" + slackfake.DefaultBotUserID + "> ask <@" + raya.SlackID + "> about the retry plan",
			})
			response, err := slackfake.Send(nil, request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("a delivery was answered %d", response.StatusCode)
			}
		}
		deliver()
		deliver()
		s.mustNotBeLogged("the retry plan")
		s.eventually("the notice", 40*time.Second, func() bool {
			for _, message := range s.fromIndex(before) {
				if message.Channel == dm && len(buttons(message)) > 0 {
					return true
				}
			}
			return false
		})
		s.quiet()
		notices, acknowledgements := 0, 0
		var notice slackfake.Message
		for _, message := range s.fromIndex(before) {
			switch {
			case message.Channel == dm && len(buttons(message)) > 0:
				notices++
				notice = message
			case message.Channel == channelGeneral && message.Ephemeral && message.User == ada.SlackID:
				acknowledgements++
			}
		}
		if notices != 1 || acknowledgements != 1 {
			t.Errorf("a mention delivered twice made %d notices and %d acknowledgements, want one of each", notices, acknowledgements)
		}
		_, listed := s.api(raya, http.MethodGet, "/v1/organizations/"+organizationID+"/agent-requests?box=inbox&origin=slack&limit=100", nil)
		requests := 0
		for _, item := range asSlice(listed["requests"]) {
			if strings.Contains(fmt.Sprint(at(item, "task")), "the retry plan") {
				requests++
			}
		}
		if requests != 1 {
			t.Errorf("a mention delivered twice made %d requests, want one", requests)
		}
		// Settle it, so that it is not left open.
		tickets := ticketsOf(notice)
		s.clickOn(raya, notice, "consent_deny", tickets["consent_deny"])
		s.noticeSettled(notice)
	})

	t.Run("a click by someone who is not the owner, or with an altered ticket, decides nothing, and the owner's own click still does", func(t *testing.T) {
		victim := s.ask(ada, raya, "about the audit plan", "")
		genuine := victim.tickets["consent_access"]
		altered := genuine[:len(genuine)-2] + map[bool]string{true: "AA", false: "BB"}[!strings.HasSuffix(genuine, "AA")]
		s.clickOn(raya, victim.notice, "consent_access", altered)
		s.clickOn(ada, victim.notice, "consent_access", genuine)
		s.clickOn(gus, victim.notice, "consent_access", genuine)
		s.quiet()
		request := s.request(raya, victim.requestID)
		if at(request, "status") != "pending" || at(request, "decision") != nil {
			t.Errorf("a click that should have decided nothing left the request as %v", request)
		}
		if message, _ := s.slack.Message(victim.notice.Channel, victim.notice.TS); message.Updates != 0 || len(buttons(message)) != 4 {
			t.Errorf("the notice changed, though nothing was decided: %+v", message)
		}
		if got := s.decisions(victim.requestID); got != 0 {
			t.Errorf("%d decisions were published, want none", got)
		}
		// The owner's own click, on the same buttons, decides it.
		s.clickOn(raya, victim.notice, "consent_deny", victim.tickets["consent_deny"])
		s.noticeSettled(victim.notice)
		if request := s.waitForStatus(raya, victim.requestID, "rejected"); at(request, "decision", "decided_by", "user_id") != raya.UserID {
			t.Errorf("the request was decided by %v", at(request, "decision", "decided_by"))
		}
	})

	t.Run("a request for a credential is an ask for a secret: Slack hears only that it was answered privately, and the owner shares it in Oryxa, once", func(t *testing.T) {
		// What the agent finds, and writes, is a key, which must never reach Slack
		// whatever else is done to it on the way.
		key := "sk_live_" + strings.Repeat("a1B2c3D4", 3)
		s.mustNotBeLogged(key)
		restore := s.vertex.setPlan(func(string) answerPlan {
			return answerPlan{
				Query:   "staging API key",
				Summary: "The staging API key is " + key,
				Content: "The staging API key is " + key + " and lives in the vault under staging.",
			}
		})
		defer restore()
		secret := s.ask(ada, raya, "can you share the staging API key?", "")
		before := s.count()
		s.clickOn(raya, secret.notice, "consent_access", secret.tickets["consent_access"])
		s.noticeSettled(secret.notice)
		s.eventually("the thread to be told it was answered privately", 90*time.Second, func() bool {
			for _, message := range s.fromIndex(before) {
				if message.Channel == channelGeneral && message.ThreadTS == secret.thread && !message.Ephemeral && strings.Contains(wording(message), "Answered privately.") {
					return true
				}
			}
			return false
		})
		s.nothingSaid(0, key, "a credential")
		s.nothingSaid(before, "lives in the vault", "an answer to a request for a credential")
		if _, ok := s.theAnswer(secret.thread); ok {
			t.Error("an answer to a request for a credential was posted in the thread")
		}

		// The owner types the value into Oryxa, and the one who asked opens it once.
		value := "staging-" + strings.Repeat("Zx9Qw", 5)
		s.mustNotBeLogged(value)
		path := "/v1/organizations/" + organizationID + "/agent-requests/" + secret.requestID + "/secret"
		if status, body := s.api(raya, http.MethodPut, path, map[string]any{"value": value, "ttl_seconds": 600}); status != http.StatusOK {
			t.Fatalf("share the secret: %d %v", status, body)
		}
		status, opened := s.api(ada, http.MethodGet, path, nil)
		if status != http.StatusOK || opened["value"] != value {
			t.Fatalf("open the secret: %d, want the value shared", status)
		}
		if status, again := s.api(ada, http.MethodGet, path, nil); status != http.StatusGone || at(again, "error", "code") != "secret_gone" {
			t.Errorf("opening the secret a second time: %d %v, want it gone", status, again)
		}
		s.nothingSaid(0, value, "a shared secret")
		s.nothingSaid(0, key, "a credential")
	})

	t.Run("uninstalling the app ends the install, and agents forgets the workspace", func(t *testing.T) {
		s.quiet()
		held := func(table string) int {
			var count int
			query := "SELECT count(*) FROM agents_primary." + table + " WHERE organization_id = $1"
			if err := s.database.QueryRow(context.Background(), query, organizationID).Scan(&count); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			return count
		}
		// What the loop has kept about the workspace by now: who is who in it, and
		// where each message it posted is.
		if held("slack_identities") == 0 || held("surface_links") == 0 {
			t.Fatalf("the loop kept %d identities and %d links, so there is nothing to forget", held("slack_identities"), held("surface_links"))
		}
		response, err := slackfake.Send(nil, s.slack.AppUninstalled(s.baseURL, slackfake.DefaultTeamID))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("the uninstall was answered %d, not 200", response.StatusCode)
		}
		var view map[string]any
		s.eventually("the install to end", 40*time.Second, func() bool {
			_, view = s.api(root, http.MethodGet, "/v1/organizations/"+organizationID+"/apps/slack/install", nil)
			return view["status"] == "ended"
		})
		if view["end_reason"] != "app_uninstalled" {
			t.Errorf("the install ended for %v, want app_uninstalled", view["end_reason"])
		}
		s.eventually("agents to forget the workspace", 40*time.Second, func() bool {
			return held("slack_identities") == 0 && held("surface_links") == 0
		})
		var ended bool
		for _, event := range s.aws.published("apps.slack.install.changed.v1") {
			ended = ended || strings.Contains(string(event.Detail), `"state":"ended"`)
		}
		if !ended {
			t.Error("apps never published that the install ended")
		}
	})

	t.Run("the process logged no question, answer, ticket, token or secret", func(t *testing.T) {
		s.quiet()
		logged := s.logs.String()
		if len(logged) < 1000 {
			t.Fatalf("the process logged almost nothing (%d bytes), so this proves nothing", len(logged))
		}
		s.mustNotBeLogged(
			"Revenue grew", "enterprise tier", "4.2 million", passage, "Q3 numbers", "the launch date",
			slackfake.DefaultBotToken, s.slack.SigningSecret(), s.slack.ClientSecret(), ticketKeyHex,
		)
		for _, value := range s.sensitive {
			if value != "" && strings.Contains(logged, value) {
				t.Errorf("the process logged %q", tail(value, 60))
			}
		}
		// The log is the process's own, and does record what it did, by id: an
		// empty one would prove nothing.
		for _, recorded := range []string{"a mention became an ask", "an owner decided a request from Slack", "an owner reviewed a held answer from Slack", "surfaces pass done", "slack job done",
			"slack reply modal opened", "slack reply recorded", "a reply was handed to the consent core", "an owner replied to a request themself"} {
			if !strings.Contains(logged, recorded) {
				t.Errorf("the process's log never says %q, so it may not be the log of what this test did", recorded)
			}
		}
	})
}

// The three ways a click in Slack and a choice on the web card meet, in the
// order the tests take them: together, the web first, and Slack first. Each is
// one decision, by whoever came first, and the other surface sees who did.
func raceOnTheCard(t *testing.T, s *stack) {
	dm := slackfake.DMChannel(raya.SlackID)
	consentPath := func(id string) string {
		return "/v1/organizations/" + organizationID + "/agent-requests/" + id + "/consent"
	}
	_ = dm

	t.Run("a click in Slack and a choice on the web at the same moment are one decision, and the loser sees the winner", func(t *testing.T) {
		// A click takes about a second to be routed, so the web's choice is sent
		// after each of a few pauses, to land before it, beside it and after it. Any
		// of them may win: what must hold is that one does, and that the other says so.
		won := map[string]int{}
		for _, pause := range []time.Duration{0, 600 * time.Millisecond, 1000 * time.Millisecond, 1400 * time.Millisecond} {
			race := s.ask(ada, raya, fmt.Sprintf("about the launch plan, round %d", pause/time.Millisecond), "")
			var wg sync.WaitGroup
			var clickErr, webErr error
			var webStatus int
			var webBody map[string]any
			wg.Add(2)
			go func() {
				defer wg.Done()
				clickErr = s.sendClick(raya, race.notice, "consent_access", race.tickets["consent_access"])
			}()
			go func() {
				defer wg.Done()
				time.Sleep(pause)
				webStatus, webBody, webErr = s.call(raya, http.MethodPost, consentPath(race.requestID), map[string]string{"choice": "summary"})
			}()
			wg.Wait()
			if clickErr != nil || webErr != nil {
				t.Fatalf("the click: %v; the web choice: %v", clickErr, webErr)
			}
			settled := s.noticeSettled(race.notice)
			request := s.waitForStatus(raya, race.requestID, "completed")

			surface, _ := at(request, "decision", "decided_by", "surface").(string)
			consent, _ := at(request, "decision", "consent").(string)
			words := wording(settled)
			switch surface {
			case "web":
				// The web card won with a summary: its call was answered, and the click
				// in Slack, which came too late, decided nothing.
				if webStatus != http.StatusOK || consent != "summary" || !strings.Contains(words, "answer shared") {
					t.Errorf("after %s the web won, and its call was %d, the consent %q, the notice %s", pause, webStatus, consent, words)
				}
			case "slack":
				// The click won with access: the web call was refused, and its answer
				// says who won, and what they chose.
				if webStatus != http.StatusConflict || at(webBody, "error", "code") != "already_decided" ||
					at(webBody, "winner", "decided_by", "surface") != "slack" || at(webBody, "winner", "consent") != "access" ||
					consent != "access" || !strings.Contains(words, "access for 1 hour") {
					t.Errorf("after %s Slack won, and the web call was %d %v, the consent %q, the notice %s", pause, webStatus, webBody, consent, words)
				}
			default:
				t.Fatalf("after %s the request was decided on %q: %v", pause, surface, at(request, "decision"))
			}
			if got := s.decisions(race.requestID); got != 1 {
				t.Errorf("after %s %d decisions were published for the request, want exactly one", pause, got)
			}
			won[surface]++
		}
		t.Logf("who won, of four races: %v", won)
	})

	t.Run("a choice on the web settles the notice, and a click on its buttons afterwards decides nothing", func(t *testing.T) {
		first := s.ask(ada, raya, "about the hiring plan", "")
		if status, body := s.api(raya, http.MethodPost, consentPath(first.requestID), map[string]string{"choice": "deny"}); status != http.StatusOK {
			t.Fatalf("the web choice: %d %v", status, body)
		}
		settled := s.noticeSettled(first.notice)
		if words := wording(settled); !strings.Contains(words, "Denied by <@"+raya.SlackID+">") {
			t.Errorf("the notice says %s", words)
		}
		// The buttons are gone from what people see, but a click on a stale copy of
		// them is a delivery all the same, and is for the router to refuse.
		s.clickOn(raya, first.notice, "consent_access", first.tickets["consent_access"])
		s.quiet()
		request := s.request(raya, first.requestID)
		if at(request, "status") != "rejected" || at(request, "decision", "decided_by", "surface") != "web" {
			t.Errorf("a click after the web's choice changed the request to %v", request)
		}
		if got := s.decisions(first.requestID); got != 1 {
			t.Errorf("%d decisions were published for the request, want exactly one", got)
		}
		// The asker is told, alone, that the owner chose not to share.
		s.eventually("the asker to be told", 30*time.Second, func() bool {
			for _, message := range s.inThread(first.thread) {
				if message.Ephemeral && message.User == ada.SlackID && strings.Contains(wording(message), "chose not to share") {
					return true
				}
			}
			return false
		})
	})

	t.Run("a click in Slack settles the card, and a choice on the web afterwards is refused with the winner", func(t *testing.T) {
		first := s.ask(ada, raya, "about the pricing plan", "")
		s.clickOn(raya, first.notice, "consent_deny", first.tickets["consent_deny"])
		s.noticeSettled(first.notice)
		status, body := s.api(raya, http.MethodPost, consentPath(first.requestID), map[string]string{"choice": "access"})
		if status != http.StatusConflict || at(body, "error", "code") != "already_decided" ||
			at(body, "winner", "decided_by", "surface") != "slack" || at(body, "winner", "decision") != "rejected" {
			t.Errorf("the web choice after Slack's = %d %v, want a refusal that names Slack's denial", status, body)
		}
		if got := s.decisions(first.requestID); got != 1 {
			t.Errorf("%d decisions were published for the request, want exactly one", got)
		}
	})
}

// quiet waits for the bus to be empty and everything it started to settle: the
// queue drained, and a moment for the work its events woke.
func (s *stack) quiet() {
	s.t.Helper()
	s.eventually("the bus to be quiet", 30*time.Second, func() bool { return s.aws.queueDepth() == 0 })
	time.Sleep(1500 * time.Millisecond)
	s.eventually("the bus to be quiet again", 30*time.Second, func() bool { return s.aws.queueDepth() == 0 })
}

// asSlice reads a JSON array.
func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// With the loop off, which is how a deployment ships until its Slack app's
// secrets are stored, apps takes a mention and says so on the bus, and agents,
// whose rule may already deliver it, acknowledges it and does nothing: nobody is
// asked, nothing is said in Slack, and the queue is left empty, where a failed
// event would sit until it moved to the dead-letter queue and rang its alarm.
func TestTheSlackLoopOffDropsWhatItHears(t *testing.T) {
	server := os.Getenv("SLACK_TEST_DATABASE_URL")
	if server == "" {
		t.Skip("SLACK_TEST_DATABASE_URL is not set")
	}
	s := bootStackWith(t, server, false)
	before := s.count()
	s.mention(ada, "ask <@"+raya.SlackID+"> for the Q3 numbers", "")
	s.eventually("agents to take the delivery off its queue", 40*time.Second, func() bool {
		return len(s.aws.published("apps.slack.message.received.v1")) == 1 && s.aws.queueDepth() == 0
	})
	s.quiet()
	if got := s.count() - before; got != 0 {
		t.Errorf("with the loop off, %d messages were posted: %+v", got, s.fromIndex(before))
	}
	if !strings.Contains(s.logs.String(), "the Slack consent loop is off; it is dropped") {
		t.Error("agents never said it dropped the delivery")
	}
	_, listed := s.api(raya, http.MethodGet, "/v1/organizations/"+organizationID+"/agent-requests?box=inbox&origin=slack&limit=100", nil)
	if got := len(asSlice(listed["requests"])); got != 0 {
		t.Errorf("with the loop off, the owner has %d requests from Slack", got)
	}
	// Nothing the loop would have made exists either: no ask, in the ledger's own table.
	var asks int
	if err := s.database.QueryRow(context.Background(), "SELECT count(*) FROM agents_primary.interaction_asks").Scan(&asks); err != nil || asks != 0 {
		t.Errorf("with the loop off there are %d asks (%v)", asks, err)
	}
}

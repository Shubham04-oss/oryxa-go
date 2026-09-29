package slackbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

const (
	answerJSON = `{"text":"The launch is on the 14th.","owner_name":"Raya","mode":"summary"}`
	noticeJSON = `{"asker_user_id":"U0ASKER","question":"When is the launch?","tickets":{"summary":"t1.a.b","access":"t1.c.d","deny":"t1.e.f"}}`
)

func (h *harness) post(key, template, params string, target Target) (PostResult, error) {
	h.t.Helper()
	return h.service.Post(context.Background(), PostRequest{
		OrganizationID: testOrg, IdempotencyKey: key, Target: target, Template: template, Params: json.RawMessage(params),
	})
}

// threadOf is the target of a reply in a thread a mention opened.
func threadOf(mention Inbound) Target {
	return Target{ChannelID: mention.ChannelID, ThreadTS: mention.ThreadTS}
}

func TestAnAnswerIsPostedInTheThreadOfAVerifiedMessageWithUnfurlsOff(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Text: askText, TS: "1780000700.000200", ThreadTS: "1780000000.000100"})

	// Naming the thread by the message that was mentioned in it works too: the
	// reply goes to the root either way.
	result, err := h.post("key-answer-1", TemplateAnswer, answerJSON, Target{ChannelID: mention.ChannelID, ThreadTS: mention.MessageTS})
	if err != nil {
		t.Fatal(err)
	}
	messages := h.fake.Messages()
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	message := messages[0]
	if result.ChannelID != "C0GENERAL" || result.TS != message.TS || result.Ephemeral || result.Replayed {
		t.Fatalf("result = %+v", result)
	}
	if message.Channel != "C0GENERAL" || message.ThreadTS != "1780000000.000100" || message.UnfurlLinks || message.UnfurlMedia ||
		len(message.Blocks) == 0 || !strings.Contains(message.Text, "The launch is on the 14th") {
		t.Fatalf("message = %+v", message)
	}
	posts := h.fake.Calls("chat.postMessage")
	if len(posts) != 1 || posts[0].Token != slackfake.DefaultBotToken {
		t.Fatalf("the post presented %q", posts[0].Token)
	}
	if posts[0].Args["unfurl_links"] != false || posts[0].Args["unfurl_media"] != false {
		t.Fatalf("unfurls were not switched off: %v", posts[0].Args)
	}

	// The token is opened once for a run of posts, not once each.
	for i := range 3 {
		if _, err := h.post(fmt.Sprintf("key-more-%d", i), TemplateStatus, `{"state":"working","owner_name":"Raya"}`, threadOf(mention)); err != nil {
			t.Fatal(err)
		}
	}
	if h.keys.opened() != 1 {
		t.Fatalf("the bot token was opened %d times", h.keys.opened())
	}
	// And never appears where it should not.
	h.assertLogsLack(slackfake.DefaultBotToken, h.fake.SigningSecret(), h.fake.ClientSecret())
}

func TestPostsGoOnlyToConversationsPeopleOpenedWithTheBot(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Channel: "C0GENERAL", User: "U0ASKER", Text: askText})
	calls := func() int { return len(h.fake.Calls()) }

	for name, test := range map[string]struct {
		template string
		params   string
		target   Target
	}{
		"a thread nobody mentioned the bot in": {TemplateStatus, `{"state":"held"}`, Target{ChannelID: "C0OTHER", ThreadTS: mention.ThreadTS}},
		"another thread in the channel":        {TemplateStatus, `{"state":"held"}`, Target{ChannelID: "C0GENERAL", ThreadTS: "1700000000.000001"}},
		"an ephemeral message for a stranger":  {TemplateRefusal, `{"reason":"no_owner"}`, Target{ChannelID: "C0GENERAL", ThreadTS: mention.ThreadTS, UserID: "U0STRANGE"}},
		"a direct message to a stranger":       {TemplateOwnerNotice, noticeJSON, Target{UserID: "U0STRANGE"}},
	} {
		before := calls()
		_, err := h.post("key-"+strings.ReplaceAll(name, " ", "-")[:20], test.template, test.params, test.target)
		if !errors.Is(err, ErrThreadUnseen) || !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s = %v, want ErrThreadUnseen", name, err)
		}
		if calls() != before {
			t.Errorf("%s: something was sent to Slack", name)
		}
	}

	// The people the message named, and its author, are people the surface saw.
	for i, user := range []string{"U0RAYA", "U0MAYA", "U0ASKER"} {
		result, err := h.post(fmt.Sprintf("key-dm-%d", i), TemplateOwnerNotice, noticeJSON, Target{UserID: user})
		if err != nil {
			t.Fatalf("a direct message to %s: %v", user, err)
		}
		if result.ChannelID != slackfake.DMChannel(user) {
			t.Fatalf("channel = %s", result.ChannelID)
		}
	}
	if opens := h.fake.Calls("conversations.open"); len(opens) != 3 {
		t.Fatalf("conversations.open calls = %d", len(opens))
	}
	// The conversation with a person is remembered.
	if _, err := h.post("key-dm-again", TemplateBypassNotice, `{"asker_user_id":"U0ASKER","question":"q","reason":"priority_channel"}`, Target{UserID: "U0RAYA"}); err != nil {
		t.Fatal(err)
	}
	if opens := h.fake.Calls("conversations.open"); len(opens) != 3 {
		t.Fatalf("conversations.open calls = %d after a second message to one person", len(opens))
	}

	// An ephemeral message to a person in the thread is only theirs.
	result, err := h.post("key-ephemeral", TemplateAskAck, `{"owners":[{"user_id":"U0RAYA"}],"open_url":"`+testWebURL+`/requests/1"}`,
		Target{ChannelID: mention.ChannelID, ThreadTS: mention.ThreadTS, UserID: "U0ASKER"})
	if err != nil || !result.Ephemeral {
		t.Fatalf("ephemeral = %+v, %v", result, err)
	}
	if got := h.fake.Calls("chat.postEphemeral"); len(got) != 1 || got[0].Args["user"] != "U0ASKER" || got[0].Args["thread_ts"] != mention.ThreadTS {
		t.Fatalf("chat.postEphemeral = %+v", got)
	}
}

func TestATemplateGoesOnlyWhereItMayGo(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Text: askText})
	inThread := threadOf(mention)
	toAsker := Target{ChannelID: mention.ChannelID, ThreadTS: mention.ThreadTS, UserID: "U0ASKER"}
	bypass := `{"asker_user_id":"U0ASKER","question":"q","reason":"org_chart"}`

	i := 0
	for name, test := range map[string]struct {
		template, params string
		target           Target
		// want is what the refusal says, so a request refused for some other
		// reason does not pass for the reason under test.
		want string
	}{
		"a notice with buttons in a thread":        {TemplateOwnerNotice, noticeJSON, inThread, "cannot be posted there"},
		"a notice with buttons, ephemeral":         {TemplateOwnerNotice, noticeJSON, toAsker, "cannot be posted there"},
		"a bypass notice in a thread":              {TemplateBypassNotice, bypass, inThread, "cannot be posted there"},
		"an answer, ephemeral":                     {TemplateAnswer, answerJSON, toAsker, "cannot be posted there"},
		"a settled notice in a thread":             {TemplateNoticeSettled, `{"asker_user_id":"U0ASKER","question":"q","decision":"expired"}`, inThread, "cannot be posted there"},
		"a template that does not exist":           {"broadcast", `{}`, inThread, "is not a template"},
		"params with a field the template has not": {TemplateStatus, `{"state":"held","blocks":[{"type":"actions"}]}`, inThread, "not one JSON object"},
		"params that are not an object":            {TemplateStatus, `"held"`, inThread, "not one JSON object"},
		"a status that is none":                    {TemplateStatus, `{"state":"celebrating"}`, inThread, "not one a status says"},
		"a target that is nothing":                 {TemplateStatus, `{"state":"held"}`, Target{}, "a target is a thread"},
		"a thread with no channel":                 {TemplateStatus, `{"state":"held"}`, Target{ThreadTS: mention.ThreadTS}, "a target is a thread"},
		"a channel with no thread":                 {TemplateStatus, `{"state":"held"}`, Target{ChannelID: mention.ChannelID}, "a target is a thread"},
		"an id that is no Slack id":                {TemplateStatus, `{"state":"held"}`, Target{ChannelID: "general", ThreadTS: mention.ThreadTS}, "Slack id of a shape"},
	} {
		i++
		before := len(h.fake.Calls())
		_, err := h.post(fmt.Sprintf("key-refused-%02d", i), test.template, test.params, test.target)
		if !errors.Is(err, domain.ErrInvalid) || err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s = %v, want ErrInvalid saying %q", name, err, test.want)
		}
		if len(h.fake.Calls()) != before {
			t.Errorf("%s: something was sent to Slack", name)
		}
	}
	if got := len(h.store.posts); got != 0 {
		t.Fatalf("%d posts were claimed for requests that were refused", got)
	}
}

func TestAPostIsIdempotentPerKey(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Text: askText})

	first, err := h.post("key-idem-1", TemplateAnswer, answerJSON, threadOf(mention))
	if err != nil {
		t.Fatal(err)
	}
	// Asked again in other JSON spelling of the same params, and by another
	// message's ts for the same thread.
	again, err := h.service.Post(context.Background(), PostRequest{
		OrganizationID: testOrg, IdempotencyKey: "key-idem-1", Template: TemplateAnswer,
		Target: Target{ChannelID: mention.ChannelID, ThreadTS: mention.MessageTS},
		Params: json.RawMessage("{ \"text\": \"The launch is on the 14th.\",\n \"owner_name\": \"Raya\", \"mode\": \"summary\" }"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.ChannelID != first.ChannelID || again.TS != first.TS {
		t.Fatalf("replay = %+v, first = %+v", again, first)
	}
	if got := len(h.fake.Calls("chat.postMessage")); got != 1 {
		t.Fatalf("posts = %d, want the replay to answer the first", got)
	}

	// The same key for something else is refused, and posts nothing.
	_, err = h.post("key-idem-1", TemplateAnswer, `{"text":"Something else.","owner_name":"Raya","mode":"summary"}`, threadOf(mention))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a key reused for another message = %v", err)
	}
	if got := len(h.fake.Calls("chat.postMessage")); got != 1 {
		t.Fatalf("posts = %d", got)
	}
}

func TestParallelRequestsWithOneKeyPostOnce(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Text: askText})

	const requests = 12
	var group sync.WaitGroup
	results := make([]error, requests)
	for i := range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			_, results[i] = h.post("key-parallel", TemplateAnswer, answerJSON, threadOf(mention))
		}()
	}
	group.Wait()
	succeeded := 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case !errors.Is(err, domain.ErrConflict):
			t.Fatalf("a parallel request answered %v", err)
		}
	}
	if succeeded == 0 {
		t.Fatal("none of the requests posted")
	}
	if got := len(h.fake.Calls("chat.postMessage")); got != 1 {
		t.Fatalf("posts = %d, want one however many asked at once", got)
	}
}

func TestSlackRateLimitingIsPassedOnWithRetryAfterAndTheKeyIsFree(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Text: askText})

	h.fake.RateLimit("chat.postMessage", 7*time.Second, 1)
	_, err := h.post("key-limited", TemplateAnswer, answerJSON, threadOf(mention))
	var limited *RateLimitedError
	if !errors.As(err, &limited) || !errors.Is(err, domain.ErrRateLimited) || limited.RetryAfterSeconds() != 7 {
		t.Fatalf("a call Slack rate limited = %v", err)
	}
	if len(h.fake.Messages()) != 0 {
		t.Fatal("a rate limited post was made")
	}
	// The key is free: the same request, tried again, goes through.
	if _, err := h.post("key-limited", TemplateAnswer, answerJSON, threadOf(mention)); err != nil {
		t.Fatalf("the retry = %v", err)
	}
	if len(h.fake.Messages()) != 1 {
		t.Fatalf("messages = %d", len(h.fake.Messages()))
	}

	// Slack also says it as a refusal of HTTP 200, without saying for how long.
	h.fake.Fail("chat.postMessage", "ratelimited", 1)
	_, err = h.post("key-limited-2", TemplateAnswer, answerJSON, threadOf(mention))
	if !errors.As(err, &limited) || limited.RetryAfterSeconds() != 1 {
		t.Fatalf("ratelimited as a refusal = %v", err)
	}
}

func TestAnOrganizationsPostsAreBudgetedAndTheKeyIsFreeWhenItIsOverBudget(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Text: askText})

	for i := range postBurst {
		if _, err := h.post(fmt.Sprintf("key-budget-%02d", i), TemplateStatus, `{"state":"working"}`, threadOf(mention)); err != nil {
			t.Fatalf("post %d inside the budget: %v", i, err)
		}
	}
	posted := len(h.fake.Calls("chat.postMessage"))
	_, err := h.post("key-budget-over", TemplateStatus, `{"state":"working"}`, threadOf(mention))
	var limited *RateLimitedError
	if !errors.As(err, &limited) || limited.Method != "" || limited.RetryAfterSeconds() < 1 || !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("a post over the budget = %v", err)
	}
	if len(h.fake.Calls("chat.postMessage")) != posted {
		t.Fatal("a post over the budget reached Slack")
	}

	// The budget refills, and the key that was refused is free.
	h.clock.Advance(time.Second)
	if _, err := h.post("key-budget-over", TemplateStatus, `{"state":"working"}`, threadOf(mention)); err != nil {
		t.Fatalf("after a second: %v", err)
	}

	// Another organization has its own.
	h.fake.AddWorkspace(slackfake.Workspace{TeamID: "T0SECOND1"})
	h.installTeam("org_2", "T0SECOND1")
	h.store.mu.Lock()
	h.store.inbound["asi_second"] = Inbound{
		ID: "asi_second", OrganizationID: "org_2", TeamID: "T0SECOND1", Kind: InboundMessage, ChannelID: "C0SECOND",
		UserID: "U0ASKER", MessageTS: "1780000800.000100", ThreadTS: "1780000800.000100", ReceivedAt: h.clock.Now(),
	}
	h.store.mu.Unlock()
	if _, err := h.service.Post(context.Background(), PostRequest{
		OrganizationID: "org_2", IdempotencyKey: "key-second-org", Template: TemplateStatus, Params: json.RawMessage(`{"state":"working"}`),
		Target: Target{ChannelID: "C0SECOND", ThreadTS: "1780000800.000100"},
	}); err != nil {
		t.Fatalf("another organization's post: %v", err)
	}
}

func TestATokenSlackRefusesEndsTheInstall(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Text: askText})

	h.fake.Fail("chat.postMessage", "token_revoked", 1)
	_, err := h.post("key-dead", TemplateAnswer, answerJSON, threadOf(mention))
	if !errors.Is(err, domain.ErrNotConnected) {
		t.Fatalf("a post with a revoked token = %v", err)
	}
	install := h.store.install(slackfake.DefaultTeamID)
	if install.Active() || install.EndReason != appsv1.SlackTokenInvalid {
		t.Fatalf("install = %+v", install)
	}
	events := h.store.publishedOf(appsv1.SlackInstallChangedDetailType)
	if len(events) != 1 || dataOf(t, events[0].Data)["reason"] != "token_invalid" {
		t.Fatalf("events = %+v", h.store.published())
	}
	calls := len(h.fake.Calls())
	if _, err := h.post("key-dead-2", TemplateAnswer, answerJSON, threadOf(mention)); !errors.Is(err, domain.ErrNotConnected) {
		t.Fatalf("a post after the end = %v", err)
	}
	if len(h.fake.Calls()) != calls {
		t.Fatal("an ended install called Slack")
	}
}

func TestSlackBeingUnreachableIsUnavailableAndTheKeyIsFree(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Text: askText})

	h.fake.Down(true)
	if _, err := h.post("key-down", TemplateAnswer, answerJSON, threadOf(mention)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("a post while Slack is down = %v", err)
	}
	h.fake.Down(false)
	if _, err := h.post("key-down", TemplateAnswer, answerJSON, threadOf(mention)); err != nil {
		t.Fatalf("the retry = %v", err)
	}

	// KMS not answering is unavailable too: nothing is wrong with the token.
	other := newHarness(t)
	other.installFor(testOrg)
	mention = other.seenThread(slackfake.Mention{Text: askText})
	other.keys.mu.Lock()
	other.keys.broken = true
	other.keys.mu.Unlock()
	if _, err := other.post("key-kms-down", TemplateAnswer, answerJSON, threadOf(mention)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("a post while KMS is down = %v", err)
	}
	if !other.store.install(slackfake.DefaultTeamID).Active() {
		t.Fatal("KMS being down ended the install")
	}
}

func TestNoInstallNoPost(t *testing.T) {
	h := newHarness(t)
	if _, err := h.post("key-none", TemplateStatus, `{"state":"held"}`, Target{ChannelID: "C0GENERAL", ThreadTS: "1780000000.000100"}); !errors.Is(err, domain.ErrNotConnected) {
		t.Fatalf("a post for an organization with no Slack = %v", err)
	}
	if _, err := h.service.Post(context.Background(), PostRequest{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an empty request = %v", err)
	}
}

func TestANoticeIsReplacedWhereItStandsAndOnlyWhenAppsPostedIt(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	h.seenThread(slackfake.Mention{Text: askText})

	notice, err := h.post("key-notice", TemplateOwnerNotice, noticeJSON, Target{UserID: "U0RAYA"})
	if err != nil {
		t.Fatal(err)
	}
	settled := `{"asker_user_id":"U0ASKER","question":"When is the launch?","decision":"approved_summary","decided_by_user_id":"U0RAYA","decided_at":"2026-09-29T12:02:00Z"}`
	result, err := h.service.Update(context.Background(), UpdateRequest{
		OrganizationID: testOrg, ChannelID: notice.ChannelID, TS: notice.TS, Template: TemplateNoticeSettled, Params: json.RawMessage(settled),
	})
	if err != nil || result.ChannelID != notice.ChannelID || result.TS != notice.TS {
		t.Fatalf("update = %+v, %v", result, err)
	}
	message, _ := h.fake.Message(notice.ChannelID, notice.TS)
	if message.Updates != 1 || len(message.Blocks) == 0 || strings.Contains(fmt.Sprint(message.Blocks), "consent_summary") || strings.Contains(fmt.Sprint(message.Blocks), `"actions"`) {
		t.Fatalf("the settled notice still has buttons: %+v", message)
	}
	if !strings.Contains(fmt.Sprint(message.Blocks), "Approved by <@U0RAYA>") {
		t.Fatalf("the settled notice does not say who approved: %v", message.Blocks)
	}

	// Only what apps posted, and not an ephemeral message.
	for name, request := range map[string]UpdateRequest{
		"a message apps never posted": {OrganizationID: testOrg, ChannelID: notice.ChannelID, TS: "1700000000.000009", Template: TemplateNoticeSettled, Params: json.RawMessage(settled)},
		"another organization's":      {OrganizationID: "org_other", ChannelID: notice.ChannelID, TS: notice.TS, Template: TemplateNoticeSettled, Params: json.RawMessage(settled)},
	} {
		if _, err := h.service.Update(context.Background(), request); err == nil {
			t.Errorf("%s was updated", name)
		}
	}
	mention := h.lastInbound()
	ephemeral, err := h.post("key-ephemeral", TemplateRefusal, `{"reason":"no_owner"}`, Target{ChannelID: mention.ChannelID, ThreadTS: mention.ThreadTS, UserID: "U0ASKER"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Update(context.Background(), UpdateRequest{
		OrganizationID: testOrg, ChannelID: ephemeral.ChannelID, TS: ephemeral.TS, Template: TemplateStatus, Params: json.RawMessage(`{"state":"held"}`),
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("updating an ephemeral message = %v", err)
	}
	// A DM's notice is replaced by a template that may be in a DM, and not by
	// one that may not.
	if _, err := h.service.Update(context.Background(), UpdateRequest{
		OrganizationID: testOrg, ChannelID: notice.ChannelID, TS: notice.TS, Template: TemplatePrivateNote, Params: json.RawMessage(`{}`),
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("replacing a direct message with a thread's note = %v", err)
	}
}

func TestProfilesAreCachedForADayAndFlagWhoIsNotAMember(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	h.fake.AddUser(slackfake.User{ID: "U0RAYA", Name: "raya", RealName: "Raya Rao", Email: "raya@acme.test", Title: "Engineer"})
	h.fake.AddUser(slackfake.User{ID: "U0GUEST", Guest: true, Email: "guest@elsewhere.test"})
	h.fake.AddUser(slackfake.User{ID: "U0ULTRA", UltraRestricted: true})
	h.fake.AddUser(slackfake.User{ID: "U0GONE", Deleted: true})
	h.fake.AddUser(slackfake.User{ID: "U0BOT", Bot: true})
	h.fake.AddUser(slackfake.User{ID: "U0AWAY", TeamID: "T0OTHER01", Email: "away@other.test"})
	h.fake.AddUser(slackfake.User{ID: "U0FLAGGED", Stranger: true})
	h.fake.AddUser(slackfake.User{ID: "U0NEW", Email: "new@acme.test", EmailUnconfirmed: true})
	ids := []string{"U0RAYA", "U0GUEST", "U0ULTRA", "U0GONE", "U0BOT", "U0AWAY", "U0FLAGGED", "U0NEW", "U0NOBODY", "U0RAYA"}

	view, err := h.service.Profiles(context.Background(), testOrg, ids)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ProfileView{}
	for _, profile := range view.Profiles {
		byID[profile.UserID] = profile
	}
	if len(view.Profiles) != 8 || len(view.Missing) != 1 || view.Missing[0] != "U0NOBODY" {
		t.Fatalf("profiles = %d, missing = %v", len(view.Profiles), view.Missing)
	}
	raya := byID["U0RAYA"]
	if raya.Email != "raya@acme.test" || !raya.EmailConfirmed || raya.Name != "Raya Rao" || raya.Title != "Engineer" ||
		raya.TeamID != slackfake.DefaultTeamID || raya.IsGuest || raya.IsBot || raya.IsDeleted || raya.IsStranger || !raya.FetchedAt.Equal(testStart) {
		t.Fatalf("raya = %+v", raya)
	}
	// An address Slack has not confirmed is said to be unconfirmed, and the
	// router does not match a person to a member by it.
	if fresh := byID["U0NEW"]; fresh.Email != "new@acme.test" || fresh.EmailConfirmed {
		t.Fatalf("a person with an unconfirmed address = %+v", fresh)
	}
	encoded, err := json.Marshal(raya)
	if err != nil || !strings.Contains(string(encoded), `"email_confirmed":true`) {
		t.Fatalf("a profile as it is answered = %s, %v", encoded, err)
	}
	if !byID["U0GUEST"].IsGuest || !byID["U0ULTRA"].IsGuest || !byID["U0GONE"].IsDeleted || !byID["U0BOT"].IsBot ||
		!byID["U0AWAY"].IsStranger || !byID["U0FLAGGED"].IsStranger {
		t.Fatalf("flags = %+v", byID)
	}

	// A day's cache: the people Slack knew are not asked about again.
	asked := len(h.fake.Calls("users.info"))
	again, err := h.service.Profiles(context.Background(), testOrg, []string{"U0RAYA", "U0NEW"})
	if err != nil || len(again.Profiles) != 2 || !again.Profiles[0].EmailConfirmed || again.Profiles[1].EmailConfirmed {
		t.Fatalf("again = %+v, %v: the cache must keep whether the address is confirmed", again, err)
	}
	if got := len(h.fake.Calls("users.info")); got != asked {
		t.Fatalf("users.info was called %d more times for cached people", got-asked)
	}
	h.clock.Advance(25 * time.Hour)
	if _, err := h.service.Profiles(context.Background(), testOrg, []string{"U0RAYA"}); err != nil {
		t.Fatal(err)
	}
	if got := len(h.fake.Calls("users.info")); got != asked+1 {
		t.Fatalf("users.info calls = %d, want the stale profile asked again", got)
	}

	for name, users := range map[string][]string{"none": nil, "a bad id": {"raya"}, "too many": make([]string, 0)} {
		if name == "too many" {
			for i := range maxProfiles + 1 {
				users = append(users, fmt.Sprintf("U0USER%03d", i))
			}
		}
		if _, err := h.service.Profiles(context.Background(), testOrg, users); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
}

func TestAChannelIsReadFreshEveryTimeAndAGroupMessageIsUnreadable(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	h.fake.AddChannel(slackfake.Channel{ID: "G0SECRET", Name: "secret", Private: true})
	h.fake.AddChannel(slackfake.Channel{ID: "G0GROUP", MPIM: true, Private: true})
	h.store.marks[testOrg+"/"+slackfake.DefaultTeamID+"/G0SECRET"] = MarkedChannel{ChannelID: "G0SECRET", Name: "secret"}

	channel, err := h.service.Channel(context.Background(), testOrg, "G0SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if !channel.IsPrivate || channel.IsShared || channel.IsMPIM || channel.IsIM || !channel.HighPriority || channel.Name != "secret" {
		t.Fatalf("channel = %+v", channel)
	}
	// Someone shares it with another organization: the next look sees that.
	h.fake.AddChannel(slackfake.Channel{ID: "G0SECRET", Name: "secret", Private: true, Shared: true})
	channel, err = h.service.Channel(context.Background(), testOrg, "G0SECRET")
	if err != nil || !channel.IsShared || !channel.HighPriority {
		t.Fatalf("channel = %+v, %v", channel, err)
	}
	if got := len(h.fake.Calls("conversations.info")); got != 2 {
		t.Fatalf("conversations.info calls = %d, want one each time", got)
	}

	if _, err := h.service.Channel(context.Background(), testOrg, "G0GROUP"); !errors.Is(err, ErrChannelUnreadable) {
		t.Fatalf("a group message = %v, want ErrChannelUnreadable", err)
	}
	if _, err := h.service.Channel(context.Background(), testOrg, "C0NOWHERE"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a channel that is not there = %v", err)
	}
	if _, err := h.service.Channel(context.Background(), testOrg, "general"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a channel that is no id = %v", err)
	}
	if _, err := h.service.Channel(context.Background(), "org_without", "G0SECRET"); !errors.Is(err, domain.ErrNotConnected) {
		t.Fatalf("an organization with no Slack = %v", err)
	}
}

func TestWhatPeopleTypedIsForgottenAfterSevenDaysAndTheThreadWithIt(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	mention := h.seenThread(slackfake.Mention{Text: askText})

	h.clock.Advance(InboundRetention - time.Hour)
	h.service.maintain(context.Background())
	if _, err := h.service.Inbound(context.Background(), mention.ID); err != nil {
		t.Fatalf("a record inside its week = %v", err)
	}
	h.clock.Advance(2 * time.Hour)
	h.service.maintain(context.Background())
	if _, err := h.service.Inbound(context.Background(), mention.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a record past its week = %v, want it forgotten", err)
	}
	if _, err := h.post("key-late", TemplateStatus, `{"state":"held"}`, threadOf(mention)); !errors.Is(err, ErrThreadUnseen) {
		t.Fatalf("a post into a thread whose record is gone = %v", err)
	}
}

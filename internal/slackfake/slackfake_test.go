package slackfake

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// call makes a Web API call the way Oryxa's client does: JSON, with the bot's
// token as the bearer.
func call(t *testing.T, fake *Fake, method, token string, args map[string]any) (int, http.Header, map[string]any) {
	t.Helper()
	body, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, fake.URL()+"/"+method, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	content, _ := io.ReadAll(response.Body)
	_ = json.Unmarshal(content, &decoded)
	return response.StatusCode, response.Header, decoded
}

// Slack's documentation gives one signed request as its example. The fake
// signs that one as Slack does, so what it sends the ingress is what Slack
// would.
func TestSignatureIsSlacksOwnDocumentedExample(t *testing.T) {
	body := "token=xyzz0WbapA4vBCDEFasx0q6G&team_id=T1DC2JH3J&team_domain=testteamnow&channel_id=G8PSS9T3V&channel_name=foobar&" +
		"user_id=U2CERLKJA&user_name=roadrunner&command=%2Fwebhook-collect&text=&" +
		"response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2FT1DC2JH3J%2F397700885554%2F96rGlfmibIGlgcZRskXaIFfN&" +
		"trigger_id=398738663015.47445629121.803a0bc887a14d10d2c447fce8b6703c"
	got := Signature("8f742231b10e8888abcd99yyyzzz85a5", "1531420618", []byte(body))
	if want := "v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503"; got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}

func TestInstallPostAndUpdateAreRecordedAndAnswered(t *testing.T) {
	fake := New(t)

	// Installing: a code, exchanged once, with the client's credentials.
	code := fake.IssueCode(DefaultTeamID)
	form := url.Values{"code": {code}, "redirect_uri": {"https://api.oryxa.test/v1/apps/slack/install/callback"}}
	request, _ := http.NewRequest(http.MethodPost, fake.URL()+"/oauth.v2.access", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(fake.ClientID(), fake.ClientSecret())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var access map[string]any
	_ = json.NewDecoder(response.Body).Decode(&access)
	response.Body.Close()
	if access["ok"] != true || access["access_token"] != DefaultBotToken || access["bot_user_id"] != DefaultBotUserID {
		t.Fatalf("oauth.v2.access = %v", access)
	}
	if again := fake.Calls("oauth.v2.access"); len(again) != 1 || again[0].BasicUser != fake.ClientID() || again[0].Args["code"] != code {
		t.Fatalf("recorded = %+v", again)
	}
	request, _ = http.NewRequest(http.MethodPost, fake.URL()+"/oauth.v2.access", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(fake.ClientID(), fake.ClientSecret())
	response, _ = http.DefaultClient.Do(request)
	var reused map[string]any
	_ = json.NewDecoder(response.Body).Decode(&reused)
	response.Body.Close()
	if reused["error"] != "code_already_used" {
		t.Fatalf("a code used twice = %v", reused)
	}

	// Posting, then replacing what was posted.
	_, _, posted := call(t, fake, "chat.postMessage", DefaultBotToken, map[string]any{
		"channel": "C0GENERAL", "thread_ts": "1.000001", "text": "hello", "blocks": []any{map[string]any{"type": "section"}},
		"unfurl_links": false, "unfurl_media": false,
	})
	ts, _ := posted["ts"].(string)
	if posted["ok"] != true || ts == "" {
		t.Fatalf("chat.postMessage = %v", posted)
	}
	message, ok := fake.Message("C0GENERAL", ts)
	if !ok || message.Text != "hello" || message.ThreadTS != "1.000001" || message.UnfurlLinks || message.UnfurlMedia || len(message.Blocks) != 1 {
		t.Fatalf("message = %+v", message)
	}
	_, _, updated := call(t, fake, "chat.update", DefaultBotToken, map[string]any{"channel": "C0GENERAL", "ts": ts, "text": "settled"})
	message, _ = fake.Message("C0GENERAL", ts)
	if updated["ok"] != true || message.Text != "settled" || message.Updates != 1 || message.Blocks != nil {
		t.Fatalf("update = %v, message = %+v (blocks are removed when an update sends none, as in Slack)", updated, message)
	}
	_, _, missing := call(t, fake, "chat.update", DefaultBotToken, map[string]any{"channel": "C0GENERAL", "ts": "9.999999", "text": "x"})
	if missing["error"] != "message_not_found" {
		t.Fatalf("update of a message never posted = %v", missing)
	}

	// An ephemeral message cannot be updated.
	_, _, ephemeral := call(t, fake, "chat.postEphemeral", DefaultBotToken, map[string]any{"channel": "C0GENERAL", "user": "U0ASKER", "text": "psst"})
	if _, _, refused := call(t, fake, "chat.update", DefaultBotToken, map[string]any{
		"channel": "C0GENERAL", "ts": ephemeral["message_ts"], "text": "x",
	}); refused["error"] != "cant_update_message" {
		t.Fatalf("updating an ephemeral message = %v", refused)
	}

	// A call must present a token the fake issued.
	if _, _, unauthorized := call(t, fake, "auth.test", "xoxb-someone-elses", nil); unauthorized["error"] != "invalid_auth" {
		t.Fatalf("a stranger's token = %v", unauthorized)
	}
	if _, _, none := call(t, fake, "auth.test", "", nil); none["error"] != "not_authed" {
		t.Fatalf("no token = %v", none)
	}
	fake.RevokeToken(DefaultTeamID)
	if _, _, revoked := call(t, fake, "auth.test", DefaultBotToken, nil); revoked["error"] != "token_revoked" {
		t.Fatalf("a revoked token = %v", revoked)
	}
}

func TestFailuresAndRateLimitsAreInjectedForTheCallsAsked(t *testing.T) {
	fake := New(t)

	fake.Fail("chat.postMessage", "channel_not_found", 1)
	if _, _, answer := call(t, fake, "chat.postMessage", DefaultBotToken, map[string]any{"channel": "C1", "text": "x"}); answer["error"] != "channel_not_found" {
		t.Fatalf("injected = %v", answer)
	}
	if _, _, answer := call(t, fake, "chat.postMessage", DefaultBotToken, map[string]any{"channel": "C1", "text": "x"}); answer["ok"] != true {
		t.Fatalf("after its one time = %v", answer)
	}

	fake.RateLimit("chat.postMessage", 7*time.Second, 2)
	for range 2 {
		status, header, answer := call(t, fake, "chat.postMessage", DefaultBotToken, map[string]any{"channel": "C1", "text": "x"})
		if status != http.StatusTooManyRequests || header.Get("Retry-After") != "7" || answer["error"] != "ratelimited" {
			t.Fatalf("rate limited = %d %q %v", status, header.Get("Retry-After"), answer)
		}
	}
	if status, _, _ := call(t, fake, "chat.postMessage", DefaultBotToken, map[string]any{"channel": "C1", "text": "x"}); status != http.StatusOK {
		t.Fatalf("after the limit lifted = %d", status)
	}

	fake.Down(true)
	if status, _, _ := call(t, fake, "auth.test", DefaultBotToken, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("down = %d", status)
	}
	fake.Reset()
	if status, _, _ := call(t, fake, "auth.test", DefaultBotToken, nil); status != http.StatusOK || len(fake.Calls()) != 1 {
		t.Fatalf("after Reset = %d, calls = %d", status, len(fake.Calls()))
	}
}

func TestUsersAndChannelsAreAnsweredAsSlackAnswersThem(t *testing.T) {
	fake := New(t)
	fake.AddUser(User{ID: "U0RAYA", Name: "raya", Email: "raya@acme.test", Title: "Engineer"})
	fake.AddUser(User{ID: "U0GUEST", Guest: true, Email: "guest@elsewhere.test"})
	fake.AddChannel(Channel{ID: "G0SECRET", Name: "secret", Private: true})
	fake.AddChannel(Channel{ID: "G0GROUP", MPIM: true, Private: true})
	fake.AddChannel(Channel{ID: "C0CONNECT", Name: "connect", Shared: true})

	_, _, info := call(t, fake, "users.info", DefaultBotToken, map[string]any{"user": "U0RAYA"})
	user, _ := info["user"].(map[string]any)
	profile, _ := user["profile"].(map[string]any)
	if profile["email"] != "raya@acme.test" || profile["title"] != "Engineer" || user["team_id"] != DefaultTeamID {
		t.Fatalf("users.info = %v", info)
	}
	if _, _, unknown := call(t, fake, "users.info", DefaultBotToken, map[string]any{"user": "U0NOBODY"}); unknown["error"] != "user_not_found" {
		t.Fatalf("an unknown user = %v", unknown)
	}

	_, _, secret := call(t, fake, "conversations.info", DefaultBotToken, map[string]any{"channel": "G0SECRET"})
	if channel, _ := secret["channel"].(map[string]any); channel["is_private"] != true || channel["is_shared"] != false || channel["is_mpim"] != false {
		t.Fatalf("a private channel = %v", secret)
	}
	_, _, connect := call(t, fake, "conversations.info", DefaultBotToken, map[string]any{"channel": "C0CONNECT"})
	if channel, _ := connect["channel"].(map[string]any); channel["is_shared"] != true || channel["is_private"] != false {
		t.Fatalf("a shared channel = %v", connect)
	}
	// A group DM needs a read scope the bot does not have: Slack says so.
	if _, _, group := call(t, fake, "conversations.info", DefaultBotToken, map[string]any{"channel": "G0GROUP"}); group["error"] != "missing_scope" {
		t.Fatalf("a group DM = %v", group)
	}

	_, _, list := call(t, fake, "conversations.list", DefaultBotToken, map[string]any{"types": "private_channel"})
	if channels, _ := list["channels"].([]any); len(channels) != 1 {
		t.Fatalf("conversations.list = %v", list)
	}
	_, _, opened := call(t, fake, "conversations.open", DefaultBotToken, map[string]any{"users": "U0RAYA"})
	if channel, _ := opened["channel"].(map[string]any); channel["id"] != DMChannel("U0RAYA") || DMChannel("U0RAYA") != "D0RAYA" {
		t.Fatalf("conversations.open = %v", opened)
	}
}

func TestRequestsAreSignedAsSlackSignsThem(t *testing.T) {
	now := time.Date(2026, time.September, 29, 9, 0, 0, 0, time.UTC)
	fake := New(t, WithClock(func() time.Time { return now }))

	mention := fake.Mention("https://api.oryxa.test", Mention{Text: "<@U0FAKEBOT> hello", EventID: "Ev1", ThreadTS: "1.1"})
	body := requestBody(mention)
	if mention.URL.String() != "https://api.oryxa.test"+EventsPath || mention.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("mention = %s %s", mention.URL, mention.Header.Get("Content-Type"))
	}
	if stamp := mention.Header.Get("X-Slack-Request-Timestamp"); stamp != strconv.FormatInt(now.Unix(), 10) ||
		mention.Header.Get("X-Slack-Signature") != Signature(fake.SigningSecret(), stamp, body) {
		t.Fatalf("headers = %v", mention.Header)
	}
	var envelope struct {
		Type    string `json:"type"`
		TeamID  string `json:"team_id"`
		EventID string `json:"event_id"`
		Event   struct {
			Type     string `json:"type"`
			User     string `json:"user"`
			Channel  string `json:"channel"`
			Text     string `json:"text"`
			ThreadTS string `json:"thread_ts"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Type != "event_callback" || envelope.TeamID != DefaultTeamID || envelope.EventID != "Ev1" ||
		envelope.Event.Type != "app_mention" || envelope.Event.User != "U0ASKER" || envelope.Event.Channel != "C0GENERAL" ||
		envelope.Event.Text != "<@U0FAKEBOT> hello" || envelope.Event.ThreadTS != "1.1" {
		t.Fatalf("envelope = %+v", envelope)
	}

	click := fake.Click("", Click{User: "U0RAYA", ActionID: "consent_deny", Value: "t1.opaque"})
	form, err := url.ParseQuery(string(requestBody(click)))
	if err != nil || click.URL.Path != InteractivityPath || click.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("click = %s %s %v", click.URL, click.Header.Get("Content-Type"), err)
	}
	var payload struct {
		Type string `json:"type"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Container struct {
			ChannelID string `json:"channel_id"`
		} `json:"container"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(form.Get("payload")), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Type != "block_actions" || payload.User.ID != "U0RAYA" || payload.Container.ChannelID != "D0RAYA" ||
		len(payload.Actions) != 1 || payload.Actions[0].ActionID != "consent_deny" || payload.Actions[0].Value != "t1.opaque" {
		t.Fatalf("payload = %+v", payload)
	}
	// Two clicks are two deliveries: their bodies differ.
	if other := fake.Click("", Click{User: "U0RAYA", ActionID: "consent_deny", Value: "t1.opaque"}); bytes.Equal(requestBody(other), requestBody(click)) {
		t.Fatal("two clicks made the same body")
	}

	// A stale request is signed for its own, older, time.
	stale := fake.Mention("", Mention{Text: "x"})
	fake.Sign(stale, now.Add(-10*time.Minute))
	stamp := stale.Header.Get("X-Slack-Request-Timestamp")
	if stamp != strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10) ||
		stale.Header.Get("X-Slack-Signature") != Signature(fake.SigningSecret(), stamp, requestBody(stale)) {
		t.Fatalf("stale headers = %v", stale.Header)
	}

	// The lifecycle events.
	uninstalled := requestBody(fake.AppUninstalled("", DefaultTeamID))
	revoked := requestBody(fake.TokensRevoked("", DefaultTeamID, DefaultBotUserID))
	if !strings.Contains(string(uninstalled), `"app_uninstalled"`) || !strings.Contains(string(revoked), `"bot":["`+DefaultBotUserID+`"]`) {
		t.Fatalf("uninstalled = %s, revoked = %s", uninstalled, revoked)
	}
	if verification := requestBody(fake.URLVerification("", "3eZbrw1a")); !strings.Contains(string(verification), `"challenge":"3eZbrw1a"`) {
		t.Fatalf("verification = %s", verification)
	}
}

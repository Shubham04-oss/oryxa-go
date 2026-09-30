package slackfake

import (
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a time a test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

// modal is a modal that keeps Slack's limits, carrying metadata.
func modal(metadata string) map[string]any {
	return map[string]any{
		"type": "modal", "callback_id": "oryxa_reply", "private_metadata": metadata,
		"title": map[string]any{"type": "plain_text", "text": "Reply yourself"}, "submit": map[string]any{"type": "plain_text", "text": "Send"},
		"blocks": []any{map[string]any{
			"type": "input", "block_id": "reply", "label": map[string]any{"type": "plain_text", "text": "Your reply"},
			"element": map[string]any{"type": "plain_text_input", "action_id": "text", "multiline": true, "max_length": 3000},
		}},
	}
}

// triggerOf is the trigger_id of a click the fake built.
func triggerOf(t *testing.T, fake *Fake, c Click) string {
	t.Helper()
	request := fake.Click("", c)
	form, err := url.ParseQuery(string(requestBody(request)))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		TriggerID string `json:"trigger_id"`
	}
	if err := json.Unmarshal([]byte(form.Get("payload")), &payload); err != nil || payload.TriggerID == "" {
		t.Fatalf("the click carries no trigger: %v", err)
	}
	return payload.TriggerID
}

// TestAClicksTriggerOpensOneModalWhileItIsFresh: views.open takes a trigger the
// fake issued with a click, once and within three seconds, for a view that
// keeps Slack's limits, and needs no scope; the modal is kept, and a
// submission of it carries back its callback id, its metadata and what was
// typed.
func TestAClicksTriggerOpensOneModalWhileItIsFresh(t *testing.T) {
	now := &clock{now: time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)}
	fake := New(t, WithClock(now.Now))
	trigger := triggerOf(t, fake, Click{User: "U0RAYA", ActionID: "reply", Value: "t1.reply.sig"})

	_, _, opened := call(t, fake, "views.open", DefaultBotToken, map[string]any{"trigger_id": trigger, "view": modal(`{"t":"t1.reply.sig"}`)})
	view, _ := opened["view"].(map[string]any)
	if opened["ok"] != true || view["id"] == "" || view["callback_id"] != "oryxa_reply" {
		t.Fatalf("views.open = %v", opened)
	}
	if views := fake.Views(); len(views) != 1 || views[0].User != "U0RAYA" || views[0].TriggerID != trigger || views[0].TeamID != DefaultTeamID {
		t.Fatalf("views = %+v", views)
	}
	// Once.
	if _, _, again := call(t, fake, "views.open", DefaultBotToken, map[string]any{"trigger_id": trigger, "view": modal("")}); again["error"] != "exchanged_trigger_id" {
		t.Fatalf("a trigger used twice = %v", again)
	}
	// Three seconds, and no more.
	late := triggerOf(t, fake, Click{User: "U0RAYA", ActionID: "reply", Value: "t1.reply.sig"})
	now.advance(3*time.Second + time.Millisecond)
	if _, _, expired := call(t, fake, "views.open", DefaultBotToken, map[string]any{"trigger_id": late, "view": modal("")}); expired["error"] != "expired_trigger_id" {
		t.Fatalf("a stale trigger = %v", expired)
	}
	// A trigger it never issued, and a view outside Slack's limits.
	if _, _, unknown := call(t, fake, "views.open", DefaultBotToken, map[string]any{"trigger_id": "123.456.abc", "view": modal("")}); unknown["error"] != "invalid_trigger_id" {
		t.Fatalf("an unknown trigger = %v", unknown)
	}
	for name, change := range map[string]func(map[string]any){
		"a long title": func(v map[string]any) {
			v["title"] = map[string]any{"type": "plain_text", "text": strings.Repeat("t", 25)}
		},
		"long metadata":   func(v map[string]any) { v["private_metadata"] = strings.Repeat("m", 3001) },
		"no blocks":       func(v map[string]any) { v["blocks"] = []any{} },
		"a box too large": func(v map[string]any) { modalInput(v)["max_length"] = 4000 },
		"not a modal":     func(v map[string]any) { v["type"] = "home" },
	} {
		view := modal("")
		change(view)
		fresh := triggerOf(t, fake, Click{User: "U0RAYA", ActionID: "reply", Value: "t1.reply.sig"})
		if _, _, refused := call(t, fake, "views.open", DefaultBotToken, map[string]any{"trigger_id": fresh, "view": view}); refused["error"] != "invalid_arguments" {
			t.Errorf("%s = %v, want invalid_arguments", name, refused)
		}
	}

	request := fake.Submit("", Submission{Values: map[string]map[string]string{"reply": {"text": "We ship on the 14th."}}})
	form, err := url.ParseQuery(string(requestBody(request)))
	if err != nil {
		t.Fatal(err)
	}
	var submitted struct {
		Type string `json:"type"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		View struct {
			CallbackID      string `json:"callback_id"`
			PrivateMetadata string `json:"private_metadata"`
			State           struct {
				Values map[string]map[string]struct {
					Value string `json:"value"`
				} `json:"values"`
			} `json:"state"`
		} `json:"view"`
	}
	if err := json.Unmarshal([]byte(form.Get("payload")), &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.Type != "view_submission" || submitted.User.ID != "U0RAYA" || submitted.View.CallbackID != "oryxa_reply" ||
		submitted.View.PrivateMetadata != `{"t":"t1.reply.sig"}` || submitted.View.State.Values["reply"]["text"].Value != "We ship on the 14th." {
		t.Fatalf("submission = %+v", submitted)
	}
	if request.Header.Get("X-Slack-Signature") == "" || !strings.HasSuffix(request.URL.Path, InteractivityPath) {
		t.Fatalf("the submission is not a signed delivery to the interactivity URL: %v %v", request.URL, request.Header)
	}
}

func modalInput(view map[string]any) map[string]any {
	blocks := view["blocks"].([]any)
	return blocks[0].(map[string]any)["element"].(map[string]any)
}

// TestAClickCarriesTheMessageItWasOn: for a message the bot posted, the click
// carries its blocks, with the bot as its user, as Slack does.
func TestAClickCarriesTheMessageItWasOn(t *testing.T) {
	fake := New(t)
	blocks := []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "<@U0ASKER> is asking you through Oryxa:\n> When?"}}}
	_, _, posted := call(t, fake, "chat.postMessage", DefaultBotToken, map[string]any{"channel": "D0RAYA", "text": "notice", "blocks": blocks})
	ts, _ := posted["ts"].(string)
	request := fake.Click("", Click{User: "U0RAYA", Channel: "D0RAYA", MessageTS: ts, ActionID: "reply", Value: "t1.reply.sig"})
	form, _ := url.ParseQuery(string(requestBody(request)))
	var payload struct {
		Message struct {
			User   string `json:"user"`
			Blocks []struct {
				Text struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"blocks"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(form.Get("payload")), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Message.User != DefaultBotUserID || len(payload.Message.Blocks) != 1 || !strings.Contains(payload.Message.Blocks[0].Text.Text, "is asking you") {
		t.Fatalf("the click's message = %+v", payload.Message)
	}
}

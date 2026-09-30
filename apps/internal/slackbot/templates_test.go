package slackbot

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/templates from what the templates render now")

// renderedJSON is a rendering as its golden holds it: indented, with Slack's
// markup readable rather than escaped for HTML.
func renderedJSON(t *testing.T, rendered Rendered) string {
	t.Helper()
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(struct {
		Text   string `json:"text"`
		Blocks []any  `json:"blocks"`
	}{rendered.Text, rendered.Blocks}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func (h *harness) renderOK(template, params string) Rendered {
	h.t.Helper()
	rendered, _, err := h.service.render(template, json.RawMessage(params))
	if err != nil {
		h.t.Fatalf("render(%s, %s) = %v", template, params, err)
	}
	return rendered
}

const (
	tickets = `"tickets":{"summary":"t1.summary.sig","access":"t1.access.sig","deny":"t1.deny.sig"}`
	decided = `"decided_by_user_id":"U0RAYA","decided_at":"2026-09-29T14:02:00Z"`
)

// The wording and the structure of every template, pinned. A change to what
// people read in Slack shows in a golden's diff.
var templateGoldens = []struct{ name, template, params string }{
	{"owner_notice", TemplateOwnerNotice, `{"asker_user_id":"U0ASKER","question":"When is the launch?\nAnd who signs it off?",` + tickets + `}`},
	{"owner_notice_asker_by_name", TemplateOwnerNotice, `{"asker_name":"Alex Kim","question":"Can I see the dev keys?",` + tickets + `}`},
	{"notice_settled_summary", TemplateNoticeSettled, `{"asker_user_id":"U0ASKER","question":"When is the launch?","decision":"approved_summary",` + decided + `}`},
	{"notice_settled_access", TemplateNoticeSettled, `{"asker_user_id":"U0ASKER","question":"When is the launch?","decision":"approved_access",` + decided + `}`},
	{"notice_settled_denied", TemplateNoticeSettled, `{"asker_user_id":"U0ASKER","question":"When is the launch?","decision":"denied",` + decided + `}`},
	{"notice_settled_answered_by_other", TemplateNoticeSettled, `{"asker_user_id":"U0ASKER","question":"When is the launch?","decision":"answered_by_other","decided_by_name":"Maya"}`},
	{"notice_settled_expired", TemplateNoticeSettled, `{"asker_user_id":"U0ASKER","question":"When is the launch?","decision":"expired"}`},
	{"ask_ack", TemplateAskAck, `{"owners":[{"user_id":"U0RAYA"},{"name":"Maya"}],"open_url":"` + testWebURL + `/?org=org_1&view=requests&request=req_1"}`},
	{"ask_ack_without_link", TemplateAskAck, `{"owners":[{"user_id":"U0RAYA"}]}`},
	{"answer_summary", TemplateAnswer, `{"text":"The launch is on the 14th.","owner_name":"Raya","mode":"summary"}`},
	{"answer_access", TemplateAnswer, `{"text":"The launch is on the 14th, per the plan: \"ship Tuesday\".","owner_name":"Raya","mode":"access"}`},
	{"answer_lease", TemplateAnswer, `{"text":"Still the 14th.","owner_name":"Raya","mode":"lease"}`},
	{"answer_bypass", TemplateAnswer, `{"text":"The launch is on the 14th.","owner_name":"Raya","mode":"bypass"}`},
	{"status_waiting", TemplateStatus, `{"state":"waiting","owner_name":"Raya"}`},
	{"status_working", TemplateStatus, `{"state":"working","owner_name":"Raya"}`},
	{"status_held", TemplateStatus, `{"state":"held","owner_name":"Raya"}`},
	{"status_denied", TemplateStatus, `{"state":"denied","owner_name":"Raya"}`},
	{"status_expired", TemplateStatus, `{"state":"expired"}`},
	{"status_answered_elsewhere", TemplateStatus, `{"state":"answered_elsewhere"}`},
	{"status_failed", TemplateStatus, `{"state":"failed"}`},
	{"refusal_not_member", TemplateRefusal, `{"reason":"not_member","organization_name":"Acme"}`},
	{"refusal_owner_not_member", TemplateRefusal, `{"reason":"owner_not_member","user_id":"U0MAYA"}`},
	{"refusal_group_dm", TemplateRefusal, `{"reason":"group_dm"}`},
	{"refusal_external_channel", TemplateRefusal, `{"reason":"external_channel"}`},
	{"refusal_no_owner", TemplateRefusal, `{"reason":"no_owner"}`},
	{"refusal_self_mention", TemplateRefusal, `{"reason":"no_owner","hint":"self"}`},
	{"refusal_too_many_asks", TemplateRefusal, `{"reason":"too_many_asks"}`},
	{"refusal_too_many_people", TemplateRefusal, `{"reason":"too_many_asks","hint":"too_many_people"}`},
	{"refusal_screen_failed", TemplateRefusal, `{"reason":"screen_failed"}`},
	{"refusal_not_work", TemplateRefusal, `{"reason":"not_work"}`},
	{"refusal_injection", TemplateRefusal, `{"reason":"injection"}`},
	{"refusal_implausible", TemplateRefusal, `{"reason":"implausible"}`},
	{"refusal_no_grant", TemplateRefusal, `{"reason":"no_grant"}`},
	{"refusal_owner_department", TemplateRefusal, `{"reason":"owner_department","user_id":"U0MAYA"}`},
	{"refusal_owner_department_by_name", TemplateRefusal, `{"reason":"owner_department","name":"Maya"}`},
	{"refusal_unknown_reason", TemplateRefusal, `{"reason":"something_newer"}`},
	{"private_note", TemplatePrivateNote, `{}`},
	{"bypass_notice_priority_channel", TemplateBypassNotice, `{"asker_user_id":"U0ASKER","question":"Where are the dev keys?","reason":"priority_channel","channel_id":"G0SECRET"}`},
	{"bypass_notice_org_chart", TemplateBypassNotice, `{"asker_name":"Dana","question":"Where are the dev keys?","reason":"org_chart"}`},
}

func TestEveryTemplateRendersAsPinned(t *testing.T) {
	h := newHarness(t)
	seen := map[string]bool{}
	for _, test := range templateGoldens {
		seen[test.template] = true
		t.Run(test.name, func(t *testing.T) {
			got := renderedJSON(t, h.renderOK(test.template, test.params))
			path := filepath.Join("testdata", "templates", test.name+".json")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no golden for %s (run go test -update): %v", test.name, err)
			}
			if got != string(want) {
				t.Errorf("%s changed\n got  %s\n want %s", test.name, got, want)
			}
		})
	}
	// Every template is pinned.
	for name := range templates {
		if !seen[name] {
			t.Errorf("template %s has no golden", name)
		}
	}
}

func TestTheOwnerNoticeHasOnlyTheAskerTheQuestionAndThreeButtons(t *testing.T) {
	h := newHarness(t)
	rendered := h.renderOK(TemplateOwnerNotice,
		`{"asker_user_id":"U0ASKER","question":"When is the launch?","tickets":{"summary":"t1.a.b","access":"t1.c.d","deny":"t1.e.f"}}`)

	var blocks []map[string]any
	raw, _ := json.Marshal(rendered.Blocks)
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0]["type"] != "section" || blocks[1]["type"] != "actions" {
		t.Fatalf("blocks = %s", raw)
	}
	text := blocks[0]["text"].(map[string]any)["text"].(string)
	if text != "<@U0ASKER> is asking you through Oryxa:\n> When is the launch?" {
		t.Fatalf("the notice says %q", text)
	}
	buttons := blocks[1]["elements"].([]any)
	if len(buttons) != 3 {
		t.Fatalf("buttons = %d", len(buttons))
	}
	for i, want := range []struct{ label, action, value string }{
		{"Share answer", ActionSummary, "t1.a.b"},
		{"Allow for 1 hour", ActionAccess, "t1.c.d"},
		{"Deny", ActionDeny, "t1.e.f"},
	} {
		button := buttons[i].(map[string]any)
		label := button["text"].(map[string]any)["text"]
		if button["type"] != "button" || label != want.label || button["action_id"] != want.action || button["value"] != want.value {
			t.Errorf("button %d = %v", i, button)
		}
		// No style nudges the owner towards the wider choice.
		if _, styled := button["style"]; styled {
			t.Errorf("button %d is styled", i)
		}
	}
	// Nothing else: no links, no images, no fields, no footer.
	whole := string(raw)
	for _, forbidden := range []string{"http", `"image"`, `"context"`, `"fields"`, `"accessory"`, `"url"`} {
		if strings.Contains(whole, forbidden) {
			t.Errorf("the notice holds %s: %s", forbidden, whole)
		}
	}
	// A notification says who, and not what.
	if strings.Contains(rendered.Text, "launch") || !strings.Contains(rendered.Text, "<@U0ASKER>") {
		t.Fatalf("text = %q", rendered.Text)
	}
}

func TestWhatACallerSuppliesCannotBecomeMarkup(t *testing.T) {
	h := newHarness(t)
	// The entity comes first, so it survives the cut a name is given.
	hostile := "&lt;b&gt; <!channel> <!here> <@U0EVIL> <#C0SECRET> <https://evil.example|Open in Oryxa> <mailto:x@evil.example> <!everyone>"

	for name, test := range map[string]struct {
		template, params string
	}{
		"a question":       {TemplateOwnerNotice, `{"asker_name":"` + hostile + `","question":"` + hostile + `",` + tickets + `}`},
		"a settled notice": {TemplateNoticeSettled, `{"asker_name":"x","question":"` + hostile + `","decision":"answered_by_other","decided_by_name":"` + hostile + `"}`},
		"an answer":        {TemplateAnswer, `{"text":"` + hostile + `","owner_name":"` + hostile + `","mode":"access"}`},
		"a status":         {TemplateStatus, `{"state":"held","owner_name":"` + hostile + `"}`},
		"a refusal":        {TemplateRefusal, `{"reason":"owner_not_member","name":"` + hostile + `"}`},
		"an ack":           {TemplateAskAck, `{"owners":[{"name":"` + hostile + `"}]}`},
		"a bypass notice":  {TemplateBypassNotice, `{"asker_name":"` + hostile + `","question":"` + hostile + `","reason":"org_chart"}`},
	} {
		got := renderedJSON(t, h.renderOK(test.template, test.params))
		for _, raw := range []string{"<!channel>", "<!here>", "<!everyone>", "<@U0EVIL>", "<#C0SECRET>", "<https://evil", "<mailto:"} {
			if strings.Contains(got, raw) {
				t.Errorf("%s: %s reached Slack as markup:\n%s", name, raw, got)
			}
		}
		if !strings.Contains(got, "&lt;!channel&gt;") {
			t.Errorf("%s: the text is not shown, escaped:\n%s", name, got)
		}
		// Even an entity a person typed is shown as they typed it.
		if !strings.Contains(got, "&amp;lt;b&amp;gt;") {
			t.Errorf("%s: an entity was left to be read as markup:\n%s", name, got)
		}
	}
	// The only mention the notice makes is the asker's, which it made itself:
	// once in the message and once in what a notification says.
	notice := renderedJSON(t, h.renderOK(TemplateOwnerNotice, `{"asker_user_id":"U0ASKER","question":"`+hostile+`",`+tickets+`}`))
	if strings.Count(notice, "<@") != 2 || strings.Count(notice, "<@U0ASKER>") != 2 {
		t.Fatalf("the notice mentions people the template did not:\n%s", notice)
	}
}

func TestNoTemplateEverRendersASecret(t *testing.T) {
	h := newHarness(t)
	leaks := []string{
		"xoxb-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx",
		"xoxp-123456789012-123456789012-123456789012-abcdef0123456789abcdef0123456789",
		"xapp-1-A0123456789-1234567890123-abcdef0123456789",
		"AKIAIOSFODNN7EXAMPLE",
		"-----BEGIN RSA PRIVATE KEY-----\\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu\\n-----END RSA PRIVATE KEY-----",
		"ghp_0123456789abcdefghijklmnopqrstuvwxyz",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
		"sk-abcdefghijklmnopqrstuvwxyz0123456789",
		"AIzaSyA-abcdefghijklmnopqrstuvwxyz01234",
		"password: correcthorsebatterystaple",
		h.fake.ClientSecret(),
		h.fake.SigningSecret(),
	}
	for _, leak := range leaks {
		bare := strings.SplitN(strings.ReplaceAll(leak, `\n`, "\n"), "\n", 2)[0]
		stuffed := "before " + leak + " after"
		for name, test := range map[string]struct{ template, params string }{
			"a question":       {TemplateOwnerNotice, fmt.Sprintf(`{"asker_name":%q,"question":%q,%s}`, stuffed, stuffed, tickets)},
			"a settled notice": {TemplateNoticeSettled, fmt.Sprintf(`{"asker_name":%q,"question":%q,"decision":"answered_by_other","decided_by_name":%q}`, stuffed, stuffed, stuffed)},
			"an answer":        {TemplateAnswer, fmt.Sprintf(`{"text":%q,"owner_name":%q,"mode":"summary"}`, stuffed, stuffed)},
			"a status":         {TemplateStatus, fmt.Sprintf(`{"state":"held","owner_name":%q}`, stuffed)},
			"a refusal":        {TemplateRefusal, fmt.Sprintf(`{"reason":"not_member","organization_name":%q}`, stuffed)},
			"an ack":           {TemplateAskAck, fmt.Sprintf(`{"owners":[{"name":%q}]}`, stuffed)},
			"a bypass notice":  {TemplateBypassNotice, fmt.Sprintf(`{"asker_name":%q,"question":%q,"reason":"org_chart"}`, stuffed, stuffed)},
		} {
			got := renderedJSON(t, h.renderOK(test.template, test.params))
			if strings.Contains(got, bare) {
				t.Errorf("%s: %q reached Slack:\n%s", name, bare, got)
			}
			if !strings.Contains(got, "[credential removed]") {
				t.Errorf("%s: nothing says a credential was taken out of %q:\n%s", name, bare, got)
			}
		}
	}
	// A ticket is a button's value, and is not text: it is never shown.
	notice := renderedJSON(t, h.renderOK(TemplateOwnerNotice, `{"asker_user_id":"U0ASKER","question":"q",`+tickets+`}`))
	if strings.Count(notice, "t1.deny.sig") != 1 {
		t.Fatalf("a ticket is somewhere but its button's value:\n%s", notice)
	}
}

func TestOrdinaryTalkOfTokensAndPasswordsIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	for _, text := range []string{
		"When is the launch? See https://example.com/launch and docs/deploy.md",
		"Rotate the API token tomorrow, and reset the password flow.",
		"The key is in the vault; ask Ops for the password.",
		"sk-2 is the second stage, ask-me-later",
	} {
		got := renderedJSON(t, h.renderOK(TemplateAnswer, fmt.Sprintf(`{"text":%q,"owner_name":"Raya","mode":"summary"}`, text)))
		if strings.Contains(got, "[credential removed]") {
			t.Errorf("%q was scrubbed:\n%s", text, got)
		}
		if !strings.Contains(got, escape(text)) {
			t.Errorf("%q is not in the answer:\n%s", text, got)
		}
	}
}

func TestALinkIsAPageOfOursAndNothingThatEndsIt(t *testing.T) {
	h := newHarness(t)
	for url, ok := range map[string]bool{
		testWebURL + "/?org=org_1&view=requests&request=req_1": true,
		testWebURL + "/a?b=c":                      true,
		"https://evil.example/agents/requests/1":   false,
		"http://app.oryxa.test/agents":             false,
		"javascript:alert(1)":                      false,
		"//evil.example":                           false,
		testWebURL + "/a|Open in Oryxa><https://e": false,
		testWebURL + "/a b":                        false,
		"https://app.oryxa.test.evil.example/":     false,
		"https://user@app.oryxa.test/":             false,
	} {
		_, _, err := h.service.render(TemplateAskAck, json.RawMessage(fmt.Sprintf(`{"owners":[{"name":"Raya"}],"open_url":%q}`, url)))
		if ok != (err == nil) || (!ok && !errors.Is(err, domain.ErrInvalid)) {
			t.Errorf("%q: err = %v, want allowed = %v", url, err, ok)
		}
	}
	// With no web app configured there are no links at all.
	bare := newHarness(t, func(o *harnessOptions) { o.config = func(c *Config) { c.WebOrigins = nil } })
	if _, _, err := bare.service.render(TemplateAskAck, json.RawMessage(`{"owners":[{"name":"Raya"}],"open_url":"`+testWebURL+`/x"}`)); err == nil {
		t.Fatal("a link was allowed with no web origin")
	}
}

func TestLongTextIsCutAndSplitToWhatSlackTakes(t *testing.T) {
	h := newHarness(t)

	long := strings.Repeat("Why is this so long? ", 400)
	notice := h.renderOK(TemplateOwnerNotice, fmt.Sprintf(`{"asker_user_id":"U0ASKER","question":%q,%s}`, long, tickets))
	section := notice.Blocks[0].(map[string]any)["text"].(map[string]any)["text"].(string)
	if utf8.RuneCountInString(section) > 3000 || !strings.HasSuffix(section, "…") {
		t.Fatalf("the question was not cut: %d characters", utf8.RuneCountInString(section))
	}

	var paragraphs []string
	for i := range 300 {
		paragraphs = append(paragraphs, fmt.Sprintf("Paragraph %03d says something worth reading, at some length, so that it adds up.", i))
	}
	answer := h.renderOK(TemplateAnswer, fmt.Sprintf(`{"text":%q,"owner_name":"Raya","mode":"access"}`, strings.Join(paragraphs, "\n")))
	if len(answer.Blocks) < 3 || len(answer.Blocks) > 12 {
		t.Fatalf("blocks = %d", len(answer.Blocks))
	}
	for i, block := range answer.Blocks[:len(answer.Blocks)-1] {
		text := block.(map[string]any)["text"].(map[string]any)["text"].(string)
		if n := utf8.RuneCountInString(text); n == 0 || n > maxSectionRunes {
			t.Errorf("section %d is %d characters", i, n)
		}
	}
	if last := answer.Blocks[len(answer.Blocks)-1].(map[string]any); last["type"] != "context" {
		t.Fatalf("the attribution is not last: %v", last)
	}
	if utf8.RuneCountInString(answer.Text) > 400 {
		t.Fatalf("the notification text is %d characters", utf8.RuneCountInString(answer.Text))
	}
}

func TestParamsAreCheckedAgainstTheirTemplate(t *testing.T) {
	h := newHarness(t)
	for name, test := range map[string]struct{ template, params string }{
		"a notice with no question":            {TemplateOwnerNotice, `{"asker_user_id":"U0ASKER",` + tickets + `}`},
		"a notice with no asker":               {TemplateOwnerNotice, `{"question":"q",` + tickets + `}`},
		"a notice with an asker that is no id": {TemplateOwnerNotice, `{"asker_user_id":"raya","question":"q",` + tickets + `}`},
		"a notice with no tickets":             {TemplateOwnerNotice, `{"asker_user_id":"U0ASKER","question":"q"}`},
		"a notice with one ticket missing":     {TemplateOwnerNotice, `{"asker_user_id":"U0ASKER","question":"q","tickets":{"summary":"a","access":"b"}}`},
		"a ticket with a space in it":          {TemplateOwnerNotice, `{"asker_user_id":"U0ASKER","question":"q","tickets":{"summary":"a b","access":"b","deny":"c"}}`},
		"a ticket too long for a button":       {TemplateOwnerNotice, `{"asker_user_id":"U0ASKER","question":"q","tickets":{"summary":"` + strings.Repeat("a", 2001) + `","access":"b","deny":"c"}}`},
		"a settled notice that is nothing":     {TemplateNoticeSettled, `{"asker_user_id":"U0ASKER","question":"q","decision":"sulking"}`},
		"a decision with no decider":           {TemplateNoticeSettled, `{"asker_user_id":"U0ASKER","question":"q","decision":"denied"}`},
		"a time that is none":                  {TemplateNoticeSettled, `{"asker_user_id":"U0ASKER","question":"q","decision":"denied","decided_by_name":"Raya","decided_at":"soon"}`},
		"an ack with no owners":                {TemplateAskAck, `{"owners":[]}`},
		"an ack with too many owners":          {TemplateAskAck, `{"owners":[{"name":"a"},{"name":"b"},{"name":"c"},{"name":"d"},{"name":"e"},{"name":"f"}]}`},
		"an owner with neither id nor name":    {TemplateAskAck, `{"owners":[{}]}`},
		"an answer with no text":               {TemplateAnswer, `{"owner_name":"Raya","mode":"summary"}`},
		"an answer with no owner":              {TemplateAnswer, `{"text":"t","mode":"summary"}`},
		"an answer in no mode":                 {TemplateAnswer, `{"text":"t","owner_name":"Raya"}`},
		"a bypass with a channel that is none": {TemplateBypassNotice, `{"asker_name":"a","question":"q","reason":"org_chart","channel_id":"general"}`},
		"a bypass for no reason":               {TemplateBypassNotice, `{"asker_name":"a","question":"q","reason":"because"}`},
		"a private note with params":           {TemplatePrivateNote, `{"text":"x"}`},
		"params with a NUL in them":            {TemplateStatus, "{\"state\":\"held\",\"owner_name\":\"a\\u0000b\"}"},
	} {
		_, _, err := h.service.render(test.template, json.RawMessage(test.params))
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
}

// Where a template may go is part of what it is: the notice and the bypass
// notice, which name the asker and carry the question, go to a direct
// message and nowhere else.
func TestEachTemplateMayGoOnlyWhereItIsMeantTo(t *testing.T) {
	want := map[string]placement{
		TemplateOwnerNotice:   inDM,
		TemplateNoticeSettled: inDM,
		TemplateAskAck:        inThread | toPerson,
		TemplateAnswer:        inThread | inDM,
		TemplateStatus:        inThread | toPerson | inDM,
		TemplateRefusal:       inThread | toPerson,
		TemplatePrivateNote:   inThread | toPerson,
		TemplateBypassNotice:  inDM,
	}
	if len(templates) != len(want) {
		t.Fatalf("there are %d templates, and %d are pinned", len(templates), len(want))
	}
	for name, placements := range want {
		if got := templates[name].placements; got != placements {
			t.Errorf("%s may go %03b, want %03b", name, got, placements)
		}
	}
}

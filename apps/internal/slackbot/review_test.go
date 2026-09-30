package slackbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

const (
	reviewTickets       = `"tickets":{"send":"t1.send.sig","withhold":"t1.withhold.sig"}`
	reviewTicketsMasked = `"tickets":{"send":"t1.send.sig","send_masked":"t1.masked.sig","withhold":"t1.withhold.sig"}`
)

// agentsReasons are the reasons agents' surfaces name for holding an answer, in
// the order they say them: the vocabulary the two services share, each side's
// copy pinned by a test of its own (surfaces' TestTheReasonsAreTheReviewTemplatesOwn).
var agentsReasons = []string{
	"irrelevant", "ungrounded", "no_sources", "copied", "link", "denied_topic",
	"email", "phone", "address",
	"off_topic", "personal_life", "health", "hr", "compensation", "others_private", "credentials",
	"unchecked", "policy",
}

// everyReason is every reason agents names, and the one it names for a kind it
// has no reason for, as a review's params spell them.
var everyReason = func() string {
	encoded, _ := json.Marshal(append(append([]string(nil), agentsReasons...), "other"))
	return string(encoded)
}()

// blocksOf is a rendering's blocks as JSON objects.
func blocksOf(t *testing.T, rendered Rendered) []map[string]any {
	t.Helper()
	raw, _ := json.Marshal(rendered.Blocks)
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	return blocks
}

// markup is a block's text, whatever kind of block it is.
func markup(block map[string]any) string {
	if text, ok := block["text"].(map[string]any); ok {
		value, _ := text["text"].(string)
		return value
	}
	if elements, ok := block["elements"].([]any); ok && len(elements) > 0 {
		if element, ok := elements[0].(map[string]any); ok {
			value, _ := element["text"].(string)
			return value
		}
	}
	return ""
}

type reviewButton struct{ label, action, value string }

// reviewButtons are the buttons of a rendering's one actions block, and fails
// unless the blocks have one, last.
func reviewButtons(t *testing.T, blocks []map[string]any) []reviewButton {
	t.Helper()
	last := blocks[len(blocks)-1]
	if last["type"] != "actions" || last["block_id"] != "review" {
		t.Fatalf("the last block is %v, want the review's buttons", last)
	}
	var found []reviewButton
	for _, element := range last["elements"].([]any) {
		button := element.(map[string]any)
		if button["type"] != "button" {
			t.Fatalf("a %v among the buttons", button["type"])
		}
		if _, styled := button["style"]; styled {
			t.Errorf("button %v is styled: nothing nudges the owner towards sending", button["action_id"])
		}
		label := button["text"].(map[string]any)["text"].(string)
		found = append(found, reviewButton{label, button["action_id"].(string), button["value"].(string)})
	}
	return found
}

// TestAReviewShowsItsOwnerTheAnswerAndAButtonForEachReview: who asked, what, why
// it was held, the answer, and Send and Don't send, whose values are the
// tickets, and nothing else.
func TestAReviewShowsItsOwnerTheAnswerAndAButtonForEachReview(t *testing.T) {
	h := newHarness(t)
	rendered := h.renderOK(TemplateHeldReview,
		`{"asker_user_id":"U0ASKER","question":"When is the launch?","reasons":["irrelevant"],"preview":"Revenue grew in Q3.",`+reviewTickets+`}`)
	blocks := blocksOf(t, rendered)
	if len(blocks) != 5 {
		t.Fatalf("blocks = %v", blocks)
	}
	for index, want := range []string{
		"<@U0ASKER> asked you through Oryxa:\n> When is the launch?",
		"*Your agent's answer is held for your review.*\n• The answer may not address the question.",
		"*Your agent's answer*, which only you can see:\n> Revenue grew in Q3.",
		"*Send* shares the whole answer with them. *Don't send* tells them you chose not to share it.",
	} {
		if got := markup(blocks[index]); got != want {
			t.Errorf("block %d says %q, want %q", index, got, want)
		}
	}
	if got, want := reviewButtons(t, blocks), []reviewButton{
		{"Send", ActionReviewSend, "t1.send.sig"}, {"Don't send", ActionReviewWithhold, "t1.withhold.sig"},
	}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("buttons = %v, want %v", got, want)
	}
	// A notification says who, and neither what was asked nor the answer.
	if rendered.Text != "Your agent's answer for <@U0ASKER> is held for your review." {
		t.Fatalf("text = %q", rendered.Text)
	}
	// Nothing else: no links, no images, no fields. A ticket is a button's value
	// and is never shown.
	whole := renderedJSON(t, rendered)
	for _, forbidden := range []string{"http", `"image"`, `"fields"`, `"accessory"`, `"url"`} {
		if strings.Contains(whole, forbidden) {
			t.Errorf("the review holds %s: %s", forbidden, whole)
		}
	}
	if strings.Count(whole, "t1.send.sig") != 1 || strings.Count(whole, "t1.withhold.sig") != 1 {
		t.Fatalf("a ticket is somewhere but its button's value:\n%s", whole)
	}
}

// TestAMaskedReviewShowsTheMaskedVersionAndOffersToSendIt: the preview is the
// masked version, the owner is told what each button sends, and there is a
// third button, between the two.
func TestAMaskedReviewShowsTheMaskedVersionAndOffersToSendIt(t *testing.T) {
	h := newHarness(t)
	blocks := blocksOf(t, h.renderOK(TemplateHeldReview,
		`{"asker_name":"Alex Kim","question":"How do I reach Priya?","reasons":["phone"],"preview":"Call {PHONE}.","masked":true,`+reviewTicketsMasked+`}`))
	if got := markup(blocks[2]); got != "*Your agent's answer*, with what was flagged masked:\n> Call {PHONE}." {
		t.Fatalf("the preview reads %q", got)
	}
	// Send does not send what is shown: the owner is told so.
	if got := markup(blocks[3]); !strings.Contains(got, "*Send* shares the whole answer with them, flagged details included.") ||
		!strings.Contains(got, "*Send masked* shares it with those details masked, as here.") {
		t.Fatalf("the owner is told %q", got)
	}
	if got, want := reviewButtons(t, blocks), []reviewButton{
		{"Send", ActionReviewSend, "t1.send.sig"}, {"Send masked", ActionReviewSendMasked, "t1.masked.sig"}, {"Don't send", ActionReviewWithhold, "t1.withhold.sig"},
	}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("buttons = %v, want %v", got, want)
	}
	if got := markup(blocks[0]); got != "*Alex Kim* asked you through Oryxa:\n> How do I reach Priya?" {
		t.Fatalf("the asker is %q", got)
	}
}

// TestAReviewOfASecretShowsNoAnswerAndHasNoButtons: a secret is never in Slack,
// so its owner is told why the answer waits, and to review it in Oryxa.
func TestAReviewOfASecretShowsNoAnswerAndHasNoButtons(t *testing.T) {
	h := newHarness(t)
	blocks := blocksOf(t, h.renderOK(TemplateHeldReview, `{"asker_user_id":"U0ASKER","question":"The staging key?","reasons":["unchecked"],"secret":true}`))
	if len(blocks) != 3 || blocks[2]["type"] != "context" {
		t.Fatalf("blocks = %v", blocks)
	}
	if got := markup(blocks[2]); got != "It is about a secret, which is never shown in Slack. Review it in Oryxa." {
		t.Fatalf("the owner is told %q", got)
	}
	for _, block := range blocks {
		if block["type"] == "actions" {
			t.Fatalf("a review of a secret has buttons: %v", blocks)
		}
	}
}

// TestReasonsAreSaidOnceEachInPlainWords: in the order agents gave them, a
// reason given twice once, and one with no words of its own, however many, as
// the one general line.
func TestReasonsAreSaidOnceEachInPlainWords(t *testing.T) {
	h := newHarness(t)
	blocks := blocksOf(t, h.renderOK(TemplateHeldReview,
		`{"asker_user_id":"U0ASKER","question":"q","reasons":["phone","ungrounded","phone","something_newer","other"],"preview":"p",`+reviewTickets+`}`))
	want := "*Your agent's answer is held for your review.*\n" +
		"• It includes a phone number.\n" +
		"• The answer may not be supported by what your agent read.\n" +
		"• Oryxa flagged it for your review."
	if got := markup(blocks[1]); got != want {
		t.Fatalf("the reasons read %q, want %q", got, want)
	}
}

// TestEveryReasonAgentsNamesHasItsOwnWords: the vocabulary is shared, and a
// reason agents names that apps has no words for would read as the general
// line; "other" is the one that does on purpose.
func TestEveryReasonAgentsNamesHasItsOwnWords(t *testing.T) {
	if len(heldReasonWords) != len(agentsReasons) {
		t.Fatalf("apps has words for %d reasons, and agents names %d", len(heldReasonWords), len(agentsReasons))
	}
	seen := map[string]bool{}
	for _, reason := range agentsReasons {
		words, ok := heldReasonWords[reason]
		if !ok || words == heldReasonOther || seen[words] {
			t.Errorf("reason %q is said as %q", reason, words)
		}
		seen[words] = true
	}
	if _, ok := heldReasonWords["other"]; ok {
		t.Error(`"other" has words of its own; it is the general line`)
	}
}

func TestReviewParamsAreCheckedAgainstTheirTemplate(t *testing.T) {
	h := newHarness(t)
	const asked = `"asker_user_id":"U0ASKER","question":"q",`
	for name, test := range map[string]struct{ template, params string }{
		"a review with no question":              {TemplateHeldReview, `{"asker_user_id":"U0ASKER","reasons":["irrelevant"],"preview":"p",` + reviewTickets + `}`},
		"a review with no asker":                 {TemplateHeldReview, `{"question":"q","reasons":["irrelevant"],"preview":"p",` + reviewTickets + `}`},
		"a review with no reason":                {TemplateHeldReview, `{` + asked + `"reasons":[],"preview":"p",` + reviewTickets + `}`},
		"a review with its reasons left out":     {TemplateHeldReview, `{` + asked + `"preview":"p",` + reviewTickets + `}`},
		"a reason in words":                      {TemplateHeldReview, `{` + asked + `"reasons":["It is wrong"],"preview":"p",` + reviewTickets + `}`},
		"a reason in capitals":                   {TemplateHeldReview, `{` + asked + `"reasons":["IRRELEVANT"],"preview":"p",` + reviewTickets + `}`},
		"too many reasons":                       {TemplateHeldReview, `{` + asked + `"reasons":[` + strings.Repeat(`"phone",`, maxReasons) + `"phone"],"preview":"p",` + reviewTickets + `}`},
		"a review with no answer":                {TemplateHeldReview, `{` + asked + `"reasons":["irrelevant"],` + reviewTickets + `}`},
		"a review with no tickets":               {TemplateHeldReview, `{` + asked + `"reasons":["irrelevant"],"preview":"p"}`},
		"a review with no send ticket":           {TemplateHeldReview, `{` + asked + `"reasons":["irrelevant"],"preview":"p","tickets":{"withhold":"t1.w"}}`},
		"a review with no withhold ticket":       {TemplateHeldReview, `{` + asked + `"reasons":["irrelevant"],"preview":"p","tickets":{"send":"t1.s"}}`},
		"a ticket with a space":                  {TemplateHeldReview, `{` + asked + `"reasons":["irrelevant"],"preview":"p","tickets":{"send":"t1 s","withhold":"t1.w"}}`},
		"a masked preview with no masked send":   {TemplateHeldReview, `{` + asked + `"reasons":["phone"],"preview":"p","masked":true,` + reviewTickets + `}`},
		"a masked send with no masked preview":   {TemplateHeldReview, `{` + asked + `"reasons":["phone"],"preview":"p",` + reviewTicketsMasked + `}`},
		"a masked send ticket too long":          {TemplateHeldReview, `{` + asked + `"reasons":["phone"],"preview":"p","masked":true,"tickets":{"send":"t1.s","send_masked":"` + strings.Repeat("a", 2001) + `","withhold":"t1.w"}}`},
		"a secret's review with an answer":       {TemplateHeldReview, `{` + asked + `"reasons":["unchecked"],"secret":true,"preview":"the key is here"}`},
		"a secret's review with buttons":         {TemplateHeldReview, `{` + asked + `"reasons":["unchecked"],"secret":true,` + reviewTickets + `}`},
		"a secret's review that is masked":       {TemplateHeldReview, `{` + asked + `"reasons":["unchecked"],"secret":true,"masked":true}`},
		"a review with a field it does not take": {TemplateHeldReview, `{` + asked + `"reasons":["irrelevant"],"preview":"p","blocks":[],` + reviewTickets + `}`},
		"a settled review that is nothing":       {TemplateHeldReviewSettled, `{` + asked + `"outcome":"sulking"}`},
		"a settled review with no outcome":       {TemplateHeldReviewSettled, `{` + asked[:len(asked)-1] + `}`},
		"a settled review with no question":      {TemplateHeldReviewSettled, `{"asker_user_id":"U0ASKER","outcome":"sent"}`},
		"a settled review with an answer":        {TemplateHeldReviewSettled, `{` + asked + `"outcome":"sent","preview":"p"}`},
		"a settled review at no time":            {TemplateHeldReviewSettled, `{` + asked + `"outcome":"sent","decided_at":"soon"}`},
	} {
		_, _, err := h.service.render(test.template, json.RawMessage(test.params))
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
}

// TestWhatAReviewShowsCannotBecomeMarkup: the question, the answer and the
// asker's name are a person's and a model's words, escaped as every text is.
func TestWhatAReviewShowsCannotBecomeMarkup(t *testing.T) {
	h := newHarness(t)
	hostile := "&lt;b&gt; <!channel> <!here> <@U0EVIL> <#C0SECRET> <https://evil.example|Open in Oryxa> <mailto:x@evil.example> <!everyone>"
	for name, params := range map[string]string{
		"a review":          fmt.Sprintf(`{"asker_name":%q,"question":%q,"reasons":["irrelevant"],"preview":%q,%s}`, hostile, hostile, hostile, reviewTickets),
		"a masked review":   fmt.Sprintf(`{"asker_name":%q,"question":%q,"reasons":["phone"],"preview":%q,"masked":true,%s}`, hostile, hostile, hostile, reviewTicketsMasked),
		"a secret's review": fmt.Sprintf(`{"asker_name":%q,"question":%q,"reasons":["unchecked"],"secret":true}`, hostile, hostile),
	} {
		got := renderedJSON(t, h.renderOK(TemplateHeldReview, params))
		for _, raw := range []string{"<!channel>", "<!here>", "<!everyone>", "<@U0EVIL>", "<#C0SECRET>", "<https://evil", "<mailto:"} {
			if strings.Contains(got, raw) {
				t.Errorf("%s: %s reached Slack as markup:\n%s", name, raw, got)
			}
		}
		if !strings.Contains(got, "&lt;!channel&gt;") || !strings.Contains(got, "&amp;lt;b&amp;gt;") {
			t.Errorf("%s: the text is not shown, escaped:\n%s", name, got)
		}
	}
	settled := renderedJSON(t, h.renderOK(TemplateHeldReviewSettled, fmt.Sprintf(`{"asker_name":%q,"question":%q,"outcome":"sent"}`, hostile, hostile)))
	if strings.Contains(settled, "<!channel>") || !strings.Contains(settled, "&lt;!channel&gt;") {
		t.Errorf("a settled review: the text reached Slack as markup:\n%s", settled)
	}
}

// TestAReviewNeverRendersACredential: the guards took them out already; this is
// for the one they missed, as for every template.
func TestAReviewNeverRendersACredential(t *testing.T) {
	h := newHarness(t)
	for _, leak := range []string{
		"xoxb-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx",
		"AKIAIOSFODNN7EXAMPLE",
		"ghp_0123456789abcdefghijklmnopqrstuvwxyz",
		"password: correcthorsebatterystaple",
		h.fake.SigningSecret(),
	} {
		stuffed := "before " + leak + " after"
		got := renderedJSON(t, h.renderOK(TemplateHeldReview,
			fmt.Sprintf(`{"asker_name":%q,"question":%q,"reasons":["irrelevant"],"preview":%q,%s}`, stuffed, stuffed, stuffed, reviewTickets)))
		if strings.Contains(got, leak) || !strings.Contains(got, "[credential removed]") {
			t.Errorf("%q reached Slack, or nothing says it was taken out:\n%s", leak, got)
		}
	}
}

// TestALongAnswerIsCutToWhatAReviewShows: enough to judge it by, cut, and
// split where one of Slack's sections would not take it, with every line of it
// still quoted.
func TestALongAnswerIsCutToWhatAReviewShows(t *testing.T) {
	h := newHarness(t)
	for name, test := range map[string]struct {
		line     string
		sections int
	}{
		// Long lines: the cut alone keeps it to one section.
		"long lines": {"Revenue grew by about a fifth in Q3, and the launch is set for mid October.", 1},
		// Many short ones: quoting each of them takes it past a section.
		"short lines": {"x", 2},
	} {
		lines := make([]string, 1200)
		for i := range lines {
			lines[i] = test.line
		}
		blocks := blocksOf(t, h.renderOK(TemplateHeldReview, fmt.Sprintf(`{"asker_user_id":"U0ASKER","question":"q","reasons":["irrelevant"],"preview":%q,%s}`,
			strings.Join(lines, "\n"), reviewTickets)))
		sections := blocks[2 : len(blocks)-2]
		if len(sections) != test.sections {
			t.Fatalf("%s: the answer takes %d sections, want %d", name, len(sections), test.sections)
		}
		var preview []string
		for _, block := range sections {
			text := markup(block)
			if n := utf8.RuneCountInString(text); n == 0 || n > maxSectionRunes {
				t.Fatalf("%s: a section of the answer is %d characters", name, n)
			}
			preview = append(preview, text)
		}
		whole := strings.Join(preview, "\n")
		if !strings.HasSuffix(whole, "…") || utf8.RuneCountInString(strings.ReplaceAll(whole, "\n> ", "\n")) > maxPreviewRunes+100 {
			t.Fatalf("%s: the answer was not cut:\n%s", name, whole)
		}
		for _, line := range strings.Split(whole, "\n")[1:] {
			if !strings.HasPrefix(line, "> ") {
				t.Fatalf("%s: a line of the answer is not quoted: %q", name, line)
			}
		}
	}
}

// TestASettledReviewSaysHowItEndedAndNothingOfTheAnswer: the same asker and
// question, one line, when the owner reviewed it where they did, and no
// buttons. Oryxa's own endings have no time: nobody decided them.
func TestASettledReviewSaysHowItEndedAndNothingOfTheAnswer(t *testing.T) {
	h := newHarness(t)
	decidedAt := time.Date(2026, time.September, 29, 14, 2, 0, 0, time.UTC)
	for outcome, want := range map[string]string{
		ReviewSent:             "Sent" + atTime(&decidedAt),
		ReviewSentMasked:       "Sent masked" + atTime(&decidedAt),
		ReviewNotSent:          "Not sent" + atTime(&decidedAt),
		ReviewDecidedElsewhere: "Already decided in Oryxa" + atTime(&decidedAt),
		ReviewExpired:          "Not sent: the time to review it ran out",
		ReviewAnsweredByOther:  "Not sent: someone else answered first",
	} {
		rendered := h.renderOK(TemplateHeldReviewSettled, fmt.Sprintf(`{"asker_user_id":"U0ASKER","question":"When is the launch?","outcome":%q,"decided_at":"2026-09-29T14:02:00Z"}`, outcome))
		blocks := blocksOf(t, rendered)
		if len(blocks) != 2 || markup(blocks[0]) != "<@U0ASKER> asked you through Oryxa:\n> When is the launch?" || blocks[1]["type"] != "context" {
			t.Fatalf("%s: blocks = %v", outcome, blocks)
		}
		if got := markup(blocks[1]); got != want {
			t.Errorf("%s: says %q, want %q", outcome, got, want)
		}
		if strings.Contains(rendered.Text, "<!date") || rendered.Text != plain(want) {
			t.Errorf("%s: the notification text is %q", outcome, rendered.Text)
		}
	}
}

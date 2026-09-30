package slackbot

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The owner's review of an answer the guards held. When agents' guards hold an
// answer to a question asked in Slack, the thread is told only that it is held
// (status), and its owner is sent this, in a direct message: who asked what,
// why the answer was held, the answer, and three buttons, Send, Send masked
// (only where there is a masked version) and Don't send. It is the one message
// that shows a held answer, and it goes to a direct message and nowhere else.
// Once the owner has reviewed it, on either surface, it is replaced with how the
// review ended (held_review_settled), which shows the answer no more.
const (
	TemplateHeldReview        = "held_review"
	TemplateHeldReviewSettled = "held_review_settled"
)

// The buttons of a review, by their action ids. A button's value is its
// ticket, a review's, which says the same thing again, signed: agents refuses a
// consent ticket on a review's button, and a review's on a notice's.
const (
	ActionReviewSend       = "review_send"
	ActionReviewSendMasked = "review_send_masked"
	ActionReviewWithhold   = "review_withhold"
)

const (
	// maxPreviewRunes caps how much of a held answer a review shows: enough to
	// judge it by. The whole of it is in Oryxa.
	maxPreviewRunes = 1500
	// maxReasons bounds how many reasons a review names.
	maxReasons = 20
)

// reasonCodePattern is what a reason is: a short code, never words of anyone's.
var reasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// heldReasonWords are the reasons agents names for holding an answer, from what
// its guards found, in plain words. Each is one line of the review.
var heldReasonWords = map[string]string{
	"irrelevant":     "The answer may not address the question.",
	"ungrounded":     "The answer may not be supported by what your agent read.",
	"no_sources":     "Your agent found nothing to base the answer on.",
	"copied":         "It copies long passages from what your agent read, and you allowed a summary only.",
	"link":           "It includes a link, and you allowed a summary only.",
	"denied_topic":   "It touches a topic Oryxa doesn't share.",
	"email":          "It includes an email address.",
	"phone":          "It includes a phone number.",
	"address":        "It includes a street address.",
	"off_topic":      "It may include something the question didn't ask for.",
	"personal_life":  "It may include personal-life details.",
	"health":         "It may include health details.",
	"hr":             "It may include HR details.",
	"compensation":   "It may include pay details.",
	"others_private": "It may include someone else's private messages or data.",
	"credentials":    "It may include secret values.",
	"unchecked":      "Oryxa couldn't check it automatically.",
	"policy":         "Your organization asks you to review answers before they're shared.",
}

// heldReasonOther is how a reason with no words of its own is said, agents'
// "other" among them, so a newer reason reaches people before its wording does.
const heldReasonOther = "Oryxa flagged it for your review."

type heldReviewParams struct {
	AskerUserID string `json:"asker_user_id"`
	AskerName   string `json:"asker_name"`
	Question    string `json:"question"`
	// Reasons are why the answer was held, as codes: one to maxReasons.
	Reasons []string `json:"reasons"`
	// Preview is what the owner is shown of the answer, and Masked says it is
	// its masked version, which Send masked sends, while Send sends the answer
	// as it was held. For an ask of a secret, Secret, there is neither, and no
	// buttons: a secret is never in Slack.
	Preview string `json:"preview"`
	Masked  bool   `json:"masked"`
	Secret  bool   `json:"secret"`
	// Tickets are the buttons' values, opaque to apps, which only passes them
	// along and never reads or logs them. SendMasked is there exactly when the
	// preview is masked.
	Tickets *struct {
		Send       string `json:"send"`
		SendMasked string `json:"send_masked"`
		Withhold   string `json:"withhold"`
	} `json:"tickets"`
}

// heldReasonLines are the reasons as the lines a review says them in, once
// each and in the order they were given.
func heldReasonLines(reasons []string) (string, error) {
	if len(reasons) == 0 || len(reasons) > maxReasons {
		return "", invalid("reasons is one to %d codes", maxReasons)
	}
	lines := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if !reasonCodePattern.MatchString(reason) {
			return "", invalid("%q is not a reason code", truncate(reason, 32))
		}
		words, known := heldReasonWords[reason]
		if !known {
			words = heldReasonOther
		}
		line := "• " + words
		if !slices.Contains(lines, line) {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// renderHeldReview is the owner's review of an answer the guards held for them:
// who asked, what, why it was held, the answer as it would be sent, and a button
// for each review. Where the answer has a masked version, that is what it
// shows, and the owner is told that Send sends the answer as written. For an
// ask of a secret it shows no answer and has no buttons: the owner reviews it in
// Oryxa.
func renderHeldReview(r renderer, raw json.RawMessage) (Rendered, error) {
	var p heldReviewParams
	if err := decodeParams(raw, &p); err != nil {
		return Rendered{}, err
	}
	if err := requireText(p.Question, "question"); err != nil {
		return Rendered{}, err
	}
	asker, err := r.asker(p.AskerUserID, p.AskerName)
	if err != nil {
		return Rendered{}, err
	}
	why, err := heldReasonLines(p.Reasons)
	if err != nil {
		return Rendered{}, err
	}
	// A notification says who, and not what.
	text := "Your agent's answer for " + plain(asker) + " is held for your review."
	blocks := []any{
		section(asker + " asked you through Oryxa:\n" + quoted(r.shown(p.Question, maxQuestionRunes))),
		section("*Your agent's answer is held for your review.*\n" + why),
	}
	if p.Secret {
		if strings.TrimSpace(p.Preview) != "" || p.Masked || p.Tickets != nil {
			return Rendered{}, invalid("a review of an answer about a secret shows no answer and has no buttons")
		}
		blocks = append(blocks, contextBlock("It is about a secret, which is never shown in Slack. Review it in Oryxa."))
		return Rendered{Text: text, Blocks: blocks}, nil
	}
	if err := requireText(p.Preview, "preview"); err != nil {
		return Rendered{}, err
	}
	if p.Tickets == nil {
		return Rendered{}, invalid("tickets is required")
	}
	for _, ticket := range []struct{ name, value string }{{"send", p.Tickets.Send}, {"withhold", p.Tickets.Withhold}} {
		if err := checkTicket(ticket.name, ticket.value); err != nil {
			return Rendered{}, err
		}
	}
	switch {
	case p.Masked && p.Tickets.SendMasked == "":
		return Rendered{}, invalid("a masked preview needs tickets.send_masked")
	case !p.Masked && p.Tickets.SendMasked != "":
		return Rendered{}, invalid("tickets.send_masked needs a masked preview")
	case p.Masked:
		if err := checkTicket("send_masked", p.Tickets.SendMasked); err != nil {
			return Rendered{}, err
		}
	}
	label := "*Your agent's answer*, which only you can see:"
	guide := "*Send* shares the whole answer with them. *Don't send* tells them you chose not to share it."
	buttons := []any{button(ActionReviewSend, "Send", p.Tickets.Send)}
	if p.Masked {
		// What is shown is the masked version, and Send does not send that: the
		// owner is told so beside the buttons.
		label = "*Your agent's answer*, with what was flagged masked:"
		guide = "*Send* shares the whole answer with them, flagged details included. *Send masked* shares it with those details masked, as here. " +
			"*Don't send* tells them you chose not to share it."
		buttons = append(buttons, button(ActionReviewSendMasked, "Send masked", p.Tickets.SendMasked))
	}
	buttons = append(buttons, button(ActionReviewWithhold, "Don't send", p.Tickets.Withhold))
	for _, part := range splitSections(label+"\n"+quoted(r.shown(p.Preview, maxPreviewRunes)), maxSectionRunes) {
		blocks = append(blocks, section(part))
	}
	blocks = append(blocks, contextBlock(guide), actionsBlock("review", buttons...))
	return Rendered{Text: text, Blocks: blocks}, nil
}

// The outcomes a review settles into.
const (
	// ReviewSent, ReviewSentMasked and ReviewNotSent are the owner's review in
	// Slack.
	ReviewSent       = "sent"
	ReviewSentMasked = "sent_masked"
	ReviewNotSent    = "not_sent"
	// ReviewDecidedElsewhere is the owner's review in Oryxa, whatever it was.
	ReviewDecidedElsewhere = "decided_elsewhere"
	// ReviewExpired and ReviewAnsweredByOther are Oryxa's: the time to review it
	// ran out, or another owner's answer settled the question first.
	ReviewExpired         = "expired"
	ReviewAnsweredByOther = "answered_by_other"
)

type heldReviewSettledParams struct {
	AskerUserID string     `json:"asker_user_id"`
	AskerName   string     `json:"asker_name"`
	Question    string     `json:"question"`
	Outcome     string     `json:"outcome"`
	DecidedAt   *time.Time `json:"decided_at"`
}

// renderHeldReviewSettled is a review after it ended: the same asker and
// question, and one line saying how it ended, with no answer and no buttons. It
// replaces the review with chat.update, so the buttons, and the answer, are gone
// whichever surface the owner reviewed on.
func renderHeldReviewSettled(r renderer, raw json.RawMessage) (Rendered, error) {
	var p heldReviewSettledParams
	if err := decodeParams(raw, &p); err != nil {
		return Rendered{}, err
	}
	if err := requireText(p.Question, "question"); err != nil {
		return Rendered{}, err
	}
	asker, err := r.asker(p.AskerUserID, p.AskerName)
	if err != nil {
		return Rendered{}, err
	}
	var line string
	switch p.Outcome {
	case ReviewSent:
		line = "Sent" + atTime(p.DecidedAt)
	case ReviewSentMasked:
		line = "Sent masked" + atTime(p.DecidedAt)
	case ReviewNotSent:
		line = "Not sent" + atTime(p.DecidedAt)
	case ReviewDecidedElsewhere:
		line = "Already decided in Oryxa" + atTime(p.DecidedAt)
	case ReviewExpired:
		line = "Not sent: the time to review it ran out"
	case ReviewAnsweredByOther:
		line = "Not sent: someone else answered first"
	default:
		return Rendered{}, invalid("outcome %q is not one a review settles into", truncate(p.Outcome, 32))
	}
	return Rendered{
		Text: plain(line),
		Blocks: []any{
			section(asker + " asked you through Oryxa:\n" + quoted(r.shown(p.Question, maxQuestionRunes))),
			contextBlock(line),
		},
	}, nil
}

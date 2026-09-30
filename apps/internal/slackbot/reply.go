package slackbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// "Reply yourself". The owner's notice, and their review of an answer the
// guards held, carry a fourth button, Reply yourself, for an owner who would
// rather answer in their own words than let their agent. Its value is a reply
// ticket, as opaque to apps as every ticket.
//
// A click on it decides nothing and is recorded nowhere. The surface opens a
// modal at once, which only a click that fresh may do: views.open with the
// click's trigger_id, which Slack takes once and for three seconds. The modal
// says who asked what, as the message the button was on said it, and has a box
// for the reply, Send and Cancel. The ticket rides in its private_metadata, with
// the message the button was on. views.open needs no scope of its own: the bot's
// token is enough, so the app asks for nothing new (BotScopes).
//
// Send is a view_submission, verified as every delivery is and answered at once:
// with nothing, which closes the modal, or, for a reply that is empty or too
// long, with the error shown under the box. A reply that is sent is kept as an
// inbound record of its own kind, InboundReply, with its words, its ticket and
// the message the button was on, and announced as a click is
// (apps.slack.action.received.v1): agents' router hands it to the ledger, which
// checks the ticket and who sent it, and the surfaces replace the message the
// button was on with "You replied", or with how it was decided first.

const (
	// ActionReply is the Reply yourself button, on a notice and on a review.
	ActionReply = "reply"
	// replyLabel is the button's words, and the modal's title.
	replyLabel = "Reply yourself"
	// replyCallbackID names the modal, so that its submission is known as a
	// reply's and nothing else's.
	replyCallbackID = "oryxa_reply"
	// replyBlockID and replyInputID name the box, as the submission and an
	// error about it do.
	replyBlockID = "reply"
	replyInputID = "text"
	// MaxReplyRunes is the most a reply holds, once trimmed, which is what
	// agents' ledger takes (interactions.MaxReplyRunes).
	MaxReplyRunes = 4000
	// replyBoxRunes is the most the box lets anyone type: Slack's own cap on a
	// plain-text input, which is under MaxReplyRunes.
	replyBoxRunes = 3000
	// maxShownReplyRunes caps how much of a reply a settled message shows.
	maxShownReplyRunes = 1500
	// maxTriggerBytes bounds a trigger_id, which Slack makes short.
	maxTriggerBytes = 255
)

// renderReplied is a notice, or a review, after its owner replied in their own
// words: who asked what, and "You replied", with when and what they wrote. The
// reply is left out when it was not given, which is a reply to a question that
// asked for a secret: its answer is never in Slack. It never shows anything of
// an answer that was held: that is gone.
func renderReplied(r renderer, asker, question, reply string, at *time.Time) (Rendered, error) {
	line := "You replied" + atTime(at)
	blocks := []any{section(asker + " asked you through Oryxa:\n" + quoted(r.shown(question, maxQuestionRunes)))}
	if strings.TrimSpace(reply) == "" {
		blocks = append(blocks, contextBlock(line))
		return Rendered{Text: plain(line), Blocks: blocks}, nil
	}
	for _, part := range splitSections("*You replied*"+atTime(at)+":\n"+quoted(r.shown(reply, maxShownReplyRunes)), maxSectionRunes) {
		blocks = append(blocks, section(part))
	}
	return Rendered{Text: plain(line), Blocks: blocks}, nil
}

// clickedMessage is the message a click was on, as Slack sends it back with the
// click.
type clickedMessage struct {
	TS     string `json:"ts"`
	User   string `json:"user"`
	Blocks []struct {
		Type string `json:"type"`
		Text struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"text"`
	} `json:"blocks"`
}

// askLead is the start of the first block of a notice and of a review, which
// names who asked (renderOwnerNotice, renderHeldReview), and askerMarkup what a
// renderer names them with: a mention, or a name in bold (mention).
var (
	askLead     = regexp.MustCompile(`^(.{1,300}?) (?:is asking you|asked you) through Oryxa:\n`)
	askerMarkup = regexp.MustCompile(`^(?:<@[UW][A-Z0-9]{2,31}>|\*[^*\n]{1,200}\*)$`)
)

// askOf is who asked what, from the message a Reply yourself button was on:
// its first block, as this surface rendered it and Slack sent it back. It is
// only ever the bot's own message that has the button, and what it says is
// shown only to the one who clicked it, who has just read it. It answers
// nothing for a message it cannot read that way, and the modal then says less.
func askOf(message clickedMessage, botUserID string) (asker, question string) {
	if message.User != "" && message.User != botUserID || len(message.Blocks) == 0 {
		return "", ""
	}
	first := message.Blocks[0]
	if first.Type != "section" || first.Text.Type != "mrkdwn" {
		return "", ""
	}
	lead := askLead.FindStringSubmatch(first.Text.Text)
	if lead == nil || !askerMarkup.MatchString(lead[1]) {
		return "", ""
	}
	return lead[1], strings.TrimPrefix(first.Text.Text, lead[0])
}

// replyMetadata is what the modal carries through Slack and back: the ticket of
// the button that opened it, and the message the button was on.
type replyMetadata struct {
	Ticket    string `json:"t"`
	ChannelID string `json:"c"`
	MessageTS string `json:"m"`
}

// valid is whether metadata names a ticket and a message of the shapes the
// surface takes.
func (metadata replyMetadata) valid() bool {
	return checkTicket("reply", metadata.Ticket) == nil &&
		channelIDPattern.MatchString(metadata.ChannelID) && tsPattern.MatchString(metadata.MessageTS)
}

func plainText(text string) map[string]any {
	return map[string]any{"type": "plain_text", "text": text, "emoji": false}
}

// replyModal is the modal Reply yourself opens: "Reply to" who asked, what they
// asked, a box for the reply, and Send and Cancel. With nobody to name it asks
// for a reply to the question in the message the button was on.
func (r renderer) replyModal(asker, question string, metadata replyMetadata) (map[string]any, error) {
	carried, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	var blocks []any
	switch {
	case asker != "":
		// A question cut short ends in an ellipsis, which the section's room
		// holds as well.
		lead := "Reply to " + asker + ", who asked:\n"
		blocks = append(blocks, section(lead+firstRunes(question, maxSectionRunes-utf8.RuneCountInString(lead)-1)))
	default:
		blocks = append(blocks, section("Reply to the question in the message you clicked, in your own words."))
	}
	blocks = append(blocks,
		map[string]any{
			"type": "input", "block_id": replyBlockID, "label": plainText("Your reply"),
			"element": map[string]any{
				"type": "plain_text_input", "action_id": replyInputID, "multiline": true, "max_length": replyBoxRunes,
				"focus_on_load": true, "placeholder": plainText("Answer in your own words"),
			},
		},
		contextBlock("It is sent as your own answer, where they asked. Your agent does not answer it."),
	)
	return map[string]any{
		"type": "modal", "callback_id": replyCallbackID, "private_metadata": string(carried),
		"title": plainText(replyLabel), "submit": plainText("Send"), "close": plainText("Cancel"),
		"blocks": blocks,
	}, nil
}

// printableTrigger is whether a trigger_id is one to hand Slack: short, and
// printable text with no spaces.
func printableTrigger(trigger string) bool {
	if trigger == "" || len(trigger) > maxTriggerBytes {
		return false
	}
	for i := 0; i < len(trigger); i++ {
		if trigger[i] <= ' ' || trigger[i] > '~' {
			return false
		}
	}
	return true
}

// openReply opens the modal of a click on Reply yourself, now: the trigger lasts
// three seconds, inside which the ingress answers Slack too. A click the surface
// cannot open a modal for is dropped, as any click of a shape it does not know
// is; Slack refusing to open one, or not answering, is an error, which Slack
// shows beside the button, and the owner clicks again.
func (s *Service) openReply(ctx context.Context, install Install, click interaction) error {
	metadata := replyMetadata{
		Ticket:    click.Actions[0].Value,
		ChannelID: firstNonEmpty(click.Container.ChannelID, click.Channel.ID),
		MessageTS: firstNonEmpty(click.Container.MessageTS, click.Message.TS),
	}
	if !metadata.valid() || !userIDPattern.MatchString(click.User.ID) || !printableTrigger(click.TriggerID) {
		s.logger.WarnContext(ctx, "a Slack click on Reply yourself of a shape the surface does not know was dropped", "team_id", install.TeamID)
		return nil
	}
	asker, question := askOf(click.Message, install.BotUserID)
	view, err := s.renderer().replyModal(asker, question, metadata)
	if err != nil {
		return err
	}
	token, err := s.botToken(ctx, install)
	if err != nil {
		return err
	}
	if err := s.api.OpenView(ctx, token, click.TriggerID, view); err != nil {
		var refused *Error
		if errors.As(err, &refused) && !refused.tokenDead() {
			// The trigger ran out, or was used: the click is too old to open
			// anything, and the owner clicks again.
			return fmt.Errorf("%w: slack would not open the reply's modal (%s)", domain.ErrUnavailable, refused.Code)
		}
		return s.slackFailed(ctx, install, err)
	}
	// Not the ticket, nor the question: ids alone.
	s.logger.InfoContext(ctx, "slack reply modal opened", "organization_id", install.OrganizationID, "team_id", install.TeamID)
	return nil
}

// submittedView is a modal as its view_submission sends it back.
type submittedView struct {
	CallbackID      string `json:"callback_id"`
	PrivateMetadata string `json:"private_metadata"`
	State           struct {
		Values map[string]map[string]struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"values"`
	} `json:"state"`
}

// replyErrors is the answer that keeps the modal open and says, under the box,
// what is wrong with what was typed.
func replyErrors(message string) (json.RawMessage, error) {
	encoded, err := json.Marshal(map[string]any{"response_action": "errors", "errors": map[string]string{replyBlockID: message}})
	return json.RawMessage(encoded), err
}

// submitReply takes Send on the reply's modal. A reply that is empty once
// trimmed, or longer than the ledger takes, is answered with the error in the
// modal, and nothing is kept. Anything else is kept as an inbound record and
// announced, and the modal is closed: whether the reply lands is the ledger's
// to say, and the message the button was on says it. A submission of another
// modal, of a shape the surface does not know, or for a workspace with no
// active install, closes it and keeps nothing.
func (s *Service) submitReply(ctx context.Context, key string, submission interaction, at time.Time) (json.RawMessage, error) {
	if submission.View.CallbackID != replyCallbackID {
		s.logger.DebugContext(ctx, "a Slack modal the surface did not open was dropped")
		return nil, nil
	}
	install, found, err := s.installFor(ctx, teamsOfClick(submission))
	if err != nil {
		return nil, err
	}
	if !found {
		s.logger.InfoContext(ctx, "a Slack reply for a workspace with no active install was dropped")
		return nil, nil
	}
	var metadata replyMetadata
	if err := json.Unmarshal([]byte(submission.View.PrivateMetadata), &metadata); err != nil || !metadata.valid() ||
		!userIDPattern.MatchString(submission.User.ID) {
		s.logger.WarnContext(ctx, "a Slack reply of a shape the surface does not know was dropped", "team_id", install.TeamID)
		return nil, nil
	}
	text := submission.View.State.Values[replyBlockID][replyInputID].Value
	switch trimmed := strings.TrimSpace(text); {
	case trimmed == "":
		return replyErrors("Write your reply first.")
	case utf8.RuneCountInString(trimmed) > MaxReplyRunes:
		return replyErrors(fmt.Sprintf("A reply holds at most %d characters.", MaxReplyRunes))
	}
	id, err := newID(s.random, "asi_")
	if err != nil {
		return nil, err
	}
	// The reply's thread is the message the button was on, as a click's is.
	inbound := Inbound{
		ID: id, OrganizationID: install.OrganizationID, TeamID: install.TeamID, Kind: InboundReply,
		ChannelID: metadata.ChannelID, UserID: submission.User.ID, MessageTS: metadata.MessageTS, ThreadTS: metadata.MessageTS,
		ActionID: ActionReply, ActionValue: metadata.Ticket, Text: cleanText(text, maxTextRunes), ReceivedAt: at,
	}
	notice, err := s.event(appsv1.SlackActionReceivedDetailType, appsv1.SlackActionReceived{
		InboundID: id, OrganizationID: install.OrganizationID, TeamID: install.TeamID, ReceivedAt: at,
	}, at)
	if err != nil {
		return nil, err
	}
	recorded, err := s.store.RecordInbound(ctx, key, at, inbound, notice)
	if err != nil {
		return nil, err
	}
	if recorded {
		// Neither the words nor the ticket: ids alone.
		s.logger.InfoContext(ctx, "slack reply recorded", "inbound_id", id, "organization_id", install.OrganizationID, "team_id", install.TeamID)
	}
	return nil, nil
}

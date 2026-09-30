package slackbot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
)

// Everything apps posts to Slack is one of these templates, rendered here from
// typed parameters. A caller supplies text and ids and never Block Kit, so the
// structure of a message (its blocks, its buttons, which of them can be
// clicked) is always apps', and a bug in a service that asks for a post can
// change what is said but never what can be clicked or who is pinged.
//
// Every string a caller supplies is escaped for Slack's markup before it is
// shown, so it cannot become a mention of everyone, a link that reads as
// another, or a button. It is also scrubbed of anything that looks like a
// credential, and of the app's own secrets. Nothing in a template ever
// carries a secret, and none has a place for one: secrets are never sent to
// Slack.
//
// Where a message may go is part of the template. A notice that names the
// asker and carries buttons goes to a direct message and nowhere public.
const (
	TemplateOwnerNotice   = "owner_notice"
	TemplateNoticeSettled = "notice_settled"
	TemplateAskAck        = "ask_ack"
	TemplateAnswer        = "answer"
	TemplateStatus        = "status"
	TemplateRefusal       = "refusal"
	TemplatePrivateNote   = "private_note"
	TemplateBypassNotice  = "bypass_notice"
)

// The buttons of an owner's notice, by their action ids: what the owner is
// asked to choose between. A button's value is its ticket, which says the
// same thing again, signed.
const (
	ActionSummary = "consent_summary"
	ActionAccess  = "consent_access"
	ActionDeny    = "consent_deny"
)

// Where a template may go.
type placement uint8

const (
	// inThread is a reply everyone in the channel sees.
	inThread placement = 1 << iota
	// toPerson is a message only one person in a thread sees.
	toPerson
	// inDM is a direct message from the bot.
	inDM
)

// Rendered is a message ready to post.
type Rendered struct {
	// Text is what notifications and screen readers show.
	Text   string
	Blocks []any
}

type templateSpec struct {
	placements placement
	render     func(r renderer, raw json.RawMessage) (Rendered, error)
}

var templates = map[string]templateSpec{
	TemplateOwnerNotice:   {inDM, renderOwnerNotice},
	TemplateNoticeSettled: {inDM, renderNoticeSettled},
	TemplateAskAck:        {inThread | toPerson, renderAskAck},
	TemplateAnswer:        {inThread | inDM, renderAnswer},
	TemplateStatus:        {inThread | toPerson | inDM, renderStatus},
	TemplateRefusal:       {inThread | toPerson, renderRefusal},
	TemplatePrivateNote:   {inThread | toPerson, renderPrivateNote},
	TemplateBypassNotice:  {inDM, renderBypassNotice},
}

// renderer is what rendering knows of the deployment: the pages a message may
// link to, and the secrets it must never show.
type renderer struct {
	webOrigins []string
	hidden     []string
}

// Render renders a template, and says where it may go. Params that do not fit
// the template are domain.ErrInvalid.
func (s *Service) render(template string, raw json.RawMessage) (Rendered, placement, error) {
	spec, ok := templates[template]
	if !ok {
		return Rendered{}, 0, fmt.Errorf("%q is not a template: %w", template, domain.ErrInvalid)
	}
	rendered, err := spec.render(s.renderer(), raw)
	if err != nil {
		return Rendered{}, 0, fmt.Errorf("%s: %w", template, err)
	}
	return rendered, spec.placements, nil
}

func (s *Service) renderer() renderer {
	return renderer{
		webOrigins: s.config.WebOrigins,
		hidden:     []string{s.config.App.ClientSecret.Reveal(), s.config.App.SigningSecret.Reveal()},
	}
}

// Caps on what a message shows. Slack takes 3000 characters in a section.
const (
	maxQuestionRunes = 1500
	maxAnswerRunes   = 20_000
	maxNameRunes     = 80
	maxSectionRunes  = 2800
	maxOwners        = 5
)

// ---- Building blocks ----

func section(markup string) map[string]any {
	return map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": markup}}
}

func contextBlock(markup string) map[string]any {
	return map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "text": markup}}}
}

func button(actionID, label, value string) map[string]any {
	return map[string]any{
		"type": "button", "action_id": actionID, "value": value,
		"text": map[string]any{"type": "plain_text", "text": label, "emoji": false},
	}
}

func actionsBlock(blockID string, elements ...any) map[string]any {
	return map[string]any{"type": "actions", "block_id": blockID, "elements": elements}
}

// ---- Escaping and scrubbing ----

// escape makes text safe to put in a message: Slack's three characters, which
// are all that make markup out of what a person typed.
func escape(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	return strings.ReplaceAll(text, ">", "&gt;")
}

// credentialPatterns are what a credential looks like, for the last check
// before something is said in Slack. The answer guard takes credentials out
// of an answer long before it gets here; this is for the one it missed.
var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`xox[abposr]-[A-Za-z0-9-]{8,}`),
	regexp.MustCompile(`xapp-[A-Za-z0-9-]{8,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA|AIDA|AROA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{30,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`(?i)\b(?:secret|token|password|passwd|api[_-]?key)\s*[:=]\s*["']?[A-Za-z0-9/+_.=-]{16,}`),
}

const scrubbed = "[credential removed]"

// scrub takes what looks like a credential out of text, and the secrets the
// surface itself holds.
func (r renderer) scrub(text string) string {
	for _, secret := range r.hidden {
		if len(secret) >= 8 {
			text = strings.ReplaceAll(text, secret, scrubbed)
		}
	}
	for _, pattern := range credentialPatterns {
		text = pattern.ReplaceAllString(text, scrubbed)
	}
	return text
}

// shown makes a caller's text ready to put in a message: scrubbed, cut to
// limit characters, and escaped.
func (r renderer) shown(text string, limit int) string {
	text = strings.TrimSpace(r.scrub(text))
	if utf8.RuneCountInString(text) > limit {
		text = strings.TrimSpace(string([]rune(text)[:limit])) + "…"
	}
	return escape(text)
}

// quoted is text as a block quote, every line of it.
func quoted(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

// ---- Parameters ----

func decodeParams(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		return fmt.Errorf("the params are not one JSON object of the template's fields: %w", domain.ErrInvalid)
	}
	if err := cleantext.Check(target); err != nil {
		return fmt.Errorf("the params: %v: %w", err, domain.ErrInvalid)
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, domain.ErrInvalid)...)
}

// person is someone a message names: by Slack id, which shows as a mention,
// or by name.
type person struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

// mention shows a person: as a mention when they have an id, which the
// message's readers see as their name, and otherwise as the name given.
func (r renderer) mention(who person, fallback string) (string, error) {
	switch {
	case who.UserID != "":
		if !userIDPattern.MatchString(who.UserID) {
			return "", invalid("%q is not a Slack user id", truncate(who.UserID, 32))
		}
		return "<@" + who.UserID + ">", nil
	case strings.TrimSpace(who.Name) != "":
		return "*" + r.shown(who.Name, maxNameRunes) + "*", nil
	}
	return fallback, nil
}

// named shows a name that must be given.
func (r renderer) named(name, field string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", invalid("%s is required", field)
	}
	return r.shown(name, maxNameRunes), nil
}

// asker reads the person who asks: by id or by name.
func (r renderer) asker(id, name string) (string, error) {
	if id == "" && strings.TrimSpace(name) == "" {
		return "", invalid("asker_user_id or asker_name is required")
	}
	return r.mention(person{UserID: id, Name: name}, "")
}

func requireText(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return invalid("%s is required", field)
	}
	return nil
}

// ---- The templates ----

type ownerNoticeParams struct {
	AskerUserID string `json:"asker_user_id"`
	AskerName   string `json:"asker_name"`
	Question    string `json:"question"`
	// Tickets are the buttons' values: one per choice, opaque to apps, which
	// only passes them along and never reads or logs them.
	Tickets struct {
		Summary string `json:"summary"`
		Access  string `json:"access"`
		Deny    string `json:"deny"`
	} `json:"tickets"`
}

// renderOwnerNotice is the owner's notice, in a direct message: who is asking,
// what, and three buttons. It says nothing else. The owner decides with what
// they see here and on the card in Oryxa, which holds the rest. The buttons'
// words are the card's, from web-next/src/components/request-card-format.ts
// (Share answer, Allow for 1 hour, Deny): keep the two the same.
func renderOwnerNotice(r renderer, raw json.RawMessage) (Rendered, error) {
	var p ownerNoticeParams
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
	for name, ticket := range map[string]string{"summary": p.Tickets.Summary, "access": p.Tickets.Access, "deny": p.Tickets.Deny} {
		if err := checkTicket(name, ticket); err != nil {
			return Rendered{}, err
		}
	}
	return Rendered{
		Text: asker + " is asking you a question through Oryxa.",
		Blocks: []any{
			section(asker + " is asking you through Oryxa:\n" + quoted(r.shown(p.Question, maxQuestionRunes))),
			actionsBlock("consent",
				button(ActionSummary, "Share answer", p.Tickets.Summary),
				button(ActionAccess, "Allow for 1 hour", p.Tickets.Access),
				button(ActionDeny, "Deny", p.Tickets.Deny),
			),
		},
	}, nil
}

func checkTicket(name, ticket string) error {
	switch {
	case ticket == "":
		return invalid("tickets.%s is required", name)
	case len(ticket) > maxActionValueBytes:
		return invalid("tickets.%s is longer than a button's value can be", name)
	}
	for i := 0; i < len(ticket); i++ {
		if ticket[i] < '!' || ticket[i] > '~' {
			return invalid("tickets.%s is not printable text without spaces", name)
		}
	}
	return nil
}

// The decisions a notice can settle into.
const (
	DecisionSummary   = "approved_summary"
	DecisionAccess    = "approved_access"
	DecisionDenied    = "denied"
	DecisionAnswered  = "answered_by_other"
	DecisionExpired   = "expired"
	DecisionWithdrawn = "withdrawn"
)

type noticeSettledParams struct {
	AskerUserID string `json:"asker_user_id"`
	AskerName   string `json:"asker_name"`
	Question    string `json:"question"`
	Decision    string `json:"decision"`
	// DecidedBy is who decided, or for answered_by_other who answered;
	// DecidedAt when.
	DecidedByUserID string     `json:"decided_by_user_id"`
	DecidedByName   string     `json:"decided_by_name"`
	DecidedAt       *time.Time `json:"decided_at"`
}

// renderNoticeSettled is a notice after it was decided: the same asker and
// question, no buttons, and one line saying what became of it. It replaces
// the notice with chat.update, so the buttons are gone whichever surface the
// owner decided on.
func renderNoticeSettled(r renderer, raw json.RawMessage) (Rendered, error) {
	var p noticeSettledParams
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
	var outcome string
	switch p.Decision {
	case DecisionSummary:
		// What the button the owner pressed said: Share answer.
		outcome = "answer shared"
	case DecisionAccess:
		outcome = "access for 1 hour"
	case DecisionDenied, DecisionWithdrawn, DecisionExpired, DecisionAnswered:
	default:
		return Rendered{}, invalid("decision %q is not one a notice settles into", truncate(p.Decision, 32))
	}
	who, err := r.mention(person{UserID: p.DecidedByUserID, Name: p.DecidedByName}, "")
	if err != nil {
		return Rendered{}, err
	}
	if who == "" && (p.Decision != DecisionExpired && p.Decision != DecisionWithdrawn) {
		return Rendered{}, invalid("decided_by_user_id or decided_by_name is required")
	}
	line := ""
	switch p.Decision {
	case DecisionSummary, DecisionAccess:
		line = "Approved by " + who + atTime(p.DecidedAt) + " · " + outcome
	case DecisionDenied:
		line = "Denied by " + who + atTime(p.DecidedAt)
	case DecisionAnswered:
		line = "Answered by " + who
	case DecisionExpired:
		line = "Expired without an answer"
	case DecisionWithdrawn:
		line = "Withdrawn"
	}
	return Rendered{
		Text: plain(line),
		Blocks: []any{
			section(asker + " asked you through Oryxa:\n" + quoted(r.shown(p.Question, maxQuestionRunes))),
			contextBlock(line),
		},
	}, nil
}

// atTime is " at 14:02" in the reader's own time zone, which Slack works out
// from a date token, with a fixed fallback for clients that cannot.
func atTime(at *time.Time) string {
	if at == nil || at.IsZero() {
		return ""
	}
	return fmt.Sprintf(" at <!date^%d^{date_short_pretty} {time}|%s>", at.Unix(), at.UTC().Format("2006-01-02 15:04 UTC"))
}

// plain is a line of markup as the plain text a notification shows: mentions
// and date tokens taken out of it.
func plain(markup string) string {
	markup = dateToken.ReplaceAllString(markup, "$1")
	return strings.ReplaceAll(markup, "*", "")
}

var dateToken = regexp.MustCompile(`<!date\^[0-9]+\^[^|>]*\|([^>]*)>`)

type askAckParams struct {
	Owners []person `json:"owners"`
	// OpenURL is the request in Oryxa, and is a page on the web app or
	// nothing.
	OpenURL string `json:"open_url"`
}

// renderAskAck is what the asker is told, only they see it, once the owners
// have been asked.
func renderAskAck(r renderer, raw json.RawMessage) (Rendered, error) {
	var p askAckParams
	if err := decodeParams(raw, &p); err != nil {
		return Rendered{}, err
	}
	if len(p.Owners) == 0 || len(p.Owners) > maxOwners {
		return Rendered{}, invalid("owners is one to %d people", maxOwners)
	}
	names := make([]string, 0, len(p.Owners))
	for _, owner := range p.Owners {
		shown, err := r.mention(owner, "")
		if err != nil {
			return Rendered{}, err
		}
		if shown == "" {
			return Rendered{}, invalid("an owner needs a user_id or a name")
		}
		names = append(names, shown)
	}
	line := "Asked " + joinNames(names)
	if p.OpenURL != "" {
		link, err := r.link(p.OpenURL)
		if err != nil {
			return Rendered{}, err
		}
		// A link's own ampersands are escaped, as Slack asks of all its markup:
		// the web app's links carry several parameters.
		line += " · <" + strings.ReplaceAll(link, "&", "&amp;") + "|Open in Oryxa>"
	}
	return Rendered{Text: plain(line), Blocks: []any{section(line)}}, nil
}

// link checks a page a message links to: https, on the web app's own origin,
// and with nothing that would make it read as another address.
func (r renderer) link(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", invalid("open_url is not an absolute URL")
	}
	origin := parsed.Scheme + "://" + parsed.Host
	for _, allowed := range r.webOrigins {
		if strings.EqualFold(strings.TrimRight(allowed, "/"), origin) {
			// Slack's own markup ends a link at these.
			if strings.ContainsAny(raw, "<>|\"' \t\r\n") {
				return "", invalid("open_url holds a character a link cannot")
			}
			return parsed.String(), nil
		}
	}
	return "", invalid("open_url is not a page of Oryxa's web app")
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// The modes an answer was allowed in, which its attribution says.
const (
	ModeSummary = "summary"
	ModeAccess  = "access"
	ModeLease   = "lease"
	ModeBypass  = "bypass"
)

type answerParams struct {
	Text      string `json:"text"`
	OwnerName string `json:"owner_name"`
	Mode      string `json:"mode"`
}

// renderAnswer is an owner's agent's answer, with who it is from and what the
// owner allowed. It is for a thread or a direct message. It names the owner
// and never mentions them: an answer must not page the person who has already
// been asked.
func renderAnswer(r renderer, raw json.RawMessage) (Rendered, error) {
	var p answerParams
	if err := decodeParams(raw, &p); err != nil {
		return Rendered{}, err
	}
	if err := requireText(p.Text, "text"); err != nil {
		return Rendered{}, err
	}
	owner, err := r.named(p.OwnerName, "owner_name")
	if err != nil {
		return Rendered{}, err
	}
	var line string
	switch p.Mode {
	case ModeSummary:
		line = "From " + owner + "'s agent · approved by " + owner + " (AI summary only)"
	case ModeAccess:
		line = "From " + owner + "'s agent · approved by " + owner
	case ModeLease:
		line = "From " + owner + "'s agent · approved by " + owner + " (1-hour access)"
	case ModeBypass:
		line = "From " + owner + "'s agent · shared under a priority rule, and " + owner + " was told"
	default:
		return Rendered{}, invalid("mode %q is not one an answer is allowed in", truncate(p.Mode, 32))
	}
	body := r.shown(p.Text, maxAnswerRunes)
	blocks := make([]any, 0, 8)
	for _, part := range splitSections(body, maxSectionRunes) {
		blocks = append(blocks, section(part))
	}
	blocks = append(blocks, contextBlock(line))
	return Rendered{Text: plain(line) + ": " + firstRunes(plain(body), 200), Blocks: blocks}, nil
}

// splitSections cuts text into parts of at most limit characters, at line
// breaks where it can, since Slack takes only so much in one block.
func splitSections(text string, limit int) []string {
	runes := []rune(text)
	var parts []string
	for len(runes) > limit {
		cut := limit
		// Cut at the last line break in the second half of the part, so a
		// paragraph is not split when it need not be.
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
	}
	return append(parts, strings.TrimSpace(string(runes)))
}

func firstRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "…"
}

// The states a status says, none of which carries anything of the answer.
const (
	StatusWaiting           = "waiting"
	StatusWorking           = "working"
	StatusHeld              = "held"
	StatusDenied            = "denied"
	StatusExpired           = "expired"
	StatusAnsweredElsewhere = "answered_elsewhere"
	// StatusFailed is a request that ended without an answer for a reason of
	// its own, not for want of time: the work could not be done, or Oryxa closed
	// it. It says no more than that, and never why.
	StatusFailed = "failed"
)

type statusParams struct {
	State     string `json:"state"`
	OwnerName string `json:"owner_name"`
}

// renderStatus tells the asker where their question is, without a word of its
// answer: a held answer is the owner's to review before anyone else reads it.
func renderStatus(r renderer, raw json.RawMessage) (Rendered, error) {
	var p statusParams
	if err := decodeParams(raw, &p); err != nil {
		return Rendered{}, err
	}
	owner := "the owner"
	if strings.TrimSpace(p.OwnerName) != "" {
		owner = r.shown(p.OwnerName, maxNameRunes)
	}
	var line string
	switch p.State {
	case StatusWaiting:
		line = "Waiting for " + owner + " to respond."
	case StatusWorking:
		line = owner + "'s agent is working on it."
	case StatusHeld:
		line = "The answer is held for " + owner + "'s review."
	case StatusDenied:
		line = owner + " chose not to share."
	case StatusExpired:
		line = "The request expired without an answer."
	case StatusAnsweredElsewhere:
		line = "Someone else answered."
	case StatusFailed:
		line = "The request could not be completed."
	default:
		return Rendered{}, invalid("state %q is not one a status says", truncate(p.State, 32))
	}
	return Rendered{Text: line, Blocks: []any{contextBlock(line)}}, nil
}

// The reasons a question is refused. The wording is fixed, and never quotes
// the question.
const (
	RefusalNotMember      = "not_member"
	RefusalOwnerNotMember = "owner_not_member"
	RefusalGroupDM        = "group_dm"
	RefusalExternal       = "external_channel"
	RefusalNoOwner        = "no_owner"
	RefusalTooManyAsks    = "too_many_asks"
	// The screen and department reasons give a brief private explanation,
	// without quoting the question or exposing the screen's checks or scores.
	RefusalScreenFailed    = "screen_failed"
	RefusalNotWork         = "not_work"
	RefusalInjection       = "injection"
	RefusalImplausible     = "implausible"
	RefusalNoGrant         = "no_grant"
	RefusalOwnerDepartment = "owner_department"
)

type refusalParams struct {
	Reason           string `json:"reason"`
	Hint             string `json:"hint"`
	OrganizationName string `json:"organization_name"`
	// UserID or Name is the person the refusal is about, for owner_not_member.
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

// renderRefusal says no to a question, briefly and only to the asker where it
// can. A reason it does not know is a plain no, so a newer refusal reaches
// people before its wording does.
func renderRefusal(r renderer, raw json.RawMessage) (Rendered, error) {
	var p refusalParams
	if err := decodeParams(raw, &p); err != nil {
		return Rendered{}, err
	}
	if p.Hint != "" && !(p.Reason == RefusalNoOwner && p.Hint == "self") && !(p.Reason == RefusalTooManyAsks && p.Hint == "too_many_people") {
		return Rendered{}, fmt.Errorf("the refusal hint does not match its reason: %w", domain.ErrInvalid)
	}
	var line string
	switch p.Reason {
	case RefusalNotMember:
		organization := "this organization"
		if strings.TrimSpace(p.OrganizationName) != "" {
			organization = r.shown(p.OrganizationName, maxNameRunes)
		}
		line = "Only members of " + organization + " in Oryxa can ask me."
	case RefusalOwnerNotMember:
		who, err := r.mention(person{UserID: p.UserID, Name: p.Name}, "They")
		if err != nil {
			return Rendered{}, err
		}
		line = who + " isn't in Oryxa, so I didn't ask them."
	case RefusalGroupDM:
		line = "I don't work in group messages. Ask me in a channel."
	case RefusalExternal:
		line = "I don't answer in channels shared with other organizations."
	case RefusalNoOwner:
		line = "Mention the person by picking them from the list."
		if p.Hint == "self" {
			line = "You named yourself. Pick someone else from the mention list so I can ask them."
		}
	case RefusalTooManyAsks:
		line = "You have too many open questions. Wait for some to be answered."
		if p.Hint == "too_many_people" {
			line = "Ask up to five people at a time. Pick fewer people from the mention list."
		}
	case RefusalScreenFailed:
		line = "I couldn't check that just now, so I didn't ask anyone. Try again in a moment."
	case RefusalNotWork:
		line = "I only pass on work questions."
	case RefusalNoGrant:
		line = "Give Oryxa's agent access to act for you in Settings, then ask again."
	case RefusalOwnerDepartment:
		who, err := r.mention(person{UserID: p.UserID, Name: p.Name}, "They")
		if err != nil {
			return Rendered{}, err
		}
		line = "I didn't ask " + who + ": this looks like a question for another team."
	case RefusalInjection:
		line = "I couldn't pass that request on safely, so I didn't ask anyone."
	case RefusalImplausible:
		line = "This request doesn't look appropriate for your role or team, so I didn't ask anyone."
	default:
		line = "I can't help with that here."
	}
	return Rendered{Text: plain(line), Blocks: []any{section(line)}}, nil
}

// renderPrivateNote is the thread's note that an answer went to one person
// alone, in a direct message.
func renderPrivateNote(_ renderer, raw json.RawMessage) (Rendered, error) {
	var p struct{}
	if err := decodeParams(raw, &p); err != nil {
		return Rendered{}, err
	}
	line := "Answered privately."
	return Rendered{Text: line, Blocks: []any{contextBlock(line)}}, nil
}

// The reasons an owner is told an answer was given without asking them.
const (
	BypassPriorityChannel = "priority_channel"
	BypassOrgChart        = "org_chart"
)

type bypassNoticeParams struct {
	AskerUserID string `json:"asker_user_id"`
	AskerName   string `json:"asker_name"`
	Question    string `json:"question"`
	Reason      string `json:"reason"`
	// ChannelID is where the question was asked, shown as a link to it.
	ChannelID string `json:"channel_id"`
}

// renderBypassNotice tells an owner, at once, that their agent answered
// without waiting for them, and why. It is the audit the owner sees.
func renderBypassNotice(r renderer, raw json.RawMessage) (Rendered, error) {
	var p bypassNoticeParams
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
	where := ""
	if p.ChannelID != "" {
		if !channelIDPattern.MatchString(p.ChannelID) {
			return Rendered{}, invalid("%q is not a Slack channel id", truncate(p.ChannelID, 32))
		}
		where = " in <#" + p.ChannelID + ">"
	}
	var why string
	switch p.Reason {
	case BypassPriorityChannel:
		why = "the channel is marked high priority"
	case BypassOrgChart:
		why = "of their role"
	default:
		return Rendered{}, invalid("reason %q is not one a bypass has", truncate(p.Reason, 32))
	}
	return Rendered{
		Text: plain(asker) + " asked you a question" + plain(where) + ", and your agent answered without asking you first.",
		Blocks: []any{
			section(asker + " asked you" + where + ", and your agent answered without asking you first, because " + why + ":\n" +
				quoted(r.shown(p.Question, maxQuestionRunes))),
		},
	}, nil
}

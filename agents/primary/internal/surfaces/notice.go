package surfaces

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

// The templates apps renders that this service posts. It never posts ask_ack:
// the router tells the asker who was asked, and telling them twice is noise.
const (
	templateOwnerNotice   = "owner_notice"
	templateNoticeSettled = "notice_settled"
	templateAnswer        = "answer"
	templateStatus        = "status"
	templatePrivateNote   = "private_note"
	templateBypassNotice  = "bypass_notice"
)

// askerName stands in for an asker the router never matched to a Slack user, in
// a message that has to name them.
const askerName = "A teammate"

// ownerNoticeParams are the parameters of apps' owner_notice template. apps
// decodes them strictly, so these are exactly the fields it takes.
type ownerNoticeParams struct {
	AskerUserID string        `json:"asker_user_id,omitempty"`
	AskerName   string        `json:"asker_name,omitempty"`
	Question    string        `json:"question"`
	Tickets     noticeTickets `json:"tickets"`
	// AnswerBy is "owner" for an ask that needs the owner's own answer, whose
	// notice leads with Reply yourself; left out otherwise.
	AnswerBy string `json:"answer_by,omitempty"`
}

// noticeTickets are the values of the notice's buttons: the three choices, and
// Reply yourself, which an ask of a secret has none of.
type noticeTickets struct {
	Summary string `json:"summary"`
	Access  string `json:"access"`
	Deny    string `json:"deny"`
	Reply   string `json:"reply,omitempty"`
}

// noticeSettledParams are the parameters of apps' notice_settled template.
type noticeSettledParams struct {
	AskerUserID     string     `json:"asker_user_id,omitempty"`
	AskerName       string     `json:"asker_name,omitempty"`
	Question        string     `json:"question"`
	Decision        string     `json:"decision"`
	DecidedByUserID string     `json:"decided_by_user_id,omitempty"`
	DecidedByName   string     `json:"decided_by_name,omitempty"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	// Reply is what the owner replied, as it was delivered, for a notice they
	// replied to; left out for an ask of a secret, whose answer is never in
	// Slack.
	Reply string `json:"reply,omitempty"`
}

// bypassNoticeParams are the parameters of apps' bypass_notice template.
type bypassNoticeParams struct {
	AskerUserID string `json:"asker_user_id,omitempty"`
	AskerName   string `json:"asker_name,omitempty"`
	Question    string `json:"question"`
	Reason      string `json:"reason"`
	ChannelID   string `json:"channel_id,omitempty"`
}

// noticeState is what the owner's notice should say now: statePending while the
// owner has not decided and the request has not lapsed, otherwise how it ended.
// It is empty when no notice was ever due: a lease or a bypass approved the
// request without asking its owner, and a request the owner decided elsewhere
// before the notice was sent has nothing to settle (notice reads that from
// whether a notice exists).
func (p *pass) noticeState() string {
	request := p.request
	if decision := request.Decision; decision != nil && decision.DecidedBy != nil &&
		(decision.DecidedBy.Kind == interactions.DecidedByLease || decision.DecidedBy.Kind == interactions.DecidedByBypass) {
		return ""
	}
	// Another owner's answer settled the ask, and this owner's notice says so
	// whatever they had chosen.
	if interactions.AnsweredElsewhere(p.ask, request) {
		return stateAnsweredByOther
	}
	if request.Status == interactions.StatusPending {
		if !request.ExpiresAt.After(p.now) {
			return stateExpired
		}
		return statePending
	}
	decision := request.Decision
	switch {
	case decision == nil:
		return stateExpired
	case decision.Decision == interactions.DecisionRejected:
		if decision.DecidedBy != nil && decision.DecidedBy.Kind == interactions.DecidedByMember {
			return stateDenied
		}
		// Oryxa closed it: there is no owner's word to show.
		return stateExpired
	case decision.Consent == interactions.ConsentReply:
		// The owner answered it themself, on either surface.
		return stateReplied
	case decision.Consent == interactions.ConsentAccess:
		return stateApprovedAccess
	}
	return stateApprovedSummary
}

// notice keeps the owner's notice as the ledger says it is: sent while the
// request waits for its owner, and replaced with how it ended once it does not.
func (p *pass) notice(ctx context.Context) error {
	link, linked, err := p.said(ctx, roleNotice)
	if err != nil {
		return err
	}
	if linked && link.State == stateFailed {
		return nil
	}
	switch state := p.noticeState(); {
	case state == "":
		return nil
	case state == statePending:
		if linked {
			return nil
		}
		return p.sendNotice(ctx)
	default:
		// Nothing was sent to replace when the owner decided before the notice went.
		if !linked || link.State == state {
			return nil
		}
		return p.settleNotice(ctx, link, state)
	}
}

// askerParams names the person who asked in a message the owner reads: by Slack
// user, which Slack shows as a mention, or failing that by a plain word.
func (p *pass) askerParams(ctx context.Context) (userID, name string, err error) {
	asker, err := p.member(ctx, p.request.Requester.UserID)
	if err != nil {
		return "", "", err
	}
	if asker.slackUserID != "" {
		return asker.slackUserID, "", nil
	}
	return "", askerName, nil
}

// sendNotice sends the owner their notice: who asked, what, and three buttons
// whose tickets the ledger makes for it, and a fourth, Reply yourself, which
// opens a modal for their own answer. An ask of a secret has no Reply yourself:
// its owner shares the secret once, in Oryxa, and nothing they write is put in
// Slack. An ask that needs the owner's own answer (interactions.OwnerFirst)
// leads with Reply yourself instead, and apps renders it so.
func (p *pass) sendNotice(ctx context.Context) error {
	request := p.request
	owner, err := p.member(ctx, request.Target.UserID)
	if err != nil {
		return err
	}
	if owner.slackUserID == "" {
		// The router matched them when it made the ask, so this is a match that
		// was forgotten since. They can still decide in Oryxa.
		return p.giveUp(ctx, roleNotice, errors.New("the owner is not matched to a Slack user"))
	}
	tickets, err := p.service.deps.Tickets.ConsentTickets(ctx, request.ID, noticeID(request.ID))
	switch {
	case errors.Is(err, interactions.ErrConflict), errors.Is(err, interactions.ErrExpired):
		// Decided, or lapsed, since this pass read the request. There are no
		// buttons to send, and the pass that follows says how it ended.
		return nil
	case errors.Is(err, interactions.ErrNotFound), errors.Is(err, interactions.ErrInvalid), errors.Is(err, interactions.ErrConsentUnavailable):
		return p.giveUp(ctx, roleNotice, fmt.Errorf("no tickets can be made for this request: %w", err))
	case err != nil:
		return fmt.Errorf("make the notice's tickets: %w", err)
	}
	askerUser, askerWord, err := p.askerParams(ctx)
	if err != nil {
		return err
	}
	buttons := noticeTickets{Summary: tickets.Summary, Access: tickets.Access, Deny: tickets.Deny}
	answerBy := ""
	if p.ask.Kind != interactions.AskSecret {
		buttons.Reply = tickets.Reply
		if interactions.OwnerFirst(request) {
			// It needs the owner's own answer: the notice leads with Reply
			// yourself, and offers to let their agent answer after it.
			answerBy = interactions.AnswerByOwner
		}
	}
	posted, sent, err := p.send(ctx, roleNotice, slackrouter.Post{
		Target:   directTarget(owner.slackUserID),
		Template: templateOwnerNotice,
		Params: ownerNoticeParams{
			AskerUserID: askerUser, AskerName: askerWord, Question: request.Task, Tickets: buttons, AnswerBy: answerBy,
		},
	})
	if err != nil || !sent {
		return err
	}
	expires := request.ExpiresAt.UTC()
	return p.putLink(ctx, roleNotice, posted.ChannelID, posted.TS, statePending, &expires)
}

// settleNotice replaces the owner's notice with how it ended, which takes its
// buttons away whichever surface the owner decided on.
func (p *pass) settleNotice(ctx context.Context, link Link, state string) error {
	request := p.request
	askerUser, askerWord, err := p.askerParams(ctx)
	if err != nil {
		return err
	}
	params := noticeSettledParams{AskerUserID: askerUser, AskerName: askerWord, Question: request.Task, Decision: state}
	switch state {
	case stateAnsweredByOther:
		userID, name, err := p.answeredBy(ctx)
		if err != nil {
			return err
		}
		params.DecidedByUserID, params.DecidedByName = userID, name
	case stateApprovedSummary, stateApprovedAccess, stateDenied:
		userID, name, err := p.deciderOf(ctx)
		if err != nil {
			return err
		}
		params.DecidedByUserID, params.DecidedByName = userID, name
		if request.Decision != nil && !request.Decision.DecidedAt.IsZero() {
			at := request.Decision.DecidedAt.UTC()
			params.DecidedAt = &at
		}
	case stateReplied:
		// The notice is the owner's own direct message: it says what they
		// replied, as it was delivered, unless it was an ask of a secret.
		if request.Decision != nil && !request.Decision.DecidedAt.IsZero() {
			at := request.Decision.DecidedAt.UTC()
			params.DecidedAt = &at
		}
		params.Reply = p.replyShown()
	}
	err = p.service.deps.Slack.Update(ctx, appsclient.Update{
		OrganizationID: request.OrganizationID, ChannelID: link.ChannelID, TS: link.TS,
		Template: templateNoticeSettled, Params: params,
	})
	switch {
	case err == nil:
	case slackrouter.IsPermanent(err) || errors.Is(err, slackrouter.ErrGone):
		// The message cannot be replaced: apps does not know it, or Slack will
		// not take the change. Nothing more can be done for it, so it is settled
		// as far as this service can settle it.
		p.service.logger.WarnContext(ctx, "the owner's notice could not be replaced and will not be tried again",
			"request_id", request.ID, "organization_id", request.OrganizationID, "state", state, "reason", err)
	default:
		return fmt.Errorf("replace the owner's notice: %w", err)
	}
	return p.service.links.SetLinkState(ctx, request.ID, roleNotice, state)
}

// deciderOf says who decided the request, as the notice names them: the owner,
// by Slack user when they have one, else by name. An owner who decided is the
// request's owner, whatever the recorded decider says of an agent acting for
// them.
func (p *pass) deciderOf(ctx context.Context) (userID, name string, err error) {
	owner, err := p.member(ctx, p.request.Target.UserID)
	if err != nil {
		return "", "", err
	}
	if owner.slackUserID != "" {
		return owner.slackUserID, "", nil
	}
	display, err := p.nameOf(ctx, p.request.Target)
	if err != nil {
		return "", "", err
	}
	return "", orDefault(display, "the owner"), nil
}

// answeredBy says who answered the ask, from the request that won it.
func (p *pass) answeredBy(ctx context.Context) (userID, name string, err error) {
	winner, found, err := p.winnerRequest(ctx)
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "another owner", nil
	}
	answerer, err := p.member(ctx, winner.Target.UserID)
	if err != nil {
		return "", "", err
	}
	if answerer.slackUserID != "" {
		return answerer.slackUserID, "", nil
	}
	display, err := p.nameOf(ctx, winner.Target)
	if err != nil {
		return "", "", err
	}
	return "", orDefault(display, "another owner"), nil
}

// bypassNotice tells the owner, once and at once, that their agent answered
// without asking them, and why. It is the audit the owner sees in Slack.
func (p *pass) bypassNotice(ctx context.Context) error {
	request := p.request
	decision := request.Decision
	if decision == nil || decision.DecidedBy == nil || decision.DecidedBy.Kind != interactions.DecidedByBypass {
		return nil
	}
	if _, said, err := p.said(ctx, roleBypassNotice); err != nil || said {
		return err
	}
	reason := consentpolicy.BypassReason(decision.DecidedBy.Reason)
	if reason != consentpolicy.PriorityChannel && reason != consentpolicy.OrgChart {
		// The template says why in words it has for these two, and refuses any
		// other. Telling the owner with words that are not true would be worse.
		return p.giveUp(ctx, roleBypassNotice, fmt.Errorf("the bypass reason %q is not one the notice has words for", reason))
	}
	owner, err := p.member(ctx, request.Target.UserID)
	if err != nil {
		return err
	}
	if owner.slackUserID == "" {
		return p.giveUp(ctx, roleBypassNotice, errors.New("the owner is not matched to a Slack user"))
	}
	askerUser, askerWord, err := p.askerParams(ctx)
	if err != nil {
		return err
	}
	posted, sent, err := p.send(ctx, roleBypassNotice, slackrouter.Post{
		Target:   directTarget(owner.slackUserID),
		Template: templateBypassNotice,
		Params: bypassNoticeParams{
			AskerUserID: askerUser, AskerName: askerWord, Question: request.Task,
			Reason: string(reason), ChannelID: p.ask.ChannelID,
		},
	})
	if err != nil || !sent {
		return err
	}
	return p.putLink(ctx, roleBypassNotice, posted.ChannelID, posted.TS, stateSent, nil)
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

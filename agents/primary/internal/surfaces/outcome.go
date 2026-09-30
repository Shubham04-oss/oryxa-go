package surfaces

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

// answerParams are the parameters of apps' answer template.
type answerParams struct {
	Text      string `json:"text"`
	OwnerName string `json:"owner_name"`
	Mode      string `json:"mode"`
}

// statusParams are the parameters of apps' status template, which says where a
// question is and never a word of its answer.
type statusParams struct {
	State     string `json:"state"`
	OwnerName string `json:"owner_name,omitempty"`
}

// The modes an answer says it was allowed in, and the states a status says, as
// apps' templates name them.
const (
	modeSummary = "summary"
	modeAccess  = "access"
	modeLease   = "lease"
	modeBypass  = "bypass"
	// modeReply is an answer its owner wrote themself, which says it is theirs
	// ("Raya replied:") and nothing of an agent's or of what they allowed.
	modeReply = "reply"

	statusHeld   = "held"
	statusDenied = "denied"
	// statusExpired is a request that ran out of time, and statusFailed one that
	// ended for a reason of its own: the work could not be done, or Oryxa closed
	// it. Neither says more.
	statusExpired = "expired"
	statusFailed  = "failed"
)

// noAnswerText is what an answer that has no words says, which is the fact.
const noAnswerText = "The agent finished the request without a written answer."

// outcomeKind is what the asker is to be told of how the request stands.
type outcomeKind int

const (
	// outcomeNone is a request still waiting for its owner or its owner's agent.
	outcomeNone outcomeKind = iota
	// outcomeElsewhere is one that another owner's answer settled: the asker has
	// that answer, and this request has nothing more to say.
	outcomeElsewhere
	// outcomeHeld is an answer held for its owner's review.
	outcomeHeld
	outcomeAnswered
	// outcomeDenied is an owner who chose not to share.
	outcomeDenied
	// outcomeClosed is a request that ended without an answer: it lapsed, or the
	// work could not be done.
	outcomeClosed
)

// outcomeKind reads how the request stands from the ledger's row. An answer is
// read only from a request that completed: the result of one held for review, or
// withheld, is never looked at.
func (p *pass) outcomeKind() outcomeKind {
	request := p.request
	if interactions.AnsweredElsewhere(p.ask, request) {
		return outcomeElsewhere
	}
	lapsed := !request.ExpiresAt.After(p.now)
	switch request.Status {
	case interactions.StatusPending, interactions.StatusApproved:
		if lapsed {
			return outcomeClosed
		}
		return outcomeNone
	case interactions.StatusReview:
		if lapsed {
			// An answer can no longer be reviewed once its request has lapsed:
			// it reads as the withholding the ledger's next sweep records.
			return outcomeClosed
		}
		return outcomeHeld
	case interactions.StatusCompleted:
		if request.Result == nil || request.Result.Status != interactions.StatusCompleted {
			return outcomeClosed
		}
		return outcomeAnswered
	case interactions.StatusRejected:
		if decision := request.Decision; decision != nil && decision.DecidedBy != nil && decision.DecidedBy.Kind == interactions.DecidedByMember {
			return outcomeDenied
		}
		return outcomeClosed
	case interactions.StatusWithheld:
		if review := request.Review; review != nil && review.DecidedBy.Kind == interactions.DecidedByMember {
			return outcomeDenied
		}
		return outcomeClosed
	}
	return outcomeClosed
}

// outcome tells the asker what the ledger says of how their request stands.
func (p *pass) outcome(ctx context.Context) error {
	switch p.outcomeKind() {
	case outcomeHeld:
		return p.tellHeld(ctx)
	case outcomeAnswered:
		return p.tellAnswer(ctx)
	case outcomeDenied:
		return p.tellAskerAlone(ctx, roleDenied, statusDenied)
	case outcomeClosed:
		return p.tellAskerAlone(ctx, roleClosed, p.closedStatus())
	}
	return nil
}

// closedStatus is how a request that ended without an answer is said to have
// ended. One still waiting, or with its answer waiting for review, that has run
// out of time, or an answer withheld because its time ran out in review, is
// expired. Every other ending is a failure, and is said to be: the work could
// not be done, or Oryxa closed the request, and to say the time ran out of one
// that failed is to say what did not happen.
func (p *pass) closedStatus() string {
	switch p.request.Status {
	case interactions.StatusPending, interactions.StatusApproved, interactions.StatusReview, interactions.StatusWithheld:
		return statusExpired
	}
	return statusFailed
}

// where is a place a message to the asker goes.
type where int

const (
	// inThread is the ask's own thread, which everyone in the channel reads.
	inThread where = iota
	// inDM is the asker's direct message with the bot.
	inDM
)

// destination says where what the owner's agent found goes: the thread, or the
// asker alone. Everything decided by a bypass goes where consentpolicy.AnswerFor
// says for the reason recorded with the decision, so that whoever reads the
// ledger reaches the place the router decided on. The thread is only the place
// while the channel is still what the bypass took it for: private, unshared and
// marked by an administrator. That is read again now, since an administrator can
// unmark a channel, and anyone can share it, between the decision and the answer;
// when it is not, the answer goes to the asker alone.
func (p *pass) destination(ctx context.Context) (where, error) {
	decision := p.request.Decision
	if decision == nil || decision.DecidedBy == nil || decision.DecidedBy.Kind != interactions.DecidedByBypass {
		return inThread, nil
	}
	reason := consentpolicy.BypassReason(decision.DecidedBy.Reason)
	if consentpolicy.AnswerFor(reason) != consentpolicy.InThread {
		return inDM, nil
	}
	channel, err := p.channelDetails(ctx)
	switch {
	case errors.Is(err, slackrouter.ErrGone), errors.Is(err, slackrouter.ErrUnreadable):
		// A channel Slack will not describe cannot be told from one that is
		// shared or no longer marked.
		p.rerouted(ctx, "the channel could not be read")
		return inDM, nil
	case err != nil:
		// Neither posting in the thread nor going private is right until it is
		// known what the channel is.
		return inThread, fmt.Errorf("read the channel's mark again: %w", err)
	}
	if channel.Private && channel.HighPriority && !channel.Shared && !channel.MPIM && !channel.IM {
		return inThread, nil
	}
	p.rerouted(ctx, "the channel is no longer private, unshared and marked")
	return inDM, nil
}

func (p *pass) rerouted(ctx context.Context, why string) {
	p.service.logger.WarnContext(ctx, "a bypassed answer goes to the asker alone: its channel no longer allows the thread",
		"request_id", p.request.ID, "organization_id", p.request.OrganizationID, "reason", why)
}

// tellHeld says an answer is held for its owner's review, and nothing of the
// answer: it goes where the answer would, so that a private answer is not
// announced in a public thread.
func (p *pass) tellHeld(ctx context.Context) error {
	if _, said, err := p.said(ctx, roleHeld); err != nil || said {
		return err
	}
	place, err := p.destination(ctx)
	if err != nil {
		return p.abandonOrRetry(ctx, roleHeld, err)
	}
	ownerName, err := p.ownerName(ctx)
	if err != nil {
		return err
	}
	target := p.threadTarget()
	if place == inDM {
		asker, err := p.member(ctx, p.request.Requester.UserID)
		if err != nil {
			return err
		}
		if asker.slackUserID == "" {
			return p.giveUp(ctx, roleHeld, errors.New("the asker is not matched to a Slack user"))
		}
		target = directTarget(asker.slackUserID)
	}
	posted, sent, err := p.send(ctx, roleHeld, slackrouter.Post{
		Target: target, Template: templateStatus, Params: statusParams{State: statusHeld, OwnerName: ownerName},
	})
	if err != nil || !sent {
		return err
	}
	return p.putLink(ctx, roleHeld, posted.ChannelID, posted.TS, stateSent, nil)
}

// tellAskerAlone says what the asker alone need hear: that their owner chose not
// to share, or that the request ended unanswered. Only they see it.
func (p *pass) tellAskerAlone(ctx context.Context, role, state string) error {
	if _, said, err := p.said(ctx, role); err != nil || said {
		return err
	}
	asker, err := p.member(ctx, p.request.Requester.UserID)
	if err != nil {
		return err
	}
	if asker.slackUserID == "" {
		return p.giveUp(ctx, role, errors.New("the asker is not matched to a Slack user"))
	}
	ownerName, err := p.ownerName(ctx)
	if err != nil {
		return err
	}
	posted, sent, err := p.send(ctx, role, slackrouter.Post{
		Target: p.askerInThreadTarget(asker.slackUserID), Template: templateStatus, Params: statusParams{State: state, OwnerName: ownerName},
	})
	if err != nil || !sent {
		return err
	}
	return p.putLink(ctx, role, posted.ChannelID, posted.TS, stateSent, nil)
}

// tellAnswer posts the answer once it is delivered: in the thread it was asked
// in, with who it is from and how far its owner let it go, or to the asker alone
// where a bypass says so, with a note in the thread that it was answered
// privately.
//
// A secret is never posted. The answer to an ask of a secret is read in Oryxa,
// where the owner shares the value as a one-time view; the thread is told only
// that it was answered privately, whatever the answer holds.
func (p *pass) tellAnswer(ctx context.Context) error {
	if _, said, err := p.said(ctx, roleAnswer); err != nil {
		return err
	} else if said {
		// The answer is said; its note in the thread may not be.
		return p.privateNote(ctx)
	}
	if p.ask.Kind == interactions.AskSecret {
		return p.privateNote(ctx)
	}
	place, err := p.destination(ctx)
	if err != nil {
		return p.abandonOrRetry(ctx, roleAnswer, err)
	}
	ownerName, err := p.ownerName(ctx)
	if err != nil {
		return err
	}
	target := p.threadTarget()
	if place == inDM {
		asker, err := p.member(ctx, p.request.Requester.UserID)
		if err != nil {
			return err
		}
		if asker.slackUserID == "" {
			// Nothing was delivered anywhere, so nothing is said to have been.
			return p.giveUp(ctx, roleAnswer, errors.New("the asker is not matched to a Slack user"))
		}
		target = directTarget(asker.slackUserID)
	}
	posted, sent, err := p.send(ctx, roleAnswer, slackrouter.Post{
		Target: target, Template: templateAnswer,
		Params: answerParams{Text: answerText(p.request.Result), OwnerName: ownerName, Mode: answerMode(p.request)},
	})
	if err != nil || !sent {
		return err
	}
	state := stateSent
	if place == inDM {
		state = stateSentPrivately
	}
	if err := p.putLink(ctx, roleAnswer, posted.ChannelID, posted.TS, state, nil); err != nil {
		return err
	}
	return p.privateNote(ctx)
}

// privateNote tells the thread that the answer went to one person, when it did:
// to the asker's direct message, or for an ask of a secret, to Oryxa. Where the
// answer went is what was recorded when it was posted, and is not changed by the
// channel changing since.
func (p *pass) privateNote(ctx context.Context) error {
	if _, said, err := p.said(ctx, rolePrivateNote); err != nil || said {
		return err
	}
	private := p.ask.Kind == interactions.AskSecret
	if !private {
		link, said, err := p.said(ctx, roleAnswer)
		if err != nil {
			return err
		}
		private = said && link.State == stateSentPrivately
	}
	if !private {
		return nil
	}
	posted, sent, err := p.send(ctx, rolePrivateNote, slackrouter.Post{Target: p.threadTarget(), Template: templatePrivateNote})
	if err != nil || !sent {
		return err
	}
	return p.putLink(ctx, rolePrivateNote, posted.ChannelID, posted.TS, stateSent, nil)
}

// ownerName is what an answer or a status calls the owner: the name they chose,
// else the name Slack has for them, else a plain word.
func (p *pass) ownerName(ctx context.Context) (string, error) {
	name, err := p.nameOf(ctx, p.request.Target)
	if err != nil {
		return "", err
	}
	return orDefault(name, "the owner"), nil
}

// replyShown is what the owner's own direct message says they replied: their
// reply as it was delivered. It is nothing for an ask of a secret, whose answer
// is never in Slack, and for an answer that is not theirs.
func (p *pass) replyShown() string {
	result := p.request.Result
	if p.ask.Kind == interactions.AskSecret || result == nil || !result.WrittenByOwner {
		return ""
	}
	if text := strings.TrimSpace(result.Content); text != "" {
		return text
	}
	return strings.TrimSpace(result.Summary)
}

// answerText is the words of an answer: what the owner's agent wrote for the
// asker, or failing that its summary. Only a delivered result is ever given to
// it: a held one is in Held, which is not looked at.
func answerText(result *agents.InteractionResult) string {
	if result == nil {
		return noAnswerText
	}
	if text := strings.TrimSpace(result.Content); text != "" {
		return text
	}
	if text := strings.TrimSpace(result.Summary); text != "" {
		return text
	}
	return noAnswerText
}

// answerMode is how far the answer was allowed to go, as its attribution says:
// a lease or a bypass approved it without the owner's click, and otherwise the
// owner's own choice did. An approval that says nothing of consent reads as the
// narrowest. An answer the owner wrote themself is theirs, whatever let it go,
// and says so instead.
func answerMode(request agents.Interaction) string {
	if request.Result != nil && request.Result.WrittenByOwner {
		return modeReply
	}
	if decision := request.Decision; decision != nil {
		if decision.DecidedBy != nil {
			switch decision.DecidedBy.Kind {
			case interactions.DecidedByBypass:
				return modeBypass
			case interactions.DecidedByLease:
				return modeLease
			}
		}
		if decision.Consent == interactions.ConsentAccess {
			return modeAccess
		}
	}
	return modeSummary
}

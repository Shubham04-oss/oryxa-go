// Package slackrouter turns what happens in Slack into what happens in Oryxa.
//
// apps keeps every verified Slack delivery, checks nothing more than its
// signature and workspace, and nudges over the bus with ids only. The router
// consumes those nudges as durable jobs, the way interactiondelivery does,
// reads each delivery back from apps with a service token, and routes it:
//
//   - A message that mentions the bot is an ask. The router says who is asking
//     and who is being asked, from Slack profiles and auth's members, has the
//     words screened (one Jev read: is it work, is it aimed at the agents, is
//     it plausible for this asker, does it ask for a credential, and does it
//     fit each owner's department), asks the policy (consentpolicy) whether
//     the ask is refused, needs the owner's consent or bypasses it, and files
//     the ask with the consent core, as an ask for a secret when the words ask
//     for a credential. It posts only what it must, each shown to the asker
//     alone: a refusal, who was left out, and who was asked. The screen fails
//     closed: with no Jev, or a Jev that fails or is slow, the ask is refused
//     and nobody is asked.
//   - A click on a button is handed, unchanged, to the consent core, with the
//     member who clicked: a click on a button of an owner's review of a held
//     answer to the review, and every other click to the decision. The router
//     never decides or reviews anything.
//   - A reply an owner sent from the modal a Reply yourself button opened is
//     handed, unchanged, to the consent core too, with the member who sent it:
//     the ticket the button carried and what they wrote, which is the answer
//     and is never logged.
//   - Everything else is dropped.
//
// What the router never does is decide an ask: the owner does, or a bypass the
// policy allows, and the consent core records it. It holds nothing worth
// stealing. A job is ids. A match between a Slack user and a member is kept for
// whoever must go from one to the other, and authorizes nothing: every message
// and click is matched again from a fresh Slack profile and auth's members.
// What people wrote stays in apps, which purges it.
//
// Where the router meets the rest of Oryxa it does so through the interfaces
// in contracts.go, defined here and satisfied by adapters the service wires.
package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Deps is what a Router is built from.
type Deps struct {
	Slack      Slack
	Members    Members
	Identities Identities
	Asks       Asks
	// Screen judges the words of an ask before anyone is asked or pinged.
	// Without one every mention is refused, and nobody is asked: an ask nobody
	// screened may not go ahead. NewJevScreen is the production one, and its
	// Jev is the one interactions and rooms use.
	Screen Screen
	// Directory reads the teams of the asker and of the people they name, as the
	// asker sees them, so the screen can judge whether a question fits each
	// owner's department, and refuses an asker who has given agents no standing
	// access (no_grant). Without one no department is known, no owner is left
	// out for theirs, and no grant is asked for. NewAskerDirectory is the
	// production one.
	Directory Directory
	// Redact takes what must never travel out of the words of an ask: they are
	// sent to the screen, whose model is another company's, and then to the
	// owner's agent as its task, which is kept in the ledger. An asker who
	// pastes a key into their question has sent it to Slack already, and that
	// is where it stops. Without one the words go as they are. It is applied
	// once, right after the words are cleaned and before anything reads them,
	// and it must return text the screen can still judge: masking a credential
	// with a name for its kind does.
	Redact func(text string) string
	// Authority answers whether an asker outranks an owner. Without one, nobody
	// does, and no ask bypasses on authority.
	Authority Authority
	// Asked records the ask a mention became. Without it, a retry of a mention
	// whose ask was made but whose job did not finish is routed again from the
	// start, and the ask it made counts against its asker.
	Asked Asked
	// Refused keeps the members' mentions that were refused, which count toward
	// their limit of open asks for RefusedAskWindow. Without it a refusal costs
	// its asker nothing, and they may ask again, and be screened again, at once.
	Refused Refused
	// OpenURL is the page of Oryxa's web app that shows an ask, for the link in
	// the asker's acknowledgement. Without it the acknowledgement has no link.
	// It must be a page on an origin apps has been told is the web app's.
	OpenURL func(askID string) string
	// SkipAck leaves the acknowledgement of an ask to the surfaces service,
	// which can tell the asker who was asked from the ask's own event. Set it
	// where that service does, so the asker is not told twice: the router's
	// acknowledgement and that one are different messages.
	SkipAck bool
	Logger  *slog.Logger
}

// Router routes one job at a time. It is safe to use from many goroutines.
type Router struct {
	deps     Deps
	resolver *resolver
	logger   *slog.Logger
}

func NewRouter(deps Deps) (*Router, error) {
	switch {
	case deps.Slack == nil:
		return nil, errors.New("slackrouter: a router needs apps' Slack API")
	case deps.Members == nil:
		return nil, errors.New("slackrouter: a router needs to read members")
	case deps.Identities == nil:
		return nil, errors.New("slackrouter: a router needs somewhere to keep the Slack users it matches")
	case deps.Asks == nil:
		return nil, errors.New("slackrouter: a router needs the consent core")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if deps.Screen == nil {
		logger.Warn("slackrouter: no screen is configured, so every mention will be refused and nobody asked")
	}
	return &Router{
		deps:     deps,
		logger:   logger,
		resolver: &resolver{members: deps.Members, identities: deps.Identities, logger: logger},
	}, nil
}

// Handle routes one job. It returns nil when the job is done, an error marked
// Permanent when it can never be done, and any other error when trying again
// may work. Done includes a refusal: an ask that was refused and told so was
// answered.
func (router *Router) Handle(ctx context.Context, job Job) error {
	if job.Kind == KindMessage && job.AskID != "" {
		// An earlier attempt made this mention's ask and failed after it.
		// Making it again would count it against its asker's own limit.
		router.logger.InfoContext(ctx, "a mention's ask was already made; nothing is done again",
			"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "ask_id", job.AskID)
		return nil
	}
	inbound, err := router.deps.Slack.Inbound(ctx, job.InboundID)
	switch {
	case errors.Is(err, ErrGone):
		return Permanent(fmt.Errorf("the inbound row is gone: %w", err))
	case err != nil:
		return fmt.Errorf("read the inbound row: %w", err)
	}
	// The event named an organization and a workspace, and the row belongs to
	// one. A row that says otherwise is not what the event was about, and
	// nothing is done with it.
	if (inbound.ID != "" && inbound.ID != job.InboundID) || inbound.OrganizationID != job.OrganizationID ||
		inbound.TeamID != job.TeamID || !sameKind(job.Kind, inbound.Kind) {
		return Permanent(errors.New("the inbound row is not the one its event named"))
	}
	switch inbound.Kind {
	case KindMessage:
		return router.mention(ctx, job, inbound)
	case KindAction:
		return router.click(ctx, job, inbound)
	case KindReply:
		return router.reply(ctx, job, inbound)
	}
	return Permanent(fmt.Errorf("unknown inbound kind %q", inbound.Kind))
}

// sameKind is whether an inbound row is of the kind its job's event named: a
// message's event names a message, and an interaction's names a click or a
// reply sent from a modal, which apps announces the same way.
func sameKind(job, inbound string) bool {
	return job == inbound || job == KindAction && inbound == KindReply
}

// click hands a click to the consent core, as the member who made it: to the
// review for a button of an owner's review, and to the decision for any other.
func (router *Router) click(ctx context.Context, job Job, inbound Inbound) error {
	click := inbound.Click
	switch {
	case click == nil:
		return Permanent(errors.New("an action delivery carries no click"))
	case click.UserID == "" || click.Value == "":
		return Permanent(errors.New("the click names no user or carries no ticket"))
	}
	profiles, err := router.profiles(ctx, job, []string{click.UserID})
	if err != nil {
		return err
	}
	clicker, err := router.resolver.resolve(ctx, job.OrganizationID, job.TeamID, click.UserID, profiles)
	if err != nil {
		return err
	}
	if !clicker.Member {
		// Nobody who is not a member of the organization can be the owner a
		// ticket names. The click is dropped without a word: the message the
		// button is on is the owner's own.
		return Permanent(fmt.Errorf("the click was not made by a member: %s", clicker.Why))
	}
	// The ticket is opaque to the router, and never logged: it is a credential
	// for the one decision, or review, it names. Which of the two the click is
	// for is only where the ticket goes: the core refuses a ticket of the other
	// kind.
	var handed error
	if IsReviewAction(click.ActionID) {
		handed = router.deps.Asks.Review(ctx, click.Value, clicker.Person.UserID)
	} else {
		handed = router.deps.Asks.Consent(ctx, click.Value, clicker.Person.UserID, SurfaceSlack)
	}
	if handed != nil {
		// What the core says goes into logs and onto the job's row. It should not
		// quote the ticket, and if some layer of it ever does, it is not carried on.
		return fmt.Errorf("hand the click to the consent core: %w", scrub(handed, click.Value, "[ticket]"))
	}
	router.logger.InfoContext(ctx, "a click was handed to the consent core",
		"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "action_id", click.ActionID)
	return nil
}

// minScrubbedReply is the shortest reply whose words are taken out of an error
// that quotes them. The ledger never quotes them; a shorter one would take out
// letters of the error's own words.
const minScrubbedReply = 8

// reply hands a reply an owner sent from Slack to the ledger, as the member who
// sent it, as click hands a click: the router records nothing, and the ledger
// checks the ticket, the sender and the words. Neither the ticket nor the words
// are logged or carried on in an error.
func (router *Router) reply(ctx context.Context, job Job, inbound Inbound) error {
	reply := inbound.Reply
	switch {
	case reply == nil:
		return Permanent(errors.New("a reply delivery carries no reply"))
	case reply.UserID == "" || reply.Value == "":
		return Permanent(errors.New("the reply names no user or carries no ticket"))
	case strings.TrimSpace(reply.Text) == "":
		return Permanent(errors.New("the reply holds no words"))
	}
	profiles, err := router.profiles(ctx, job, []string{reply.UserID})
	if err != nil {
		return err
	}
	sender, err := router.resolver.resolve(ctx, job.OrganizationID, job.TeamID, reply.UserID, profiles)
	if err != nil {
		return err
	}
	if !sender.Member {
		// Nobody who is not a member of the organization can be the owner a
		// ticket names; the modal was their own, and nothing is said to them.
		return Permanent(fmt.Errorf("the reply was not sent by a member: %s", sender.Why))
	}
	if err := router.deps.Asks.Reply(ctx, reply.Value, sender.Person.UserID, reply.Text); err != nil {
		err = scrub(err, reply.Value, "[ticket]")
		if len(strings.TrimSpace(reply.Text)) >= minScrubbedReply {
			err = scrub(err, strings.TrimSpace(reply.Text), "[reply]")
		}
		return fmt.Errorf("hand the reply to the consent core: %w", err)
	}
	router.logger.InfoContext(ctx, "a reply was handed to the consent core",
		"inbound_id", job.InboundID, "organization_id", job.OrganizationID)
	return nil
}

// profiles reads the profiles of Slack users in the job's workspace.
func (router *Router) profiles(ctx context.Context, job Job, userIDs []string) (map[string]Profile, error) {
	list, err := router.deps.Slack.Profiles(ctx, job.OrganizationID, job.TeamID, userIDs)
	if err != nil {
		return nil, fmt.Errorf("read Slack profiles: %w", err)
	}
	profiles := make(map[string]Profile, len(list))
	for _, profile := range list {
		profiles[profile.UserID] = profile
	}
	return profiles, nil
}

// scrubbedError is an error with a secret taken out of its message. The error
// it wraps is still what errors.Is and errors.As see.
type scrubbedError struct {
	err     error
	message string
}

func (e *scrubbedError) Error() string { return e.message }
func (e *scrubbedError) Unwrap() error { return e.err }

// scrub returns err with every occurrence of secret in its message replaced by
// mask, or err itself when it never says it.
func scrub(err error, secret, mask string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return &scrubbedError{err: err, message: strings.ReplaceAll(err.Error(), secret, mask)}
}

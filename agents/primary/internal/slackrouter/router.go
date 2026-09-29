// Package slackrouter turns what happens in Slack into what happens in Oryxa.
//
// apps keeps every verified Slack delivery, checks nothing more than its
// signature and workspace, and nudges over the bus with ids only. The router
// consumes those nudges as durable jobs, the way interactiondelivery does,
// reads each delivery back from apps with a service token, and routes it:
//
//   - A message that mentions the bot is an ask. The router says who is asking
//     and who is being asked, from Slack profiles and auth's members, asks the
//     policy (consentpolicy) whether the ask is refused, needs the owner's
//     consent or bypasses it, and files the ask with the consent core. It posts
//     only what it must: a refusal, or an acknowledgement, each shown to the
//     asker alone.
//   - A click on a button is handed, unchanged, to the consent core, with the
//     member who clicked. The router never decides.
//   - Everything else is dropped.
//
// It decides nothing itself and holds nothing worth stealing: a job is ids, a
// match between a Slack user and a member is kept for whoever must go from one
// to the other, and what people wrote stays in apps, which purges it.
//
// Where the router meets the rest of Oryxa it does so through the interfaces
// in contracts.go, defined here and satisfied by adapters the service wires.
package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Deps is what a Router is built from.
type Deps struct {
	Slack      Slack
	Members    Members
	Identities Identities
	Asks       Asks
	// Authority answers whether an asker outranks an owner. Without one, nobody
	// does, and no ask bypasses on authority.
	Authority Authority
	// Asked records the ask a mention became. Without it, a retry of a mention
	// whose ask was made but whose job did not finish is routed again from the
	// start, and the ask it made counts against its asker.
	Asked  Asked
	Logger *slog.Logger
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
		inbound.TeamID != job.TeamID || inbound.Kind != job.Kind {
		return Permanent(errors.New("the inbound row is not the one its event named"))
	}
	switch inbound.Kind {
	case KindMessage:
		return router.mention(ctx, job, inbound)
	case KindAction:
		return router.click(ctx, job, inbound)
	}
	return Permanent(fmt.Errorf("unknown inbound kind %q", inbound.Kind))
}

// click hands a click to the consent core, as the member who made it.
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
	// for the one decision it names.
	if err := router.deps.Asks.Consent(ctx, click.Value, clicker.Person.UserID, SurfaceSlack); err != nil {
		return fmt.Errorf("hand the click to the consent core: %w", err)
	}
	router.logger.InfoContext(ctx, "a click was handed to the consent core",
		"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "action_id", click.ActionID)
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

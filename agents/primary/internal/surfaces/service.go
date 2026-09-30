package surfaces

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

// Deps is what a Service is built from. Everything but Notes is required.
type Deps struct {
	Requests Requests
	Asks     Asks
	Tickets  Tickets
	Slack    Slack
	Users    SlackUsers
	// Notes writes the asker's line in their own agent chat. Without it none is
	// written, and everything else is said as it would be.
	Notes Notes
	// Members, when set, has a note written only for an asker who still belongs
	// to the organization. Without it a note is written for whoever asked.
	Members Members
	Logger  *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// Service does one request's pass: it reads the request from the ledger and says
// in Slack, and in the asker's agent chat, whatever they do not yet say.
type Service struct {
	deps   Deps
	links  links
	logger *slog.Logger
	now    func() time.Time
}

// NewService returns the service that does a pass, over the links the store
// keeps.
func NewService(links links, deps Deps) (*Service, error) {
	switch {
	case links == nil:
		return nil, errors.New("surfaces: a service needs somewhere to keep what it posted")
	case deps.Requests == nil:
		return nil, errors.New("surfaces: a service needs to read the ledger's requests")
	case deps.Asks == nil:
		return nil, errors.New("surfaces: a service needs to read the ledger's asks")
	case deps.Tickets == nil:
		return nil, errors.New("surfaces: a service needs the ledger to make its notices' tickets")
	case deps.Slack == nil:
		return nil, errors.New("surfaces: a service needs apps' Slack API")
	case deps.Users == nil:
		return nil, errors.New("surfaces: a service needs to find the Slack user a member is")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Service{deps: deps, links: links, logger: logger, now: now}, nil
}

// New returns the worker that keeps Slack in step with the ledger: a service
// over the store, and the queue the store is.
func New(store *Store, deps Deps, config WorkerConfig) (*Worker, error) {
	if store == nil {
		return nil, errors.New("surfaces: a worker needs its store")
	}
	service, err := NewService(store, deps)
	if err != nil {
		return nil, err
	}
	return NewWorker(store, service, config, service.logger), nil
}

// Handle does the pass a job asks for. It returns nil when everything the
// ledger says is said in Slack, or has been given up on, an error marked
// slackrouter.Permanent when the request is not one it can ever be done for,
// and any other error when trying again may work. What was done before a
// failure stays done: each message is said once, whatever else has to be tried
// again.
func (service *Service) Handle(ctx context.Context, job Job) error {
	request, err := service.deps.Requests.Get(ctx, job.RequestID)
	switch {
	case errors.Is(err, interactions.ErrNotFound):
		return slackrouter.Permanent(fmt.Errorf("the request is gone: %w", err))
	case err != nil:
		return fmt.Errorf("read the request: %w", err)
	case request.OrganizationID != job.OrganizationID:
		// The event named an organization and the request belongs to another. It
		// is not what the event was about, and nothing is said for it.
		return slackrouter.Permanent(errors.New("the request is not in the organization its event named"))
	case request.Origin != interactions.OriginSlack || strings.TrimSpace(request.AskID) == "":
		return slackrouter.Permanent(errors.New("the request was not asked in Slack"))
	}
	ask, err := service.deps.Asks.Ask(ctx, request.AskID)
	switch {
	case errors.Is(err, interactions.ErrNotFound):
		return slackrouter.Permanent(fmt.Errorf("the request's ask is gone: %w", err))
	case err != nil:
		return fmt.Errorf("read the request's ask: %w", err)
	case ask.OrganizationID != request.OrganizationID || ask.Origin != interactions.OriginSlack:
		return slackrouter.Permanent(errors.New("the request's ask is not one asked in Slack in its organization"))
	}
	p := &pass{service: service, request: request, ask: ask, now: service.now().UTC(), people: map[string]person{}}
	return p.run(ctx)
}

// pass is one look at one request: the request and its ask as the ledger holds
// them, and what has been found out about them meanwhile. It is used by a single
// goroutine.
type pass struct {
	service *Service
	request agents.Interaction
	ask     interactions.Ask
	now     time.Time

	// people is who each member is in Slack, found when first needed.
	people map[string]person
	// winner is the request that won the ask, read when a notice must say who.
	winner       agents.Interaction
	winnerLoaded bool
	// channel is the conversation the request was asked in, read once.
	channel       appsclient.ChannelDetails
	channelErr    error
	channelLoaded bool
}

// person is a member as Slack knows them: the Slack user the router matched,
// and the name Slack gave. Both are empty for a member it never matched.
type person struct {
	slackUserID string
	name        string
}

// run does each step in turn. A step that fails does not stop the others: what
// each says does not depend on the rest, and the pass is tried again for the
// step that failed alone, the others being done already.
func (p *pass) run(ctx context.Context) error {
	var failures []error
	for _, step := range []struct {
		name string
		do   func(context.Context) error
	}{
		{"the owner's notice", p.notice},
		{"the bypass notice", p.bypassNotice},
		{"the outcome for the asker", p.outcome},
		{"the asker's note", p.note},
	} {
		if err := step.do(ctx); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", step.name, err))
		}
	}
	return errors.Join(failures...)
}

// idempotencyKey is the key of a request's message in a role: apps sends one
// message for one key, so a pass that posted and failed to record it, and is
// tried again, sends it once.
func idempotencyKey(requestID, role string) string { return "surfaces:" + requestID + ":" + role }

// noticeID is the notice's own id, which its buttons' tickets name. It is the
// request's, so that a notice posted again carries the same tickets.
func noticeID(requestID string) string { return "notice_" + requestID }

// member finds who a member is in Slack. A member the router never matched is
// nobody in particular, not a failure: the notice can still be said by name,
// where it can be said at all.
func (p *pass) member(ctx context.Context, memberUserID string) (person, error) {
	if known, ok := p.people[memberUserID]; ok {
		return known, nil
	}
	identity, err := p.service.deps.Users.SlackUserFor(ctx, p.request.OrganizationID, memberUserID)
	switch {
	case errors.Is(err, slackrouter.ErrNoIdentity):
		p.people[memberUserID] = person{}
		return person{}, nil
	case err != nil:
		return person{}, fmt.Errorf("find the Slack user of a member: %w", err)
	}
	found := person{slackUserID: identity.SlackUserID, name: strings.TrimSpace(identity.DisplayName)}
	p.people[memberUserID] = found
	return found, nil
}

// nameOf is what to call the member a party is: the name they chose, else the
// name Slack has for them, else nothing. An address is never used: Slack is not
// told anyone's sign-in address.
func (p *pass) nameOf(ctx context.Context, party agents.InteractionParty) (string, error) {
	if name := strings.TrimSpace(party.UserDisplayName); name != "" {
		return name, nil
	}
	known, err := p.member(ctx, party.UserID)
	return known.name, err
}

// channelDetails reads the conversation the request was asked in, now, once for
// the pass.
func (p *pass) channelDetails(ctx context.Context) (appsclient.ChannelDetails, error) {
	if !p.channelLoaded {
		p.channel, p.channelErr = p.service.deps.Slack.ChannelDetails(ctx, p.request.OrganizationID, p.ask.ChannelID)
		p.channelLoaded = true
	}
	return p.channel, p.channelErr
}

// winnerRequest reads the request that won the ask.
func (p *pass) winnerRequest(ctx context.Context) (agents.Interaction, bool, error) {
	if !p.winnerLoaded {
		winner, err := p.service.deps.Requests.Get(ctx, p.ask.WinnerRequestID)
		switch {
		case errors.Is(err, interactions.ErrNotFound):
			p.winnerLoaded = true
			return agents.Interaction{}, false, nil
		case err != nil:
			return agents.Interaction{}, false, fmt.Errorf("read the request that won the ask: %w", err)
		}
		p.winner, p.winnerLoaded = winner, true
	}
	return p.winner, p.winner.ID != "", nil
}

// send posts a message of the request in a role, once. It reports whether the
// message is in Slack: false with a nil error is a message that cannot be sent
// and never will be, which is recorded so that no pass tries again; an error is
// one to try again.
func (p *pass) send(ctx context.Context, role string, post slackrouter.Post) (appsclient.Posted, bool, error) {
	post.OrganizationID = p.request.OrganizationID
	post.IdempotencyKey = idempotencyKey(p.request.ID, role)
	posted, err := p.service.deps.Slack.Send(ctx, post)
	if err != nil {
		return appsclient.Posted{}, false, p.abandonOrRetry(ctx, role, err)
	}
	return posted, true, nil
}

// abandonOrRetry says what a failed call comes to. What apps understood and
// refused, and what is gone, is given up on and recorded against the role; the
// rest is the pass's to try again.
func (p *pass) abandonOrRetry(ctx context.Context, role string, err error) error {
	if slackrouter.IsPermanent(err) || errors.Is(err, slackrouter.ErrGone) || errors.Is(err, slackrouter.ErrUnreadable) {
		return p.giveUp(ctx, role, err)
	}
	return fmt.Errorf("%s: %w", role, err)
}

// giveUp records that a role's message will not be sent, and says so in the log,
// with why in apps' own words and never in the words of a question or an answer.
func (p *pass) giveUp(ctx context.Context, role string, cause error) error {
	p.service.logger.WarnContext(ctx, "a Slack message could not be sent and will not be tried again",
		"request_id", p.request.ID, "organization_id", p.request.OrganizationID, "role", role, "reason", cause)
	return p.putLink(ctx, role, "", "", stateFailed, nil)
}

// putLink records where a role's message is, and what it says.
func (p *pass) putLink(ctx context.Context, role, channelID, ts, state string, expiresAt *time.Time) error {
	return p.service.links.PutLink(ctx, Link{
		RequestID: p.request.ID, Role: role, OrganizationID: p.request.OrganizationID, TeamID: p.ask.TeamID,
		AskerUserID: p.request.Requester.UserID, OwnerUserID: p.request.Target.UserID,
		ChannelID: channelID, TS: ts, State: state, ExpiresAt: expiresAt,
	})
}

// said reports whether a role's message was sent or given up on.
func (p *pass) said(ctx context.Context, role string) (Link, bool, error) {
	return p.service.links.Link(ctx, p.request.ID, role)
}

// The Slack targets a request's messages go to.
//
// threadTarget is a reply everyone in the channel sees, askerTarget a direct
// message from the bot, and askerInThreadTarget a message in the thread that
// only the asker sees.
func (p *pass) threadTarget() slackrouter.Target {
	return slackrouter.Target{ChannelID: p.ask.ChannelID, ThreadTS: p.ask.ThreadTS}
}

func (p *pass) askerInThreadTarget(askerSlackUserID string) slackrouter.Target {
	return slackrouter.Target{ChannelID: p.ask.ChannelID, ThreadTS: p.ask.ThreadTS, UserID: askerSlackUserID}
}

func directTarget(slackUserID string) slackrouter.Target {
	return slackrouter.Target{UserID: slackUserID}
}

// Package interactions owns durable, cross-user agent requests. It records a
// strict request/decision/result contract and leaves delivery to EventBridge
// subscribers; it never calls a target agent directly.
package interactions

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
	"github.com/Shubham04-oss/oryxa-go/internal/jev"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

var (
	ErrNotFound  = errors.New("agent request not found")
	ErrInvalid   = errors.New("invalid agent request")
	ErrForbidden = errors.New("agent request is not available to this agent")
	ErrConflict  = errors.New("agent request cannot make that transition")
	ErrExpired   = errors.New("agent request has expired")
	// ErrIdempotencyConflict reports an idempotency key sent again with a
	// different request than the one it already names.
	ErrIdempotencyConflict = errors.New("that idempotency key already names a different agent request; send a new key for a new request")
)

const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusExpired   = "expired"

	DecisionApproved = "approved"
	DecisionRejected = "rejected"

	// AgentApproved and AgentDeclined are a decision an agent made as the
	// member who asked reads it: fixed words, never the agent's own message.
	// What an agent writes could carry what it read of its member's (a run
	// deciding holds whatever it may reach), and no guard reads a decision:
	// only an answer is held for its member's review. The agent's message
	// stays in its own thread, with the call that carried it. A member
	// deciding in their own session writes to the member who asked in their
	// own words.
	AgentApproved = "Approved by their agent."
	AgentDeclined = "Declined by their agent."

	// Who recorded a decision (agents.InteractionDecider.Kind).
	DecidedByMember = "member"
	DecidedByAgent  = "agent"
	DecidedBySystem = "system"
	// DecidedByLease is Oryxa approving a follow-up in a Slack thread under a
	// lease its owner gave: their decision, named, and made ahead of time.
	DecidedByLease = "lease"
	// DecidedByBypass is Oryxa approving a request that needs no owner's
	// click. Nobody is named: it is a rule that decided, and its reason says
	// which.
	DecidedByBypass = "bypass"

	// ApprovalAgent lets the addressed agent, or its member's primary agent,
	// decide: a request made in a teamspace to a specialist its owner
	// contributed there with automatic delegation, for as long as they keep
	// it so. An older build stored it for every request made outside a
	// teamspace as well; those are held all the same (HeldForOwner).
	ApprovalAgent = "agent"
	// ApprovalOwner holds the request for the addressed agent's member, who
	// decides it in their own session and never through an agent: every
	// request made outside a teamspace, one made in a teamspace to a
	// teammate's primary agent, or to a specialist contributed there with
	// request delegation, and one its owner has since taken deciding back
	// for, or that could not be read again.
	ApprovalOwner = "owner"

	defaultExpiry   = 24 * time.Hour
	minimumExpiry   = 5 * time.Minute
	maximumExpiry   = 7 * 24 * time.Hour
	maxPurposeRunes = 500
	maxTaskRunes    = 10_000
	maxMessageRunes = 4_000
	maxContentRunes = 20_000
	maxContextItems = 32
	maxListItems    = 32
)

const (
	// BoxInbox lists the requests addressed to the member's agents.
	BoxInbox = "inbox"
	// BoxSent lists the requests the member's agents sent.
	BoxSent = "sent"
	// BoxAll lists both, which is how either party reads the requests
	// between two members.
	BoxAll = "all"
)

// Query narrows a listing of the requests a member's agent may read. Every
// field but Box is optional.
type Query struct {
	Box string
	// TeamspaceID keeps only the requests made in that teamspace.
	TeamspaceID string
	// WithUserID keeps only the requests whose other party is that member.
	WithUserID string
	// Origin keeps only the requests made there: OriginAgent or OriginSlack.
	Origin string
	// AskID keeps only the requests of that ask.
	AskID string
	Limit int
}

// Repository is the transactional interaction ledger. Store implements it.
type Repository interface {
	// Create records a new request under an idempotency key. Under a key
	// already used it stores nothing and returns the request stored there.
	Create(context.Context, agents.Interaction, string) (agents.Interaction, error)
	// FindByKey is the request one organization's requesting agent stored
	// under an idempotency key, or ErrNotFound.
	FindByKey(ctx context.Context, organizationID, requesterAgentID, key string) (agents.Interaction, error)
	Get(context.Context, string) (agents.Interaction, error)
	// List selects, newest first, the requests of one organization that one
	// member is a party to, as the query narrows them.
	List(context.Context, string, string, Query) ([]agents.Interaction, error)
	// Decide records a pending request's decision and who made it.
	Decide(context.Context, string, agents.InteractionDecisionInput, agents.InteractionDecider, time.Time) (agents.Interaction, error)
	// Respond records an approved request's answer as the guard left it:
	// delivered, or held in review.
	Respond(ctx context.Context, id string, answer Answer, at time.Time) (agents.Interaction, error)
	// Review records a review of an answer held for its addressed member:
	// sent as held, sent masked, or withheld, by them, or withheld by Oryxa
	// with the reason.
	Review(ctx context.Context, id string, review agents.InteractionReview, at time.Time) (agents.Interaction, error)
	// Hold keeps a request for its owner's own decision: a pending one is
	// marked held, and an approved one has its approval withdrawn and is
	// pending, held, again.
	Hold(ctx context.Context, id string, at time.Time) (agents.Interaction, error)
	// Close ends, as Oryxa and with the reason, a request that no longer
	// stands in its teamspace: a pending one is rejected, an approved one
	// fails before its work begins.
	Close(ctx context.Context, id, reason string, at time.Time) (agents.Interaction, error)
}

// Teamspaces places the addressed agent in the teamspace a request is made
// in, against the live team directory: the requester must be on the team now,
// the asking agent and the target must each hold a seat there now.
// teamspaces.Service implements it; RequestSeat's errors
// (teamspaces.ErrNotFound, ErrForbidden, ErrInvalid, ErrDisabled) reach the
// caller as they are.
type Teamspaces interface {
	// RequestSeat reads the organization from directory, which Create has
	// already loaded with the requester's own token, rather than loading it
	// a second time.
	RequestSeat(ctx context.Context, requester agents.TeamspaceActor, directory agents.OrganizationDirectory, teamspaceID, targetAgentID string) (agents.TeamspaceSeat, error)
	// RequestStanding reads again where a request stands in the teamspace it
	// was made in, from directory as its addressed member sees the
	// organization; a request that no longer stands is reported as Lapsed.
	RequestStanding(ctx context.Context, organizationID string, directory agents.OrganizationDirectory, teamspaceID string, requester, target agents.InteractionParty) (agents.TeamspaceStanding, error)
}

type Config struct {
	Repository Repository
	Directory  agents.Directory
	// Policy reads the last applied organization workspace policy. Nil uses
	// the original defaults for deployments without the workspace service.
	Policy policy.Provider
	// Teamspaces enables requests made inside a teamspace. Without it a
	// request naming one is refused.
	Teamspaces Teamspaces
	// Tokens mints the addressed member's agent token, under their standing
	// grant, to read a request's teamspace again when nobody's session
	// carries a token of theirs: in the waker, or in a run they are not
	// watching. Without it, or without their grant, such a request cannot
	// be read again and is held for them rather than decided by an agent.
	Tokens memberauth.TokenSource
	// Jev judges which of its target's toolboxes a request made in a
	// teamspace that names none needs (judgeTools); (*jev.Client).Ask is it.
	// Without it, no TYPESAFE_API_KEY, such a request asks for none.
	Jev func(context.Context, jev.Request) (jev.Result, error)
	// JevTimeout bounds asking Jev; DefaultJevTimeout when zero.
	JevTimeout time.Duration
	// Guard reads every answer another member will read before they read
	// it: *answerguard.Guard, when AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID names
	// one. Without it answers are delivered as given.
	Guard Guard
	// GuardPolicy says how strictly Guard reads an answer for its owner, and
	// whether and how its relevance check runs: *guardpolicy.Service. Without
	// it answers are read as strictly as can be and the relevance check does
	// not run, since nothing says how the organization wants an off-topic
	// answer treated.
	GuardPolicy GuardPolicy
	// ConsentGuard reads the answer to a request made from Slack once Guard
	// has: *consentguard.Guard. It holds what goes further than its owner
	// allowed, does not stand on what the run read, is about a denied topic, or
	// has a credential in it. Without it such an answer is read by Guard alone,
	// as every other answer is.
	ConsentGuard ConsentGuard
	// Consents is the Slack consent loop's side of the ledger: asks, leases and
	// the owner's choices. Store implements it, and is used when Repository is
	// one and this is nil. Without either, the ask, consent and lease methods
	// answer ErrConsentUnavailable.
	Consents Consents
	// Agents finds a member's primary agent, which CreateAsk files its
	// requests between: managedagents.Service, which needs no member's token.
	// Without it CreateAsk answers ErrConsentUnavailable.
	Agents PrimaryAgents
	// TicketKey signs the tickets on the buttons of an owner's Slack notice
	// (AGENTS_PRIMARY_SLACK_TICKET_KEY), at least 32 bytes. Without one no
	// ticket is made or accepted: an owner's click in Slack decides nothing.
	TicketKey []byte
	// Logger records what Jev judged and what the guard found, by kind;
	// discarded when nil.
	Logger *slog.Logger
	Now    func() time.Time
}

type Service struct {
	repository Repository
	directory  agents.Directory
	teamspaces Teamspaces
	tokens     memberauth.TokenSource
	jev        func(context.Context, jev.Request) (jev.Result, error)
	jevTimeout time.Duration
	guard      Guard
	logger     *slog.Logger
	now        func() time.Time
	policy     policy.Provider

	guardPolicy  GuardPolicy
	consentGuard ConsentGuard
	consents     Consents
	primaries    PrimaryAgents
	tickets      *Tickets
}

func New(config Config) (*Service, error) {
	if config.Repository == nil {
		return nil, errors.New("interactions: a repository is required")
	}
	if config.Directory == nil {
		return nil, errors.New("interactions: an organization directory is required")
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	jevTimeout := config.JevTimeout
	if jevTimeout <= 0 {
		jevTimeout = DefaultJevTimeout
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	consentGuard := config.ConsentGuard
	if isNoConsentGuard(consentGuard) {
		consentGuard = nil
	}
	consents := config.Consents
	if consents == nil {
		if store, ok := config.Repository.(Consents); ok {
			consents = store
		}
	}
	var tickets *Tickets
	if len(config.TicketKey) > 0 {
		var err error
		if tickets, err = NewTickets(config.TicketKey); err != nil {
			return nil, fmt.Errorf("interactions: %w", err)
		}
	}
	return &Service{
		repository: config.Repository, directory: config.Directory, teamspaces: config.Teamspaces, tokens: config.Tokens, policy: config.Policy,
		jev: config.Jev, jevTimeout: jevTimeout, guard: config.Guard, logger: logger, now: now,
		guardPolicy: config.GuardPolicy, consentGuard: consentGuard, consents: consents, primaries: config.Agents, tickets: tickets,
	}, nil
}

func (service *Service) Create(ctx context.Context, actor agents.InteractionActor, token, idempotencyKey string, input agents.InteractionCreateInput) (agents.Interaction, error) {
	if err := validateActor(actor); err != nil {
		return agents.Interaction{}, err
	}
	input = cleanCreate(input)
	if err := validateCreate(input); err != nil {
		return agents.Interaction{}, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if utf8.RuneCountInString(idempotencyKey) > 200 {
		return agents.Interaction{}, fmt.Errorf("%w: idempotency key holds at most 200 characters", ErrInvalid)
	}
	if strings.TrimSpace(token) == "" {
		return agents.Interaction{}, fmt.Errorf("%w: member token is required to resolve the target", ErrInvalid)
	}
	// A retry is answered from the ledger before anything is looked up, so
	// it gets back the request it made even when the directory or the seat
	// that request was made from has since changed.
	if idempotencyKey != "" {
		stored, err := service.repository.FindByKey(ctx, actor.OrganizationID, actor.AgentID, idempotencyKey)
		switch {
		case err == nil:
			return service.replay(actor, stored, input)
		case !errors.Is(err, ErrNotFound):
			return agents.Interaction{}, fmt.Errorf("find agent request by idempotency key: %w", err)
		}
	}
	directory, err := service.directory.Load(ctx, actor.OrganizationID, token)
	if err != nil {
		return agents.Interaction{}, fmt.Errorf("load organization directory: %w", err)
	}
	var target agents.DirectoryAgent
	found := false
	for _, person := range directory.People {
		for _, candidate := range person.Agents {
			if candidate.ID == input.TargetAgentID {
				target, found = candidate, true
				break
			}
		}
		if found {
			break
		}
	}
	if !found || target.OwnerUserID == "" {
		return agents.Interaction{}, ErrNotFound
	}
	if target.OwnerUserID == actor.UserID {
		return agents.Interaction{}, fmt.Errorf("%w: cross-user requests must target another member", ErrInvalid)
	}
	approval, err := service.approval(ctx, actor, directory, input.TeamspaceID, target)
	if err != nil {
		return agents.Interaction{}, err
	}
	// Only once the request is known to be one that may be made is Jev
	// asked what it needs; whatever Jev says, or fails to, it is made.
	tools, toolsJudgedBy := service.requestedTools(ctx, input)
	id, err := newID("irq_")
	if err != nil {
		return agents.Interaction{}, err
	}
	now := service.now().UTC()
	settings, err := service.organizationPolicy(ctx, actor.OrganizationID)
	if err != nil {
		return agents.Interaction{}, err
	}
	expires := time.Duration(input.ExpiresInSeconds) * time.Second
	if settings.ExpireAfter > 0 {
		if expires > settings.ExpireAfter {
			return agents.Interaction{}, fmt.Errorf("%w: expires_in_seconds exceeds the organization's consent expiry", ErrInvalid)
		}
		if expires == 0 {
			expires = settings.ExpireAfter
		}
	}
	if expires == 0 {
		expires = defaultExpiry
	}
	request := agents.Interaction{
		ID: id, OrganizationID: actor.OrganizationID, TeamspaceID: input.TeamspaceID, Approval: approval,
		Requester: agents.InteractionParty{UserID: actor.UserID, AgentID: actor.AgentID, AgentName: actor.AgentName},
		Target: agents.InteractionParty{
			UserID: target.OwnerUserID, AgentID: target.ID, AgentName: target.Name,
			UserEmail: target.OwnerEmail, UserDisplayName: target.OwnerDisplayName,
		},
		Purpose: input.Purpose, Task: input.Task, Context: nonNilContext(input.Context),
		RequestedTools: nonNilStrings(tools), ToolsJudgedBy: toolsJudgedBy, RequestedPermissions: nonNilStrings(input.RequestedPermissions),
		Status: StatusPending, Revision: 1, ExpiresAt: now.Add(expires), CreatedAt: now, UpdatedAt: now,
		Origin: OriginAgent,
	}
	if idempotencyKey == "" {
		idempotencyKey = id
	}
	created, err := service.repository.Create(ctx, request, idempotencyKey)
	if err != nil {
		return agents.Interaction{}, err
	}
	if created.ID != request.ID {
		// Another create under this key landed first.
		return service.replay(actor, created, input)
	}
	return created, nil
}

func (service *Service) organizationPolicy(ctx context.Context, organizationID string) (policy.Settings, error) {
	if service.policy == nil {
		return policy.Settings{}, nil
	}
	settings, err := service.policy.Get(ctx, organizationID)
	if err != nil {
		return policy.Settings{}, fmt.Errorf("read organization workspace policy: %w", err)
	}
	return settings, nil
}

// replay answers a create whose idempotency key already names a stored
// request: that request, when this is the same one sent again, and
// ErrIdempotencyConflict when it is another — never a different request as
// if it were the new one.
func (service *Service) replay(actor agents.InteractionActor, stored agents.Interaction, input agents.InteractionCreateInput) (agents.Interaction, error) {
	if !sameRequest(actor, stored, input) {
		return agents.Interaction{}, ErrIdempotencyConflict
	}
	return service.viewFor(actor, stored), nil
}

// sameRequest is whether a stored request is the one input asks for again:
// the same requester, target, teamspace, purpose, task, context and asks.
// Both sides are compared as cleaned, which is how the request was stored.
// The tools compared are the ones its requester named: what Jev judged a
// request that named none needs is not something the requester sent.
// Omitted tools and an explicit empty list are different requests.
func sameRequest(actor agents.InteractionActor, stored agents.Interaction, input agents.InteractionCreateInput) bool {
	namedTools := namedByRequester(stored)
	return stored.OrganizationID == actor.OrganizationID &&
		stored.Requester.UserID == actor.UserID && stored.Requester.AgentID == actor.AgentID &&
		stored.Target.AgentID == input.TargetAgentID && stored.TeamspaceID == input.TeamspaceID &&
		stored.Purpose == input.Purpose && stored.Task == input.Task &&
		slices.Equal(stored.Context, input.Context) &&
		(namedTools == nil) == (input.RequestedTools == nil) &&
		slices.EqualFunc(namedTools, input.RequestedTools, strings.EqualFold) &&
		slices.Equal(stored.RequestedPermissions, input.RequestedPermissions)
}

// namedByRequester is the tools a stored request's requester named: none
// when Jev, or nobody, chose what it asks for.
func namedByRequester(stored agents.Interaction) []string {
	if stored.ToolsJudgedBy == ToolsJudgedByJev || stored.ToolsJudgedBy == ToolsJudgedByNone {
		return nil
	}
	return stored.RequestedTools
}

// HeldForOwner is whether only the addressed agent's member may decide a
// request, in their own session. It fails closed: any approval but the
// agent's own holds the request, one this code does not know included, and
// so does one naming none. A request made outside any teamspace is held
// whatever it names: no standing rule of its member's lets an agent decide
// what is asked of it there, so the "agent" an older build stored for every
// such request decides nothing.
func HeldForOwner(request agents.Interaction) bool {
	return request.TeamspaceID == "" || request.Approval != ApprovalAgent
}

// approval is who decides a new request, which follows the target's standing
// and nothing the requester sends. Outside a teamspace its owner does: whether
// to send one there is the requester's choice, and no standing rule of the
// owner's covers it. Inside one, a specialist its owner contributed with
// automatic delegation decides for itself, while a teammate's primary agent,
// or a specialist contributed with request delegation, waits for its owner.
func (service *Service) approval(ctx context.Context, actor agents.InteractionActor, directory agents.OrganizationDirectory, teamspaceID string, target agents.DirectoryAgent) (string, error) {
	if teamspaceID == "" {
		return ApprovalOwner, nil
	}
	if service.teamspaces == nil {
		return "", fmt.Errorf("%w: requests inside a teamspace are not available on this server", ErrInvalid)
	}
	seat, err := service.teamspaces.RequestSeat(ctx, agents.TeamspaceActor{
		OrganizationID: actor.OrganizationID, UserID: actor.UserID,
		AgentID: actor.AgentID, AgentName: actor.AgentName, AgentKind: actor.AgentKind,
	}, directory, teamspaceID, target.ID)
	if err != nil {
		return "", err
	}
	if seat.AgentID != target.ID || seat.OwnerUserID != target.OwnerUserID {
		return "", fmt.Errorf("%w: target_agent_id does not hold a seat of its owner's in that teamspace", ErrInvalid)
	}
	if ownersToDecide(seat) {
		return ApprovalOwner, nil
	}
	return ApprovalAgent, nil
}

func (service *Service) Inbox(ctx context.Context, actor agents.InteractionActor, limit int) ([]agents.Interaction, error) {
	return service.List(ctx, actor, Query{Box: BoxInbox, Limit: limit})
}

func (service *Service) Sent(ctx context.Context, actor agents.InteractionActor, limit int) ([]agents.Interaction, error) {
	return service.List(ctx, actor, Query{Box: BoxSent, Limit: limit})
}

// List returns, newest first, the requests the actor may read that the query
// selects. A request is only ever listed to its two parties: the member who
// asked and the member asked, as canRead has it, and a specialist sees only
// the requests it sent or was sent. Asked as either teammate with the other
// as WithUserID, it is the requests between them in both directions.
func (service *Service) List(ctx context.Context, actor agents.InteractionActor, query Query) ([]agents.Interaction, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	query = cleanQuery(query)
	if err := validateQuery(actor, query); err != nil {
		return nil, err
	}
	query.Limit = clamp(query.Limit)
	requests, err := service.repository.List(ctx, actor.OrganizationID, actor.UserID, query)
	if err != nil {
		return nil, err
	}
	return service.views(actor, filterRequests(requests, func(request agents.Interaction) bool {
		return listed(actor, query, request)
	})), nil
}

func (service *Service) Get(ctx context.Context, actor agents.InteractionActor, id string) (agents.Interaction, error) {
	if err := validateActor(actor); err != nil {
		return agents.Interaction{}, err
	}
	request, err := service.repository.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return agents.Interaction{}, err
	}
	if request.OrganizationID != actor.OrganizationID || !canRead(actor, request) {
		return agents.Interaction{}, ErrNotFound
	}
	return service.viewFor(actor, request), nil
}

// Decide records a decision an agent makes, through its interactions tool:
// the addressed agent's own, or its member's primary agent mediating. A
// request held for its owner (HeldForOwner), every one made outside a
// teamspace among them, is never an agent's to decide, whichever agent asks,
// and one made in a teamspace is read again first (agentMayDecide): held for
// its owner if they have since taken deciding back, closed if it no longer
// stands.
func (service *Service) Decide(ctx context.Context, actor agents.InteractionActor, id string, input agents.InteractionDecisionInput) (agents.Interaction, error) {
	return service.decide(ctx, actor, "", id, input, false)
}

// DecideAsOwner records the decision of the member whose agent a request
// addresses, made in their own signed-in session: the actor must be marked
// agents.SessionMember, which only the HTTP API's member path does, so no
// run's actor can reach it — and agents' tools see agents.InteractionService,
// which has no such method. It is the one way a request held for its owner
// is decided. The member may decide a request their agent could decide as
// well, as their primary agent could. token is their own, from that
// session: approving a request made in a teamspace reads it again with it
// first (ownerMayApprove), and closes it instead when it no longer stands.
func (service *Service) DecideAsOwner(ctx context.Context, actor agents.InteractionActor, token, id string, input agents.InteractionDecisionInput) (agents.Interaction, error) {
	return service.decide(ctx, actor, token, id, input, true)
}

func (service *Service) decide(ctx context.Context, actor agents.InteractionActor, token, id string, input agents.InteractionDecisionInput, asOwner bool) (agents.Interaction, error) {
	// Who decides is proved by the session, not by which method was called:
	// only the member's own signed-in session decides as the owner, and it
	// never decides as an agent.
	if asOwner && actor.Session != agents.SessionMember {
		return agents.Interaction{}, fmt.Errorf("%w: only the member, in their own signed-in session, decides as the owner", ErrForbidden)
	}
	if !asOwner && actor.Session == agents.SessionMember {
		return agents.Interaction{}, fmt.Errorf("%w: a member decides in their own session as the owner, not as an agent", ErrForbidden)
	}
	request, err := service.Get(ctx, actor, id)
	if err != nil {
		return agents.Interaction{}, err
	}
	if request.Target.UserID != actor.UserID {
		return agents.Interaction{}, ErrForbidden
	}
	if !asOwner {
		if actor.AgentID != request.Target.AgentID && actor.AgentKind != "primary" {
			return agents.Interaction{}, ErrForbidden
		}
		if HeldForOwner(request) {
			return agents.Interaction{}, fmt.Errorf("%w: this request is held for your member's own decision in Oryxa, and no agent can approve or reject it; tell your member it is waiting for them", ErrForbidden)
		}
	}
	if request.Status == StatusExpired {
		return agents.Interaction{}, ErrExpired
	}
	input = cleanDecision(input)
	if err := validateDecision(request, input); err != nil {
		return agents.Interaction{}, err
	}
	if request.TeamspaceID != "" {
		switch {
		case !asOwner:
			if err := service.agentMayDecide(ctx, request); err != nil {
				return agents.Interaction{}, err
			}
		case input.Decision == DecisionApproved:
			if strings.TrimSpace(token) == "" {
				return agents.Interaction{}, fmt.Errorf("%w: member token is required to approve a request made in a teamspace", ErrInvalid)
			}
			if err := service.ownerMayApprove(ctx, request, token); err != nil {
				return agents.Interaction{}, err
			}
		}
	}
	decider := agents.InteractionDecider{Kind: DecidedByMember, UserID: actor.UserID, Surface: SurfaceWeb}
	if !asOwner {
		decider = agents.InteractionDecider{Kind: DecidedByAgent, UserID: actor.UserID, AgentID: actor.AgentID}
		input.Message = AgentApproved
		if input.Decision == DecisionRejected {
			input.Message = AgentDeclined
		}
	}
	return service.viewed(actor)(service.repository.Decide(ctx, request.ID, input, decider, service.now().UTC()))
}

// DecidedByItsOwner is whether a request's decision is its addressed
// member's own, made in their signed-in session. A decision that names no
// decider — one an older build recorded — never is.
func DecidedByItsOwner(request agents.Interaction) bool {
	return request.Decision != nil && request.Decision.DecidedBy != nil &&
		request.Decision.DecidedBy.Kind == DecidedByMember && request.Decision.DecidedBy.UserID == request.Target.UserID
}

func (service *Service) Respond(ctx context.Context, actor agents.InteractionActor, id string, input agents.InteractionResponseInput) (agents.Interaction, error) {
	request, err := service.Get(ctx, actor, id)
	if err != nil {
		return agents.Interaction{}, err
	}
	if request.Target.UserID != actor.UserID || actor.AgentID != request.Target.AgentID && actor.AgentKind != "primary" {
		return agents.Interaction{}, ErrForbidden
	}
	if request.Status == StatusExpired {
		return agents.Interaction{}, ErrExpired
	}
	input = cleanResponse(input)
	if err := validateResponse(request, input); err != nil {
		return agents.Interaction{}, err
	}
	settings, err := service.organizationPolicy(ctx, request.OrganizationID)
	if err != nil {
		// Work has already completed. Preserve it for its owner instead of
		// causing a retry to repeat provider actions or discard the answer.
		service.logger.ErrorContext(ctx, "hold completed answer because workspace policy could not be read", "request_id", request.ID, "error", err)
		settings.HoldForReview = true
	}
	// Another member reads what is recorded here, so the guard reads it
	// first, outside the ledger's transaction.
	answer := service.answer(ctx, actor, request, input)
	if settings.HoldForReview && request.Requester.UserID != request.Target.UserID && answer.Result != nil {
		answer.Held = &agents.InteractionHeld{Result: *answer.Result}
		answer.Result = nil
	}
	return service.viewed(actor)(service.repository.Respond(ctx, request.ID, answer, service.now().UTC()))
}

func (service *Service) view(request agents.Interaction) agents.Interaction {
	if request.TeamspaceID == "" {
		// Its member decides a request made outside a teamspace, whatever an
		// older build stored for it (HeldForOwner), so it reads as held for
		// them, and their own session offers them the decision.
		request.Approval = ApprovalOwner
	}
	expired := !request.ExpiresAt.After(service.now())
	switch {
	case (request.Status == StatusPending || request.Status == StatusApproved) && expired:
		request.Status = StatusExpired
	case request.Status == StatusReview && expired:
		// An answer can no longer be reviewed once its request has expired,
		// so it reads as the withholding the next sweep records
		// (Store.WithholdExpired), whether or not a sweep runs here.
		request.Status, request.Result, request.Held = StatusWithheld, nil, nil
		request.Review = &agents.InteractionReview{
			Action: ReviewWithhold, DecidedBy: agents.InteractionDecider{Kind: DecidedBySystem},
			DecidedAt: request.ExpiresAt, Reason: ExpiredBeforeReview,
		}
	}
	return request
}

func (service *Service) views(actor agents.InteractionActor, requests []agents.Interaction) []agents.Interaction {
	if requests == nil {
		return []agents.Interaction{}
	}
	for index := range requests {
		requests[index] = service.viewFor(actor, requests[index])
	}
	return requests
}

func validateActor(actor agents.InteractionActor) error {
	if strings.TrimSpace(actor.OrganizationID) == "" || strings.TrimSpace(actor.UserID) == "" || strings.TrimSpace(actor.AgentID) == "" || strings.TrimSpace(actor.AgentName) == "" {
		return fmt.Errorf("%w: organization, member and persistent agent identity and name are required", ErrInvalid)
	}
	if actor.AgentKind != "primary" && actor.AgentKind != "specialist" {
		return fmt.Errorf("%w: requester agent kind is required", ErrInvalid)
	}
	return nil
}

func cleanCreate(input agents.InteractionCreateInput) agents.InteractionCreateInput {
	input.TargetAgentID = strings.TrimSpace(input.TargetAgentID)
	input.TeamspaceID = strings.TrimSpace(input.TeamspaceID)
	input.Purpose = cleanText(input.Purpose)
	input.Task = cleanText(input.Task)
	// Omitted tools may be selected for a direct teamspace request; an
	// explicit empty list deliberately requests no lookups.
	if input.RequestedTools != nil {
		input.RequestedTools = cleanTools(input.RequestedTools)
	}
	input.RequestedPermissions = cleanList(input.RequestedPermissions)
	for index := range input.Context {
		input.Context[index].Label = cleanLine(input.Context[index].Label)
		input.Context[index].Value = cleanText(input.Context[index].Value)
	}
	return input
}

func validateCreate(input agents.InteractionCreateInput) error {
	switch {
	case input.TargetAgentID == "":
		return fmt.Errorf("%w: target_agent_id is required", ErrInvalid)
	case utf8.RuneCountInString(input.TargetAgentID) > 128:
		return fmt.Errorf("%w: target_agent_id holds at most 128 characters", ErrInvalid)
	case !validID(input.TeamspaceID):
		return fmt.Errorf("%w: teamspace_id must be an id of at most 128 characters", ErrInvalid)
	case input.Purpose == "" || utf8.RuneCountInString(input.Purpose) > maxPurposeRunes:
		return fmt.Errorf("%w: purpose is required and holds at most %d characters", ErrInvalid, maxPurposeRunes)
	case input.Task == "" || utf8.RuneCountInString(input.Task) > maxTaskRunes:
		return fmt.Errorf("%w: task is required and holds at most %d characters", ErrInvalid, maxTaskRunes)
	case len(input.Context) > maxContextItems:
		return fmt.Errorf("%w: context holds at most %d items", ErrInvalid, maxContextItems)
	case len(input.RequestedTools) > maxListItems || len(input.RequestedPermissions) > maxListItems:
		return fmt.Errorf("%w: tools and permissions each hold at most %d items", ErrInvalid, maxListItems)
	case input.ExpiresInSeconds != 0 && (input.ExpiresInSeconds < int(minimumExpiry/time.Second) || input.ExpiresInSeconds > int(maximumExpiry/time.Second)):
		return fmt.Errorf("%w: expires_in_seconds must be between 300 and 604800", ErrInvalid)
	}
	for _, item := range input.Context {
		if item.Label == "" || item.Value == "" || utf8.RuneCountInString(item.Label) > 120 || utf8.RuneCountInString(item.Value) > 2_000 {
			return fmt.Errorf("%w: every context item needs a label up to 120 characters and a value up to 2000", ErrInvalid)
		}
	}
	if err := validateList(input.RequestedTools, "requested_tools"); err != nil {
		return err
	}
	return validateList(input.RequestedPermissions, "requested_permissions")
}

func cleanDecision(input agents.InteractionDecisionInput) agents.InteractionDecisionInput {
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.Message = cleanText(input.Message)
	input.AllowedTools = cleanTools(input.AllowedTools)
	input.GrantedPermissions = cleanList(input.GrantedPermissions)
	return input
}

func validateDecision(request agents.Interaction, input agents.InteractionDecisionInput) error {
	if request.Status != StatusPending {
		return ErrConflict
	}
	if input.Decision != DecisionApproved && input.Decision != DecisionRejected {
		return fmt.Errorf("%w: decision must be approved or rejected", ErrInvalid)
	}
	if input.Message == "" || utf8.RuneCountInString(input.Message) > maxMessageRunes {
		return fmt.Errorf("%w: decision message is required and holds at most %d characters", ErrInvalid, maxMessageRunes)
	}
	if input.Decision == DecisionRejected && (len(input.AllowedTools) > 0 || len(input.GrantedPermissions) > 0) {
		return fmt.Errorf("%w: a rejected request grants no tools or permissions", ErrInvalid)
	}
	// Nothing is granted that was not asked for: allowed_tools names only
	// requested tools, and left out it names none.
	if !isToolSubset(input.AllowedTools, request.RequestedTools) || !isSubset(input.GrantedPermissions, request.RequestedPermissions) {
		return fmt.Errorf("%w: approved tools and permissions must be requested subsets", ErrInvalid)
	}
	// A request that asks for consent is approved with the consent it grants,
	// so that how far its answer may go is never left to be guessed: an approval
	// that grants neither is no approval of it.
	if input.Decision == DecisionApproved && asksConsent(request) && ConsentFor(input.Decision, input.GrantedPermissions) == "" {
		return fmt.Errorf("%w: a request that asks for consent is approved granting consent:summary or consent:access", ErrInvalid)
	}
	return nil
}

func cleanResponse(input agents.InteractionResponseInput) agents.InteractionResponseInput {
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.Summary = cleanText(input.Summary)
	input.Content = cleanText(input.Content)
	input.ToolsUsed = cleanTools(input.ToolsUsed)
	input.Artifacts = cleanList(input.Artifacts)
	return input
}

func validateResponse(request agents.Interaction, input agents.InteractionResponseInput) error {
	if request.Status != StatusApproved || request.Decision == nil || request.Decision.Decision != DecisionApproved {
		return ErrConflict
	}
	if input.Status != StatusCompleted && input.Status != StatusFailed {
		return fmt.Errorf("%w: response status must be completed or failed", ErrInvalid)
	}
	if input.Summary == "" || utf8.RuneCountInString(input.Summary) > maxMessageRunes {
		return fmt.Errorf("%w: response summary is required and holds at most %d characters", ErrInvalid, maxMessageRunes)
	}
	if utf8.RuneCountInString(input.Content) > maxContentRunes {
		return fmt.Errorf("%w: response content holds at most %d characters", ErrInvalid, maxContentRunes)
	}
	if len(input.ToolsUsed) > maxListItems || len(input.Artifacts) > maxListItems {
		return fmt.Errorf("%w: tools_used and artifacts each hold at most %d items", ErrInvalid, maxListItems)
	}
	if err := validateList(input.ToolsUsed, "tools_used"); err != nil {
		return err
	}
	if err := validateList(input.Artifacts, "artifacts"); err != nil {
		return err
	}
	if !isToolSubset(input.ToolsUsed, request.Decision.AllowedTools) {
		return fmt.Errorf("%w: tools_used must be within allowed_tools", ErrInvalid)
	}
	return nil
}

// validID is an optional identifier a caller named: empty, or at most 128
// characters of text PostgreSQL can hold.
func validID(value string) bool {
	return utf8.RuneCountInString(value) <= 128 && cleantext.Valid(value)
}

func validateList(values []string, name string) error {
	for _, value := range values {
		if value == "" || utf8.RuneCountInString(value) > 120 {
			return fmt.Errorf("%w: every %s entry holds 1 to 120 characters", ErrInvalid, name)
		}
	}
	return nil
}

func isSubset(values, allowed []string) bool {
	for _, value := range values {
		if !slices.Contains(allowed, value) {
			return false
		}
	}
	return true
}

// isToolSubset is isSubset for tool names, which are one name in any case:
// a list keeps one of each, whatever its case (cleanList), and a request
// stored before the tool vocabulary was spelled one way may hold "Apps".
func isToolSubset(values, allowed []string) bool {
	for _, value := range values {
		if !slices.ContainsFunc(allowed, func(candidate string) bool { return strings.EqualFold(candidate, value) }) {
			return false
		}
	}
	return true
}

// actsFor is whether the actor stands on this party's side of a request: a
// member's primary agent for any of that member's agents, a specialist only
// for itself.
func actsFor(actor agents.InteractionActor, party agents.InteractionParty) bool {
	return party.UserID == actor.UserID && (actor.AgentKind == "primary" || party.AgentID == actor.AgentID)
}

func canRead(actor agents.InteractionActor, request agents.Interaction) bool {
	return actsFor(actor, request.Requester) || actsFor(actor, request.Target)
}

// listed is whether a request the repository selected belongs in the actor's
// listing: on the side of it the query names, within its teamspace and with
// its other member. Being on either side is what canRead asks, so a listed
// request is always one the actor may read. It restates the query, so a
// listing never rests on the repository alone.
func listed(actor agents.InteractionActor, query Query, request agents.Interaction) bool {
	if request.OrganizationID != actor.OrganizationID || query.TeamspaceID != "" && request.TeamspaceID != query.TeamspaceID {
		return false
	}
	with := func(other agents.InteractionParty) bool {
		return query.WithUserID == "" || other.UserID == query.WithUserID
	}
	if query.Origin != "" && originOf(request) != query.Origin || query.AskID != "" && request.AskID != query.AskID {
		return false
	}
	asTarget := actsFor(actor, request.Target) && with(request.Requester)
	asRequester := actsFor(actor, request.Requester) && with(request.Target)
	switch query.Box {
	case BoxInbox:
		return asTarget
	case BoxSent:
		return asRequester
	default:
		return asTarget || asRequester
	}
}

func cleanQuery(query Query) Query {
	query.Box = strings.ToLower(strings.TrimSpace(query.Box))
	query.TeamspaceID = strings.TrimSpace(query.TeamspaceID)
	query.WithUserID = strings.TrimSpace(query.WithUserID)
	query.Origin = strings.ToLower(strings.TrimSpace(query.Origin))
	query.AskID = strings.TrimSpace(query.AskID)
	return query
}

// originOf is where a request was made: OriginAgent for one that names none,
// as a request stored before Slack asks does not.
func originOf(request agents.Interaction) string {
	if request.Origin == "" {
		return OriginAgent
	}
	return request.Origin
}

func validateQuery(actor agents.InteractionActor, query Query) error {
	switch {
	case query.Box != BoxInbox && query.Box != BoxSent && query.Box != BoxAll:
		return fmt.Errorf("%w: a listing is the inbox, the sent requests, or all of them", ErrInvalid)
	case !validID(query.TeamspaceID):
		return fmt.Errorf("%w: teamspace_id must be an id of at most 128 characters", ErrInvalid)
	case !validID(query.WithUserID):
		return fmt.Errorf("%w: with_user must be a member id of at most 128 characters", ErrInvalid)
	case query.WithUserID != "" && query.WithUserID == actor.UserID:
		return fmt.Errorf("%w: with_user must name another member, since a request is always between two", ErrInvalid)
	case query.Origin != "" && query.Origin != OriginAgent && query.Origin != OriginSlack:
		return fmt.Errorf("%w: origin is agent or slack", ErrInvalid)
	case !validID(query.AskID):
		return fmt.Errorf("%w: ask_id must be an id of at most 128 characters", ErrInvalid)
	}
	return nil
}

func filterRequests(requests []agents.Interaction, keep func(agents.Interaction) bool) []agents.Interaction {
	filtered := make([]agents.Interaction, 0, len(requests))
	for _, request := range requests {
		if keep(request) {
			filtered = append(filtered, request)
		}
	}
	return filtered
}

func cleanList(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = cleanLine(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

// cleanTools is cleanList for tool names, with the toolboxes a request may
// ask for spelled as the vocabulary spells them (agents.RequestTool), so an
// approval of "Apps" grants the apps toolbox as one of "apps" would.
func cleanTools(values []string) []string {
	cleaned := cleanList(values)
	for index, value := range cleaned {
		cleaned[index] = agents.RequestTool(value)
	}
	return cleaned
}

func cleanLine(value string) string { return strings.Join(strings.Fields(value), " ") }
func cleanText(value string) string { return strings.TrimSpace(value) }
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
func nonNilContext(values []agents.InteractionContextItem) []agents.InteractionContextItem {
	if values == nil {
		return []agents.InteractionContextItem{}
	}
	return values
}
func clamp(limit int) int {
	if limit <= 0 {
		return 20
	}
	return min(limit, 100)
}

func newID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create interaction id: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

var _ agents.InteractionService = (*Service)(nil)

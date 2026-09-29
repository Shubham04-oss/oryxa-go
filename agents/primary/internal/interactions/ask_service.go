package interactions

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
)

// PrimaryAgents finds a member's primary agent, creating it on first use:
// what an ask files its requests between. managedagents.Service implements
// it, and needs no member's token to.
type PrimaryAgents interface {
	Primary(context.Context, managedagents.Scope) (managedagents.Definition, error)
}

const (
	// maxAskOwners bounds the members one message asks.
	maxAskOwners = 10
	// slackPurpose is why an owner's agent is asked, in words that are the
	// ledger's own: it is a member's message that named the owner, and no
	// agent chose them.
	slackPurpose = "Named in a message a member sent in Slack."
)

// slackID is what a Slack workspace, channel or message id is made of.
// Colons are left out on purpose: the idempotency key joins them with one.
var slackID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// reasonCode is a bypass's reason: a short code and nothing a person wrote,
// since it is kept, sent on the bus and shown to the owner.
var reasonCode = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,63}$`)

// SlackThread is where an ask was made, and where its answer goes.
type SlackThread struct {
	// TeamID, ChannelID and ThreadTS are the workspace, the channel, and the
	// thread the answer is posted in: the message's thread_ts, or its own ts
	// when it started none.
	TeamID, ChannelID, ThreadTS string
	// TriggerTS is the asking message's own ts, which is what a retried
	// delivery of it is recognised by.
	TriggerTS string
}

// AskOwner is a member an ask is put to.
type AskOwner struct {
	UserID string
	// Email and DisplayName are the member's, for the request that is
	// addressed to them and for who answered when another owner does. Either
	// may be empty.
	Email, DisplayName string
	// Bypass, when set, is why this owner's request needs no owner click:
	// the reason code the router decided it on. The request is then approved
	// as it is created, as a bypass, unless a live lease of theirs already
	// covers the asker, which is the owner's own permission and is used first.
	// The ledger only records it: whether a bypass applies is the router's to
	// say.
	Bypass string
}

// AskInput is a Slack message that asked one or more members a question.
type AskInput struct {
	OrganizationID string
	// AskerUserID is the member who wrote the message.
	AskerUserID string
	Slack       SlackThread
	// Question is what the asker asked, in their words.
	Question string
	Owners   []AskOwner
	// Kind is AskQuestion, or AskSecret for a credential; AskQuestion when it
	// is left empty.
	Kind string
	// ExpiresIn is how long the owners have to answer; what the organization
	// allows, at most, or a day when it says nothing.
	ExpiresIn time.Duration
}

// AskResult is an ask as CreateAsk stored it.
type AskResult struct {
	Ask Ask
	// Requests holds a request for each owner, as it stands: pending, or
	// approved when a lease or a bypass covered it.
	Requests []agents.Interaction
}

// CreateAsk records what a Slack message asked, as an ask with a request for
// each owner it names. The requests are filed as the asker's primary agent, to
// each owner's primary agent, with the ledger's own purpose and no context;
// they ask for the organization's knowledge and for consent, an AI summary
// (consent:summary) or access (consent:access), and are held for their owner
// to decide, as every request made outside a teamspace is. Each owner's
// request is made under the key
//
//	slack:{team}:{channel}:{trigger ts}:{owner}
//
// so an ask made again, as a retried Slack delivery makes it, stores nothing
// new and returns what it made: with ErrIdempotencyConflict when the message
// it is made again from asks something else.
//
// It is called by the router, which has already matched Slack's people to
// members and decided who may ask whom: no member token is involved, and none
// is needed to find a primary agent. A request the owner's live lease covers is
// approved as it is created, under that lease; one the router asked to bypass
// is approved, as a bypass.
func (service *Service) CreateAsk(ctx context.Context, input AskInput) (AskResult, error) {
	if service.consents == nil || service.primaries == nil {
		return AskResult{}, fmt.Errorf("%w: asks are not set up", ErrConsentUnavailable)
	}
	input, err := cleanAsk(input)
	if err != nil {
		return AskResult{}, err
	}
	settings, err := service.organizationPolicy(ctx, input.OrganizationID)
	if err != nil {
		return AskResult{}, err
	}
	expires := input.ExpiresIn
	if settings.ExpireAfter > 0 {
		if expires > settings.ExpireAfter {
			return AskResult{}, fmt.Errorf("%w: expires_in exceeds the organization's consent expiry", ErrInvalid)
		}
		if expires == 0 {
			expires = settings.ExpireAfter
		}
	}
	if expires == 0 {
		expires = defaultExpiry
	}
	asker, err := service.primaries.Primary(ctx, managedagents.Scope{OrganizationID: input.OrganizationID, UserID: input.AskerUserID})
	if err != nil {
		return AskResult{}, fmt.Errorf("find the asker's primary agent: %w", err)
	}
	askID, err := newID("iak_")
	if err != nil {
		return AskResult{}, err
	}
	now := service.now().UTC()
	ask := Ask{
		ID: askID, OrganizationID: input.OrganizationID, Origin: OriginSlack,
		TeamID: input.Slack.TeamID, ChannelID: input.Slack.ChannelID, ThreadTS: input.Slack.ThreadTS, TriggerTS: input.Slack.TriggerTS,
		AskerUserID: input.AskerUserID, Kind: input.Kind, CreatedAt: now,
	}
	items := make([]AskRequest, 0, len(input.Owners))
	for _, owner := range input.Owners {
		primary, err := service.primaries.Primary(ctx, managedagents.Scope{OrganizationID: input.OrganizationID, UserID: owner.UserID})
		if err != nil {
			return AskResult{}, fmt.Errorf("find %s's primary agent: %w", owner.UserID, err)
		}
		id, err := newID("irq_")
		if err != nil {
			return AskResult{}, err
		}
		request := agents.Interaction{
			ID: id, OrganizationID: input.OrganizationID, Approval: ApprovalOwner, Origin: OriginSlack,
			Requester: agents.InteractionParty{UserID: input.AskerUserID, AgentID: asker.ID, AgentName: asker.Name},
			Target: agents.InteractionParty{
				UserID: owner.UserID, AgentID: primary.ID, AgentName: primary.Name,
				UserEmail: owner.Email, UserDisplayName: owner.DisplayName,
			},
			Purpose: slackPurpose, Task: input.Question, Context: []agents.InteractionContextItem{},
			RequestedTools: []string{agents.KnowledgeToolboxName}, ToolsJudgedBy: ToolsJudgedByRequester,
			RequestedPermissions: []string{PermissionConsentSummary, PermissionConsentAccess},
			Status:               StatusPending, Revision: 1, ExpiresAt: now.Add(expires), CreatedAt: now, UpdatedAt: now,
		}
		onLease := decisionUnderStanding(request, "Approved: under the access this owner gave in this thread.")
		item := AskRequest{
			Request: request, OnLease: &onLease,
			Key: fmt.Sprintf("slack:%s:%s:%s:%s", input.Slack.TeamID, input.Slack.ChannelID, input.Slack.TriggerTS, owner.UserID),
		}
		if owner.Bypass != "" {
			item.Bypass = &BypassDecision{Reason: owner.Bypass, Input: decisionUnderStanding(request, bypassApproved)}
		}
		items = append(items, item)
	}
	stored, made, err := service.consents.CreateAsk(ctx, ask, items, now)
	if err != nil {
		return AskResult{}, err
	}
	requests := make([]agents.Interaction, 0, len(made))
	for _, asked := range made {
		request := asked.Request
		if request.Task != input.Question || request.Requester.UserID != input.AskerUserID {
			return AskResult{}, ErrIdempotencyConflict
		}
		// The bypasses this call made, and not one a retry finds already made.
		if decision := request.Decision; asked.Created && decision != nil && decision.DecidedBy != nil && decision.DecidedBy.Kind == DecidedByBypass {
			service.logBypass(ctx, request, decision.DecidedBy.Reason)
		}
		requests = append(requests, request)
	}
	return AskResult{Ask: stored, Requests: requests}, nil
}

// logBypass puts a bypass on record beside the ledger's: who it was between and
// why, and never what was asked.
func (service *Service) logBypass(ctx context.Context, request agents.Interaction, reason string) {
	service.logger.WarnContext(ctx, "an agent request was approved by a bypass, with no owner click",
		"request_id", request.ID, "ask_id", request.AskID, "organization_id", request.OrganizationID,
		"owner_user_id", request.Target.UserID, "asker_user_id", request.Requester.UserID, "reason", reason)
}

// bypassApproved is what a request approved without an owner click says.
const bypassApproved = "Approved: no owner click was needed."

// decisionUnderStanding is the decision a lease or a bypass makes of a request:
// an approval that allows what it asked for, and grants consent to the level of
// access, excerpts included, whatever the owner's own choice might have been.
func decisionUnderStanding(request agents.Interaction, message string) agents.InteractionDecisionInput {
	return agents.InteractionDecisionInput{
		Decision: DecisionApproved, Message: message, AllowedTools: nonNilStrings(append([]string{}, request.RequestedTools...)),
		GrantedPermissions: []string{PermissionConsentSummary, PermissionConsentAccess},
	}
}

func cleanAsk(input AskInput) (AskInput, error) {
	if err := cleantext.Check(input); err != nil {
		return AskInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.OrganizationID = strings.TrimSpace(input.OrganizationID)
	input.AskerUserID = strings.TrimSpace(input.AskerUserID)
	input.Question = cleanText(input.Question)
	if input.Kind == "" {
		input.Kind = AskQuestion
	}
	switch {
	case input.OrganizationID == "" || !validID(input.OrganizationID) || input.AskerUserID == "" || !validID(input.AskerUserID):
		return AskInput{}, fmt.Errorf("%w: an ask names its organization and the member who asked", ErrInvalid)
	case !slackID.MatchString(input.Slack.TeamID) || !slackID.MatchString(input.Slack.ChannelID) ||
		!slackID.MatchString(input.Slack.ThreadTS) || !slackID.MatchString(input.Slack.TriggerTS):
		return AskInput{}, fmt.Errorf("%w: an ask names the Slack workspace, channel, thread and message it was made in", ErrInvalid)
	case input.Question == "" || utf8.RuneCountInString(input.Question) > maxTaskRunes:
		return AskInput{}, fmt.Errorf("%w: the question is required and holds at most %d characters", ErrInvalid, maxTaskRunes)
	case input.Kind != AskQuestion && input.Kind != AskSecret:
		return AskInput{}, fmt.Errorf("%w: an ask is a question or a secret", ErrInvalid)
	case len(input.Owners) == 0 || len(input.Owners) > maxAskOwners:
		return AskInput{}, fmt.Errorf("%w: an ask names from 1 to %d owners", ErrInvalid, maxAskOwners)
	case input.ExpiresIn != 0 && (input.ExpiresIn < minimumExpiry || input.ExpiresIn > maximumExpiry):
		return AskInput{}, fmt.Errorf("%w: an ask lasts from 5 minutes to 7 days", ErrInvalid)
	}
	seen := map[string]struct{}{}
	owners := make([]AskOwner, 0, len(input.Owners))
	for _, owner := range input.Owners {
		owner.UserID = strings.TrimSpace(owner.UserID)
		owner.Email = strings.TrimSpace(owner.Email)
		owner.DisplayName = cleanLine(owner.DisplayName)
		owner.Bypass = strings.TrimSpace(owner.Bypass)
		switch {
		case owner.UserID == "" || !validID(owner.UserID):
			return AskInput{}, fmt.Errorf("%w: every owner is a member id of at most 128 characters", ErrInvalid)
		case owner.UserID == input.AskerUserID:
			return AskInput{}, fmt.Errorf("%w: a member cannot ask themself", ErrInvalid)
		case utf8.RuneCountInString(owner.Email) > 254 || utf8.RuneCountInString(owner.DisplayName) > 120:
			return AskInput{}, fmt.Errorf("%w: an owner's email holds at most 254 characters and their name 120", ErrInvalid)
		case owner.Bypass != "" && !reasonCode.MatchString(owner.Bypass):
			return AskInput{}, fmt.Errorf("%w: a bypass reason is a short code of letters, digits and _ . : -", ErrInvalid)
		}
		if _, dup := seen[owner.UserID]; dup {
			continue
		}
		seen[owner.UserID] = struct{}{}
		owners = append(owners, owner)
	}
	input.Owners = owners
	return input, nil
}

// CountOpenAsks is how many asks the member asked are still open: not yet
// answered, and with a request still waiting. The router refuses an ask from a
// member who has MaxOpenAsks open.
func (service *Service) CountOpenAsks(ctx context.Context, organizationID, askerUserID string) (int, error) {
	if service.consents == nil {
		return 0, ErrConsentUnavailable
	}
	return service.consents.CountOpenAsks(ctx, strings.TrimSpace(organizationID), strings.TrimSpace(askerUserID), service.now().UTC())
}

// Ask reads one ask, which is where the router, and whatever posts to Slack,
// find the thread an answer goes to and who won it.
func (service *Service) Ask(ctx context.Context, id string) (Ask, error) {
	if service.consents == nil {
		return Ask{}, ErrConsentUnavailable
	}
	return service.consents.Ask(ctx, strings.TrimSpace(id))
}

// Bypass approves a pending request with no owner's click, recording it as a
// bypass with a short code saying why: the decision is
// decided_by {kind: bypass, surface: bypass, reason}, allowing the request's
// tools and granting both consent permissions, so the answer may quote what its
// agent read. The decided event carries all of it, which is how the owner is
// told, on every surface that hears of a decision, and the request and its
// event are the audit.
//
// The ledger only records it. It is the router that decides whether a bypass
// applies, from the channel and who asked, and calls this for each request of an
// ask, or has CreateAsk do it as the request is made; the screen, the guard
// and the rule that a secret is never shared in Slack all still apply to what
// follows. A request already decided is ErrConflict, and one that does not ask
// for consent is ErrInvalid.
func (service *Service) Bypass(ctx context.Context, requestID, reason string) (agents.Interaction, error) {
	if service.consents == nil {
		return agents.Interaction{}, ErrConsentUnavailable
	}
	reason = strings.TrimSpace(reason)
	if !reasonCode.MatchString(reason) {
		return agents.Interaction{}, fmt.Errorf("%w: a bypass reason is a short code of letters, digits and _ . : -", ErrInvalid)
	}
	request, err := service.repository.Get(ctx, strings.TrimSpace(requestID))
	if err != nil {
		return agents.Interaction{}, err
	}
	if !asksConsent(request) {
		return agents.Interaction{}, fmt.Errorf("%w: this request does not ask for consent", ErrInvalid)
	}
	input := decisionUnderStanding(request, bypassApproved)
	if err := validateDecision(request, input); err != nil {
		return agents.Interaction{}, err
	}
	decided, err := service.consents.Bypass(ctx, request.ID, BypassDecision{Reason: reason, Input: input}, service.now().UTC())
	if err != nil {
		return agents.Interaction{}, err
	}
	service.logBypass(ctx, decided, reason)
	return decided, nil
}

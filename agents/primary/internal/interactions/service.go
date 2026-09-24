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
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
)

var (
	ErrNotFound  = errors.New("agent request not found")
	ErrInvalid   = errors.New("invalid agent request")
	ErrForbidden = errors.New("agent request is not available to this agent")
	ErrConflict  = errors.New("agent request cannot make that transition")
	ErrExpired   = errors.New("agent request has expired")
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

	// ApprovalAgent lets the addressed agent, or its member's primary agent,
	// decide: every request made outside a teamspace, and one to a specialist
	// its owner contributed to the teamspace with automatic delegation.
	ApprovalAgent = "agent"
	// ApprovalOwner holds the request for the addressed agent's member, who
	// decides it in their own session and never through an agent: one to a
	// teammate's primary agent, and one to a specialist contributed with
	// request delegation.
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

// Repository is the transactional interaction ledger. Store implements it.
type Repository interface {
	Create(context.Context, agents.Interaction, string) (agents.Interaction, error)
	Get(context.Context, string) (agents.Interaction, error)
	Inbox(context.Context, string, string, int) ([]agents.Interaction, error)
	Sent(context.Context, string, string, int) ([]agents.Interaction, error)
	Decide(context.Context, string, agents.InteractionDecisionInput, time.Time) (agents.Interaction, error)
	Respond(context.Context, string, agents.InteractionResponseInput, time.Time) (agents.Interaction, error)
}

// Teamspaces places the addressed agent in the teamspace a request is made
// in, against the live team directory: the requester must be on the team now,
// and the target must hold a seat there now. teamspaces.Service implements
// it; its errors (teamspaces.ErrNotFound, ErrForbidden, ErrInvalid,
// ErrDisabled) reach the caller as they are.
type Teamspaces interface {
	RequestSeat(ctx context.Context, requester agents.TeamspaceActor, token, teamspaceID, targetAgentID string) (agents.TeamspaceSeat, error)
}

type Config struct {
	Repository Repository
	Directory  agents.Directory
	// Teamspaces enables requests made inside a teamspace. Without it a
	// request naming one is refused.
	Teamspaces Teamspaces
	Now        func() time.Time
}

type Service struct {
	repository Repository
	directory  agents.Directory
	teamspaces Teamspaces
	now        func() time.Time
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
	return &Service{repository: config.Repository, directory: config.Directory, teamspaces: config.Teamspaces, now: now}, nil
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
	approval, err := service.approval(ctx, actor, token, input.TeamspaceID, target)
	if err != nil {
		return agents.Interaction{}, err
	}
	id, err := newID("irq_")
	if err != nil {
		return agents.Interaction{}, err
	}
	now := service.now().UTC()
	expires := time.Duration(input.ExpiresInSeconds) * time.Second
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
		RequestedTools: nonNilStrings(input.RequestedTools), RequestedPermissions: nonNilStrings(input.RequestedPermissions),
		Status: StatusPending, Revision: 1, ExpiresAt: now.Add(expires), CreatedAt: now, UpdatedAt: now,
	}
	if idempotencyKey == "" {
		idempotencyKey = id
	}
	return service.repository.Create(ctx, request, idempotencyKey)
}

// approval is who decides a new request. Outside a teamspace the addressed
// agent does, as it always has. Inside one it follows the target's standing
// there and nothing the requester sends: a specialist its owner contributed
// with automatic delegation decides for itself, while a teammate's primary
// agent, or a specialist contributed with request delegation, waits for its
// owner.
func (service *Service) approval(ctx context.Context, actor agents.InteractionActor, token, teamspaceID string, target agents.DirectoryAgent) (string, error) {
	if teamspaceID == "" {
		return ApprovalAgent, nil
	}
	if service.teamspaces == nil {
		return "", fmt.Errorf("%w: requests inside a teamspace are not available on this server", ErrInvalid)
	}
	seat, err := service.teamspaces.RequestSeat(ctx, agents.TeamspaceActor{
		OrganizationID: actor.OrganizationID, UserID: actor.UserID,
		AgentID: actor.AgentID, AgentName: actor.AgentName, AgentKind: actor.AgentKind,
	}, token, teamspaceID, target.ID)
	if err != nil {
		return "", err
	}
	if seat.AgentID != target.ID || seat.OwnerUserID != target.OwnerUserID {
		return "", fmt.Errorf("%w: target_agent_id does not hold a seat of its owner's in that teamspace", ErrInvalid)
	}
	if seat.Kind == "specialist" && seat.DelegationMode == "automatic" {
		return ApprovalAgent, nil
	}
	return ApprovalOwner, nil
}

func (service *Service) Inbox(ctx context.Context, actor agents.InteractionActor, limit int) ([]agents.Interaction, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	requests, err := service.repository.Inbox(ctx, actor.OrganizationID, actor.UserID, clamp(limit))
	if err == nil && actor.AgentKind != "primary" {
		requests = filterRequests(requests, func(request agents.Interaction) bool { return request.Target.AgentID == actor.AgentID })
	}
	return service.views(requests), err
}

func (service *Service) Sent(ctx context.Context, actor agents.InteractionActor, limit int) ([]agents.Interaction, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	requests, err := service.repository.Sent(ctx, actor.OrganizationID, actor.UserID, clamp(limit))
	if err == nil && actor.AgentKind != "primary" {
		requests = filterRequests(requests, func(request agents.Interaction) bool { return request.Requester.AgentID == actor.AgentID })
	}
	return service.views(requests), err
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
	return service.view(request), nil
}

// Decide records a decision an agent makes, through its interactions tool:
// the addressed agent's own, or its member's primary agent mediating. A
// request held for its owner (ApprovalOwner) is never an agent's to decide,
// whichever agent asks.
func (service *Service) Decide(ctx context.Context, actor agents.InteractionActor, id string, input agents.InteractionDecisionInput) (agents.Interaction, error) {
	return service.decide(ctx, actor, id, input, false)
}

// DecideAsOwner records the decision of the member whose agent a request
// addresses, made in their own signed-in session. Only the HTTP API calls it
// — agents' tools see agents.InteractionService, which has no such method —
// and it is the one way a request held for its owner is decided. The member
// may decide a request their agent could decide as well, as their primary
// agent could.
func (service *Service) DecideAsOwner(ctx context.Context, actor agents.InteractionActor, id string, input agents.InteractionDecisionInput) (agents.Interaction, error) {
	return service.decide(ctx, actor, id, input, true)
}

func (service *Service) decide(ctx context.Context, actor agents.InteractionActor, id string, input agents.InteractionDecisionInput, asOwner bool) (agents.Interaction, error) {
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
		if request.Approval == ApprovalOwner {
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
	return service.repository.Decide(ctx, request.ID, input, service.now().UTC())
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
	return service.repository.Respond(ctx, request.ID, input, service.now().UTC())
}

func (service *Service) view(request agents.Interaction) agents.Interaction {
	if (request.Status == StatusPending || request.Status == StatusApproved) && !request.ExpiresAt.After(service.now()) {
		request.Status = StatusExpired
	}
	return request
}

func (service *Service) views(requests []agents.Interaction) []agents.Interaction {
	if requests == nil {
		return []agents.Interaction{}
	}
	for index := range requests {
		requests[index] = service.view(requests[index])
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
	input.RequestedTools = cleanList(input.RequestedTools)
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
	case input.ExpiresInSeconds != 0 && (time.Duration(input.ExpiresInSeconds)*time.Second < minimumExpiry || time.Duration(input.ExpiresInSeconds)*time.Second > maximumExpiry):
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
	input.AllowedTools = cleanList(input.AllowedTools)
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
	if !isSubset(input.AllowedTools, request.RequestedTools) || !isSubset(input.GrantedPermissions, request.RequestedPermissions) {
		return fmt.Errorf("%w: approved tools and permissions must be requested subsets", ErrInvalid)
	}
	return nil
}

func cleanResponse(input agents.InteractionResponseInput) agents.InteractionResponseInput {
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.Summary = cleanText(input.Summary)
	input.Content = cleanText(input.Content)
	input.ToolsUsed = cleanList(input.ToolsUsed)
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
	if !isSubset(input.ToolsUsed, request.Decision.AllowedTools) {
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

func canRead(actor agents.InteractionActor, request agents.Interaction) bool {
	if actor.AgentKind == "primary" {
		return request.Requester.UserID == actor.UserID || request.Target.UserID == actor.UserID
	}
	return request.Requester.UserID == actor.UserID && request.Requester.AgentID == actor.AgentID ||
		request.Target.UserID == actor.UserID && request.Target.AgentID == actor.AgentID
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

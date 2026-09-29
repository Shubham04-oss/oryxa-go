// Package agentsystem adapts the persistent agent registry and team catalogue
// to the primary agent's conversational management and delegation tools.
package agentsystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

const (
	maxAssignments   = 8
	maxConfirmations = 32
	maxTaskRunes     = 10_000
	maxContextRunes  = 10_000
)

// The statuses a delegated assignment ends in, and only completed is success.
// completed, needs_input and blocked are the specialist's own word about its
// work, from the result envelope it returns; needs_confirmation is a tool of
// its own waiting for the member's approval; failed is a run that did not
// finish, or a specialist that said it failed.
const (
	statusCompleted         = "completed"
	statusNeedsInput        = "needs_input"
	statusBlocked           = "blocked"
	statusNeedsConfirmation = "needs_confirmation"
	statusFailed            = "failed"
)

// What a result says of a specialist's work is bounded, and one line each:
// it is read by Krude and by the member, and is data, never instructions.
const (
	// maxReasonRunes bounds what a specialist said of why it could not finish.
	maxReasonRunes = 300
	// maxActionRunes bounds the description of one action waiting for the
	// member, maxArgumentRunes one argument's value in it, and maxArguments
	// how many arguments it shows the value of, the rest by name alone.
	maxActionRunes   = 400
	maxArgumentRunes = 80
	maxArguments     = 6
)

type Service struct {
	agents AgentRegistry
	teams  TeamRegistry
}

type AgentRegistry interface {
	List(context.Context, managedagents.Scope) ([]managedagents.Definition, error)
	Create(context.Context, managedagents.Scope, managedagents.CreateInput) (managedagents.Definition, error)
	Update(context.Context, managedagents.Scope, string, managedagents.UpdateInput) (managedagents.Definition, error)
	Delete(context.Context, managedagents.Scope, string) error
	StartRun(context.Context, threads.Member, string, string, threads.RunInput) (managedagents.StartedRun, error)
}

type TeamRegistry interface {
	List(context.Context, agentteams.Scope) ([]agentteams.Team, error)
	Get(context.Context, agentteams.Scope, string) (agentteams.Team, error)
	Create(context.Context, agentteams.Scope, agentteams.CreateInput) (agentteams.Team, error)
	Update(context.Context, agentteams.Scope, string, agentteams.UpdateInput) (agentteams.Team, error)
	Delete(context.Context, agentteams.Scope, string) error
}

func New(agents AgentRegistry, teams TeamRegistry) (*Service, error) {
	if agents == nil || teams == nil {
		return nil, errors.New("agent system: agent registry and team service are required")
	}
	return &Service{agents: agents, teams: teams}, nil
}

func (service *Service) ListAgents(ctx context.Context, actor primaryagents.AgentSystemActor) ([]primaryagents.SystemAgent, error) {
	if err := primaryOnly(actor); err != nil {
		return nil, err
	}
	definitions, err := service.agents.List(ctx, scope(actor))
	if err != nil {
		return nil, err
	}
	result := make([]primaryagents.SystemAgent, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, systemAgent(definition))
	}
	return result, nil
}

func (service *Service) CreateAgent(ctx context.Context, actor primaryagents.AgentSystemActor, input primaryagents.SystemAgentCreateInput) (primaryagents.SystemAgent, error) {
	if err := primaryOnly(actor); err != nil {
		return primaryagents.SystemAgent{}, err
	}
	created, err := service.agents.Create(ctx, scope(actor), managedagents.CreateInput{
		Name: input.Name, Description: input.Description, Instructions: input.Instructions,
	})
	return systemAgent(created), err
}

func (service *Service) UpdateAgent(ctx context.Context, actor primaryagents.AgentSystemActor, input primaryagents.SystemAgentUpdateInput) (primaryagents.SystemAgent, error) {
	if err := primaryOnly(actor); err != nil {
		return primaryagents.SystemAgent{}, err
	}
	update := managedagents.UpdateInput{Name: input.Name, Description: input.Description, Instructions: input.Instructions}
	if input.Status != nil {
		status := managedagents.Status(*input.Status)
		update.Status = &status
	}
	updated, err := service.agents.Update(ctx, scope(actor), strings.TrimSpace(input.AgentID), update)
	return systemAgent(updated), err
}

func (service *Service) DeleteAgent(ctx context.Context, actor primaryagents.AgentSystemActor, id string) error {
	if err := primaryOnly(actor); err != nil {
		return err
	}
	return service.agents.Delete(ctx, scope(actor), id)
}

func (service *Service) ListTeams(ctx context.Context, actor primaryagents.AgentSystemActor) ([]primaryagents.SystemTeam, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	teams, err := service.teams.List(ctx, scope(actor))
	if err != nil {
		return nil, err
	}
	result := make([]primaryagents.SystemTeam, 0, len(teams))
	for _, team := range teams {
		result = append(result, systemTeam(team))
	}
	return result, nil
}

func (service *Service) GetTeam(ctx context.Context, actor primaryagents.AgentSystemActor, id string) (primaryagents.SystemTeam, error) {
	if err := validateActor(actor); err != nil {
		return primaryagents.SystemTeam{}, err
	}
	team, err := service.teams.Get(ctx, scope(actor), id)
	return systemTeam(team), err
}

func (service *Service) CreateTeam(ctx context.Context, actor primaryagents.AgentSystemActor, input primaryagents.SystemTeamCreateInput) (primaryagents.SystemTeam, error) {
	if err := primaryOnly(actor); err != nil {
		return primaryagents.SystemTeam{}, err
	}
	team, err := service.teams.Create(ctx, scope(actor), agentteams.CreateInput{
		Name: input.Name, Description: input.Description, Members: teamMemberInputs(input.Members),
	})
	return systemTeam(team), err
}

func (service *Service) UpdateTeam(ctx context.Context, actor primaryagents.AgentSystemActor, input primaryagents.SystemTeamUpdateInput) (primaryagents.SystemTeam, error) {
	if err := primaryOnly(actor); err != nil {
		return primaryagents.SystemTeam{}, err
	}
	update := agentteams.UpdateInput{Name: input.Name, Description: input.Description}
	if input.Status != nil {
		status := agentteams.Status(*input.Status)
		update.Status = &status
	}
	if input.Members != nil {
		members := teamMemberInputs(*input.Members)
		update.Members = &members
	}
	team, err := service.teams.Update(ctx, scope(actor), strings.TrimSpace(input.TeamID), update)
	return systemTeam(team), err
}

func (service *Service) DeleteTeam(ctx context.Context, actor primaryagents.AgentSystemActor, id string) error {
	if err := primaryOnly(actor); err != nil {
		return err
	}
	return service.teams.Delete(ctx, scope(actor), id)
}

func (service *Service) Delegate(ctx context.Context, actor primaryagents.AgentSystemActor, input primaryagents.SystemTeamDelegateInput) (primaryagents.SystemDelegation, error) {
	if err := primaryOnly(actor); err != nil {
		return primaryagents.SystemDelegation{}, err
	}
	if origin := memberauth.RunOrigin(ctx); origin != agentsv1.StartedByMember && origin != agentsv1.StartedByTeam && origin != agentsv1.StartedByWorkflow {
		return primaryagents.SystemDelegation{}, fmt.Errorf("team delegation requires a member-present or existing team run")
	}
	team, err := service.teams.Get(ctx, scope(actor), strings.TrimSpace(input.TeamID))
	if err != nil {
		return primaryagents.SystemDelegation{}, err
	}
	if team.Status != agentteams.StatusActive {
		return primaryagents.SystemDelegation{}, agentteams.ErrDisabled
	}
	if team.CoordinatorAgentID != actor.AgentID {
		return primaryagents.SystemDelegation{}, agentteams.ErrForbidden
	}
	if err := validateAssignments(input.Assignments); err != nil {
		return primaryagents.SystemDelegation{}, err
	}
	members := make(map[string]agentteams.Member, len(team.Members))
	for _, member := range team.Members {
		members[member.AgentID] = member
	}
	// Every assignment is checked before any is started: a specialist that
	// has begun cannot be taken back when a later assignment turns out to
	// name somebody who is not on the team.
	assigned := make([]agentteams.Member, len(input.Assignments))
	for index, assignment := range input.Assignments {
		member, ok := members[strings.TrimSpace(assignment.AgentID)]
		if !ok {
			return primaryagents.SystemDelegation{}, fmt.Errorf("%w: agent %q is not a member of team %q", agentteams.ErrInvalid, assignment.AgentID, team.ID)
		}
		if member.Status != managedagents.StatusActive {
			return primaryagents.SystemDelegation{}, fmt.Errorf("%w: agent %q is disabled", agentteams.ErrInvalid, assignment.AgentID)
		}
		assigned[index] = member
	}
	results := make([]primaryagents.SystemDelegationResult, len(input.Assignments))
	var running sync.WaitGroup
	for index, assignment := range input.Assignments {
		running.Go(func() {
			results[index] = service.delegateOne(ctx, actor, team, assigned[index], assignment)
		})
	}
	running.Wait()
	return primaryagents.SystemDelegation{TeamID: team.ID, Results: results}, nil
}

func (service *Service) Continue(ctx context.Context, actor primaryagents.AgentSystemActor, input primaryagents.SystemTeamContinueInput) (primaryagents.SystemDelegationResult, error) {
	if err := primaryOnly(actor); err != nil {
		return primaryagents.SystemDelegationResult{}, err
	}
	if memberauth.RunOrigin(ctx) != agentsv1.StartedByMember {
		return primaryagents.SystemDelegationResult{}, fmt.Errorf("an assignment confirmation requires the member to be present")
	}
	_, member, err := service.teamMember(ctx, actor, input.TeamID, input.AgentID)
	if err != nil {
		return primaryagents.SystemDelegationResult{}, err
	}
	threadID := strings.TrimSpace(input.ThreadID)
	if threadID == "" {
		return primaryagents.SystemDelegationResult{}, fmt.Errorf("%w: thread id is required", agentteams.ErrInvalid)
	}
	answers, err := confirmationAnswers(input.Confirmations)
	if err != nil {
		return primaryagents.SystemDelegationResult{}, err
	}
	started, err := service.agents.StartRun(ctx, threads.Member{
		UserID: actor.UserID, OrganizationID: actor.OrganizationID,
		AccessToken: memberauth.AccessToken(ctx),
	}, member.AgentID, threadID, threads.RunInput{Confirmations: answers, StartedBy: agentsv1.StartedByTeam})
	if err != nil {
		return primaryagents.SystemDelegationResult{}, err
	}
	return collectRun(ctx, member, started), nil
}

func (service *Service) teamMember(ctx context.Context, actor primaryagents.AgentSystemActor, teamID, agentID string) (agentteams.Team, agentteams.Member, error) {
	team, err := service.teams.Get(ctx, scope(actor), strings.TrimSpace(teamID))
	if err != nil {
		return agentteams.Team{}, agentteams.Member{}, err
	}
	if team.Status != agentteams.StatusActive {
		return agentteams.Team{}, agentteams.Member{}, agentteams.ErrDisabled
	}
	if team.CoordinatorAgentID != actor.AgentID {
		return agentteams.Team{}, agentteams.Member{}, agentteams.ErrForbidden
	}
	id := strings.TrimSpace(agentID)
	for _, member := range team.Members {
		if member.AgentID == id {
			if member.Status != managedagents.StatusActive {
				return agentteams.Team{}, agentteams.Member{}, fmt.Errorf("%w: agent %q is disabled", agentteams.ErrInvalid, id)
			}
			return team, member, nil
		}
	}
	return agentteams.Team{}, agentteams.Member{}, fmt.Errorf("%w: agent %q is not a member of team %q", agentteams.ErrInvalid, id, team.ID)
}

func (service *Service) delegateOne(ctx context.Context, actor primaryagents.AgentSystemActor, team agentteams.Team, member agentteams.Member, assignment primaryagents.SystemTeamAssignment) primaryagents.SystemDelegationResult {
	message := delegationMessage(actor, team, member, assignment)
	startedBy := agentsv1.StartedByTeam
	if memberauth.RunOrigin(ctx) == agentsv1.StartedByWorkflow {
		startedBy = agentsv1.StartedByWorkflow
	}
	started, err := service.agents.StartRun(ctx, threads.Member{
		UserID: actor.UserID, OrganizationID: actor.OrganizationID,
		AccessToken: memberauth.AccessToken(ctx),
	}, member.AgentID, "", threads.RunInput{Message: message, StartedBy: startedBy})
	if err != nil {
		return primaryagents.SystemDelegationResult{AgentID: member.AgentID, AgentName: member.Name, Status: statusFailed, Error: err.Error()}
	}
	return collectRun(ctx, member, started)
}

// collectRun runs a specialist to the end and says how it went. A run that
// stopped for the member's approval says which actions wait, not only which
// calls. One that ran to the end says what the specialist itself made of its
// work, not merely that it ended: a specialist that reports it is blocked, or
// whose model stopped before it had answered, has not done the work, whatever
// state its run finished in.
func collectRun(ctx context.Context, member agentteams.Member, started managedagents.StartedRun) primaryagents.SystemDelegationResult {
	result := primaryagents.SystemDelegationResult{AgentID: member.AgentID, AgentName: member.Name}
	result.ThreadID, result.RunID = started.ThreadID, started.Run.ID()
	var ended, stopped string
	var pending []threads.Item
	started.Run.Execute(ctx, func(item threads.Item) error {
		switch item.Type {
		case threads.ItemMessage:
			if item.Author != threads.AuthorMember && item.Author != threads.AuthorApps {
				result.Output, stopped = item.Text, item.Error
			}
		case threads.ItemConfirmation:
			if item.Status == threads.ConfirmationPending {
				result.Confirmations = append(result.Confirmations, item.CallID)
				pending = append(pending, item)
			}
		case threads.ItemRunFailed:
			ended, result.Error = item.Type, item.Error
		case threads.ItemRunCompleted:
			ended = item.Type
		}
		return nil
	})
	switch {
	case len(result.Confirmations) > 0:
		result.Status = statusNeedsConfirmation
		result.Output = withPendingActions(result.Output, pending)
	case ended == threads.ItemRunCompleted && stopped != "":
		// The model blocked or cut short what the specialist was saying; its
		// last message is not the deliverable, whatever it holds.
		result.Status = statusFailed
		result.Error = withReason("the specialist's model stopped before it had answered", oneLine(stopped, maxReasonRunes))
	case ended == threads.ItemRunCompleted:
		result.Status, result.Error = specialistOutcome(result.Output)
	default:
		result.Status = statusFailed
		if result.Error == "" {
			result.Error = "specialist run did not complete"
		}
	}
	return result
}

// specialistOutcome is how a specialist whose run ended says its work went,
// in the result envelope it returns: its status and, when the work is not
// done, what it said about why. Output that is no envelope, or an envelope
// with no status, says nothing against the work having been done. A status
// an envelope may not carry is not taken for success.
func specialistOutcome(output string) (status, reason string) {
	var envelope struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &envelope) != nil {
		return statusCompleted, ""
	}
	summary := oneLine(envelope.Summary, maxReasonRunes)
	switch reported := strings.ToLower(strings.TrimSpace(envelope.Status)); reported {
	case "", statusCompleted:
		return statusCompleted, ""
	case statusNeedsInput:
		return statusNeedsInput, withReason("the specialist needs an answer from the member before it can go on", summary)
	case statusBlocked:
		return statusBlocked, withReason("the specialist reports that it is blocked", summary)
	case statusFailed:
		return statusFailed, withReason("the specialist reports that it failed", summary)
	default:
		return statusFailed, fmt.Sprintf("the specialist reported a status of %q, which is not one it may report", oneLine(reported, 40))
	}
}

func withReason(text, reason string) string {
	if reason == "" {
		return text
	}
	return text + ": " + reason
}

// withPendingActions is output followed by what each confirmation waiting for
// the member would do. The member decides on what Krude tells them, and a
// call ID says nothing of what is being approved, so every waiting action is
// listed under its ID with the tool it calls, the tool's own reason for
// asking and a short summary of its arguments.
func withPendingActions(output string, pending []threads.Item) string {
	var text strings.Builder
	if output = strings.TrimSpace(output); output != "" {
		text.WriteString(output)
		text.WriteString("\n\n")
	}
	text.WriteString("Waiting for the member to approve or decline:\n")
	for _, item := range pending {
		text.WriteString("- ")
		text.WriteString(describeAction(item))
		text.WriteByte('\n')
	}
	return strings.TrimSpace(text.String())
}

// describeAction is one line saying what a waiting confirmation is: its ID
// as the member's answer will name it, then the tool and, in brackets, what
// it would be called with, then the tool's reason for asking. What the model
// wrote into the arguments is data that goes into Krude's context and the
// member's, so it is cut to a few short values on one line, and a value under
// a name that holds a secret is not shown at all.
func describeAction(item threads.Item) string {
	tool := oneLine(item.Tool, 80)
	if tool == "" {
		tool = "a tool"
	}
	var action strings.Builder
	action.WriteString(oneLine(item.CallID, 128))
	action.WriteString(": ")
	action.WriteString(tool)
	if arguments := summarizeArguments(item.Arguments); arguments != "" {
		action.WriteString("(" + arguments + ")")
	}
	if hint := oneLine(item.Hint, 160); hint != "" {
		action.WriteString(" — " + hint)
	}
	return oneLine(action.String(), maxActionRunes)
}

// summarizeArguments shows the value of the first few arguments, by name,
// and only the names of the rest: what is approved is never described as less
// than it is.
func summarizeArguments(arguments map[string]any) string {
	names := slices.Sorted(maps.Keys(arguments))
	var shown, more []string
	for index, name := range names {
		if index >= maxArguments {
			more = append(more, oneLine(name, 40))
			continue
		}
		shown = append(shown, oneLine(name, 40)+"="+summarizeValue(name, arguments[name]))
	}
	if len(more) > 0 {
		shown = append(shown, "and also "+strings.Join(more, ", "))
	}
	return strings.Join(shown, ", ")
}

func summarizeValue(name string, value any) string {
	if holdsSecret(name) {
		return "[hidden]"
	}
	if text, ok := value.(string); ok {
		return strconv.Quote(oneLine(text, maxArgumentRunes))
	}
	encoded, err := json.Marshal(withoutSecrets(value))
	if err != nil {
		return "[unreadable]"
	}
	return oneLine(string(encoded), maxArgumentRunes)
}

// withoutSecrets is value with whatever sits under a name that holds a
// secret, however deep, replaced.
func withoutSecrets(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for name, inner := range typed {
			if holdsSecret(name) {
				clean[name] = "[hidden]"
			} else {
				clean[name] = withoutSecrets(inner)
			}
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for index, inner := range typed {
			clean[index] = withoutSecrets(inner)
		}
		return clean
	}
	return value
}

// secretNames are the parts of an argument's name that say its value is a
// secret. A name that only looks like one is hidden too: the member loses a
// value they can ask for, not one they cannot take back.
var secretNames = []string{"password", "passwd", "secret", "token", "authorization", "bearer", "api_key", "api-key", "apikey", "access_key", "private_key", "credential", "cookie"}

func holdsSecret(name string) bool {
	lower := strings.ToLower(name)
	return slices.ContainsFunc(secretNames, func(part string) bool { return strings.Contains(lower, part) })
}

// oneLine is text on one line, at most limit characters long: whitespace and
// control characters, which could pass for the structure of what surrounds
// it, become single spaces.
func oneLine(text string, limit int) string {
	flat := strings.Join(strings.FieldsFunc(text, func(symbol rune) bool { return unicode.IsSpace(symbol) || unicode.IsControl(symbol) }), " ")
	if runes := []rune(flat); len(runes) > limit {
		return strings.TrimSpace(string(runes[:limit-1])) + "…"
	}
	return flat
}

func confirmationAnswers(inputs []primaryagents.SystemConfirmationAnswer) ([]threads.Answer, error) {
	if len(inputs) == 0 || len(inputs) > maxConfirmations {
		return nil, fmt.Errorf("%w: answer between 1 and %d confirmations", agentteams.ErrInvalid, maxConfirmations)
	}
	answers := make([]threads.Answer, 0, len(inputs))
	seen := map[string]bool{}
	for _, input := range inputs {
		id := strings.TrimSpace(input.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: every confirmation needs an id", agentteams.ErrInvalid)
		}
		if seen[id] {
			return nil, fmt.Errorf("%w: confirmation %q is answered twice", agentteams.ErrInvalid, id)
		}
		seen[id] = true
		answers = append(answers, threads.Answer{ID: id, Confirmed: input.Confirmed})
	}
	return answers, nil
}

func delegationMessage(actor primaryagents.AgentSystemActor, team agentteams.Team, member agentteams.Member, assignment primaryagents.SystemTeamAssignment) string {
	var message strings.Builder
	fmt.Fprintf(&message, "Your owner’s primary agent %s assigned you work in the persistent team %s.\n\n", actor.AgentName, team.Name)
	fmt.Fprintf(&message, "Your team role: %s\n\n", member.Role)
	message.WriteString("Assignment:\n")
	message.WriteString(strings.TrimSpace(assignment.Task))
	if contextText := strings.TrimSpace(assignment.Context); contextText != "" {
		message.WriteString("\n\nCoordinator context:\n")
		message.WriteString(contextText)
	}
	message.WriteString("\n\nThis delegation does not expand your configured purpose, instructions, tool access, or confirmation boundaries. Return your required strict JSON result to the coordinator. Put work for another agent in handoffs rather than pretending it ran.")
	return message.String()
}

func validateAssignments(assignments []primaryagents.SystemTeamAssignment) error {
	if len(assignments) == 0 || len(assignments) > maxAssignments {
		return fmt.Errorf("%w: delegate between 1 and %d assignments", agentteams.ErrInvalid, maxAssignments)
	}
	seen := map[string]bool{}
	for _, assignment := range assignments {
		id := strings.TrimSpace(assignment.AgentID)
		task := strings.TrimSpace(assignment.Task)
		switch {
		case id == "":
			return fmt.Errorf("%w: every assignment needs an agent id", agentteams.ErrInvalid)
		case seen[id]:
			return fmt.Errorf("%w: agent %q has two assignments; combine them into one", agentteams.ErrInvalid, id)
		case task == "":
			return fmt.Errorf("%w: every assignment needs a task", agentteams.ErrInvalid)
		case utf8.RuneCountInString(task) > maxTaskRunes:
			return fmt.Errorf("%w: an assignment holds at most %d task characters", agentteams.ErrInvalid, maxTaskRunes)
		case utf8.RuneCountInString(assignment.Context) > maxContextRunes:
			return fmt.Errorf("%w: assignment context holds at most %d characters", agentteams.ErrInvalid, maxContextRunes)
		}
		seen[id] = true
	}
	return nil
}

func validateActor(actor primaryagents.AgentSystemActor) error {
	if strings.TrimSpace(actor.OrganizationID) == "" || strings.TrimSpace(actor.UserID) == "" || strings.TrimSpace(actor.AgentID) == "" {
		return fmt.Errorf("%w: organization, member and agent identity are required", agentteams.ErrInvalid)
	}
	return nil
}

func primaryOnly(actor primaryagents.AgentSystemActor) error {
	if err := validateActor(actor); err != nil {
		return err
	}
	if actor.AgentKind != string(managedagents.KindPrimary) {
		return agentteams.ErrForbidden
	}
	return nil
}

func scope(actor primaryagents.AgentSystemActor) managedagents.Scope {
	return managedagents.Scope{OrganizationID: actor.OrganizationID, UserID: actor.UserID}
}

func systemAgent(definition managedagents.Definition) primaryagents.SystemAgent {
	return primaryagents.SystemAgent{
		ID: definition.ID, Kind: string(definition.Kind), Name: definition.Name,
		Description: definition.Description, Instructions: definition.Instructions,
		Status: string(definition.Status), Revision: definition.Revision,
	}
}

func systemTeam(team agentteams.Team) primaryagents.SystemTeam {
	members := make([]primaryagents.SystemTeamMember, 0, len(team.Members))
	for _, member := range team.Members {
		members = append(members, primaryagents.SystemTeamMember{
			AgentID: member.AgentID, Name: member.Name, Description: member.Description,
			Status: string(member.Status), Role: member.Role, Position: member.Position,
		})
	}
	return primaryagents.SystemTeam{
		ID: team.ID, Name: team.Name, Description: team.Description,
		CoordinatorAgentID: team.CoordinatorAgentID, CoordinatorName: team.CoordinatorName,
		Status: string(team.Status), Revision: team.Revision, Members: members,
	}
}

func teamMemberInputs(inputs []primaryagents.SystemTeamMemberInput) []agentteams.MemberInput {
	result := make([]agentteams.MemberInput, 0, len(inputs))
	for _, input := range inputs {
		result = append(result, agentteams.MemberInput{AgentID: input.AgentID, Role: input.Role})
	}
	return result
}

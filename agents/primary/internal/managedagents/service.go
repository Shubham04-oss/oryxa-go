package managedagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/s3artifact"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

const (
	maxNameRunes         = 60
	maxDescriptionRunes  = 500
	maxInstructionsRunes = 20_000
)

// Repository is the persistent registry beneath Service. Store implements it.
type Repository interface {
	EnsurePrimary(context.Context, Scope) (Definition, error)
	Create(context.Context, Scope, Definition) (Definition, error)
	List(context.Context, Scope) ([]Definition, error)
	Get(context.Context, Scope, string) (Definition, error)
	Update(context.Context, Scope, string, UpdateInput) (Definition, error)
	Delete(context.Context, Scope, string) error
}

// Factory builds the ADK runtime for a stored definition. The machine name it
// returns remains stable even when Definition.Name changes.
type Factory func(Definition) (agent.Agent, error)

// UnattendedProfile says how far a run nobody is watching may reach: it names
// the reduced tool set (runprofile.Toolset) the run's reduced agent is built
// with, and with what started the run, its mode in runprofile's table. What
// steers such a run is text somebody else wrote — another member's request,
// or whatever a tool read — so it answers on a reduced agent rather than the
// one a member talks to.
type UnattendedProfile string

const (
	// UnattendedReads may read the member's apps and knowledge: the member's
	// own unwatched work, and an interaction their agent has approved.
	UnattendedReads = UnattendedProfile(runprofile.ToolsReads)
	// UnattendedQuiet keeps the member's apps out of the run altogether, for
	// a request nobody has approved yet.
	UnattendedQuiet = UnattendedProfile(runprofile.ToolsQuiet)
	// DeliveryReads and DeliveryQuiet reach as far as UnattendedReads and
	// UnattendedQuiet, for a run that delivers one interaction: its
	// interactions toolbox holds only what that request needs, reading,
	// deciding and answering it, and nothing that lists the member's other
	// requests. Such a run starts with all it may reach loaded, and has
	// nothing to list or load. DeliveryReads is approved work allowed both
	// the member's apps and their knowledge; DeliveryQuiet is approved work
	// allowed the knowledge alone.
	DeliveryReads = UnattendedProfile(runprofile.ToolsDeliveryReads)
	DeliveryQuiet = UnattendedProfile(runprofile.ToolsDeliveryQuiet)
	// DeliveryApps is approved work allowed the member's apps, read-only,
	// and not their knowledge.
	DeliveryApps = UnattendedProfile(runprofile.ToolsDeliveryApps)
	// DeliveryReport reaches the one request alone: for deciding a new
	// request, for telling the member who asked that it was rejected or
	// answered, and for approved work allowed neither the member's apps nor
	// their knowledge.
	DeliveryReport = UnattendedProfile(runprofile.ToolsDeliveryReport)
	// RoomSeat is a seat's turn in a room, its owner reading the room or
	// away alike: what steers it is what everyone in the room wrote, and
	// everyone in the room reads what it says. It answers from the room
	// alone, reaching none of the member's apps, knowledge, memory or
	// requests, and files what it cannot answer with its member: a room
	// turn (runprofile.RoomTurn), whoever started it.
	RoomSeat = UnattendedProfile(runprofile.ToolsRoom)
)

// ReadsApps is whether a run on this profile may read the member's apps.
func (profile UnattendedProfile) ReadsApps() bool {
	return profile == UnattendedReads || profile == DeliveryReads || profile == DeliveryApps
}

// Delivers is whether this profile is for a run that delivers one
// interaction.
func (profile UnattendedProfile) Delivers() bool {
	return profile == DeliveryReads || profile == DeliveryApps || profile == DeliveryQuiet || profile == DeliveryReport
}

// UnattendedFactory builds the restricted runtime a profile allows. It must
// answer to the same name as [Factory] for the same definition: a run picks
// between the two, and ADK addresses the agent by name. When a service has
// no unattended factory, an unwatched run falls back to the full agent, so
// wiring one is what makes the restriction real; a turn in a room does not
// run at all without one.
type UnattendedFactory func(Definition, UnattendedProfile) (agent.Agent, error)

type Config struct {
	Repository Repository
	Factory    Factory
	// Unattended builds the reduced agent for a run nobody is watching.
	// Without it every unwatched run answers on the full agent.
	Unattended UnattendedFactory
	Sessions   session.Service
	Artifacts  artifact.Service
	Memory     *adkmemory.Service
	Runs       threads.RunStore
	Prices     *pricing.Table
	Logger     *slog.Logger
	// Drain is the process's, shared with every engine (threads.Config.Drain).
	Drain *threads.Drain
}

// Run is the execution handle returned to HTTP. *threads.Run implements it.
type Run interface {
	ID() string
	Execute(context.Context, func(threads.Item) error)
	Discard(context.Context, string)
}

// StartedRun includes the stable conversation allocated for a new run. A
// caller sends the returned ThreadID on later turns and confirmation answers.
type StartedRun struct {
	ThreadID string
	Run      Run
}

// Service validates definitions and adapts them to the existing thread/run
// engine, preserving member identity, tool confirmation and durable history.
type Service struct {
	repository Repository
	factory    Factory
	unattended UnattendedFactory
	sessions   session.Service
	artifacts  artifact.Service
	memory     *adkmemory.Service
	runs       threads.RunStore
	prices     *pricing.Table
	logger     *slog.Logger
	drain      *threads.Drain
}

func New(config Config) (*Service, error) {
	switch {
	case config.Repository == nil:
		return nil, errors.New("managed agents: a repository is required")
	case config.Factory == nil:
		return nil, errors.New("managed agents: an agent factory is required")
	case config.Sessions == nil:
		return nil, errors.New("managed agents: a session service is required")
	case config.Runs == nil:
		return nil, errors.New("managed agents: a run store is required")
	case config.Prices == nil:
		return nil, errors.New("managed agents: a price table is required")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		repository: config.Repository, factory: config.Factory, unattended: config.Unattended, sessions: config.Sessions,
		artifacts: config.Artifacts, memory: config.Memory, runs: config.Runs,
		prices: config.Prices, logger: logger, drain: config.Drain,
	}, nil
}

func (service *Service) List(ctx context.Context, scope Scope) ([]Definition, error) {
	if _, err := service.repository.EnsurePrimary(ctx, scope); err != nil {
		return nil, err
	}
	return service.repository.List(ctx, scope)
}

// Primary returns the member's primary definition, creating it on first use.
func (service *Service) Primary(ctx context.Context, scope Scope) (Definition, error) {
	return service.repository.EnsurePrimary(ctx, scope)
}

func (service *Service) Get(ctx context.Context, scope Scope, id string) (Definition, error) {
	if strings.TrimSpace(id) == "" {
		return Definition{}, ErrNotFound
	}
	return service.repository.Get(ctx, scope, id)
}

func (service *Service) Create(ctx context.Context, scope Scope, input CreateInput) (Definition, error) {
	if _, err := service.repository.EnsurePrimary(ctx, scope); err != nil {
		return Definition{}, err
	}
	definition := Definition{
		Name: cleanLine(input.Name), Description: cleanText(input.Description), Instructions: cleanText(input.Instructions),
	}
	if err := validateDefinition(definition); err != nil {
		return Definition{}, err
	}
	return service.repository.Create(ctx, scope, definition)
}

func (service *Service) Update(ctx context.Context, scope Scope, id string, input UpdateInput) (Definition, error) {
	if input.Name == nil && input.Description == nil && input.Instructions == nil && input.Status == nil && input.Grant == nil && input.ModelTier == nil {
		return Definition{}, fmt.Errorf("%w: send at least one field to change", ErrInvalid)
	}
	current, err := service.repository.Get(ctx, scope, id)
	if err != nil {
		return Definition{}, err
	}
	// The primary runs on Krude's own instruction, which no stored text
	// reaches, so instructions written to it would be kept and never followed.
	if input.Instructions != nil && current.Kind == KindPrimary {
		return Definition{}, fmt.Errorf("%w: the primary agent follows Oryxa's own instructions; give a specialist instructions instead", ErrInvalid)
	}
	if input.ModelTier != nil {
		if current.Kind != KindSpecialist {
			return Definition{}, fmt.Errorf("%w: only a specialist has a model tier; the primary always answers on the main model", ErrInvalid)
		}
		if *input.ModelTier != modelrouter.Lite && *input.ModelTier != modelrouter.Main {
			return Definition{}, fmt.Errorf("%w: model_tier must be lite or main", ErrInvalid)
		}
	}
	if input.Name != nil {
		value := cleanLine(*input.Name)
		input.Name = &value
		current.Name = value
	}
	if input.Description != nil {
		value := cleanText(*input.Description)
		input.Description = &value
		current.Description = value
	}
	if input.Instructions != nil {
		value := cleanText(*input.Instructions)
		input.Instructions = &value
		current.Instructions = value
	}
	if input.Status != nil {
		if *input.Status != StatusActive && *input.Status != StatusDisabled {
			return Definition{}, fmt.Errorf("%w: status must be active or disabled", ErrInvalid)
		}
		current.Status = *input.Status
	}
	if input.Grant != nil {
		if current.Kind != KindSpecialist {
			return Definition{}, fmt.Errorf("%w: only a specialist has a grant; the primary reaches all you do", ErrInvalid)
		}
		grant, err := cleanGrant(*input.Grant)
		if err != nil {
			return Definition{}, err
		}
		input.Grant = &grant
	}
	if err := validateDefinition(current); err != nil {
		return Definition{}, err
	}
	return service.repository.Update(ctx, scope, id, input)
}

func (service *Service) Delete(ctx context.Context, scope Scope, id string) error {
	return service.repository.Delete(ctx, scope, id)
}

// ListThreads returns this member's conversations with one specialist. The
// agent's stable ID selects a separate session namespace from the primary.
func (service *Service) ListThreads(ctx context.Context, member threads.Member, id string) ([]threads.Thread, error) {
	definition, err := service.repository.Get(ctx, Scope{OrganizationID: member.OrganizationID, UserID: member.UserID}, id)
	if err != nil {
		return nil, err
	}
	engine, err := service.engine(definition, UnattendedReads)
	if err != nil {
		return nil, err
	}
	member.AgentID, member.AgentName, member.AgentKind = definition.ID, definition.Name, string(definition.Kind)
	return engine.List(ctx, member)
}

// GetThread reads durable history in the same ownership boundary as runs.
func (service *Service) GetThread(ctx context.Context, member threads.Member, id, threadID string) (threads.Detail, error) {
	definition, err := service.repository.Get(ctx, Scope{OrganizationID: member.OrganizationID, UserID: member.UserID}, id)
	if err != nil {
		return threads.Detail{}, err
	}
	engine, err := service.engine(definition, UnattendedReads)
	if err != nil {
		return threads.Detail{}, err
	}
	member.AgentID, member.AgentName, member.AgentKind = definition.ID, definition.Name, string(definition.Kind)
	return engine.Get(ctx, member, threadID)
}

// StartRun starts or continues one agent-owned conversation. An empty
// threadID allocates it; a later call uses the returned ID for follow-ups or
// tool confirmation answers.
func (service *Service) StartRun(ctx context.Context, member threads.Member, id, threadID string, input threads.RunInput) (StartedRun, error) {
	return service.startRun(ctx, member, id, threadID, input, UnattendedReads)
}

// StartUnattendedRun starts a run whose origin makes it unwatched, saying how
// far the reduced agent may reach. A caller delivering what another member's
// agent asked for passes [DeliveryQuiet] until this member's agent has
// approved the request, so nothing unapproved reads their apps.
func (service *Service) StartUnattendedRun(ctx context.Context, member threads.Member, id, threadID string, input threads.RunInput, profile UnattendedProfile) (StartedRun, error) {
	return service.startRun(ctx, member, id, threadID, input, profile)
}

func (service *Service) startRun(ctx context.Context, member threads.Member, id, threadID string, input threads.RunInput, profile UnattendedProfile) (StartedRun, error) {
	scope := Scope{OrganizationID: member.OrganizationID, UserID: member.UserID}
	definition, err := service.repository.Get(ctx, scope, id)
	if err != nil {
		return StartedRun{}, err
	}
	if definition.Status != StatusActive {
		return StartedRun{}, ErrDisabled
	}
	application := appName(definition.ID)
	if input.Delivery.PrimaryChat {
		// This switch comes only from trusted delivery metadata. Nothing a
		// member or model names may move a specialist into the primary chat,
		// or let approved work use the asking member's conversation.
		if definition.Kind != KindPrimary || profile != DeliveryReport ||
			input.Delivery.Purpose != primaryagents.DeliveryReports || input.Delivery.RequestID == "" {
			return StartedRun{}, fmt.Errorf("%w: only an outcome report to a primary agent may continue its primary chat", threads.ErrInvalid)
		}
		application = primaryagents.RootName
	}
	engine, err := service.engineIn(definition, profile, application)
	if err != nil {
		return StartedRun{}, err
	}
	member.AgentID = definition.ID
	member.AgentName = definition.Name
	member.AgentKind = string(definition.Kind)
	if strings.TrimSpace(threadID) == "" {
		created, err := engine.Create(ctx, member)
		if err != nil {
			return StartedRun{}, err
		}
		threadID = created.ID
	}
	started, err := engine.StartRun(ctx, member, threadID, input)
	if err != nil {
		return StartedRun{}, err
	}
	return StartedRun{ThreadID: threadID, Run: started}, nil
}

// RenameThread gives one of this member's conversations with a specialist a
// title of their own, by the same rules and in the same ownership boundary as
// the primary agent's threads (threads.Service.Rename).
func (service *Service) RenameThread(ctx context.Context, member threads.Member, id, threadID, title string) (threads.Thread, error) {
	definition, err := service.repository.Get(ctx, Scope{OrganizationID: member.OrganizationID, UserID: member.UserID}, id)
	if err != nil {
		return threads.Thread{}, err
	}
	engine, err := service.engine(definition, UnattendedReads)
	if err != nil {
		return threads.Thread{}, err
	}
	member.AgentID, member.AgentName, member.AgentKind = definition.ID, definition.Name, string(definition.Kind)
	return engine.Rename(ctx, member, threadID, title)
}

// DeleteThread removes one known persistent agent conversation. It addresses
// the session by the agent's stable id instead of loading its definition, so
// cleanup still works after a specialist itself was deleted.
func (service *Service) DeleteThread(ctx context.Context, member threads.Member, id, threadID string) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(threadID) == "" {
		return threads.ErrNotFound
	}
	app := appName(id)
	got, err := service.sessions.Get(ctx, &session.GetRequest{AppName: app, UserID: member.UserID, SessionID: threadID})
	if errors.Is(err, session.ErrNotFound) {
		return threads.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read managed agent thread: %w", err)
	}
	if got == nil || got.Session == nil {
		return threads.ErrNotFound
	}
	stored := got.Session
	organizationID, _ := stored.State().Get(adksession.StateOrganizationID)
	if stored.UserID() != member.UserID || organizationID != member.OrganizationID {
		return threads.ErrNotFound
	}
	if err := service.sessions.Delete(ctx, &session.DeleteRequest{AppName: app, UserID: member.UserID, SessionID: threadID}); err != nil && !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("delete managed agent thread: %w", err)
	}
	service.deleteArtifacts(s3artifact.WithOrganization(ctx, member.OrganizationID), app, member.UserID, threadID)
	return nil
}

func (service *Service) deleteArtifacts(ctx context.Context, app, userID, threadID string) {
	if service.artifacts == nil {
		return
	}
	listed, err := service.artifacts.List(ctx, &artifact.ListRequest{AppName: app, UserID: userID, SessionID: threadID})
	if err != nil {
		service.logger.WarnContext(ctx, "list a deleted managed agent thread's artifacts", "thread_id", threadID, "error", err)
		return
	}
	for _, name := range listed.FileNames {
		if strings.HasPrefix(name, "user:") {
			continue
		}
		if err := service.artifacts.Delete(ctx, &artifact.DeleteRequest{
			AppName: app, UserID: userID, SessionID: threadID, FileName: name,
		}); err != nil {
			service.logger.WarnContext(ctx, "delete a managed agent thread's artifact", "thread_id", threadID, "error", err)
		}
	}
}

func (service *Service) engine(definition Definition, profile UnattendedProfile) (*threads.Service, error) {
	return service.engineIn(definition, profile, appName(definition.ID))
}

func (service *Service) engineIn(definition Definition, profile UnattendedProfile, application string) (*threads.Service, error) {
	built, err := service.factory(definition)
	if err != nil {
		return nil, fmt.Errorf("build agent: %w", err)
	}
	var unattended agent.Agent
	if service.unattended != nil {
		unattended, err = service.unattended(definition, profile)
		if err != nil {
			return nil, fmt.Errorf("build the agent for an unwatched run: %w", err)
		}
	}
	// A turn in a room answers on the reduced agent whoever started it
	// (runprofile.RoomTurn): without that agent it does not run at all,
	// rather than on the full one.
	if profile == RoomSeat && unattended == nil {
		return nil, errors.New("managed agents: a turn in a room needs the reduced agent built for it, and none is wired")
	}
	state, err := runState(definition)
	if err != nil {
		return nil, err
	}
	engine, err := threads.New(threads.Config{
		AppName: application, Agent: built, Unattended: unattended, UnattendedTools: runprofile.Toolset(profile),
		Sessions: service.sessions, Artifacts: service.artifacts, Memory: service.memory, Runs: service.runs,
		Prices: service.prices, Logger: service.logger, Drain: service.drain,
		InitialState: state,
	})
	if err != nil {
		return nil, err
	}
	return engine, nil
}

// runState is what every run of the agent starts by writing into its
// session: who it is, and a specialist's grant as it is now, which the agent
// token the run asks for carries (memberauth).
func runState(definition Definition) (map[string]any, error) {
	state := map[string]any{
		primaryagents.AgentIDKey:   definition.ID,
		primaryagents.AgentNameKey: definition.Name,
		primaryagents.AgentKindKey: string(definition.Kind),
	}
	if definition.Kind == KindSpecialist {
		grant, err := json.Marshal(definition.SpecialistGrant())
		if err != nil {
			return nil, fmt.Errorf("encode the agent's grant: %w", err)
		}
		state[memberauth.GrantStateKey] = string(grant)
	}
	return state, nil
}

func validateDefinition(definition Definition) error {
	switch {
	case definition.Name == "":
		return fmt.Errorf("%w: name is required", ErrInvalid)
	case utf8.RuneCountInString(definition.Name) > maxNameRunes:
		return fmt.Errorf("%w: name holds at most %d characters", ErrInvalid, maxNameRunes)
	case utf8.RuneCountInString(definition.Description) > maxDescriptionRunes:
		return fmt.Errorf("%w: description holds at most %d characters", ErrInvalid, maxDescriptionRunes)
	case strings.TrimSpace(definition.Instructions) == "" && definition.Kind != KindPrimary:
		return fmt.Errorf("%w: instructions are required", ErrInvalid)
	case utf8.RuneCountInString(definition.Instructions) > maxInstructionsRunes:
		return fmt.Errorf("%w: instructions hold at most %d characters", ErrInvalid, maxInstructionsRunes)
	default:
		return nil
	}
}

// maxGrantEntries bounds each of a grant's lists.
const maxGrantEntries = 200

// Slugs as apps checks them (apps/internal/domain).
var (
	toolkitSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	toolSlug    = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_]{0,127}$`)
)

// cleanGrant normalizes a grant as apps compares it, toolkits lowercase and
// tools uppercase, without duplicates, and refuses what apps could never
// match. Only Apps may be AllApps: a write is always named.
func cleanGrant(grant runprofile.Grant) (runprofile.Grant, error) {
	clean := func(field string, values []string, upper, all bool) ([]string, error) {
		if len(values) > maxGrantEntries {
			return nil, fmt.Errorf("%w: a grant's %s hold at most %d entries", ErrInvalid, field, maxGrantEntries)
		}
		cleaned := []string{}
		for _, value := range values {
			value = strings.TrimSpace(value)
			pattern := toolkitSlug
			if upper {
				value, pattern = strings.ToUpper(value), toolSlug
			} else {
				value = strings.ToLower(value)
			}
			if !(all && value == runprofile.AllApps) && !pattern.MatchString(value) {
				return nil, fmt.Errorf("%w: %q is not one of the grant's %s", ErrInvalid, value, field)
			}
			if !slices.Contains(cleaned, value) {
				cleaned = append(cleaned, value)
			}
		}
		return cleaned, nil
	}
	var err error
	var cleaned runprofile.Grant
	if cleaned.Apps, err = clean("apps", grant.Apps, false, true); err != nil {
		return runprofile.Grant{}, err
	}
	if cleaned.Tools, err = clean("tools", grant.Tools, true, false); err != nil {
		return runprofile.Grant{}, err
	}
	if cleaned.WriteApps, err = clean("write_apps", grant.WriteApps, false, false); err != nil {
		return runprofile.Grant{}, err
	}
	if cleaned.WriteTools, err = clean("write_tools", grant.WriteTools, true, false); err != nil {
		return runprofile.Grant{}, err
	}
	return cleaned, nil
}

func cleanLine(value string) string { return strings.Join(strings.Fields(value), " ") }
func cleanText(value string) string { return strings.TrimSpace(value) }

func appName(id string) string {
	return "managed_" + strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(id)
}

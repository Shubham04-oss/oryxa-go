package managedagents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
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

type Config struct {
	Repository Repository
	Factory    Factory
	Sessions   session.Service
	Artifacts  artifact.Service
	Memory     *adkmemory.Service
	Runs       threads.RunStore
	Prices     *pricing.Table
	Logger     *slog.Logger
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
	sessions   session.Service
	artifacts  artifact.Service
	memory     *adkmemory.Service
	runs       threads.RunStore
	prices     *pricing.Table
	logger     *slog.Logger
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
		repository: config.Repository, factory: config.Factory, sessions: config.Sessions,
		artifacts: config.Artifacts, memory: config.Memory, runs: config.Runs,
		prices: config.Prices, logger: logger,
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
	if input.Name == nil && input.Description == nil && input.Instructions == nil && input.Status == nil {
		return Definition{}, fmt.Errorf("%w: send at least one field to change", ErrInvalid)
	}
	current, err := service.repository.Get(ctx, scope, id)
	if err != nil {
		return Definition{}, err
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
	if err := validateDefinition(current); err != nil {
		return Definition{}, err
	}
	return service.repository.Update(ctx, scope, id, input)
}

func (service *Service) Delete(ctx context.Context, scope Scope, id string) error {
	return service.repository.Delete(ctx, scope, id)
}

// StartRun starts or continues one agent-owned conversation. An empty
// threadID allocates it; a later call uses the returned ID for follow-ups or
// tool confirmation answers.
func (service *Service) StartRun(ctx context.Context, member threads.Member, id, threadID string, input threads.RunInput) (StartedRun, error) {
	scope := Scope{OrganizationID: member.OrganizationID, UserID: member.UserID}
	definition, err := service.repository.Get(ctx, scope, id)
	if err != nil {
		return StartedRun{}, err
	}
	if definition.Status != StatusActive {
		return StartedRun{}, ErrDisabled
	}
	engine, err := service.engine(definition)
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
	service.deleteArtifacts(ctx, app, member.UserID, threadID)
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

func (service *Service) engine(definition Definition) (*threads.Service, error) {
	built, err := service.factory(definition)
	if err != nil {
		return nil, fmt.Errorf("build agent: %w", err)
	}
	engine, err := threads.New(threads.Config{
		AppName: appName(definition.ID), Agent: built, Sessions: service.sessions,
		Artifacts: service.artifacts, Memory: service.memory, Runs: service.runs,
		Prices: service.prices, Logger: service.logger,
		InitialState: map[string]any{
			primaryagents.AgentIDKey:   definition.ID,
			primaryagents.AgentNameKey: definition.Name,
			primaryagents.AgentKindKey: string(definition.Kind),
		},
	})
	if err != nil {
		return nil, err
	}
	return engine, nil
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

func cleanLine(value string) string { return strings.Join(strings.Fields(value), " ") }
func cleanText(value string) string { return strings.TrimSpace(value) }

func appName(id string) string {
	return "managed_" + strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(id)
}

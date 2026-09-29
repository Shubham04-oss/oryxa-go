// Package primary assembles the agents service for a host: Krude, and the
// run API a member's conversations with it go through. The agents
// themselves, and everything they run with, live under internal/.
package primary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"google.golang.org/adk/v2/agent"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentsystem"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteamrooms"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentworkflows"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/assembly"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/directoryclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/inbound"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactiondelivery"
	agentinteractions "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/organizationagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/rooms"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/wakeups"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/workspaceclient"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/jev"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
	workspacestore "github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

// Name is how ORYXA_SERVICES selects agents primary.
const Name = "agents"

// schema is where agents keeps everything: ADK's sessions, the runs, and the
// outbox.
const schema = "agents_primary"

// closeTimeout bounds stopping what Krude opened, such as AgentCore sessions,
// on the way down.
const closeTimeout = 10 * time.Second

// How app events wake Krude: how often due batches are looked for, the most
// events one wake-up hands over (the rest wait for the next), and how many
// members are woken at once.
const (
	wakeupInterval                   = 15 * time.Second
	wakeupLargestBatch               = 20
	wakeupConcurrency                = 4
	interactionDeliveryInterval      = 5 * time.Second
	interactionDeliveryConcurrency   = 4
	organizationSynthesisInterval    = 5 * time.Second
	organizationSynthesisConcurrency = 4
)

// Definition is how a host builds agents primary.
var Definition = host.Definition{Name: Name, EventsQueueVariable: "AGENTS_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs agents' tables: the runs and the outbox, named with their
// schema, on the host's pool; then ADK's, which ADK names without one, over
// a pool of their own whose search_path leads with agents_primary.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	if err := runstore.New(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	if err := wakeups.NewStore(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	if err := managedagents.NewStore(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	if err := agentteams.NewStore(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	if err := agentworkflows.NewStore(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	if err := agentteamrooms.NewStore(platform.Database).Migrate(ctx); err != nil {
		return fmt.Errorf("migrate agent team rooms: %w", err)
	}
	if err := agentinteractions.NewStore(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	if err := rooms.NewStore(platform.Database).Migrate(ctx); err != nil {
		return fmt.Errorf("migrate rooms: %w", err)
	}
	if err := teamspaces.NewStore(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	if err := organizationagents.NewStore(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	if err := interactiondelivery.NewStore(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	scoped, err := platform.Database.WithSchema(ctx, schema, "oryxa-migrate-agents", 2)
	if err != nil {
		return err
	}
	defer scoped.Close()
	return adksession.Migrate(ctx, scoped)
}

// Mount assembles agents on what the host shares. Without a Google Cloud
// project there is no model to run Krude on, so the service mounts its
// routes answering 503 with the reason, rather than stopping every other
// service in the process from starting.
func Mount(ctx context.Context, platform host.Platform) (host.Service, error) {
	settings, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	timeouts := host.Timeouts{
		Read: 15 * time.Second,
		// Enough for any answer but a run's: a run streams for as long as it
		// takes, so the run route sets its own deadline through
		// http.ResponseController, past this one.
		Write: 2 * time.Minute,
	}
	if settings.GoogleCloudProject == "" {
		logger.Warn("GOOGLE_CLOUD_PROJECT is not set: agents mounts, and answers every conversation with 503, until there is a model to run Krude on")
		server := httpserver.New(nil, nil, httpserver.Config{Unavailable: "Krude is not set up on this server."}, logger)
		return host.Service{Handler: server, Routes: server.Patterns(), Timeouts: timeouts}, nil
	}
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	if settings.AuthURL == "" {
		return host.Service{}, errors.New("AUTH_URL is required: agents asks auth who is calling")
	}
	if len(settings.AllowedOrigins) == 0 {
		logger.Warn("AGENTS_ALLOWED_ORIGINS is not set: members can reach Krude only with bearer tokens, not from the web app")
	}

	prices, err := pricing.ParseOverrides(settings.ModelPrices)
	if err != nil {
		return host.Service{}, err
	}
	priceTable, err := pricing.NewTable(prices)
	if err != nil {
		return host.Service{}, err
	}

	// ADK names its tables without a schema, so its sessions live on a pool
	// whose search_path leads with agents_primary. It connects on first use.
	pool, err := platform.Database.WithSchema(ctx, schema, "oryxa-agents", int32(settings.DatabaseMaxConns))
	if err != nil {
		return host.Service{}, err
	}
	sessionStore, err := adksession.NewStore(pool)
	if err != nil {
		pool.Close()
		return host.Service{}, err
	}
	sessions := sessionStore.Sessions(events.NewSessionRecorder(events.NewOutbox(pool)))

	registryStore := managedagents.NewStore(platform.Database)
	interactionStore := agentinteractions.NewStore(platform.Database)
	wakeupStore := wakeups.NewStore(platform.Database)
	var workspacePolicy *policy.Loader
	if settings.WorkspaceURL != "" {
		workspacePolicy = policy.New(workspacestore.New(platform.Database))
		wakeupStore = wakeups.NewStoreWithPolicy(platform.Database, workspacePolicy)
	}
	var organizationDirectory agents.Directory
	var organizationContext agents.ContextDirectory
	if settings.TeamsURL != "" {
		organizationDirectory, err = directoryclient.New(settings.TeamsURL, &http.Client{Transport: observability.Transport(nil)}, registryStore)
		if err != nil {
			sessionStore.Close()
			return host.Service{}, err
		}
	} else {
		logger.Warn("TEAMS_URL is not set: agents cannot discover people or teams")
	}
	if settings.WorkspaceURL != "" {
		organizationContext, err = workspaceclient.New(settings.WorkspaceURL, &http.Client{Timeout: 15 * time.Second, Transport: observability.Transport(nil)})
		if err != nil {
			sessionStore.Close()
			return host.Service{}, err
		}
	}
	// One agent-token exchange serves Krude's tools and the request ledger,
	// which reads a request's teamspace again as its addressed member.
	memberTokens := assembly.MemberTokens(settings, platform.AWS)
	// Jev answers a room about every message, and judges which of its
	// target's toolboxes a request made in a teamspace that names none
	// needs. Without a TypeSafe key there is none.
	typeSafe := jevClient(settings, logger)
	var interactionService *agentinteractions.Service
	var teamspaceService *teamspaces.Service
	// answerGuard reads what one member's agent tells another before they
	// read it: answers to requests, and what agents say in rooms.
	var answerGuard *answerguard.Guard
	teamspaceStore := teamspaces.NewStore(platform.Database)
	roomStore := rooms.NewStore(platform.Database)
	teamRoomStore := agentteamrooms.NewStore(platform.Database)
	if organizationDirectory != nil {
		teamspaceService, err = teamspaces.New(teamspaceStore, organizationDirectory)
		if err != nil {
			sessionStore.Close()
			return host.Service{}, err
		}
		// A request made inside a teamspace is approved as the target's
		// owner chose there, so the ledger asks the teamspace for its seat.
		interactionConfig := agentinteractions.Config{
			Repository: interactionStore, Directory: organizationDirectory, Teamspaces: teamspaceService, Tokens: memberTokens,
			Logger: logger,
		}
		if settings.WorkspaceURL != "" {
			interactionConfig.Policy = workspacePolicy
		}
		if typeSafe != nil {
			interactionConfig.Jev = typeSafe.Ask
		}
		// Every answer another member reads is checked first when a
		// guardrail is named. The SDK honours AWS_ENDPOINT_URL_BEDROCK_RUNTIME,
		// which points development at a stand-in.
		if settings.AnswerGuardrailIdentifier != "" {
			guard, err := answerguard.New(bedrockruntime.NewFromConfig(platform.AWS), answerguard.Config{
				Identifier: settings.AnswerGuardrailIdentifier, Version: settings.AnswerGuardrailVersion, Logger: logger,
			})
			if err != nil {
				sessionStore.Close()
				return host.Service{}, err
			}
			answerGuard = guard
			interactionConfig.Guard = guard
			logger.Info("the answer guard checks every answer another member reads before they read it",
				"guardrail", settings.AnswerGuardrailIdentifier, "version", settings.AnswerGuardrailVersion)
		} else {
			logger.Warn("AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID is not set: answers reach the member who asked unchecked")
		}
		interactionService, err = agentinteractions.New(interactionConfig)
		if err != nil {
			sessionStore.Close()
			return host.Service{}, err
		}
	}
	agentSystemBinding := agents.NewAgentSystemBinding()
	assemblyOptions := assembly.Options{
		Settings: settings, AWS: platform.AWS, Google: platform.Google, Logger: logger,
		Directory: organizationDirectory, Context: organizationContext, AgentSystem: agentSystemBinding, Tokens: memberTokens,
	}
	if workspacePolicy != nil {
		assemblyOptions.Policy = workspacePolicy
	}
	// interactionService and teamspaceService are concrete *Service pointers,
	// nil when TEAMS_URL is unset. Boxed directly into these interface fields
	// they would leave Build seeing a configured-but-nil collaborator rather
	// than none at all, so each is set only when it holds something real.
	if interactionService != nil {
		assemblyOptions.Interactions = interactionService
	}
	if teamspaceService != nil {
		assemblyOptions.Teamspaces = teamspaceService
	}
	krude, err := assembly.Build(ctx, assemblyOptions)
	if err != nil {
		sessionStore.Close()
		return host.Service{}, fmt.Errorf("assemble Krude: %w", err)
	}
	var localWorkflowRuntime *agentworkflows.LocalRuntime
	closeAll := func() {
		if localWorkflowRuntime != nil {
			localWorkflowRuntime.Close()
		}
		closeContext, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if err := krude.Close(closeContext); err != nil {
			logger.Warn("stop what Krude opened", "error", err)
		}
		sessionStore.Close()
	}

	service, err := threads.New(threads.Config{
		AppName:    agents.RootName,
		Agent:      krude.Root,
		Unattended: krude.Unattended,
		Sessions:   sessions,
		Artifacts:  krude.Artifacts,
		Memory:     krude.Memory,
		Runs:       runstore.New(platform.Database),
		Prices:     priceTable,
		Logger:     logger,
		RunTimeout: settings.RunTimeout,
	})
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	agentRegistry, err := managedagents.New(managedagents.Config{
		Repository: registryStore,
		Factory: func(definition managedagents.Definition) (agent.Agent, error) {
			if definition.Kind == managedagents.KindPrimary {
				return krude.Root, nil
			}
			return krude.Managed(agents.ManagedSpec{
				ID: definition.ID, Name: definition.Name, Description: definition.Description, Instructions: definition.Instructions,
			})
		},
		// What answers when nobody is watching the run: the same agent with
		// the reduced toolboxes and no memory, and without the member's apps
		// at all until their agent has approved the request that woke it. A
		// run delivering one interaction is offered nothing that lists the
		// member's other requests, and starts with all it may reach loaded.
		// A seat's turn in a room answers from the room alone, whether or
		// not its member is reading the room.
		Unattended: func(definition managedagents.Definition, profile managedagents.UnattendedProfile) (agent.Agent, error) {
			spec := agents.ManagedSpec{
				ID: definition.ID, Name: definition.Name, Description: definition.Description, Instructions: definition.Instructions,
			}
			if profile == managedagents.RoomSeat {
				if definition.Kind == managedagents.KindPrimary {
					return krude.Room, nil
				}
				return krude.ManagedRoom(spec)
			}
			if profile.Delivers() {
				if definition.Kind == managedagents.KindPrimary {
					return krude.Delivering(deliveryReach(profile)), nil
				}
				return krude.ManagedDelivering(spec, deliveryReach(profile))
			}
			withApps := profile.ReadsApps()
			if definition.Kind == managedagents.KindPrimary {
				if withApps {
					return krude.Unattended, nil
				}
				return krude.UnattendedQuiet, nil
			}
			return krude.ManagedUnattended(spec, withApps)
		},
		Sessions: sessions, Artifacts: krude.Artifacts, Memory: krude.Memory,
		Runs: runstore.New(platform.Database), Prices: priceTable, Logger: logger,
	})
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	teamStore := agentteams.NewStore(platform.Database)
	teamService, err := agentteams.New(teamStore)
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	agentSystem, err := agentsystem.New(agentRegistry, teamService)
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	if err := agentSystemBinding.Bind(agentSystem); err != nil {
		closeAll()
		return host.Service{}, err
	}
	workflowStore := agentworkflows.NewStore(platform.Database)
	var workflowOrchestrator agentworkflows.Orchestrator
	var workflowScheduler agentworkflows.Scheduler
	var workflowSFN *sfn.Client
	awsWorkflowConfigured := settings.WorkflowStateMachineARN != "" && settings.WorkflowQueueURL != ""
	if settings.WorkflowLocal {
		localWorkflowRuntime = agentworkflows.NewLocalRuntime(settings.RunTimeout+time.Minute, logger)
		workflowOrchestrator = localWorkflowRuntime
		workflowScheduler = localWorkflowRuntime
		logger.Warn("AGENTS_WORKFLOW_LOCAL is enabled: workflows run in process for development and are not durable across restarts")
	} else if awsWorkflowConfigured {
		workflowSFN = sfn.NewFromConfig(platform.AWS, func(options *sfn.Options) {
			if settings.StepFunctionsEndpoint != "" {
				options.BaseEndpoint = aws.String(settings.StepFunctionsEndpoint)
			}
		})
		workflowOrchestrator, err = agentworkflows.NewStepFunctions(workflowSFN, settings.WorkflowStateMachineARN)
		if err != nil {
			closeAll()
			return host.Service{}, err
		}
		if settings.WorkflowSchedulerRoleARN != "" {
			schedulerClient := scheduler.NewFromConfig(platform.AWS, func(options *scheduler.Options) {
				if settings.SchedulerEndpoint != "" {
					options.BaseEndpoint = aws.String(settings.SchedulerEndpoint)
				}
			})
			workflowScheduler, err = agentworkflows.NewEventBridgeScheduler(schedulerClient, agentworkflows.SchedulerConfig{
				StateMachineARN: settings.WorkflowStateMachineARN, RoleARN: settings.WorkflowSchedulerRoleARN,
				DeadLetterARN: settings.WorkflowScheduleDLQARN, GroupName: settings.WorkflowSchedulerGroup,
			})
			if err != nil {
				closeAll()
				return host.Service{}, err
			}
		}
	} else if settings.WorkflowStateMachineARN != "" || settings.WorkflowQueueURL != "" {
		logger.Warn("agent workflows need both AGENTS_WORKFLOW_STATE_MACHINE_ARN and AGENTS_WORKFLOW_QUEUE_URL; execution remains unavailable")
	}
	workflowService, err := agentworkflows.New(agentworkflows.Config{
		Repository: workflowStore, Teams: teamService, Agents: agentRegistry, Delegator: agentSystem,
		Orchestrator: workflowOrchestrator, Scheduler: workflowScheduler, ExecutionLease: settings.RunTimeout + time.Minute,
	})
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	if localWorkflowRuntime != nil {
		if err := localWorkflowRuntime.Bind(workflowService); err != nil {
			closeAll()
			return host.Service{}, err
		}
	}
	organizationStore := organizationagents.NewStore(platform.Database)
	organizationRuntime, err := organizationagents.NewRuntime(organizationagents.RuntimeConfig{
		Factory: func(definition teamspaces.OrganizationAgent) (agent.Agent, error) {
			return krude.OrganizationAgent(agents.OrganizationSpec{
				ID: definition.ID, Name: definition.Name, Description: definition.Description, Instructions: definition.Instructions,
			})
		},
		Sessions: sessions, Artifacts: krude.Artifacts, Runs: runstore.New(platform.Database),
		Prices: priceTable, Logger: logger, RunTimeout: settings.RunTimeout,
	})
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	organizationProcessor, err := organizationagents.NewProcessor(
		organizationStore, teamspaceStore, organizationRuntime,
		organizationagents.ProcessorConfig{
			Interval: organizationSynthesisInterval, Lease: settings.RunTimeout + time.Minute,
			Concurrency: organizationSynthesisConcurrency,
		}, logger)
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	// A room is a teamspace read as a conversation, so it exists only where
	// teamspaces do. Everything a room decides — who answers, what is worth
	// keeping, when its brief is stale — is one judgment per message: Jev
	// when a key is configured, and rules that answer less when it is not.
	var roomService *rooms.Service
	if teamspaceService != nil {
		decider := roomDecider(typeSafe, logger)
		speaker, err := rooms.NewAgentSpeaker(agentRegistry, roomStore, settings.RunTimeout)
		if err != nil {
			closeAll()
			return host.Service{}, err
		}
		relay, err := rooms.NewRelay(roomStore, speaker)
		if err != nil {
			closeAll()
			return host.Service{}, err
		}
		relay.Logger = logger
		// The team reads what an agent says in a room, so the answer guard
		// reads it first. Set only when there is one: a nil *Guard in the
		// interface would not read as none.
		if answerGuard != nil {
			relay.Guard = answerGuard
		}
		compactor, err := rooms.NewModelCompactor(krude.Compact)
		if err != nil {
			closeAll()
			return host.Service{}, err
		}
		roomService, err = rooms.New(rooms.Config{
			Store: roomStore, Spaces: teamspaceService, Ledger: teamspaceService,
			Decider: decider, Compactor: compactor, Relay: relay, Logger: logger,
		})
		if err != nil {
			closeAll()
			return host.Service{}, err
		}
		logger.Info("teamspaces are rooms: people and their agents talk in them", "decider", deciderName(typeSafe))
	}
	teamRoomService, err := agentteamrooms.New(teamRoomStore, teamService, agentRegistry, logger)
	if err != nil {
		closeAll()
		return host.Service{}, err
	}

	httpConfig := httpserver.Config{
		AllowedOrigins:      settings.AllowedOrigins,
		RunTimeout:          settings.RunTimeout,
		Agents:              agentRegistry,
		Teams:               teamService,
		TeamRooms:           teamRoomService,
		Workflows:           workflowService,
		OrganizationContext: organizationStore,
		Preferences:         wakeupStore,
	}
	// Same reasoning as assemblyOptions above: a nil *Service must leave the
	// interface field unset, or server.interactions/.teamspaces/.rooms would
	// compare non-nil and the routes would call a method on a nil receiver
	// instead of answering 503.
	if interactionService != nil {
		httpConfig.Interactions = interactionService
	}
	if teamspaceService != nil {
		httpConfig.Teamspaces = teamspaceService
	}
	if roomService != nil {
		httpConfig.Rooms = roomService
	}
	server := httpserver.New(authclient.New(settings.AuthURL), httpserver.ForThreads(service), httpConfig, logger)

	table, err := outbox.NewTable(platform.Database.Pool, schema+".agents_outbox")
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	eventClient := eventbridge.NewFromConfig(platform.AWS, func(options *eventbridge.Options) {
		if settings.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(settings.EventBridgeEndpoint)
		}
	})
	signer, err := platform.EventAuth.Signer(Name)
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	publisher, err := events.NewPublisher(eventClient, platform.EventBusName, signer)
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	dispatcher := outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
		return publisher.PublishEvent(ctx, events.Event{
			ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data,
			TraceContext: event.TraceContext,
		})
	}), logger)

	workers := []host.Worker{dispatcher.Run}
	if awsWorkflowConfigured && !settings.WorkflowLocal {
		queue := sqs.NewFromConfig(platform.AWS, func(options *sqs.Options) {
			if settings.SQSEndpoint != "" {
				options.BaseEndpoint = aws.String(settings.SQSEndpoint)
			}
		})
		workflowWorker, err := agentworkflows.NewWorker(queue, workflowSFN, workflowService, agentworkflows.WorkerConfig{
			QueueURL: settings.WorkflowQueueURL, Visibility: settings.RunTimeout + 2*time.Minute,
		}, logger)
		if err != nil {
			closeAll()
			return host.Service{}, err
		}
		workers = append(workers, workflowWorker.Poll)
		logger.Info("durable agent workflow worker configured", "queue", settings.WorkflowQueueURL,
			"state_machine", settings.WorkflowStateMachineARN, "scheduler", workflowScheduler != nil)
	}
	if settings.EventsQueueURL != "" {
		store := wakeupStore
		deliveryStore := interactiondelivery.NewStore(platform.Database)
		deliveryCleaner := interactiondelivery.NewCleaner(deliveryStore, agentRegistry)
		consumer, err := inbound.NewConsumer(store, deliveryStore, []inbound.Forgetter{
			deliveryCleaner.Forget,
			interactionStore.Forget,
			organizationStore.Forget,
			teamspaceStore.Forget,
			roomStore.Forget,
			workflowService.Forget,
			teamStore.Forget,
			registryStore.Forget,
		}, platform.EventAuth.Verifier(), logger)
		if err != nil {
			closeAll()
			return host.Service{}, err
		}
		// A member's threads and memory, and their wake-ups, need no other
		// cleanup done first and none needs them, so they run apart from the
		// list above: Memory Bank being down cannot hold up the rest, nor the
		// rest failing keep a member's memory from being forgotten.
		consumer.WithIndependentForgetters(
			func(ctx context.Context, organizationID, memberID string) error {
				_, err := service.Forget(ctx, organizationID, memberID)
				return err
			},
			store.Forget,
		).
			WithTeamForgetter(teamspaceStore.ForgetTeam).
			WithTeamMemberForgetter(teamspaceStore.ForgetTeamMember).
			WithOrganizationSynthesisRecorder(organizationStore)
		queue := sqs.NewFromConfig(platform.AWS, func(options *sqs.Options) {
			if settings.SQSEndpoint != "" {
				options.BaseEndpoint = aws.String(settings.SQSEndpoint)
			}
		})
		waker := wakeups.NewWaker(store, service, sessionStore, wakeups.Config{
			Rule:     wakeups.Rule{Size: settings.WakeupBatchSize, MaxWait: settings.WakeupMaxWait, Largest: wakeupLargestBatch},
			Interval: wakeupInterval,
			// A claimed batch is held for as long as its run may take, and a
			// little more, so a slow run is not woken twice.
			Lease:       settings.RunTimeout + time.Minute,
			Concurrency: wakeupConcurrency,
		}, logger)
		deliveryConfig := interactiondelivery.Config{
			Interval:    interactionDeliveryInterval,
			Lease:       settings.RunTimeout + time.Minute,
			Concurrency: interactionDeliveryConcurrency,
			// The message that wakes an agent names the member who asked, as
			// auth keeps them, and the teamspace the request was made in.
			Members: sessionStore, Teamspaces: teamspaceStore,
			// Each run's start is logged with the toolboxes it was given.
			Toolboxes: func(profile managedagents.UnattendedProfile) []string {
				return krude.DeliveryToolboxNames(deliveryReach(profile))
			},
		}
		// The ledger reads a request made in a teamspace again before its
		// agent is woken; without one, such requests wake nobody.
		if interactionService != nil {
			deliveryConfig.Standing = interactionService
		}
		interactionWaker := interactiondelivery.NewWaker(deliveryStore, agentRegistry, interactionStore, deliveryConfig, logger)
		// A delivery the consumer records wakes its agent at once; the
		// waker's poll is the backstop.
		consumer.WithInteractionNudge(interactionWaker.Nudge)
		workers = append(workers,
			func(ctx context.Context) { consumer.Poll(ctx, queue, settings.EventsQueueURL) },
			waker.Run,
			interactionWaker.Run,
			organizationProcessor.Run)
		logger.Info("events wake persistent agents", "app_batch_size", settings.WakeupBatchSize,
			"app_max_wait", settings.WakeupMaxWait.String(), "interaction_interval", interactionDeliveryInterval.String(),
			"organization_synthesis_interval", organizationSynthesisInterval.String())
	} else {
		logger.Warn("AGENTS_EVENTS_QUEUE_URL is not set: app events, interaction requests, and shared publications will not wake agents, and a member's threads outlive their place in an organization")
		// The interaction waker withholds answers left in review past their
		// request's expiry; without it, this does.
		if interactionService != nil {
			workers = append(workers, func(ctx context.Context) {
				agentinteractions.SweepExpiredReviews(ctx, interactionStore, interactionDeliveryInterval, logger)
			})
		}
	}

	logger.Info("agents mounted", "model", settings.Model, "model_lite", settings.ModelLite, "run_timeout", settings.RunTimeout.String())
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: timeouts,
		Workers:  workers,
		Close:    closeAll,
	}, nil
}

// deliveryReach is how far a run delivering one interaction on profile
// reaches. A profile this does not know reaches least: the request alone.
func deliveryReach(profile managedagents.UnattendedProfile) assembly.DeliveryReach {
	switch profile {
	case managedagents.DeliveryReads:
		return assembly.DeliveryReads
	case managedagents.DeliveryApps:
		return assembly.DeliveryApps
	case managedagents.DeliveryQuiet:
		return assembly.DeliveryQuiet
	default:
		return assembly.DeliveryReport
	}
}

// roomDecider is what a room asks about every message. Jev answers in a few
// hundred milliseconds for a fraction of a model turn, which is what makes
// asking on every message reasonable at all; without a key the rules answer
// less and keep less, and say so.
// roomCompactEvery backstops a room's brief when no judgment says to rewrite
// it: however quiet a room's messages look, the brief is rebuilt after this
// many of them.
const roomCompactEvery = 25

func roomDecider(client *jev.Client, logger *slog.Logger) rooms.Decider {
	fallback := rooms.Rules{CompactEvery: roomCompactEvery}
	if client == nil {
		return fallback
	}
	return rooms.Jev{Ask: client.Ask, Fallback: fallback, Logger: logger}
}

// deciderName is for the one line that says which way a room is running.
func deciderName(client *jev.Client) string {
	if client == nil {
		return "rules"
	}
	return "jev"
}

// jevClient is TypeSafe's client for Jev, or nil without a key, or when its
// settings are refused, which it says: rooms then fall back to rules, and a
// request made in a teamspace that names no tools asks for none.
func jevClient(settings serviceconfig.Config, logger *slog.Logger) *jev.Client {
	if settings.TypeSafeAPIKey == "" {
		return nil
	}
	client, err := jev.New(jev.Config{APIKey: settings.TypeSafeAPIKey, BaseURL: settings.TypeSafeBaseURL})
	if err != nil {
		logger.Warn("Jev is not asked: its client could not be built, so rooms fall back to rules and requests ask for no tools", "error", err)
		return nil
	}
	return client
}

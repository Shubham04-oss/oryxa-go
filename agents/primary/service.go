// Package primary assembles the agents service for a host: Krude, and the
// run API a member's conversations with it go through. The agents
// themselves, and everything they run with, live under internal/.
package primary

import (
	"context"
	"errors"
	"fmt"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
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
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgemembers"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/inbound"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactiondelivery"
	agentinteractions "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/organizationagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/rooms"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/secretshares"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/subagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/surfaces"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/wakeups"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/workspaceclient"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/jev"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
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
	// The access decision log the activity feed reads (internal/access).
	if err := access.Migrate(ctx, platform.Database); err != nil {
		return fmt.Errorf("migrate access decisions: %w", err)
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
	if err := edgemembers.NewStore(platform.Database).Migrate(ctx); err != nil {
		return fmt.Errorf("migrate edge room members: %w", err)
	}
	if err := interactiondelivery.NewStore(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	if err := guardpolicy.New(platform.Database, nil, guardpolicy.Cap{}).Migrate(ctx); err != nil {
		return fmt.Errorf("migrate guard policy: %w", err)
	}
	// The Slack consent loop's tables exist whether or not the loop is on, so
	// that switching it on is a setting and never a migration, and so that what
	// it holds is forgotten with a member in a deployment that turned it off.
	// The secret store needs a sealer to be built at all; with no key, this
	// one only migrates.
	if err := slackrouter.NewStore(platform.Database).Migrate(ctx); err != nil {
		return fmt.Errorf("migrate the Slack router: %w", err)
	}
	if err := surfaces.NewStore(platform.Database).Migrate(ctx); err != nil {
		return fmt.Errorf("migrate the Slack surfaces: %w", err)
	}
	// The sub-agents single-agent Krude starts. The table exists whether or
	// not AGENTS_PRIMARY_SINGLE_AGENT is on, so turning it on is a setting
	// and never a migration.
	if err := subagents.NewPostgresStore(platform.Database).Migrate(ctx); err != nil {
		return fmt.Errorf("migrate sub-agents: %w", err)
	}
	shares, err := secretshares.New(platform.Database, unavailableSealer{}, nil)
	if err != nil {
		return err
	}
	if err := shares.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate one-time secrets: %w", err)
	}
	// Primary agents an earlier build named "Nia-WETP" are renamed "Nia". It
	// runs last, once every table that keeps a copy of an agent's name exists,
	// and finds nothing to do once each has been renamed.
	renamed, err := managedagents.NewStore(platform.Database).RenameSuffixedPrimaries(ctx)
	if err != nil {
		return fmt.Errorf("rename suffixed primary agents: %w", err)
	}
	if renamed > 0 && platform.Logger != nil {
		platform.Logger.InfoContext(ctx, "primary agents renamed from their generated suffixed names", "renamed", renamed)
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
	decisionLog := access.NewLog(platform.Database.Pool, logger)
	workspacePolicy := platform.WorkspaceSettings()
	wakeupStore := wakeups.NewStoreWithPolicy(platform.Database, workspacePolicy)
	// What each member chose about how strictly the answer guard reads what
	// their agent says, apart for an answer they started themselves and one
	// another member reads. The workspace provider supplies the organization's
	// minimums, relevance mode and regulated status. Unreadable policy applies
	// strict floors and regulated handling; the deployment's ceiling still
	// holds the relevance check to what it has earned.
	guardSettings := guardpolicy.New(platform.Database, guardpolicy.NewWorkspacePolicies(workspacePolicy), guardpolicy.Cap{
		Relevance: settings.GuardRelevanceCap, HoldOrgs: settings.GuardHoldOrgs,
	}).WithLogger(logger)
	logger.Info("the answer guard's relevance check is held to a ceiling",
		"ceiling", settings.GuardRelevanceCap, "organizations_exempt", len(settings.GuardHoldOrgs))
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
	// primaries finds the primary agents an ask is filed between. The registry
	// they are found through is built after the ledger, which Krude needs, so it
	// is bound to this once it exists.
	primaries := &primaryAgents{}
	var teamspaceService *teamspaces.Service
	// answerGuard reads what one member's agent tells another before they
	// read it: answers to requests, what agents say in rooms, what they
	// publish to a teamspace's ledger, and, through the check route, what a
	// side conversation is about to show a member. It is built before any of
	// them, since each takes it as it is made.
	//
	// Every answer another member reads is checked first when a guardrail is
	// named. The SDK honours AWS_ENDPOINT_URL_BEDROCK_RUNTIME, which points
	// development at a stand-in. The consent guard, which reads answers to
	// questions asked in Slack, asks the same client, with its own guardrail.
	bedrock := bedrockruntime.NewFromConfig(platform.AWS)
	var answerGuard *answerguard.Guard
	if settings.AnswerGuardrailIdentifier != "" {
		guardOptions, err := relevanceOptions(ctx, settings, platform, logger)
		if err != nil {
			sessionStore.Close()
			return host.Service{}, err
		}
		guard, err := answerguard.New(bedrock, answerguard.Config{
			Identifier: settings.AnswerGuardrailIdentifier, Version: settings.AnswerGuardrailVersion, Logger: logger,
		}, guardOptions...)
		if err != nil {
			sessionStore.Close()
			return host.Service{}, err
		}
		answerGuard = guard
		logger.Info("the answer guard checks every answer another member reads before they read it",
			"guardrail", settings.AnswerGuardrailIdentifier, "version", settings.AnswerGuardrailVersion)
	} else {
		logger.Warn("AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID is not set: answers reach the member who asked unchecked, and the relevance check, which runs in the answer guard, does not run")
	}
	teamspaceStore := teamspaces.NewStore(platform.Database)
	roomStore := rooms.NewStore(platform.Database)
	teamRoomStore := agentteamrooms.NewStore(platform.Database)
	if organizationDirectory != nil {
		// The guard is passed only when there is one: a nil *Guard in the
		// interface would not read as none.
		teamspaceOptions := []teamspaces.Option{teamspaces.WithLogger(logger), teamspaces.WithGuardPolicy(guardSettings)}
		if answerGuard != nil {
			teamspaceOptions = append(teamspaceOptions, teamspaces.WithGuard(answerGuard))
		}
		teamspaceService, err = teamspaces.New(teamspaceStore, organizationDirectory, teamspaceOptions...)
		if err != nil {
			sessionStore.Close()
			return host.Service{}, err
		}
		// A request made inside a teamspace is approved as the target's
		// owner chose there, so the ledger asks the teamspace for its seat.
		interactionConfig := agentinteractions.Config{
			Repository: interactionStore, Directory: organizationDirectory, Teamspaces: teamspaceService, Tokens: memberTokens,
			Decisions: decisionLog, Logger: logger,
		}
		interactionConfig.Policy = workspacePolicy
		if typeSafe != nil {
			interactionConfig.Jev = typeSafe.Ask
		}
		if answerGuard != nil {
			interactionConfig.Guard = answerGuard
		}
		// How strictly the guard reads an answer, and what its relevance check
		// does with what it finds, follow what the member who gives it chose,
		// the organization's minimum and this deployment's ceiling: the same
		// settings the check route reads with.
		interactionConfig.GuardPolicy = guardSettings
		if settings.SlackConsent {
			// The tickets on an owner's buttons, the primary agents an ask is
			// filed between, and the guard that reads what is answered before the
			// asker reads it in Slack. Without them the ledger refuses asks and
			// clicks (ErrConsentUnavailable).
			consent, err := newConsentGuard(settings, bedrock, logger)
			if err != nil {
				sessionStore.Close()
				return host.Service{}, err
			}
			interactionConfig.Agents = primaries
			interactionConfig.TicketKey = settings.SlackTicketKey
			interactionConfig.ConsentGuard = consent
		}
		interactionService, err = agentinteractions.New(interactionConfig)
		if err != nil {
			sessionStore.Close()
			return host.Service{}, err
		}
	}
	if settings.SlackConsent && interactionService == nil {
		sessionStore.Close()
		return host.Service{}, errors.New("AGENTS_PRIMARY_SLACK_CONSENT is on, and TEAMS_URL is not set: a request is between two members, and agents finds them through Teams")
	}
	// One-time secrets are kept sealed, for the hour they are shared, in a store
	// of their own. Without a KMS key it still runs, for the forgetting and the
	// sweeping, and shares nothing.
	secretShares, secretsSealed, err := newSecretShares(settings, platform, logger)
	if err != nil {
		sessionStore.Close()
		return host.Service{}, err
	}
	if settings.SecretsKMSKeyID == "" && settings.SlackConsent {
		logger.Warn("AGENTS_PRIMARY_SECRETS_KMS_KEY_ID is not set: an owner cannot share a credential with a member who asked for one in Slack, whose answer is a one-time view in Oryxa")
	}
	agentSystemBinding := agents.NewAgentSystemBinding()
	assemblyOptions := assembly.Options{
		Settings: settings, AWS: platform.AWS, Google: platform.Google, Logger: logger,
		Directory: organizationDirectory, Context: organizationContext, AgentSystem: agentSystemBinding, Tokens: memberTokens,
	}
	assemblyOptions.Policy = workspacePolicy
	if settings.SingleAgent {
		// A sub-agent's transcript is a session of its own, which is not a
		// thread: its sessions report no lifecycle events.
		assemblyOptions.Subagents = &assembly.SubagentStorage{
			Store:    subagents.NewPostgresStore(platform.Database),
			Sessions: sessionStore.Sessions(),
		}
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
	var spicedbWriter *projector.Engine
	closeAll := func() {
		spicedbWriter.Close()
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
	// Agents and their grants reach SpiceDB as they commit, whichever
	// services decide from it; the projector still follows from the events.
	if spicedbWriter, err = projector.OpenWriter(platform); err != nil {
		closeAll()
		return host.Service{}, err
	}
	if spicedbWriter != nil {
		registryStore.WithRelationships(agentRelationships{spicedbWriter}, logger)
	}

	// Every run this process holds is tracked in one drain, whichever engine
	// started it, so that stopping the process lets them finish first.
	runDrain := threads.NewDrain()
	// Every engine a primary agent's runs go through shares the process's
	// sub-agents, when single-agent Krude has them; a nil *Manager is left
	// out, or the interface would hold it.
	var runSubagents threads.Subagents
	if krude.Subagents != nil {
		runSubagents = krude.Subagents
	}
	service, err := threads.New(threads.Config{
		Drain:      runDrain,
		Subagents:  runSubagents,
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
				Model: definition.ModelTier,
			}, definition.SpecialistGrant())
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
				Model: definition.ModelTier,
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
				return krude.ManagedDelivering(spec, deliveryReach(profile), definition.SpecialistGrant())
			}
			withApps := profile.ReadsApps()
			if definition.Kind == managedagents.KindPrimary {
				if withApps {
					return krude.Unattended, nil
				}
				return krude.UnattendedQuiet, nil
			}
			return krude.ManagedUnattended(spec, withApps, definition.SpecialistGrant())
		},
		Sessions: sessions, Artifacts: krude.Artifacts, Memory: krude.Memory,
		Runs: runstore.New(platform.Database), Prices: priceTable, Logger: logger, Drain: runDrain,
		Subagents: runSubagents,
	})
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	primaries.bind(agentRegistry)
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
		Prices: priceTable, Logger: logger, RunTimeout: settings.RunTimeout, Drain: runDrain,
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

	// The Slack consent loop, when it is on: the router that turns a mention
	// into an ask, and the worker that says in Slack what became of it.
	var slack *slackLoop
	if settings.SlackConsent {
		slack, err = newSlackLoop(slackLoopConfig{
			Settings: settings, Platform: platform, Logger: logger,
			Interactions: interactionService, InteractionStore: interactionStore, Threads: service, Sessions: sessionStore,
			Directory: organizationDirectory, Context: organizationContext, Tokens: memberTokens, Jev: typeSafe,
		})
		if err != nil {
			closeAll()
			return host.Service{}, fmt.Errorf("the Slack consent loop: %w", err)
		}
		logger.Info("the Slack consent loop is on: a member's mention of the bot asks the people it names, in Slack",
			"apps", settings.AppsURL, "consent_guardrail", settings.ConsentGuardrailIdentifier != "", "secret_store", secretsSealed)
	}

	httpConfig := httpserver.Config{
		AllowedOrigins: settings.AllowedOrigins,
		Documents: httpserver.DocumentsConfig{
			PublicURL: settings.DocumentServerURL, InternalURL: settings.DocumentServerInternalURL,
			APIURL: settings.DocumentAPIURL, JWTSecret: settings.DocumentJWTSecret,
			TicketSecret: settings.DocumentTicketSecret,
		},
		RunTimeout:          settings.RunTimeout,
		Agents:              agentRegistry,
		Teams:               teamService,
		TeamRooms:           teamRoomService,
		Workflows:           workflowService,
		OrganizationContext: organizationStore,
		Preferences:         wakeupStore,
		// Always built above, so never a nil *Service in the interface.
		AnswerGuardPreferences: guardSettings,
	}
	// A document server that was set up but cannot be used says why once, here,
	// and not as a 503 to the first member who opens a file.
	if problem := httpConfig.Documents.Problem(); problem != "" {
		logger.Warn("document editing is off", "reason", problem)
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
	httpConfig.Runs = runDrain
	// The check route reads with the same guard, when there is one; without
	// it the route answers that nothing was checked.
	if answerGuard != nil {
		httpConfig.AnswerGuard = answerGuard
	}
	// The three routes that share, open and revoke a one-time secret answer
	// 503 without a store that can seal one: set only when there is one, since
	// a nil *Store in the interface would not read as none.
	if secretsSealed {
		httpConfig.SecretShares = secretShares
	}
	// The voice agent in a call hands real work over through the dispatch
	// route; the answers go back to the edge (docs/design/live-layer/03, B5).
	edgeDispatch, edgeRoute, err := mountEdgeDispatch(settings, platform, service, sessionStore, logger)
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	httpConfig.EdgeDispatch = edgeRoute
	// Rooms on the edge learn their members from Go (A18): the admit route
	// lets a member in once Go has checked they may be, and leaving a team or
	// the organization takes them out again (the consumer below).
	edgeMembers, err := mountEdgeMembers(settings, platform, teamspaceStore, logger)
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	if edgeMembers != nil {
		httpConfig.EdgeMembers = edgeMembers
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

	// The secret store sweeps what expired, whatever the deployment set: a
	// secret is never kept past its hour.
	workers := []host.Worker{dispatcher.Run, secretShares.Run}
	if slack != nil {
		workers = append(workers, slack.router.Run, slack.surfaces.Run)
	}
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
		// A member's threads and memory, their wake-ups and their answer guard
		// settings need no other cleanup done first and none needs them, so
		// they run apart from the list above: Memory Bank being down cannot
		// hold up the rest, nor the rest failing keep a member's memory from
		// being forgotten.
		independent := []inbound.Forgetter{
			func(ctx context.Context, organizationID, memberID string) error {
				_, err := service.Forget(ctx, organizationID, memberID)
				return err
			},
			store.Forget,
			guardSettings.Forget,
			// A member's one-time secrets, whoever shared or was to open them: an
			// empty member means the whole organization.
			secretShares.Forget,
		}
		forgetTeamMember := teamspaceStore.ForgetTeamMember
		forgetTeam := teamspaceStore.ForgetTeam
		if edgeMembers != nil {
			// A deleted team's teamspace rooms on the edge are emptied first,
			// while its teamspaces can still be found; a failure fails the
			// event, which comes again with nothing yet forgotten.
			forgetTeam = func(ctx context.Context, organizationID, teamID string) error {
				if err := edgeMembers.TeamDeleted(ctx, organizationID, teamID); err != nil {
					return err
				}
				return teamspaceStore.ForgetTeam(ctx, organizationID, teamID)
			}
			// Out of the team's teamspace rooms on the edge too, whatever
			// became of the teamspace cleanup: each is retried on redelivery.
			independent = append(independent, edgeMembers.Forget)
			forgetTeamMember = func(ctx context.Context, organizationID, teamID, userID string, removedAt time.Time) error {
				return errors.Join(teamspaceStore.ForgetTeamMember(ctx, organizationID, teamID, userID, removedAt),
					edgeMembers.MemberLeftTeam(ctx, organizationID, teamID, userID))
			}
		}
		consumer.
			WithTeamForgetter(forgetTeam).
			WithTeamMemberForgetter(forgetTeamMember).
			WithOrganizationSynthesisRecorder(organizationStore)
		if slack != nil {
			// The router and the surfaces are handed their events, and forget what
			// they hold for a member, or for a workspace whose install ended. Each
			// is independent of the others.
			independent = append(independent, slack.memberForgetters...)
			consumer.WithSlackRouter(slack.router).WithSurfaces(slack.surfaces).WithWorkspaceForgetters(slack.workspaceForgetters...)
		}
		consumer.WithIndependentForgetters(independent...)
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
		if slack != nil {
			logger.Warn("AGENTS_EVENTS_QUEUE_URL is not set: nothing tells the Slack consent loop what happened in Slack or in the ledger, so it asks nobody and says nothing")
		}
		// The interaction waker withholds answers left in review past their
		// request's expiry; without it, this does.
		if interactionService != nil {
			workers = append(workers, func(ctx context.Context) {
				agentinteractions.SweepExpiredReviews(ctx, interactionStore, interactionDeliveryInterval, logger)
			})
		}
	}

	// Decisions older than access.Retention go, once an hour.
	workers = append(workers, func(ctx context.Context) {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if purged, err := decisionLog.Purge(ctx, access.Retention); err != nil {
				logger.Warn("purge access decisions", "error", err)
			} else if purged > 0 {
				logger.Info("access decisions purged", "count", purged)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})

	logger.Info("agents mounted", "model", settings.Model, "model_lite", settings.ModelLite, "run_timeout", settings.RunTimeout.String())
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: timeouts,
		Workers:  workers,
		Close:    closeAll,
		// A deploy lets the runs going finish, and interrupts what is left at
		// the drain's deadline: whoever started those starts them again on
		// the next process (threads.ErrInterrupted).
		Drain: func(ctx context.Context) {
			going := runDrain.Running()
			interrupted := runDrain.Drain(ctx)
			if edgeDispatch != nil {
				// A dispatched run that ended, or was interrupted, still has to
				// tell the edge, which may then send it to the next process.
				answering, cancel := context.WithTimeout(context.WithoutCancel(ctx), edgeAnswerGrace)
				edgeDispatch.Wait(answering)
				cancel()
			}
			if interrupted > 0 {
				logger.Warn("agent runs were interrupted by the shutdown", "going", going, "interrupted", interrupted)
				return
			}
			logger.Info("agent runs drained", "going", going)
		},
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

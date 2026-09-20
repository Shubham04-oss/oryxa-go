// Package assembly builds Krude and everything it runs with — its models,
// the AgentCore sandbox and browser, the apps toolbox and the artifact store
// — once, for whatever runs it: the agents service in the oryxa binary, and
// cmd/devui.
package assembly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"cloud.google.com/go/auth"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentcorebrowser"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentcoresandbox"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardrail"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelcache"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/ragclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/s3artifact"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// Options are what Build needs.
type Options struct {
	Settings serviceconfig.Config
	// AWS carries the region and middleware; the sandbox, browser, artifact
	// store and agent-token signer are built from it.
	AWS aws.Config
	// Google reaches Vertex AI for the models and Memory Bank. Nil means
	// Application Default Credentials.
	Google *auth.Credentials
	Logger *slog.Logger
	// Tokens mints an agent token for a run that carries no member access
	// token of its own. When nil, Build uses auth's agent-token exchange if
	// Settings names both AuthURL and ServiceKeyID, and otherwise none.
	Tokens memberauth.TokenSource
	// HTTP is the client the apps toolbox's MCP calls go out on. Defaults to
	// a fresh http.Client.
	HTTP *http.Client
	// Directory is the caller-visible people, team and agent directory. When
	// nil, the directory toolbox is absent.
	Directory agents.Directory
	// Interactions is the durable cross-user request ledger. When nil, agents
	// can discover one another but cannot exchange requests.
	Interactions agents.InteractionService
	// AgentSystem binds the member's persistent agent/team registry after the
	// agents themselves are assembled. When nil, conversational creation and
	// team delegation are absent.
	AgentSystem *agents.AgentSystemBinding
	// Teamspaces is the shared collaboration ledger. When nil, agents cannot
	// participate as team whisperers or contributed specialists.
	Teamspaces agents.TeamspaceService
}

// Krude is the assembled agent with what it runs with.
type Krude struct {
	// Root is Krude, the agent every conversation starts with.
	Root agent.Agent
	// Unattended is Krude for a run nobody is watching, such as a wake-up:
	// it reads the member's apps and knowledge, answers the interactions
	// another member's agent addressed to this one, and can reach nothing
	// else.
	Unattended agent.Agent
	// UnattendedQuiet is Unattended without the apps toolbox, for a run that
	// has not been allowed to read the member's apps: an interaction nobody
	// has approved yet, and the reports that follow a finished one.
	UnattendedQuiet agent.Agent
	// Artifacts keeps what conversations produce: S3 when Settings names a
	// bucket, memory otherwise.
	Artifacts artifact.Service
	// Memory is what Krude remembers across a member's threads, scoped to
	// the organization each run belongs to. It is nil unless Settings names
	// an agent engine, and then Krude has no memory at all.
	Memory *adkmemory.Service

	// router and toolboxes are kept so persistent specialist definitions can
	// receive the same models and member-scoped tools as the primary agent.
	router          *modelrouter.Router
	toolboxes       []agents.Toolbox
	unattended      []agents.Toolbox
	unattendedQuiet []agents.Toolbox

	// closers stops everything Build opened, kept in the order Build opened
	// them. Close runs them in reverse — the same order devui's own defers
	// unwound in before this assembly moved out of it — so relocating the
	// wiring changes nothing about what stops when.
	closers []func(context.Context) error
}

// Build assembles Krude. It contacts nothing but Google's credential
// discovery: AWS credentials, the MCP session and every cache are resolved
// on first use. If a step after the sandbox, browser or prompt cache are
// created fails, Build closes whatever it already opened before returning
// the error, so a failed build never leaks a running AgentCore session or an
// explicit prompt cache.
func Build(ctx context.Context, options Options) (*Krude, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	settings := options.Settings

	krude := &Krude{}
	// fail closes whatever krude has already opened and joins that close's
	// own errors (if any) into the failure that triggered it, so a cleanup
	// problem is never silently dropped on the way out.
	fail := func(stage string, err error) (*Krude, error) {
		return nil, errors.Join(fmt.Errorf("%s: %w", stage, err), krude.Close(ctx))
	}

	sandbox, err := agentcoresandbox.New(options.AWS, agentcoresandbox.Options{
		CodeInterpreterIdentifier: settings.SandboxCodeInterpreter,
		SessionTimeout:            settings.SandboxSessionTimeout,
	})
	if err != nil {
		return fail("AgentCore sandbox", err)
	}
	krude.closers = append(krude.closers, func(ctx context.Context) error {
		if err := sandbox.Close(ctx); err != nil {
			return fmt.Errorf("stop AgentCore sandboxes: %w", err)
		}
		return nil
	})
	sandboxTools, err := agentcoresandbox.NewToolset(sandbox)
	if err != nil {
		return fail("AgentCore sandbox tools", err)
	}

	browser, err := agentcorebrowser.New(options.AWS, agentcorebrowser.Options{
		BrowserIdentifier: settings.BrowserIdentifier,
		SessionTimeout:    settings.BrowserSessionTimeout,
	})
	if err != nil {
		return fail("AgentCore browser", err)
	}
	krude.closers = append(krude.closers, func(ctx context.Context) error {
		if err := browser.Close(ctx); err != nil {
			return fmt.Errorf("stop AgentCore browsers: %w", err)
		}
		return nil
	})
	browserTools, err := agentcorebrowser.NewToolset(browser)
	if err != nil {
		return fail("AgentCore browser tools", err)
	}
	logger.InfoContext(ctx, "AWS AgentCore sandbox and browser configured", "region", settings.AWSRegion)

	mainModel, err := agents.Gemini(ctx, settings.Model, settings.GoogleCloudProject, settings.GoogleCloudLocation, options.Google)
	if err != nil {
		return fail("main model", err)
	}
	liteModel, err := agents.Gemini(ctx, settings.ModelLite, settings.GoogleCloudProject, settings.GoogleCloudLocation, options.Google)
	if err != nil {
		return fail("lite model", err)
	}

	// promptCache is left as an untyped nil modelcache.Store when caching is
	// off, rather than a *modelcache.GenaiStore nil pointer boxed in the
	// interface: modelcache.Cached checks the interface itself against nil to
	// decide whether caching is active, and a boxed typed nil would not equal
	// that nil.
	var promptCache modelcache.Store
	if settings.Cache {
		client, err := agents.GeminiClient(ctx, settings.GoogleCloudProject, settings.GoogleCloudLocation, options.Google)
		if err != nil {
			return fail("prompt cache client", err)
		}
		cacheStore := modelcache.NewGenaiStore(client, settings.CacheTTL)
		promptCache = cacheStore
		krude.closers = append(krude.closers, func(ctx context.Context) error {
			if err := cacheStore.Close(ctx); err != nil {
				return fmt.Errorf("delete prompt caches: %w", err)
			}
			return nil
		})
	}
	cacheConfig := modelcache.Config{TTL: settings.CacheTTL, MinTokens: settings.CacheMinTokens}
	// Usage is counted inside the guardrail: an answer it blocks was still
	// generated, and billed.
	conversation := usage.Counted(modelcache.Cached(mainModel, promptCache, cacheConfig))
	if settings.GuardrailIdentifier != "" {
		// The main model carries the member's conversation and everything
		// that reaches it: their messages, and every tool result, the lite
		// model's work included. Screening it screens all the member sees.
		conversation = guardrail.Model(conversation, bedrockruntime.NewFromConfig(options.AWS), guardrail.Config{
			Identifier: settings.GuardrailIdentifier,
			Version:    settings.GuardrailVersion,
			FailClosed: settings.GuardrailFailClosed,
			Logger:     logger,
		})
		logger.InfoContext(ctx, "guardrail screens the member's conversation; its answers do not stream",
			"guardrail", settings.GuardrailIdentifier, "version", settings.GuardrailVersion, "fail_closed", settings.GuardrailFailClosed)
	}
	router, err := modelrouter.New(
		conversation,
		usage.Counted(modelcache.Cached(liteModel, promptCache, cacheConfig)),
	)
	if err != nil {
		return fail("model router", err)
	}
	krude.router = router
	logger.InfoContext(ctx, "models: main model with lite model for planning and bulk work",
		"model", settings.Model, "model_lite", settings.ModelLite, "prompt_cache", settings.Cache)

	toolboxes := []agents.Toolbox{
		agents.SandboxToolbox(sandboxTools),
		agents.BrowserToolbox(browserTools),
	}
	if options.AgentSystem != nil {
		agentSystemToolbox, err := agents.AgentSystemToolbox(options.AgentSystem)
		if err != nil {
			return fail("persistent agent system toolbox", err)
		}
		toolboxes = append(toolboxes, agentSystemToolbox)
		logger.InfoContext(ctx, "persistent agent system toolbox configured")
	}
	// The apps and knowledge toolboxes act as the session's member: with the
	// member's own token when the run carries it, otherwise with an agent
	// token minted under the member's standing grant.
	tokens := options.Tokens
	if tokens == nil && (settings.AppsURL != "" || settings.RagURL != "" || options.Directory != nil || options.Interactions != nil || options.Teamspaces != nil) {
		if settings.AuthURL != "" && settings.ServiceKeyID != "" {
			tokens = memberauth.NewAuthClient(memberauth.AuthClientConfig{
				BaseURL: settings.AuthURL,
				Signer:  serviceauth.Signer{Client: kms.NewFromConfig(options.AWS), KeyID: settings.ServiceKeyID, Service: "agents"},
			})
		} else {
			logger.WarnContext(ctx, "member token: AUTH_URL and AGENTS_PRIMARY_SERVICE_KEY_ID are not both set, so a run without the member's own token cannot act for them")
		}
	}
	// What a run nobody watches may use: nothing that reaches outside, since
	// text from the member's apps steers it. See agents.UnattendedRoot.
	var unattended []agents.Toolbox
	// unattendedQuiet is the unattended list without the apps toolbox, for a
	// run nobody is watching that has not been allowed to read the member's
	// apps at all — an interaction another member's agent asked for, before
	// this member's agent has approved anything.
	var unattendedQuiet []agents.Toolbox
	if settings.AppsURL != "" {
		httpClient := options.HTTP
		if httpClient == nil {
			httpClient = &http.Client{Transport: observability.Transport(nil)}
		}
		appsToolbox, err := agents.AppsToolbox(agents.AppsConfig{
			URL:    settings.AppsURL,
			HTTP:   httpClient,
			Tokens: tokens,
		})
		if err != nil {
			// Krude still runs without it: a member simply cannot reach the
			// apps service's tools until APPS_URL is fixed and the process
			// restarts.
			logger.WarnContext(ctx, "apps toolbox unavailable, running without it", "error", err)
		} else {
			toolboxes = append(toolboxes, appsToolbox)
			logger.InfoContext(ctx, "apps toolbox", "url", settings.AppsURL)
		}
		readOnly, err := agents.AppsToolbox(agents.AppsConfig{
			URL:      settings.AppsURL,
			HTTP:     httpClient,
			Tokens:   tokens,
			ReadOnly: true,
		})
		if err == nil {
			unattended = append(unattended, readOnly)
		}
	}
	if settings.RagURL != "" {
		knowledgeToolbox, err := agents.KnowledgeToolbox(agents.KnowledgeConfig{
			Rag:          ragclient.New(settings.RagURL, nil),
			Tokens:       tokens,
			MinRelevance: settings.KnowledgeMinRelevance,
		})
		if err != nil {
			logger.WarnContext(ctx, "knowledge toolbox unavailable, running without it", "error", err)
		} else {
			toolboxes = append(toolboxes, knowledgeToolbox)
			unattended = append(unattended, knowledgeToolbox)
			unattendedQuiet = append(unattendedQuiet, knowledgeToolbox)
			logger.InfoContext(ctx, "knowledge toolbox", "url", settings.RagURL)
		}
	}
	if options.Directory != nil {
		directoryToolbox, err := agents.DirectoryToolbox(agents.DirectoryConfig{Directory: options.Directory, Tokens: tokens})
		if err != nil {
			return fail("organization directory toolbox", err)
		}
		toolboxes = append(toolboxes, directoryToolbox)
		logger.InfoContext(ctx, "organization directory toolbox configured")
	}
	if options.Interactions != nil {
		interactionToolbox, err := agents.InteractionToolbox(agents.InteractionConfig{Service: options.Interactions, Tokens: tokens})
		if err != nil {
			return fail("agent interactions toolbox", err)
		}
		toolboxes = append(toolboxes, interactionToolbox)
		// An interaction is delivered to an agent whose member is away, and
		// the run has to read the request, record a decision and answer it.
		// It may not start requests of its own on other members, and nothing
		// the directory, teamspaces or agent system offer belongs in a run
		// another member asked for.
		answeringToolbox, err := agents.InteractionToolbox(agents.InteractionConfig{Service: options.Interactions, Tokens: tokens, Answering: true})
		if err != nil {
			return fail("agent interactions toolbox for an unwatched run", err)
		}
		unattended = append(unattended, answeringToolbox)
		unattendedQuiet = append(unattendedQuiet, answeringToolbox)
		logger.InfoContext(ctx, "agent interactions toolbox configured")
	}
	if options.Teamspaces != nil {
		teamspaceToolbox, err := agents.TeamspaceToolbox(agents.TeamspaceConfig{Service: options.Teamspaces, Tokens: tokens})
		if err != nil {
			return fail("teamspace collaboration toolbox", err)
		}
		toolboxes = append(toolboxes, teamspaceToolbox)
		logger.InfoContext(ctx, "teamspace collaboration toolbox configured")
	}
	krude.toolboxes = toolboxes
	krude.unattended = unattended
	krude.unattendedQuiet = unattendedQuiet

	// Memory is built before the agents, because whether Krude is given
	// load_memory depends on there being a memory service to answer it.
	if settings.MemoryAgentEngine != "" {
		bank, err := adkmemory.NewMemoryBank(ctx, adkmemory.MemoryBankOptions{
			ProjectID:   settings.GoogleCloudProject,
			Location:    settings.MemoryLocation,
			AgentEngine: settings.MemoryAgentEngine,
			Recalled:    settings.MemoryRecalled,
			Credentials: options.Google,
		})
		if err != nil {
			return fail("memory bank", err)
		}
		krude.closers = append(krude.closers, func(context.Context) error {
			if err := bank.Close(); err != nil {
				return fmt.Errorf("close the memory bank: %w", err)
			}
			return nil
		})
		remembering, err := adkmemory.New(bank)
		if err != nil {
			return fail("memory", err)
		}
		krude.Memory = remembering
		logger.InfoContext(ctx, "memories kept in Vertex AI Memory Bank", "agent_engine", settings.MemoryAgentEngine, "location", settings.MemoryLocation)
	} else {
		logger.InfoContext(ctx, "Krude remembers nothing between threads; set AGENTS_PRIMARY_MEMORY_AGENT_ENGINE to give it memory")
	}

	root, err := agents.Root(router, toolboxes, krude.Memory != nil)
	if err != nil {
		return fail("root agent", err)
	}
	krude.Root = root
	unattendedRoot, err := agents.UnattendedRoot(router, unattended)
	if err != nil {
		return fail("unattended root agent", err)
	}
	krude.Unattended = unattendedRoot
	quietRoot, err := agents.UnattendedRoot(router, unattendedQuiet)
	if err != nil {
		return fail("unattended root agent without apps", err)
	}
	krude.UnattendedQuiet = quietRoot

	artifacts := artifact.InMemoryService()
	if settings.ArtifactBucket != "" {
		artifactConfig := options.AWS
		if settings.S3Endpoint != "" {
			// The emulator takes any signature, so the client carries dummy
			// credentials instead of asking for real ones.
			artifactConfig.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
		}
		stored, err := s3artifact.NewFromConfig(artifactConfig, settings.S3Endpoint, settings.ArtifactBucket)
		if err != nil {
			return fail("artifact store", err)
		}
		artifacts = stored
		logger.InfoContext(ctx, "artifacts stored in S3", "bucket", settings.ArtifactBucket)
	} else {
		logger.InfoContext(ctx, "artifacts are held in memory and go with this process; set AGENTS_PRIMARY_ARTIFACT_BUCKET to keep them")
	}
	krude.Artifacts = artifacts

	return krude, nil
}

// Managed builds one persistent specialist from a stored definition. It is
// cheap and side-effect free: heavyweight clients and toolboxes are shared
// with the assembled primary agent.
func (krude *Krude) Managed(spec agents.ManagedSpec) (agent.Agent, error) {
	return agents.Managed(krude.router, krude.toolboxes, krude.Memory != nil, spec)
}

// ManagedUnattended builds the same specialist for a run nobody is watching:
// the reduced toolboxes Krude itself gets when it is woken, and no memory,
// because what steers such a run is text somebody else wrote. withApps says
// whether the run may read the member's connected apps; an interaction that
// has not been approved may not. The agent answers to the same name as the
// attended one, which is what binds the two in a run.
func (krude *Krude) ManagedUnattended(spec agents.ManagedSpec, withApps bool) (agent.Agent, error) {
	toolboxes := krude.unattendedQuiet
	if withApps {
		toolboxes = krude.unattended
	}
	return agents.Managed(krude.router, toolboxes, false, spec)
}

// OrganizationAgent builds an organization-owned synthesis agent. It shares
// the model router but none of the member-scoped toolboxes or memory.
func (krude *Krude) OrganizationAgent(spec agents.OrganizationSpec) (agent.Agent, error) {
	return agents.OrganizationAgent(krude.router, spec)
}

// Close stops what Krude opened: AgentCore sandboxes and browsers, and the
// prompt caches this process created. It runs every closer regardless of
// whether an earlier one failed — a stuck sandbox must never keep a cache
// from being deleted — and joins whatever errors they return.
func (krude *Krude) Close(ctx context.Context) error {
	var failures []error
	for i := len(krude.closers) - 1; i >= 0; i-- {
		if err := krude.closers[i](ctx); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

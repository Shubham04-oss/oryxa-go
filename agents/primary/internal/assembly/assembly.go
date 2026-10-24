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
	"slices"
	"strings"

	"cloud.google.com/go/auth"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardrail"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/knowledgeclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelcache"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/ragclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	workspacepolicy "github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
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
	// Policy controls live organization search behavior.
	Policy workspacepolicy.Provider
	// HTTP is the client the apps toolbox's MCP calls go out on. Defaults to
	// a fresh http.Client.
	HTTP *http.Client
	// Directory is the caller-visible people, team and agent directory. When
	// nil, the directory toolbox is absent.
	Directory agents.Directory
	Context   agents.ContextDirectory
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

// MemberTokens is auth's agent-token exchange, which mints a member's agent
// token under their standing grant for a run that carries no token of
// theirs, or nil when Settings does not name both AuthURL and ServiceKeyID.
// The service builds it once and hands the same one to Build and to the
// request ledger, so both share its cache.
func MemberTokens(settings serviceconfig.Config, awsConfig aws.Config) memberauth.TokenSource {
	if settings.AuthURL == "" || settings.ServiceKeyID == "" {
		return nil
	}
	return memberauth.NewAuthClient(memberauth.AuthClientConfig{
		BaseURL: settings.AuthURL,
		Signer:  serviceauth.Signer{Client: kms.NewFromConfig(awsConfig), KeyID: settings.ServiceKeyID, Service: "agents"},
	})
}

// DeliveryReach is how far a run that delivers one interaction may reach,
// which is also every toolbox it starts with: such a run lists and loads
// nothing, so what it may use is loaded, with its skill, from its first
// model call.
type DeliveryReach int

const (
	// DeliveryReport reads the request alone: for deciding a new request,
	// for telling the member who asked that it was rejected or answered, and
	// for approved work allowed neither the member's apps nor the knowledge.
	DeliveryReport DeliveryReach = iota
	// DeliveryQuiet adds the organization's knowledge, read-only, and never
	// the member's apps, for approved work allowed the knowledge alone.
	DeliveryQuiet
	// DeliveryReads adds the member's apps, read-only, and the knowledge,
	// for approved work allowed both.
	DeliveryReads
	// DeliveryApps is the member's apps, read-only, without the knowledge,
	// for approved work allowed the apps alone.
	DeliveryApps
)

// Krude is the assembled agent with what it runs with.
type Krude struct {
	// Root is Krude, the agent every conversation starts with.
	Root agent.Agent
	// Unattended is Krude for a run nobody is watching, such as a wake-up:
	// it reads the member's apps and knowledge, and can reach nothing else,
	// the member's requests included.
	Unattended agent.Agent
	// UnattendedQuiet is Unattended without the apps toolbox, for a run that
	// has not been allowed to read the member's apps.
	UnattendedQuiet agent.Agent
	// Room is Krude for its turn in a room its member's team shares, its
	// member reading the room or away alike: it answers from the room alone,
	// reaching none of the member's apps, knowledge, memory or requests, and
	// files what it cannot answer with its member, as a request from the
	// person who asked.
	Room agent.Agent
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
	// readOnlyApps is the apps toolbox with its reads alone, which a
	// specialist granted no writes gets in place of the full one; nil
	// without the apps service.
	readOnlyApps *agents.Toolbox
	// room is what a turn in a room has: filing what it cannot answer from
	// the room with its member, and nothing else.
	room []agents.Toolbox
	// deliveryToolboxes are the toolboxes of each reach a run delivering one
	// interaction may have, and delivering is Krude for each (Delivering).
	deliveryToolboxes map[DeliveryReach][]agents.Toolbox
	delivering        map[DeliveryReach]agent.Agent
	// deliveryThinking is the thinking level asked of the model on every call
	// of a run delivering one interaction, and of no other run; empty leaves
	// each model its own default.
	deliveryThinking genai.ThinkingLevel
	// singleAgent builds Root as one agent with tools
	// (agents.SingleAgentRoot) rather than with its sub-agents (agents.Root):
	// Settings' SingleAgent. Every other build is the same either way.
	singleAgent bool

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

	// The sandbox and browser run on AgentCore or Cloudflare per
	// organization (live.go).
	sandboxTools, browserTools, err := liveToolsets(ctx, options, krude, logger)
	if err != nil {
		return fail("sandbox and browser", err)
	}

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
	router, err := modelrouter.New(
		usage.Counted(modelcache.Cached(mainModel, promptCache, cacheConfig)),
		usage.Counted(modelcache.Cached(liteModel, promptCache, cacheConfig)),
	)
	if err != nil {
		return fail("model router", err)
	}
	if settings.GuardrailIdentifier != "" {
		// The answers that reach a person, main and lite: Krude's, its
		// specialists' and the organization agent's, whichever model they
		// answer on (L5). Internal calls (planner, helpers, sub-agents,
		// compaction) are not screened. A run whose profile is not guarded
		// skips it (threads, runprofile).
		bedrock := bedrockruntime.NewFromConfig(options.AWS)
		router = router.Guarded(func(llm model.LLM) model.LLM {
			return guardrail.Model(llm, bedrock, guardrail.Config{
				Identifier: settings.GuardrailIdentifier,
				Version:    settings.GuardrailVersion,
				FailClosed: settings.GuardrailFailClosed,
				Logger:     logger,
			})
		})
		logger.InfoContext(ctx, "guardrail screens every agent's answer; those answers do not stream",
			"guardrail", settings.GuardrailIdentifier, "version", settings.GuardrailVersion, "fail_closed", settings.GuardrailFailClosed)
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
		if tokens = MemberTokens(settings, options.AWS); tokens == nil {
			logger.WarnContext(ctx, "member token: AUTH_URL and AGENTS_PRIMARY_SERVICE_KEY_ID are not both set, so a run without the member's own token cannot act for them")
		}
	}
	// What a run nobody watches may use: nothing that reaches outside, since
	// text from the member's apps steers it, and none of the member's
	// requests, which that text must not list, decide or answer. See
	// agents.UnattendedRoot.
	var unattended []agents.Toolbox
	// unattendedQuiet is the unattended list without the apps toolbox, for a
	// run nobody is watching that has not been allowed to read the member's
	// apps at all.
	var unattendedQuiet []agents.Toolbox
	// deliveringInteractions joins either list for a run that delivers one
	// interaction, and is all a report of one's outcome has
	// (deliveryToolboxes).
	var deliveringInteractions *agents.Toolbox
	// room is what a turn in a room has (agents.RoomRoot).
	var room []agents.Toolbox
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
			krude.closers = append(krude.closers, func(context.Context) error {
				if err := appsToolbox.Close(); err != nil {
					return fmt.Errorf("close apps toolbox: %w", err)
				}
				return nil
			})
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
			krude.readOnlyApps = &readOnly
			krude.closers = append(krude.closers, func(context.Context) error {
				if err := readOnly.Close(); err != nil {
					return fmt.Errorf("close read-only apps toolbox: %w", err)
				}
				return nil
			})
		}
	}
	if settings.RagURL != "" {
		var sources agents.KnowledgeSources
		if settings.KnowledgeURL != "" {
			sources = knowledgeclient.New(settings.KnowledgeURL, nil)
		}
		knowledgeToolbox, err := agents.KnowledgeToolbox(agents.KnowledgeConfig{
			Rag:          ragclient.New(settings.RagURL, nil),
			Policy:       options.Policy,
			Sources:      sources,
			Tokens:       tokens,
			MinRelevance: settings.KnowledgeMinRelevance,
		})
		if err != nil {
			logger.WarnContext(ctx, "knowledge toolbox unavailable, running without it", "error", err)
		} else {
			toolboxes = append(toolboxes, knowledgeToolbox)
			readOnlyKnowledge, err := agents.KnowledgeToolbox(agents.KnowledgeConfig{
				Rag:          ragclient.New(settings.RagURL, nil),
				Policy:       options.Policy,
				Sources:      sources,
				ReadOnly:     true,
				Tokens:       tokens,
				MinRelevance: settings.KnowledgeMinRelevance,
			})
			if err != nil {
				return fail("read-only knowledge toolbox", err)
			}
			unattended = append(unattended, readOnlyKnowledge)
			unattendedQuiet = append(unattendedQuiet, readOnlyKnowledge)
			logger.InfoContext(ctx, "knowledge toolbox", "url", settings.RagURL)
		}
	}
	if options.Directory != nil {
		directoryToolbox, err := agents.DirectoryToolbox(agents.DirectoryConfig{Directory: options.Directory, Context: options.Context, Tokens: tokens})
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
		// the run reads that one request and decides or answers it, as its
		// delivery is for: it cannot list the member's other requests at all,
		// nor start requests of its own on other members, and nothing the
		// directory, teamspaces or agent system offer belongs in a run another
		// member asked for.
		deliveringToolbox, err := agents.InteractionToolbox(agents.InteractionConfig{Service: options.Interactions, Tokens: tokens, Delivering: true})
		if err != nil {
			return fail("agent interactions toolbox for a delivery", err)
		}
		deliveringInteractions = &deliveringToolbox
		// A turn in a room files with its member what it cannot answer from
		// the room, and holds none of their requests.
		roomToolbox, err := agents.RoomToolbox(agents.RoomConfig{Service: options.Interactions})
		if err != nil {
			return fail("room toolbox", err)
		}
		room = append(room, roomToolbox)
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
	krude.room = room
	krude.deliveryToolboxes = deliveryToolboxes(unattended, unattendedQuiet, deliveringInteractions)
	krude.deliveryThinking = settings.DeliveryThinking
	krude.singleAgent = settings.SingleAgent
	if settings.SingleAgent {
		logger.InfoContext(ctx, "Krude runs as one agent with web_search and plan, and no sub-agents")
	}
	if settings.DeliveryThinking != "" {
		logger.InfoContext(ctx, "runs delivering an agent request think at a set level; every other run as its model does by default",
			"thinking_level", settings.DeliveryThinking)
	}

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

	if err := krude.buildAgents(); err != nil {
		return fail("agents", err)
	}

	artifacts, err := artifactStore(ctx, options, logger)
	if err != nil {
		return fail("artifact store", err)
	}
	krude.Artifacts = artifacts

	return krude, nil
}

// Managed builds one persistent specialist from a stored definition, with
// the primary's toolboxes as far as grant lets it reach the member's apps
// (granted). It is cheap and side-effect free: heavyweight clients and
// toolboxes are shared with the assembled primary agent.
func (krude *Krude) Managed(spec agents.ManagedSpec, grant runprofile.Grant) (agent.Agent, error) {
	return agents.Managed(krude.router, krude.granted(krude.toolboxes, grant), krude.Memory != nil, spec)
}

// ManagedUnattended builds the same specialist for a run nobody is watching:
// the reduced toolboxes Krude itself gets when it is woken, as far as grant
// reaches, and no memory, because what steers such a run is text somebody
// else wrote. withApps says whether the run may read the member's connected
// apps; a turn in a room has ManagedRoom, and an interaction's delivery
// ManagedDelivering. The agent answers to the same name as the attended one,
// which is what binds the two in a run.
func (krude *Krude) ManagedUnattended(spec agents.ManagedSpec, withApps bool, grant runprofile.Grant) (agent.Agent, error) {
	toolboxes := krude.unattendedQuiet
	if withApps {
		toolboxes = krude.unattended
	}
	return agents.Managed(krude.router, krude.granted(toolboxes, grant), false, spec)
}

// granted is toolboxes as far as a specialist's grant reaches into the
// member's apps: without the apps toolbox when it names no app, and with
// the apps' reads alone when it grants no write. Which apps, tools and
// writes it may use within that, apps enforces on its agent token.
func (krude *Krude) granted(toolboxes []agents.Toolbox, grant runprofile.Grant) []agents.Toolbox {
	result := []agents.Toolbox{}
	for _, box := range toolboxes {
		if box.Name == agents.AppsToolboxName {
			if !grant.ReachesApps() || (!grant.Writes() && krude.readOnlyApps == nil) {
				continue
			}
			if !grant.Writes() {
				box = *krude.readOnlyApps
			}
		}
		result = append(result, box)
	}
	return result
}

// ManagedRoom builds the specialist for its turn in a room, as Room is
// Krude: it answers from the room alone, with no memory.
func (krude *Krude) ManagedRoom(spec agents.ManagedSpec) (agent.Agent, error) {
	return agents.ManagedRoom(krude.router, krude.room, spec)
}

// buildAgents builds Krude, for every kind of run, from the router and the
// toolboxes Build gathered.
func (krude *Krude) buildAgents() error {
	buildRoot := agents.Root
	if krude.singleAgent {
		buildRoot = agents.SingleAgentRoot
	}
	root, err := buildRoot(krude.router, krude.toolboxes, krude.Memory != nil)
	if err != nil {
		return fmt.Errorf("root agent: %w", err)
	}
	krude.Root = root
	if krude.Unattended, err = agents.UnattendedRoot(krude.router, krude.unattended); err != nil {
		return fmt.Errorf("unattended root agent: %w", err)
	}
	if krude.UnattendedQuiet, err = agents.UnattendedRoot(krude.router, krude.unattendedQuiet); err != nil {
		return fmt.Errorf("unattended root agent without apps: %w", err)
	}
	if krude.Room, err = agents.RoomRoot(krude.router, krude.room); err != nil {
		return fmt.Errorf("root agent in a room: %w", err)
	}
	krude.delivering = map[DeliveryReach]agent.Agent{}
	for _, reach := range []DeliveryReach{DeliveryReport, DeliveryQuiet, DeliveryReads, DeliveryApps} {
		delivering, err := agents.DeliveringRoot(krude.router, krude.toolboxesFor(reach), krude.deliveryThinking)
		if err != nil {
			return fmt.Errorf("root agent delivering an interaction: %w", err)
		}
		krude.delivering[reach] = delivering
	}
	return nil
}

// Delivering is Krude for a run that delivers one interaction, reaching as
// far as reach and starting with all of it loaded. Its interactions toolbox
// holds only what that one request needs, and none of what lists the
// member's other requests. Its model thinks at Settings' DeliveryThinking
// level, when one is set.
func (krude *Krude) Delivering(reach DeliveryReach) agent.Agent {
	if delivering, ok := krude.delivering[reach]; ok {
		return delivering
	}
	return krude.delivering[DeliveryReport]
}

// ManagedDelivering is the specialist for a run that delivers one
// interaction, as Delivering is Krude, as far as grant reaches.
func (krude *Krude) ManagedDelivering(spec agents.ManagedSpec, reach DeliveryReach, grant runprofile.Grant) (agent.Agent, error) {
	return agents.ManagedDelivering(krude.router, krude.granted(krude.toolboxesFor(reach), grant), spec, krude.deliveryThinking)
}

// DeliveryToolboxNames are the names of the toolboxes a run delivering one
// interaction with reach starts with, for the line that logs each such run.
func (krude *Krude) DeliveryToolboxNames(reach DeliveryReach) []string {
	names := []string{}
	for _, box := range krude.toolboxesFor(reach) {
		names = append(names, box.Name)
	}
	return names
}

// toolboxesFor is what a run delivering one interaction with reach starts
// with; a reach this does not know gets the least.
func (krude *Krude) toolboxesFor(reach DeliveryReach) []agents.Toolbox {
	if toolboxes, ok := krude.deliveryToolboxes[reach]; ok {
		return toolboxes
	}
	return krude.deliveryToolboxes[DeliveryReport]
}

// deliveryToolboxes are the toolboxes of each reach: approved work allowed
// the apps and the knowledge has an unwatched run's, approved work allowed
// the apps alone the same without the knowledge, and approved work allowed
// the knowledge alone those without the apps, each with the delivering
// interactions toolbox beside them; deciding a new request, and a report,
// have the delivering interactions toolbox alone.
func deliveryToolboxes(unattended, unattendedQuiet []agents.Toolbox, delivering *agents.Toolbox) map[DeliveryReach][]agents.Toolbox {
	apps := slices.DeleteFunc(slices.Clone(unattended), func(box agents.Toolbox) bool { return box.Name == agents.KnowledgeToolboxName })
	return map[DeliveryReach][]agents.Toolbox{
		DeliveryReads:  withToolbox(unattended, delivering),
		DeliveryApps:   withToolbox(apps, delivering),
		DeliveryQuiet:  withToolbox(unattendedQuiet, delivering),
		DeliveryReport: withToolbox(nil, delivering),
	}
}

// withToolbox is toolboxes with extra after them, or toolboxes as they are
// when there is no extra.
func withToolbox(toolboxes []agents.Toolbox, extra *agents.Toolbox) []agents.Toolbox {
	with := append([]agents.Toolbox{}, toolboxes...)
	if extra != nil {
		with = append(with, *extra)
	}
	return with
}

// Compact answers one prompt on the lite model and returns the text, with no
// tools, no memory and no conversation around it.
//
// It is what a room's brief is rewritten with: mechanical summarising that
// nobody is waiting on and that runs many times a day, so it belongs on the
// cheap model rather than the one members talk to.
func (krude *Krude) Compact(ctx context.Context, prompt string) (string, error) {
	llm := krude.router.For(modelrouter.RoleWork)
	request := &model.LLMRequest{
		Model:    llm.Name(),
		Contents: []*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)},
		Config: &genai.GenerateContentConfig{
			// A brief is data, not conversation: nothing here needs the model
			// to think out loud, and the JSON it returns is parsed strictly.
			Temperature: genai.Ptr[float32](0),
		},
	}
	var answered strings.Builder
	for response, err := range llm.GenerateContent(ctx, request, false) {
		if err != nil {
			return "", fmt.Errorf("compact a room: %w", err)
		}
		if response == nil || response.Content == nil {
			continue
		}
		for _, part := range response.Content.Parts {
			if part != nil && !part.Thought {
				answered.WriteString(part.Text)
			}
		}
	}
	return strings.TrimSpace(answered.String()), nil
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

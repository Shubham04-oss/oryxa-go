package primary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/inbound"
	agentinteractions "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/secretshares"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/surfaces"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/jev"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// The Slack consent loop, as agents assembles it (docs/design/slack-consent-loop.md).
//
// Everything of it lives in packages of its own, each built and tested against
// fakes; this file is where they meet. A member mentions the Oryxa app and
// names whose agents to ask; apps verifies the delivery and says so on the bus
// with ids; the consumer hands that to the router (slackrouter), which files an
// ask with the consent core (interactions); and the surfaces worker says in
// Slack, from a re-read of the ledger, whatever the ledger says that Slack does
// not yet say. The loop is off unless AGENTS_PRIMARY_SLACK_CONSENT says on.

const (
	// slackRouterConcurrency and slackSurfacesConcurrency are how many
	// deliveries the router routes at once, and how many requests the surfaces
	// worker works on at once. Each pass is a few calls to apps.
	slackRouterConcurrency   = 4
	slackSurfacesConcurrency = 4
	// askLinkTimeout bounds finding what the link in an acknowledgement opens.
	// The acknowledgement is best effort, and a link without a request is a
	// link all the same.
	askLinkTimeout = 2 * time.Second
)

// slackLoopConfig is what the loop is assembled from: what Mount has built by
// then, and the settings.
type slackLoopConfig struct {
	Settings serviceconfig.Config
	Platform host.Platform
	Logger   *slog.Logger

	Interactions     *agentinteractions.Service
	InteractionStore *agentinteractions.Store
	Threads          *threads.Service
	Sessions         *adksession.Store
	// Directory and Context read the organization as a member's agent does: its
	// teams and its chart. Either may be nil, and nobody's department is known
	// and nobody outranks anybody.
	Directory agents.Directory
	Context   agents.ContextDirectory
	Tokens    memberauth.TokenSource
	// Jev is TypeSafe's client, nil without a TYPESAFE_API_KEY, and then the
	// screen cannot judge and every mention is refused.
	Jev *jev.Client
}

// slackLoop is the loop assembled: the two workers to run, and what the
// consumer hands events to and has forgotten.
type slackLoop struct {
	router   *slackrouter.Worker
	surfaces *surfaces.Worker
	// memberForgetters remove what the loop holds for a member who left, or for
	// a deleted organization when no member is named. Each needs none of the
	// other services' to have run.
	memberForgetters []inbound.Forgetter
	// workspaceForgetters remove what it holds for a workspace whose install
	// ended.
	workspaceForgetters []inbound.WorkspaceForgetter
}

// newSlackLoop assembles the loop. The database is not touched: the migrations
// ran already (Migrate), and the workers start after Mount returns.
func newSlackLoop(config slackLoopConfig) (*slackLoop, error) {
	settings, logger := config.Settings, config.Logger
	if config.Interactions == nil {
		return nil, errors.New("the Slack consent loop needs the request ledger, which needs TEAMS_URL: requests are between members, and agents finds a member's agents through Teams")
	}
	if config.Platform.Database == nil {
		return nil, host.ErrDatabaseRequired
	}
	// One client for apps' Slack API, shared by the router, which reads what
	// people wrote and posts refusals, and the surfaces worker, which posts what
	// became of a request. It signs each call as agents with the same key agents
	// proves itself to auth with.
	apps, err := appsclient.New(appsclient.Config{
		BaseURL: settings.AppsURL,
		Signer: serviceauth.Signer{
			Client: kms.NewFromConfig(config.Platform.AWS), KeyID: settings.ServiceKeyID, Service: Name,
		},
		Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("apps' Slack API: %w", err)
	}

	routerStore := slackrouter.NewStore(config.Platform.Database)
	var askJev func(context.Context, jev.Request) (jev.Result, error)
	if config.Jev != nil {
		askJev = config.Jev.Ask
	} else {
		logger.Warn("TYPESAFE_API_KEY is not set: the Slack screen has no Jev to judge with, so every mention of the bot is refused and nobody is asked")
	}
	router, err := slackrouter.NewRouter(slackrouter.Deps{
		Slack:      apps,
		Members:    slackrouter.NewAuthMembers(config.Platform.Database),
		Identities: routerStore,
		Asks:       slackAsks{service: config.Interactions},
		Screen:     slackrouter.NewJevScreen(slackrouter.JevScreenConfig{Ask: askJev}),
		Directory:  slackrouter.NewAskerDirectory(config.Directory, config.Tokens),
		Redact:     redactCredentials,
		Authority:  slackrouter.NewChartAuthority(config.Context, config.Tokens),
		Asked:      routerStore,
		Refused:    routerStore,
		OpenURL:    askLink(firstOrigin(settings.AllowedOrigins), config.InteractionStore),
		// The router tells the asker who was asked: the surfaces worker never does.
		SkipAck: false,
		Logger:  logger,
	})
	if err != nil {
		return nil, err
	}
	routerWorker := slackrouter.NewWorker(routerStore, router, slackrouter.WorkerConfig{Concurrency: slackRouterConcurrency}, logger)

	surfacesStore := surfaces.NewStore(config.Platform.Database)
	surfacesWorker, err := surfaces.New(surfacesStore, surfaces.Deps{
		Requests: config.InteractionStore,
		Asks:     config.InteractionStore,
		Tickets:  config.Interactions,
		Slack:    apps,
		Users:    routerStore,
		Notes:    config.Threads,
		Members:  config.Sessions,
		Logger:   logger,
	}, surfaces.WorkerConfig{Concurrency: slackSurfacesConcurrency})
	if err != nil {
		return nil, err
	}
	return &slackLoop{
		router: routerWorker, surfaces: surfacesWorker,
		memberForgetters:    []inbound.Forgetter{routerStore.Forget, surfacesStore.Forget},
		workspaceForgetters: []inbound.WorkspaceForgetter{routerStore.ForgetTeam, surfacesStore.ForgetTeam},
	}, nil
}

// newConsentGuard is what reads the answer to a question asked in Slack before the
// asker reads it there. With a guardrail it grounds the answer in what the run
// read; without one it holds every such answer for its owner's review, as
// unchecked, rather than deliver what nothing read.
func newConsentGuard(settings serviceconfig.Config, client consentguard.BedrockClient, logger *slog.Logger) (*consentguard.Guard, error) {
	var grounder consentguard.Grounder
	if settings.ConsentGuardrailIdentifier != "" {
		bedrock, err := consentguard.NewBedrockGrounder(client, settings.ConsentGuardrailIdentifier, settings.ConsentGuardrailVersion)
		if err != nil {
			return nil, err
		}
		grounder = bedrock
		logger.Info("the consent guard grounds every answer to a question asked in Slack in what its run read",
			"guardrail", settings.ConsentGuardrailIdentifier, "version", settings.ConsentGuardrailVersion)
	} else {
		logger.Warn("AGENTS_PRIMARY_CONSENT_GUARDRAIL_ID is not set: every answer to a question asked in Slack is held for its owner's review as unchecked, since nothing can ground it")
	}
	return consentguard.New(grounder, consentguard.Config{Logger: logger, KnowledgePassagesOnly: settings.ConsentPassagesOnly}), nil
}

// redactCredentials is the words of an ask with every credential in them
// masked, which is what the router is given to apply before the screen reads
// them.
func redactCredentials(text string) string {
	stripped, _ := consentguard.Strip([]string{text})
	return stripped[0]
}

// firstOrigin is the origin of the web app, which is the first of the origins
// allowed to make requests as a member: production names the web app's alone,
// and apps has to be told the same one before it will link to a page on it.
func firstOrigin(origins []string) string {
	if len(origins) == 0 {
		return ""
	}
	return strings.TrimRight(origins[0], "/")
}

// askReader is what finding an ask's link reads: the ask, and the requests its
// asker sent under it. interactions.Store implements it.
type askReader interface {
	Ask(ctx context.Context, id string) (agentinteractions.Ask, error)
	List(ctx context.Context, organizationID, userID string, query agentinteractions.Query) ([]agents.Interaction, error)
}

// askLink is the address the acknowledgement of an ask links to, which the web
// app opens on Requests: for the organization the ask was made in and, when the
// ask went to one person, on the request its asker sent them. Two, and there is
// no one card to open, so the link is to Requests and the asker's Sent tab shows
// both. It is the web app's own address form (web-next's requestsLink), on the
// origin apps has been told is the web app's.
//
// With no origin there is no link. A lookup that fails, which is only the
// database being slow, leaves a link to Requests without either.
func askLink(origin string, asks askReader) func(askID string) string {
	if origin = strings.TrimRight(origin, "/"); origin == "" {
		return nil
	}
	return func(askID string) string {
		ctx, cancel := context.WithTimeout(context.Background(), askLinkTimeout)
		defer cancel()
		organizationID, requestID := "", ""
		if ask, err := asks.Ask(ctx, askID); err == nil {
			organizationID = ask.OrganizationID
			sent, err := asks.List(ctx, ask.OrganizationID, ask.AskerUserID, agentinteractions.Query{
				Box: agentinteractions.BoxSent, AskID: askID, Limit: 2,
			})
			if err == nil && len(sent) == 1 {
				requestID = sent[0].ID
			}
		}
		link := origin + "/?"
		if organizationID != "" {
			link += "org=" + url.QueryEscape(organizationID) + "&"
		}
		link += "view=requests"
		if requestID != "" {
			link += "&request=" + url.QueryEscape(requestID)
		}
		return link
	}
}

// slackAsks is the consent core as the router asks it: the router's own words
// for an ask, turned into the ledger's, and the ledger's refusals into what the
// router does with them. A refusal that the same call would meet again is
// permanent, and the job is dropped; anything else is a failure to try again.
type slackAsks struct{ service *agentinteractions.Service }

var _ slackrouter.Asks = slackAsks{}

// CreateAsk implements slackrouter.Asks.
func (asks slackAsks) CreateAsk(ctx context.Context, input slackrouter.AskInput) (slackrouter.Ask, error) {
	owners := make([]agentinteractions.AskOwner, 0, len(input.Owners))
	for _, owner := range input.Owners {
		asked := agentinteractions.AskOwner{UserID: owner.UserID, Email: owner.Email, DisplayName: owner.Name}
		if owner.Bypass != nil {
			asked.Bypass = string(owner.Bypass.Reason)
		}
		owners = append(owners, asked)
	}
	result, err := asks.service.CreateAsk(ctx, agentinteractions.AskInput{
		OrganizationID: input.OrganizationID,
		AskerUserID:    input.Asker.UserID,
		Slack: agentinteractions.SlackThread{
			TeamID: input.Origin.TeamID, ChannelID: input.Origin.ChannelID,
			ThreadTS: input.Origin.ThreadTS, TriggerTS: input.Origin.TriggerTS,
		},
		Question: input.Question,
		Owners:   owners,
		// The screen's word on what is being asked for: a question, or a credential.
		Kind: input.Kind,
	})
	if err != nil {
		if isRefusal(err) {
			return slackrouter.Ask{}, slackrouter.Permanent(err)
		}
		return slackrouter.Ask{}, err
	}
	return slackrouter.Ask{ID: result.Ask.ID}, nil
}

// CountOpenAsks implements slackrouter.Asks.
func (asks slackAsks) CountOpenAsks(ctx context.Context, organizationID, askerUserID string) (int, error) {
	return asks.service.CountOpenAsks(ctx, organizationID, askerUserID)
}

// Consent implements slackrouter.Asks. It hands the click to the ledger as it
// is: the ticket the button carried, the member who clicked and where. Whether
// the ticket is genuine, whether the clicker is who it names and whether the
// request still waits are the ledger's to check, and a click it refuses, for any
// of them, is refused for good.
func (asks slackAsks) Consent(ctx context.Context, ticket, clickerUserID, surface string) error {
	if _, err := asks.service.Consent(ctx, ticket, clickerUserID, surface); err != nil {
		if isRefusal(err) {
			return slackrouter.Permanent(err)
		}
		return err
	}
	return nil
}

// Review implements slackrouter.Asks. It hands a click on a button of an owner's
// review of a held answer to the ledger as it is: the ticket the button carried
// and the member who clicked. Whether the ticket is a genuine review ticket,
// whether the clicker is who it names and whether the answer is still in review
// are the ledger's to check, and a click it refuses, for any of them, is refused
// for good: a second click on a review that is over, on either surface, is one.
func (asks slackAsks) Review(ctx context.Context, ticket, clickerUserID string) error {
	if _, err := asks.service.ReviewFromSlack(ctx, ticket, clickerUserID); err != nil {
		if isRefusal(err) {
			return slackrouter.Permanent(err)
		}
		return err
	}
	return nil
}

// Reply implements slackrouter.Asks. It hands a reply an owner sent from the
// modal of a Reply yourself button to the ledger as it is: the ticket the button
// carried, the member who sent it, and their words. Whether the ticket is a
// genuine reply ticket, whether the sender is who it names, whether the words
// are a reply and whether the request still waits for its owner are the ledger's
// to check, and a reply it refuses, for any of them, is refused for good: a reply
// that came after the owner's choice, review or other reply is one.
func (asks slackAsks) Reply(ctx context.Context, ticket, senderUserID, text string) error {
	if _, err := asks.service.ReplyFromSlack(ctx, ticket, senderUserID, text); err != nil {
		if isRefusal(err) {
			return slackrouter.Permanent(err)
		}
		return err
	}
	return nil
}

// isRefusal reports whether err is the ledger saying no to a call that the same
// call would meet again: a ticket it did not make, of the other kind or that ran
// out, a click by another member, a request already decided or reviewed, expired,
// closed or gone, an ask it cannot take. It is not a database that did not
// answer.
func isRefusal(err error) bool {
	for _, refusal := range []error{
		agentinteractions.ErrTicketInvalid, agentinteractions.ErrTicketExpired,
		agentinteractions.ErrForbidden, agentinteractions.ErrAlreadyDecided, agentinteractions.ErrConflict,
		agentinteractions.ErrExpired, agentinteractions.ErrNotFound, agentinteractions.ErrInvalid,
		agentinteractions.ErrIdempotencyConflict, agentinteractions.ErrConsentUnavailable, agentinteractions.ErrClosed,
	} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// primaryAgents finds a member's primary agent through the agent registry,
// which the ledger is built before: the ledger is what Krude's tools use, and
// the registry needs Krude. Mount binds the registry as soon as it exists,
// before anything is served or run.
type primaryAgents struct {
	registry atomic.Pointer[managedagents.Service]
}

var _ agentinteractions.PrimaryAgents = (*primaryAgents)(nil)

func (primaries *primaryAgents) bind(registry *managedagents.Service) {
	primaries.registry.Store(registry)
}

// Primary implements interactions.PrimaryAgents.
func (primaries *primaryAgents) Primary(ctx context.Context, scope managedagents.Scope) (managedagents.Definition, error) {
	registry := primaries.registry.Load()
	if registry == nil {
		return managedagents.Definition{}, errors.New("the agent registry is not ready yet")
	}
	return registry.Primary(ctx, scope)
}

// unavailableSealer is the sealer of a secret store that has no KMS key, which
// is one that can only be migrated, forgotten from and swept: nothing is ever
// sealed in it, and no route is given it to share a secret with.
type unavailableSealer struct{}

var errNoSecretsKey = errors.New("one-time secrets have no KMS key: AGENTS_PRIMARY_SECRETS_KMS_KEY_ID is not set")

func (unavailableSealer) Seal(context.Context, secrets.Binding, []byte) (secrets.SealedData, error) {
	return secrets.SealedData{}, errNoSecretsKey
}

func (unavailableSealer) Open(context.Context, secrets.Binding, secrets.SealedData) ([]byte, error) {
	return nil, errNoSecretsKey
}

// newSecretShares is the store one-time secrets are kept in, and whether it can
// seal any. Without a KMS key it still exists, so that its table is migrated
// and forgotten from with the member, and swept, whatever the deployment
// configured, and only the routes that share a secret are left without it.
func newSecretShares(settings serviceconfig.Config, platform host.Platform, logger *slog.Logger) (*secretshares.Store, bool, error) {
	if settings.SecretsKMSKeyID == "" {
		store, err := secretshares.New(platform.Database, unavailableSealer{}, logger)
		return store, false, err
	}
	sealer, err := secretshares.NewSealer(kms.NewFromConfig(platform.AWS), settings.SecretsKMSKeyID)
	if err != nil {
		return nil, false, err
	}
	store, err := secretshares.New(platform.Database, sealer, logger)
	return store, true, err
}

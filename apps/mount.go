package apps

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/events"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/slackbot"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// URLVariable is how another service reaches apps, such as agents finding the
// MCP endpoint.
const URLVariable = "APPS_URL"

// Definition is how a host builds apps.
var Definition = host.Definition{Name: Name, URLVariable: URLVariable, EventsQueueVariable: "APPS_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs apps' schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles apps on what the host shares.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	if len(config.AllowedOrigins) == 0 {
		logger.Warn("APPS_ALLOWED_ORIGINS is not set: members can manage apps only with bearer tokens, and no page can be returned to after connecting one")
	}
	if config.ComposioWebhookSecret == "" {
		logger.Warn("COMPOSIO_WEBHOOK_SECRET is not set: every webhook delivery will be refused, so no trigger will fire")
	}
	if config.NoAutoSubscribe {
		logger.Info("apps auto-subscribe is off (APPS_AUTO_SUBSCRIBE=false): a newly connected app is subscribed to nothing until a member asks, whatever the connection itself requested")
	} else {
		logger.Info("apps auto-subscribe is on: a newly connected app is subscribed to everything it can deliver unless the connection says otherwise")
	}

	client, err := composio.New(composio.Config{
		APIKey:  config.ComposioAPIKey,
		BaseURL: config.ComposioBaseURL,
		HTTP:    &http.Client{Timeout: 60 * time.Second, Transport: observability.Transport(nil)},
	})
	if err != nil {
		return host.Service{}, err
	}
	eventClient := eventbridge.NewFromConfig(platform.AWS, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})
	signer, err := platform.EventAuth.Signer(Name)
	if err != nil {
		return host.Service{}, err
	}
	publisher, err := events.NewPublisher(eventClient, platform.EventBusName, signer)
	if err != nil {
		return host.Service{}, err
	}

	store := postgres.New(platform.Database)
	directory := authclient.New(config.AuthURL)
	oryxa, err := mountOryxa(platform, config, store, directory, logger)
	if err != nil {
		return host.Service{}, err
	}
	var services *serviceauth.Verifier
	if len(config.ServiceCallers) > 0 {
		if services, err = serviceauth.NewVerifier(serviceauth.VerifierConfig{
			Keys: kms.NewFromConfig(platform.AWS), Audience: Name, Callers: config.ServiceCallers,
		}); err != nil {
			return host.Service{}, err
		}
	}
	dependencies := Dependencies{
		Store:     store,
		Directory: directory,
		Composio:  client,
		Sink:      publisher,
		Verifier:  platform.EventAuth.Verifier(),
		Logger:    logger,
	}
	dependencies.Policy = platform.WorkspaceSettings()
	if oryxa != nil {
		dependencies.Signin, dependencies.Hooks, dependencies.Integrations = oryxa.signin, oryxa.hooks, oryxa.registry
		dependencies.Slack = oryxa.slack
	}
	if services != nil {
		dependencies.Services = services
	}
	defaults := make([]application.DefaultClient, 0, len(config.DefaultOAuthClients))
	for _, client := range config.DefaultOAuthClients {
		defaults = append(defaults, application.DefaultClient{
			Client: client.Client, ClientID: client.ClientID, ClientSecret: client.ClientSecret, Fields: client.Fields,
		})
	}
	service, err := New(dependencies, Config{
		PublicBaseURL:       config.PublicBaseURL,
		AllowedOrigins:      config.AllowedOrigins,
		WebhookSecret:       config.ComposioWebhookSecret,
		NoAutoSubscribe:     config.NoAutoSubscribe,
		OryxaToolkits:       config.OryxaToolkits,
		ToolkitBrokers:      config.ToolkitBrokers,
		SigninRedirectURI:   config.SigninRedirectURI,
		DefaultOAuthClients: defaults,
	})
	if err != nil {
		return host.Service{}, err
	}

	workers := make([]host.Worker, 0, len(service.Workers())+1)
	for _, worker := range service.Workers() {
		workers = append(workers, host.Worker(worker))
	}
	if config.EventsQueueURL != "" {
		queue := sqs.NewFromConfig(platform.AWS, func(options *sqs.Options) {
			if config.SQSEndpoint != "" {
				options.BaseEndpoint = aws.String(config.SQSEndpoint)
			}
		})
		consumer := service.Consumer()
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) })
	} else {
		logger.Warn("APPS_EVENTS_QUEUE_URL is not set: apps will not disconnect a member's apps when they leave an organization")
	}

	logger.Info("apps mounted", "webhook_url", config.PublicBaseURL+WebhookPath)
	return host.Service{
		Handler: service.Handler(),
		Routes:  service.Routes(),
		// A tool run waits on a third-party app, which is slower than
		// anything else Oryxa serves; a write must not be cut off halfway.
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 120 * time.Second},
		Workers:  workers,
	}, nil
}

// oryxaParts are Oryxa's own sign-in as Mount builds it, and the Slack
// surface that rides on its webhook ingress and its vault key.
type oryxaParts struct {
	registry *integration.Registry
	signin   *signin.Service
	hooks    *hooks.Service
	// slack is nil unless APPS_SLACK_SURFACE is on.
	slack *slackbot.Service
}

// mountOryxa builds Oryxa's own sign-in and its webhook ingress on apps'
// store, with a KMS envelope bound to service oryxa-apps, when this
// deployment has a key for it. Without APPS_SIGNIN_KMS_KEY_ID it builds
// nothing, and Oryxa's own sign-in is not configured. With it, sign-in is
// switched on only for the toolkits APPS_ORYXA_TOOLKITS names, each of which
// this build must ship.
func mountOryxa(platform host.Platform, config serviceconfig.Config, store *postgres.Store, directory slackbot.Directory, logger *slog.Logger) (*oryxaParts, error) {
	if config.SigninKMSKeyID == "" {
		logger.Info("Oryxa's own sign-in is not configured (APPS_SIGNIN_KMS_KEY_ID is not set): every app connects through Composio")
		if len(config.HookSecrets) > 0 || len(config.HookValues) > 0 {
			logger.Warn("APPS_HOOK_SECRETS or APPS_HOOK_VALUES is set, and unused without APPS_SIGNIN_KMS_KEY_ID: no webhook endpoint of Oryxa's own is served")
		}
		return nil, nil
	}
	registry, err := integrations.NewRegistry()
	if err != nil {
		return nil, err
	}
	for _, toolkit := range config.OryxaToolkits {
		if _, ok := registry.Lookup(toolkit); !ok {
			return nil, fmt.Errorf("APPS_ORYXA_TOOLKITS names %s, which no integration in this build registers", toolkit)
		}
	}
	for toolkit := range config.HookSecrets {
		// Only a deployment endpoint verifies with configured secrets, and
		// only one signed with a shared secret: Gmail's pushes are signed
		// by Google, and Notion sends its own.
		registered, ok := registry.Lookup(toolkit)
		var webhook *integration.Webhook
		if ok {
			webhook = registered.Manifest().Webhook
		}
		if webhook == nil || webhook.Scope != integration.HookScopeDeployment || webhook.Verify.Type == integration.VerifyJWTPush ||
			webhook.Handshake == integration.HandshakeNotionVerification {
			return nil, fmt.Errorf("APPS_HOOK_SECRETS names %s, whose deliveries in this build are not verified with a configured secret", toolkit)
		}
	}
	returnAllowed, err := application.ReturnChecker(config.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	envelope := secrets.Envelope{
		Keys:  secrets.KMSDataKeys{Client: kms.NewFromConfig(platform.AWS), KeyID: config.SigninKMSKeyID},
		KeyID: config.SigninKMSKeyID, Service: signin.SealService,
	}
	signinService, err := signin.New(signin.Dependencies{
		Store: store, Sealer: envelope, Integrations: registry, Logger: logger.With("component", "signin"),
	}, signin.Config{RedirectURI: config.SigninRedirectURI, ReturnAllowed: returnAllowed})
	if err != nil {
		return nil, err
	}
	// The Slack surface is a toolkit of the webhook ingress that is no
	// integration: its deliveries are verified with the app's signing secret,
	// from APPS_SLACK_APP, and go to the surface, and its bot tokens are
	// sealed with the same vault key as everything else here.
	dependencies := hooks.Dependencies{
		Store: store, Sealer: envelope, Integrations: registry, Subscriptions: application.HookSubscriptions{},
		Logger: logger.With("component", "hooks"),
	}
	deploymentSecrets := config.HookSecrets
	var slack *slackbot.Service
	if config.SlackSurface {
		if slack, err = slackbot.New(slackbot.Dependencies{
			Store: store, Sealer: envelope, Directory: directory, Logger: logger.With("component", "slackbot"),
		}, slackbot.Config{
			PublicBaseURL: config.PublicBaseURL, APIBaseURL: config.SlackAPIBaseURL, ReturnAllowed: returnAllowed,
			WebOrigins: config.AllowedOrigins,
			App: slackbot.App{
				ClientID: config.SlackApp.ClientID, ClientSecret: config.SlackApp.ClientSecret, SigningSecret: config.SlackApp.SigningSecret,
			},
		}); err != nil {
			return nil, err
		}
		dependencies.Surfaces = slack
		deploymentSecrets = maps.Clone(config.HookSecrets)
		if deploymentSecrets == nil {
			deploymentSecrets = map[string][]secrets.Secret{}
		}
		deploymentSecrets[slackbot.Toolkit] = []secrets.Secret{config.SlackApp.SigningSecret}
		logger.Info("the Slack surface is on",
			"events_url", config.PublicBaseURL+slackbot.EventsPath, "interactivity_url", config.PublicBaseURL+slackbot.InteractivityPath,
			"install_redirect_url", config.PublicBaseURL+slackbot.InstallCallbackPath)
	}
	hooksService, err := hooks.New(dependencies, hooks.Config{
		PublicBaseURL: config.PublicBaseURL, DeploymentSecrets: deploymentSecrets, Values: config.HookValues,
	})
	if err != nil {
		return nil, err
	}
	if len(config.OryxaToolkits) == 0 {
		logger.Info("Oryxa's own sign-in is configured and switched off (APPS_ORYXA_TOOLKITS is empty): connections made through it are still checked, revoked and removed")
	} else {
		logger.Info("Oryxa's own sign-in is on", "toolkits", strings.Join(config.OryxaToolkits, ","),
			"redirect_uri", config.SigninRedirectURI, "webhook_base", config.PublicBaseURL+hooks.PathPrefix)
	}
	return &oryxaParts{registry: registry, signin: signinService, hooks: hooksService, slack: slack}, nil
}

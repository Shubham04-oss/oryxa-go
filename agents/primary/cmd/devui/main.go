// Command devui runs primary's agents in ADK's development web UI, or in its
// console. It is for local development only; primary mounts in the oryxa
// binary as the service.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/console"
	"google.golang.org/adk/v2/cmd/launcher/universal"
	"google.golang.org/adk/v2/cmd/launcher/web"
	"google.golang.org/adk/v2/cmd/launcher/web/api"
	"google.golang.org/adk/v2/cmd/launcher/web/webui"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentcorebrowser"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentcoresandbox"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelcache"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	// Aliased: this file's own outbox variable is agents primary's, an
	// *events.Outbox, and would otherwise shadow the package.
	sharedoutbox "github.com/Shubham04-oss/oryxa-go/internal/outbox"
)

// defaultArgs serves the web UI and the REST API behind it on one port, clear
// of the ports the other services use locally.
var defaultArgs = []string{
	"web", "-port", "8087",
	"api", "-webui_address", "localhost:8087",
	"webui", "-api_server_address", "http://localhost:8087/api",
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	settings, err := serviceconfig.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	sessionStore, err := adksession.Open(ctx, settings.DatabaseURL, settings.AutoMigrate)
	if err != nil {
		log.Fatalf("ADK sessions: %v", err)
	}
	defer sessionStore.Close()
	log.Printf("ADK sessions connected to PostgreSQL")

	outbox := events.NewOutbox(sessionStore.Database())
	if err := outbox.Ensure(ctx); err != nil {
		log.Fatalf("events outbox: %v", err)
	}
	recorder := events.NewSessionRecorder(outbox)

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(settings.AWSRegion))
	if err != nil {
		log.Fatalf("AWS configuration: %v", err)
	}
	otelaws.AppendMiddlewares(&awsConfig.APIOptions)
	if settings.EventBridgeEndpoint != "" {
		// The emulator takes any signature, so the client carries dummy
		// credentials instead of asking for real ones.
		eventConfig := awsConfig
		eventConfig.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
		eventClient := eventbridge.NewFromConfig(eventConfig, func(options *eventbridge.Options) {
			options.BaseEndpoint = aws.String(settings.EventBridgeEndpoint)
		})
		publisher := events.NewPublisher(eventClient, settings.EventBusName)
		table, err := sharedoutbox.NewTable(sessionStore.Database().Pool, "agents_primary.agents_outbox")
		if err != nil {
			log.Fatalf("events outbox table: %v", err)
		}
		dispatcher := sharedoutbox.NewDispatcher(table, sharedoutbox.PublisherFunc(func(ctx context.Context, event sharedoutbox.Event) error {
			return publisher.PublishEvent(ctx, events.Event{ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data})
		}), slog.Default())
		go dispatcher.Run(ctx)
		log.Printf("events dispatcher publishing to bus %q at %s", settings.EventBusName, settings.EventBridgeEndpoint)
	} else {
		log.Printf("session events are recorded in the outbox; set EVENTBRIDGE_ENDPOINT to publish them now")
	}
	sandbox, err := agentcoresandbox.New(awsConfig, agentcoresandbox.Options{
		CodeInterpreterIdentifier: settings.SandboxCodeInterpreter,
		SessionTimeout:            settings.SandboxSessionTimeout,
	})
	if err != nil {
		log.Fatalf("AgentCore sandbox: %v", err)
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := sandbox.Close(shutdownContext); err != nil {
			log.Printf("stop AgentCore sandboxes: %v", err)
		}
	}()
	sandboxTools, err := agentcoresandbox.NewToolset(sandbox)
	if err != nil {
		log.Fatalf("AgentCore sandbox tools: %v", err)
	}
	browser, err := agentcorebrowser.New(awsConfig, agentcorebrowser.Options{
		BrowserIdentifier: settings.BrowserIdentifier,
		SessionTimeout:    settings.BrowserSessionTimeout,
	})
	if err != nil {
		log.Fatalf("AgentCore browser: %v", err)
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := browser.Close(shutdownContext); err != nil {
			log.Printf("stop AgentCore browsers: %v", err)
		}
	}()
	browserTools, err := agentcorebrowser.NewToolset(browser)
	if err != nil {
		log.Fatalf("AgentCore browser tools: %v", err)
	}
	log.Printf("AWS AgentCore sandbox and browser configured in %s", settings.AWSRegion)

	mainModel, err := agents.Gemini(ctx, settings.Model, settings.GoogleCloudProject, settings.GoogleCloudLocation)
	if err != nil {
		log.Fatalf("main model: %v", err)
	}
	liteModel, err := agents.Gemini(ctx, settings.ModelLite, settings.GoogleCloudProject, settings.GoogleCloudLocation)
	if err != nil {
		log.Fatalf("lite model: %v", err)
	}

	var promptCache modelcache.Store
	if settings.Cache {
		client, err := agents.GeminiClient(ctx, settings.GoogleCloudProject, settings.GoogleCloudLocation)
		if err != nil {
			log.Fatalf("prompt cache client: %v", err)
		}
		cacheStore := modelcache.NewGenaiStore(client, settings.CacheTTL)
		promptCache = cacheStore
		defer func() {
			closeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := cacheStore.Close(closeContext); err != nil {
				log.Printf("delete prompt caches: %v", err)
			}
		}()
	}
	cacheConfig := modelcache.Config{TTL: settings.CacheTTL, MinTokens: settings.CacheMinTokens}
	router, err := modelrouter.New(
		modelcache.Cached(mainModel, promptCache, cacheConfig),
		modelcache.Cached(liteModel, promptCache, cacheConfig),
	)
	if err != nil {
		log.Fatalf("model router: %v", err)
	}
	log.Printf("models: %s with %s for planning and bulk work; prompt cache %t", settings.Model, settings.ModelLite, settings.Cache)

	toolboxes := []agents.Toolbox{
		agents.SandboxToolbox(sandboxTools),
		agents.BrowserToolbox(browserTools),
	}

	root, err := agents.Root(router, toolboxes)
	if err != nil {
		log.Fatalf("root agent: %v", err)
	}

	args := os.Args[1:]
	if len(args) == 0 {
		args = defaultArgs
	}
	adk := universal.NewLauncher(console.NewLauncher(), web.NewLauncher(api.NewLauncher(), webui.NewLauncher()))
	if err := adk.Execute(ctx, &launcher.Config{
		AgentLoader:     agent.NewSingleLoader(root),
		SessionService:  sessionStore.Sessions(recorder),
		ArtifactService: artifact.InMemoryService(),
	}, args); err != nil {
		log.Fatalf("%v\n\n%s", err, adk.CommandLineSyntax())
	}
}

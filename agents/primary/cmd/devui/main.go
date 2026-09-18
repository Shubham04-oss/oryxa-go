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
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/console"
	"google.golang.org/adk/v2/cmd/launcher/universal"
	"google.golang.org/adk/v2/cmd/launcher/web"
	"google.golang.org/adk/v2/cmd/launcher/web/api"
	"google.golang.org/adk/v2/cmd/launcher/web/webui"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/assembly"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
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

// devMemberTokenSource is a memberauth.TokenSource for local testing only. It
// skips auth's real agent-token exchange — which needs an AWS KMS key, a
// Cognito agent app client and a member's completed "agents may act for me"
// consent, none of which are set up locally — and hands back a fixed bearer
// token instead, from AGENTS_PRIMARY_DEVUI_MEMBER_TOKEN. Whatever verifies
// that token decides the identity it resolves to; locally that's
// test/cmd/authstub. Never point this at a real deployment.
type devMemberTokenSource string

func (t devMemberTokenSource) AgentToken(context.Context, string) (string, time.Time, error) {
	return string(t), time.Now().Add(time.Hour), nil
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

	var tokens memberauth.TokenSource
	if devToken := os.Getenv("AGENTS_PRIMARY_DEVUI_MEMBER_TOKEN"); devToken != "" {
		log.Printf("member token: AGENTS_PRIMARY_DEVUI_MEMBER_TOKEN set, bypassing auth's agent-token exchange (local testing only)")
		tokens = devMemberTokenSource(devToken)
	}

	krude, err := assembly.Build(ctx, assembly.Options{
		Settings: settings,
		AWS:      awsConfig,
		Logger:   slog.Default(),
		Tokens:   tokens,
	})
	if err != nil {
		log.Fatalf("assembly: %v", err)
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := krude.Close(shutdownContext); err != nil {
			log.Printf("stop Krude: %v", err)
		}
	}()

	args := os.Args[1:]
	if len(args) == 0 {
		args = defaultArgs
	}
	adk := universal.NewLauncher(console.NewLauncher(), web.NewLauncher(api.NewLauncher(), webui.NewLauncher()))
	if err := adk.Execute(ctx, &launcher.Config{
		AgentLoader:     agent.NewSingleLoader(krude.Root),
		SessionService:  sessionStore.Sessions(recorder),
		ArtifactService: krude.Artifacts,
	}, args); err != nil {
		log.Fatalf("%v\n\n%s", err, adk.CommandLineSyntax())
	}
}

// Command devui runs primary's agents in ADK's development web UI, or in its
// console. It is for local development only; cmd/primary is the service.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
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
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
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

	settings, err := serviceconfig.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	sessionStore, err := adksession.Open(ctx, settings.DatabaseURL, settings.AutoMigrate)
	if err != nil {
		log.Fatalf("ADK sessions: %v", err)
	}
	defer sessionStore.Close()
	log.Printf("ADK sessions connected to PostgreSQL")

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(settings.AWSRegion))
	if err != nil {
		log.Fatalf("AWS configuration: %v", err)
	}
	otelaws.AppendMiddlewares(&awsConfig.APIOptions)
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
	log.Printf("AWS AgentCore sandbox configured in %s", settings.AWSRegion)
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
	log.Printf("AWS AgentCore browser configured in %s", settings.AWSRegion)

	llm, err := agents.Gemini(ctx, settings.Model, settings.GoogleCloudProject, settings.GoogleCloudLocation)
	if err != nil {
		log.Fatalf("gemini: %v", err)
	}
	toolboxes := append(agents.Providers(),
		agents.Toolbox{
			Name:        "sandbox",
			Description: "Isolated code, shell and file operations in AWS Bedrock AgentCore for computation and data work.",
			Toolset:     sandboxTools,
		},
		agents.Toolbox{
			Name:        "browser",
			Description: "Isolated web navigation, semantic page inspection, screenshots and confirmed interactions in AWS Bedrock AgentCore Browser.",
			Toolset:     browserTools,
		},
	)
	root, err := agents.Root(ctx, llm, toolboxes)
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
		SessionService:  sessionStore.Sessions(),
		ArtifactService: artifact.InMemoryService(),
	}, args); err != nil {
		log.Fatalf("%v\n\n%s", err, adk.CommandLineSyntax())
	}
}

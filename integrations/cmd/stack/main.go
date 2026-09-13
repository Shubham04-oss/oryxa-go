// Command stack serves integrations HTTP for the /test k6 journey.
// Postgres and LocalStack are real. Slack, KMS and the piece runner are
// the same stand-ins as the integrations e2e. This is not the product
// binary and must not be deployed.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/credentials"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/dynamo"
	intevents "github.com/Shubham04-oss/oryxa-go/integrations/internal/events"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/execution"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/queue"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/stackfakes"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhookroutes"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	organizationID := required("ORG_ID")
	databaseURL := required("DATABASE_URL")
	awsEndpoint := required("AWS_ENDPOINT_URL")
	address := value("INTEGRATIONS_ADDRESS", "127.0.0.1:18084")
	publicBase := value("INTEGRATIONS_PUBLIC_BASE_URL", "http://"+address)
	returnURL := value("INTEGRATIONS_RETURN_URL", "https://app.example.test/settings/integrations")

	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(value("AWS_REGION", "ap-south-1")),
		awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		return err
	}
	dynamoClient := dynamodb.NewFromConfig(awsConfig, func(options *dynamodb.Options) { options.BaseEndpoint = aws.String(awsEndpoint) })
	sqsClient := sqs.NewFromConfig(awsConfig, func(options *sqs.Options) { options.BaseEndpoint = aws.String(awsEndpoint) })
	eventClient := eventbridge.NewFromConfig(awsConfig, func(options *eventbridge.Options) { options.BaseEndpoint = aws.String(awsEndpoint) })

	suffix := strings.ReplaceAll(organizationID, "_", "-")
	table := "oryxa-stack-" + suffix
	if _, err := dynamoClient.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange},
		},
		BillingMode: types.BillingModePayPerRequest,
	}); err != nil {
		return fmt.Errorf("create dynamodb table: %w", err)
	}
	if err := dynamodb.NewTableExistsWaiter(dynamoClient).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)}, 30*time.Second); err != nil {
		return fmt.Errorf("wait for dynamodb table: %w", err)
	}
	createdQueue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("oryxa-stack-" + suffix)})
	if err != nil {
		return fmt.Errorf("create sqs queue: %w", err)
	}
	queueURL := aws.ToString(createdQueue.QueueUrl)

	ids := func(prefix string) (string, error) {
		value := make([]byte, 12)
		if _, err := rand.Read(value); err != nil {
			return "", err
		}
		return prefix + "_" + base64.RawURLEncoding.EncodeToString(value), nil
	}
	box := secrets.Envelope{Keys: stackfakes.BoundKeys{}, KeyID: "test-key"}
	staticCatalog := catalog.Static{PublicBaseURL: strings.TrimRight(publicBase, "/")}
	routes := dynamo.Routes{Client: dynamoClient, TableName: table}
	state := dynamo.TriggerState{Client: dynamoClient, TableName: table}
	runner := &stackfakes.PieceRunner{}
	providers := oauthflow.StaticProviders{"slack": stackfakes.SlackProvider{}}
	credentialSource := credentials.Manager{Store: store, Secrets: box, Providers: providers, IDs: ids}
	oauth := oauthflow.Service{
		Catalog: staticCatalog, Authorizer: store, Profiles: store,
		States: dynamo.OAuthStates{Client: dynamoClient, TableName: table}, Connections: store,
		Secrets: box, Providers: providers, ReturnURLs: oauthflow.AllowedReturnURLs{returnURL}, IDs: ids,
	}
	routeService := webhookroutes.Service{
		Authorizer: store, Profiles: store, Catalog: staticCatalog,
		Routes: routes, Secrets: box, PublicURL: strings.TrimRight(publicBase, "/"),
	}
	triggerService := triggerflow.Service{
		Catalog: staticCatalog, Authorizer: store, Connections: store,
		Routes: routes, Store: store, State: state, Secrets: box, Credentials: credentialSource,
		Runner: runner, IDs: ids, PublicURL: strings.TrimRight(publicBase, "/"),
	}
	webhookService := webhooks.Service{
		Routes: routes, Secrets: box, Runner: runner,
		Claims: dynamo.Claims{Client: dynamoClient, TableName: table, IDs: ids},
		Queue:  queue.WebhookQueue{Client: sqsClient, QueueURL: queueURL}, IDs: ids,
	}
	executor := execution.Service{
		Store: store, State: state, Secrets: box, Credentials: credentialSource,
		Runner: runner, Mapper: execution.DomainMapper{}, IDs: ids, PublicURL: strings.TrimRight(publicBase, "/"),
	}
	actions := actionflow.Service{
		Catalog: staticCatalog, Authorizer: store, Connections: store,
		Store: store, Credentials: credentialSource, Runner: runner,
		Mapper: actionflow.DomainMapper{}, IDs: ids, PublicURL: strings.TrimRight(publicBase, "/"),
	}
	go (queue.Consumer{Client: sqsClient, QueueURL: queueURL, Processor: executor}).Run(ctx)
	go (intevents.Dispatcher{Store: store, Publisher: intevents.Publisher{Client: eventClient, BusName: value("EVENT_BUS_NAME", "default")}}).Run(ctx)

	handler := withHealthz(httpserver.NewAPI(httpserver.Dependencies{
		Authenticator: stackfakes.TokenAuthenticator{}, Catalog: oauth, OAuth: oauth,
		WebhookRoutes: routeService, Triggers: triggerService, Webhooks: webhookService, Actions: actions,
	}))
	server := &http.Server{
		Addr: address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("integrations stack listening on %s", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func withHealthz(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", next)
	return mux
}

func required(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func value(name, fallback string) string {
	if result := strings.TrimSpace(os.Getenv(name)); result != "" {
		return result
	}
	return fallback
}

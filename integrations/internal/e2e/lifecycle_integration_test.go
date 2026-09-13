package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/dynamo"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/execution"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/queue"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhookroutes"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRegistrationWebhookAndExecutionLifecycleOnMini(t *testing.T) {
	databaseURL := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	awsEndpoint := os.Getenv("INTEGRATIONS_TEST_AWS_ENDPOINT")
	if databaseURL == "" || awsEndpoint == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL and INTEGRATIONS_TEST_AWS_ENDPOINT are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	suffix := strings.ReplaceAll(now.Format("150405.000000000"), ".", "")
	org := "e2e-org-" + suffix
	owner := "e2e-owner-" + suffix
	member := "e2e-member-" + suffix
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{owner, member} {
		if _, err := pool.Exec(ctx, `INSERT INTO auth_users(id,email,created_at,updated_at) VALUES($1,$2,$3,$3)`, user, user+"@example.test", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_organizations(id,name,slug,created_by,created_at,updated_at) VALUES($1,'E2E',$2,$3,$4,$4)`, org, "e2e-"+suffix, owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_memberships(organization_id,user_id,role,joined_at) VALUES($1,$2,'owner',$4),($1,$3,'member',$4)`, org, owner, member, now); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = pool.Exec(cleanup, `DELETE FROM integrations_outbox WHERE organization_id=$1`, org)
		_, _ = pool.Exec(cleanup, `DELETE FROM integrations_trigger_executions WHERE organization_id=$1`, org)
		_, _ = pool.Exec(cleanup, `DELETE FROM integrations_triggers WHERE organization_id=$1`, org)
		_, _ = pool.Exec(cleanup, `DELETE FROM integrations_connections WHERE organization_id=$1`, org)
		_, _ = pool.Exec(cleanup, `DELETE FROM integrations_oauth_apps WHERE organization_id=$1`, org)
		_, _ = pool.Exec(cleanup, `DELETE FROM auth_memberships WHERE organization_id=$1`, org)
		_, _ = pool.Exec(cleanup, `DELETE FROM auth_organizations WHERE id=$1`, org)
		_, _ = pool.Exec(cleanup, `DELETE FROM auth_users WHERE id=ANY($1)`, []string{owner, member})
	}()

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("ap-south-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	dynamoClient := dynamodb.NewFromConfig(awsConfig, func(options *dynamodb.Options) { options.BaseEndpoint = aws.String(awsEndpoint) })
	sqsClient := sqs.NewFromConfig(awsConfig, func(options *sqs.Options) { options.BaseEndpoint = aws.String(awsEndpoint) })
	table := "oryxa-integrations-e2e-" + suffix
	_, err = dynamoClient.CreateTable(ctx, &dynamodb.CreateTableInput{TableName: aws.String(table), AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS}}, KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange}}, BillingMode: types.BillingModePayPerRequest})
	if err != nil {
		t.Fatal(err)
	}
	createdQueue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("oryxa-integrations-e2e-" + suffix)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = dynamoClient.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
		_, _ = sqsClient.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: createdQueue.QueueUrl})
	}()

	var counter atomic.Int64
	ids := func(prefix string) (string, error) {
		return fmt.Sprintf("%s-%d-%s", prefix, counter.Add(1), suffix), nil
	}
	box := secrets.Envelope{Keys: boundTestKeys{}, KeyID: "test-key"}
	staticCatalog := catalog.Static{PublicBaseURL: "https://connect.example.test"}
	routes := dynamo.Routes{Client: dynamoClient, TableName: table}
	state := dynamo.TriggerState{Client: dynamoClient, TableName: table}
	runner := &lifecycleRunner{}
	oauth := oauthflow.Service{Catalog: staticCatalog, Authorizer: store, Profiles: store, States: dynamo.OAuthStates{Client: dynamoClient, TableName: table}, Connections: store, Secrets: box, Providers: oauthflow.StaticProviders{"slack": testProvider{}}, ReturnURLs: oauthflow.AllowedReturnURLs{"https://app.example.test"}, Now: func() time.Time { return now }, IDs: ids}
	routeService := webhookroutes.Service{Authorizer: store, Profiles: store, Catalog: staticCatalog, Routes: routes, Secrets: box, PublicURL: "https://connect.example.test", Random: strings.NewReader(strings.Repeat("r", 64))}
	triggerService := triggerflow.Service{Catalog: staticCatalog, Authorizer: store, Connections: store, Routes: routes, Store: store, State: state, Secrets: box, Runner: runner, Now: func() time.Time { return now }, IDs: ids, PublicURL: "https://connect.example.test"}
	webhookService := webhooks.Service{Routes: routes, Secrets: box, Runner: runner, Claims: dynamo.Claims{Client: dynamoClient, TableName: table, IDs: ids}, Queue: queue.WebhookQueue{Client: sqsClient, QueueURL: aws.ToString(createdQueue.QueueUrl)}, Now: func() time.Time { return now }, IDs: ids}
	server := httpserver.NewAPI(httpserver.Dependencies{Authenticator: headerAuthenticator{}, Catalog: oauth, OAuth: oauth, WebhookRoutes: routeService, Triggers: triggerService, Webhooks: webhookService})

	registration := call(t, server, owner, http.MethodPost, "/v1/organizations/"+org+"/integration-apps", `{"provider":"slack","client_id":"client-id","client_secret":"client-secret"}`, http.StatusCreated)
	var profile struct {
		ID string `json:"id"`
	}
	decode(t, registration, &profile)
	if profile.ID == "" || strings.Contains(registration.Body.String(), "client-secret") {
		t.Fatalf("profile/response = %+v/%s", profile, registration.Body.String())
	}
	available := call(t, server, member, http.MethodGet, "/v1/organizations/"+org+"/integrations", "", http.StatusOK)
	if !strings.Contains(available.Body.String(), `"state":"available"`) {
		t.Fatalf("member catalog = %s", available.Body.String())
	}
	authorization := call(t, server, owner, http.MethodPost, "/v1/organizations/"+org+"/connections/authorize", fmt.Sprintf(`{"profile_id":%q,"return_uri":"https://app.example.test/settings/integrations"}`, profile.ID), http.StatusCreated)
	var begin struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	decode(t, authorization, &begin)
	providerURL, _ := url.Parse(begin.AuthorizationURL)
	rawState := providerURL.Query().Get("state")
	if rawState == "" {
		t.Fatal("authorization state missing")
	}
	callback := call(t, server, "", http.MethodGet, "/v1/oauth/callback/slack?state="+url.QueryEscape(rawState)+"&code=provider-code", "", http.StatusSeeOther)
	location, _ := url.Parse(callback.Header().Get("location"))
	connectionID := location.Query().Get("connection_id")
	if connectionID == "" {
		t.Fatal("connection id missing")
	}
	connected := call(t, server, owner, http.MethodGet, "/v1/organizations/"+org+"/integrations", "", http.StatusOK)
	if !strings.Contains(connected.Body.String(), `"state":"connected"`) {
		t.Fatalf("owner catalog = %s", connected.Body.String())
	}
	stillAvailable := call(t, server, member, http.MethodGet, "/v1/organizations/"+org+"/integrations", "", http.StatusOK)
	if !strings.Contains(stillAvailable.Body.String(), `"state":"available"`) {
		t.Fatalf("other member catalog = %s", stillAvailable.Body.String())
	}
	call(t, server, member, http.MethodPost, "/v1/organizations/"+org+"/triggers", fmt.Sprintf(`{"connection_id":%q,"trigger_name":"new-message","props":{}}`, connectionID), http.StatusForbidden)
	routeResponse := call(t, server, owner, http.MethodPost, "/v1/organizations/"+org+"/integration-apps/"+profile.ID+"/webhook-routes", `{"trigger_name":"new-message","signing_secret":"signing-secret"}`, http.StatusCreated)
	var route struct {
		OpaqueRoute string `json:"opaque_route"`
	}
	decode(t, routeResponse, &route)
	triggerResponse := call(t, server, owner, http.MethodPost, "/v1/organizations/"+org+"/triggers", fmt.Sprintf(`{"connection_id":%q,"trigger_name":"new-message","props":{"ignoreBots":true}}`, connectionID), http.StatusCreated)
	var trigger struct {
		ID string `json:"id"`
	}
	decode(t, triggerResponse, &trigger)
	if trigger.ID == "" {
		t.Fatal("trigger id missing")
	}

	raw := fmt.Sprintf(`{"event_id":"Ev123","team_id":"T123","event":{"type":"message","channel_type":"channel","channel":"C123","user":"U123","text":"hello","ts":"1.23"}}`)
	timestamp := fmt.Sprint(now.Unix())
	signature := "v0=" + hex.EncodeToString(hmacSHA256([]byte("signing-secret"), []byte("v0:"+timestamp+":"+raw)))
	request := httptest.NewRequest(http.MethodPost, "/v1/webhooks/"+route.OpaqueRoute, strings.NewReader(raw))
	request.Header.Set("content-type", "application/json")
	request.Header.Set("x-slack-request-timestamp", timestamp)
	request.Header.Set("x-slack-signature", signature)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("webhook = %d/%s", response.Code, response.Body.String())
	}
	received, err := sqsClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: createdQueue.QueueUrl, MaxNumberOfMessages: 1, WaitTimeSeconds: 2})
	if err != nil || len(received.Messages) != 1 {
		t.Fatalf("receive webhook = %d/%v", len(received.Messages), err)
	}
	executor := execution.Service{Store: store, State: state, Secrets: box, Runner: runner, Mapper: execution.DomainMapper{}, Now: func() time.Time { return now }, IDs: ids, PublicURL: "https://connect.example.test"}
	consumer := queue.Consumer{Processor: executor}
	messageBody := aws.ToString(received.Messages[0].Body)
	if err := consumer.HandleMessage(ctx, messageBody); err != nil {
		t.Fatal(err)
	}
	if err := consumer.HandleMessage(ctx, messageBody); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	var outputs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM integrations_outbox WHERE organization_id=$1 AND detail_type=$2`, org, integrationevents.SlackMessageReceivedDetailType).Scan(&outputs); err != nil || outputs != 1 {
		t.Fatalf("domain outputs = %d/%v", outputs, err)
	}
	var detail string
	if err := pool.QueryRow(ctx, `SELECT detail::text FROM integrations_outbox WHERE organization_id=$1 AND detail_type=$2`, org, integrationevents.SlackMessageReceivedDetailType).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"signing-secret", "client-secret", "access-token", "raw_body"} {
		if strings.Contains(detail, forbidden) {
			t.Fatalf("domain event leaked %q: %s", forbidden, detail)
		}
	}
	call(t, server, owner, http.MethodGet, "/v1/organizations/not-"+org+"/integrations", "", http.StatusForbidden)
	call(t, server, member, http.MethodDelete, "/v1/organizations/"+org+"/connections/"+connectionID, "", http.StatusForbidden)
	call(t, server, owner, http.MethodDelete, "/v1/organizations/"+org+"/connections/"+connectionID, "", http.StatusNoContent)
	afterDisconnect := call(t, server, owner, http.MethodGet, "/v1/organizations/"+org+"/integrations", "", http.StatusOK)
	if !strings.Contains(afterDisconnect.Body.String(), `"state":"available"`) {
		t.Fatalf("catalog after disconnect = %s", afterDisconnect.Body.String())
	}
	runnable, err := store.RunnableTriggers(ctx, org, profile.ID, catalog.SlackPieceName, catalog.SlackPieceVersion, "new-message", "", "T123")
	if err != nil || len(runnable) != 0 {
		t.Fatalf("triggers after disconnect = %+v/%v", runnable, err)
	}
}

type headerAuthenticator struct{}

func (headerAuthenticator) Authenticate(r *http.Request) (string, error) {
	value := r.Header.Get("x-test-member")
	if value == "" {
		return "", errors.New("missing")
	}
	return value, nil
}

type testProvider struct{}

func (testProvider) AuthorizationURL(request oauthflow.AuthorizationRequest) (string, error) {
	values := url.Values{"state": {request.State}, "client_id": {request.ClientID}}
	return "https://provider.example.test/authorize?" + values.Encode(), nil
}
func (testProvider) Exchange(context.Context, oauthflow.ExchangeRequest) (oauthflow.Grant, error) {
	return oauthflow.Grant{AccessToken: "access-token", Scopes: []string{"channels:history", "groups:history"}, Data: map[string]any{"team": map[string]any{"id": "T123", "name": "Oryxa"}, "authed_user": map[string]any{"id": "U123"}}}, nil
}
func (testProvider) ResolveIdentity(context.Context, oauthflow.Grant) (domain.ProviderIdentity, error) {
	return domain.ProviderIdentity{SubjectID: "U123", AccountID: "T123", TenantID: "T123", TenantName: "Oryxa"}, nil
}

type boundTestKeys struct{}

func (boundTestKeys) GenerateDataKey(_ context.Context, _ string, binding map[string]string) ([]byte, []byte, error) {
	key := bytesOf(32, 7)
	encoded, _ := json.Marshal(binding)
	digest := sha256.Sum256(encoded)
	return key, digest[:], nil
}
func (boundTestKeys) DecryptDataKey(_ context.Context, wrapped []byte, binding map[string]string) ([]byte, error) {
	encoded, _ := json.Marshal(binding)
	digest := sha256.Sum256(encoded)
	if !hmac.Equal(wrapped, digest[:]) {
		return nil, errors.New("binding mismatch")
	}
	return bytesOf(32, 7), nil
}
func bytesOf(size int, value byte) []byte {
	result := make([]byte, size)
	for i := range result {
		result[i] = value
	}
	return result
}

type lifecycleRunner struct{}

func (lifecycleRunner) VerifyAndParse(_ context.Context, request webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	timestamp := request.Headers["x-slack-request-timestamp"]
	want := "v0=" + hex.EncodeToString(hmacSHA256(request.SigningSecret, []byte("v0:"+timestamp+":"+string(request.RawBody))))
	if !hmac.Equal([]byte(want), []byte(request.Headers["x-slack-signature"])) {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	body := request.Body.(map[string]any)
	event := body["event"].(map[string]any)
	return webhooks.RunnerResult{Verified: true, Event: event["type"].(string), IdentifierValue: body["team_id"].(string), ProviderEventID: body["event_id"].(string)}, nil
}
func (lifecycleRunner) RunTrigger(_ context.Context, request piece.TriggerInvocation) (piece.TriggerResult, error) {
	if request.Operation == "trigger.enable" {
		return piece.TriggerResult{Output: json.RawMessage(`null`), Listeners: []piece.Listener{{Events: []string{"message"}, IdentifierValue: "T123"}}}, nil
	}
	if request.Operation == "trigger.disable" {
		return piece.TriggerResult{Output: json.RawMessage(`null`)}, nil
	}
	body := request.Payload.Body.(map[string]any)
	output, err := json.Marshal([]any{body["event"]})
	return piece.TriggerResult{Output: output}, err
}

func hmacSHA256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message)
	return mac.Sum(nil)
}

func call(t *testing.T, handler http.Handler, member, method, target, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("content-type", "application/json")
	}
	if member != "" {
		request.Header.Set("x-test-member", member)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, target, response.Code, want, response.Body.String())
	}
	return response
}
func decode(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatal(err)
	}
}

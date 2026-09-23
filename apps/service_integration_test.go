package apps

import (
	"context"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"crypto/rand"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	appsevents "github.com/Shubham04-oss/oryxa-go/apps/internal/events"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/mcpserver"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

// TestConnectRunAndDeliverEndToEnd runs the whole service — its own Postgres
// schema, its HTTP API, its MCP endpoint and its outbox — against a stand-in
// Composio and a stand-in auth, and walks the path a member and their agent
// actually take: connect an app, come back from the hosted page, run a read
// and a write, subscribe a trigger, receive its signed delivery, and lose it
// all when the member leaves.
//
// It proves the wiring the unit tests cannot: that migrations install what the
// store writes, that an event written in a transaction reaches the bus, and
// that a trigger's payload is forgotten once published.
func TestConnectRunAndDeliverEndToEnd(t *testing.T) {
	store := openTestStore(t)
	composio := newComposioStub(t)
	directory := newDirectoryStub(t)
	sink := &collectingSink{}

	service, err := New(Dependencies{
		Store: store, Directory: directoryClient(t, directory.URL), Composio: composioClient(t, composio.URL),
		Sink: sink, Verifier: authHarness(t).Verifier(), Logger: slog.New(slog.DiscardHandler),
	}, Config{
		PublicBaseURL:  "https://apps.oryxa.test",
		AllowedOrigins: []string{"https://app.oryxa.test"},
		WebhookSecret:  webhookSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(service.Handler())
	t.Cleanup(api.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	// The dispatcher runs as it does in the binary, so every event below
	// travels the same way it would in production.
	dispatcher, waitForDispatcher := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		for _, worker := range service.Workers() {
			if worker != nil {
				go worker(dispatcher)
			}
		}
		<-dispatcher.Done()
		close(done)
	}()
	t.Cleanup(func() {
		waitForDispatcher()
		<-done
	})

	// 1. The member connects GitHub. The link is Composio's; the state lives
	// only in it and in a hash here.
	var connected struct {
		Connection struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"connection"`
		RedirectURL string `json:"redirect_url"`
	}
	post(t, api.URL+"/v1/organizations/org_1/apps/connections",
		`{"toolkit":"github","return_to":"https://app.oryxa.test/apps"}`, http.StatusCreated, &connected)
	if connected.Connection.Status != "pending" || connected.RedirectURL == "" {
		t.Fatalf("connection = %+v", connected)
	}
	// Starting is announced too, so what follows is always the end of
	// something the bus saw begin.
	initiated := decodeEvent(t, waitForEvent(t, sink, appsv1.ConnectionInitiatedDetailType))
	if initiated["connection_id"] != connected.Connection.ID || initiated["toolkit"] != "github" {
		t.Fatalf("initiated event = %v", initiated)
	}
	callback, err := url.Parse(composio.lastCallbackURL())
	if err != nil {
		t.Fatal(err)
	}
	state := callback.Query().Get("state")
	if state == "" {
		t.Fatalf("no state in %q", composio.lastCallbackURL())
	}

	// 2. Composio sends the browser back. The account is read back from
	// Composio, not believed from the query.
	response := get(t, api.URL+"/v1/apps/connect/callback?state="+url.QueryEscape(state)+"&status=success&connected_account_id="+composio.accountID, 0)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback = %d", response.StatusCode)
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("status") != "active" {
		t.Fatalf("returned with %q", location.Query().Get("status"))
	}
	waitForEvent(t, sink, appsv1.ConnectionConnectedDetailType)

	connectionID := connected.Connection.ID
	if connection, err := store.Connection(ctx, "org_1", connectionID); err != nil || connection.Status != domain.ConnectionActive {
		t.Fatalf("stored connection = %+v, %v", connection, err)
	}

	// 3. The agent looks around over MCP and runs a read. A read announces
	// nothing, so the bus stays quiet.
	agent := mcpSession(t, api.URL)
	listed := callTool(t, agent, mcpserver.ToolConnections, map[string]any{})
	// A connection is listed by its app and status: the agent acts through
	// an app, and is not shown the connection's id.
	if !strings.Contains(listed, `"toolkit":"github"`) || !strings.Contains(listed, `"status":"active"`) {
		t.Fatalf("connections = %s", listed)
	}
	read := callTool(t, agent, mcpserver.ToolRunRead, map[string]any{
		"tool": "GITHUB_LIST_ISSUES", "arguments": map[string]any{"repo": "oryxa"},
	})
	if !strings.Contains(read, `"successful":true`) {
		t.Fatalf("read = %s", read)
	}

	// 4. A write needs a key, and the same key twice does not write twice.
	write := callTool(t, agent, mcpserver.ToolRunWrite, map[string]any{
		"tool": "GITHUB_CREATE_AN_ISSUE", "arguments": map[string]any{"title": "bug"},
		"idempotency_key": "issue-2026-09-17",
	})
	if !strings.Contains(write, `"successful":true`) {
		t.Fatalf("write = %s", write)
	}
	replay := callTool(t, agent, mcpserver.ToolRunWrite, map[string]any{
		"tool": "GITHUB_CREATE_AN_ISSUE", "arguments": map[string]any{"title": "bug"},
		"idempotency_key": "issue-2026-09-17",
	})
	if !strings.Contains(replay, `"replayed":true`) {
		t.Fatalf("replay = %s", replay)
	}
	if runs := composio.executions("GITHUB_CREATE_AN_ISSUE"); runs != 1 {
		t.Fatalf("the write ran %d times", runs)
	}
	// The event says an agent did it, so a member can see what was done in
	// their name. Postgres reformats jsonb, so the data is decoded rather
	// than matched as text.
	action := decodeEvent(t, waitForEvent(t, sink, appsv1.ActionCompletedDetailType))
	if action["agent"] != true || action["tool"] != "GITHUB_CREATE_AN_ISSUE" || action["read_only"] != false {
		t.Fatalf("action event = %v", action)
	}

	// 5. The member subscribes a trigger, and the app delivers through
	// Composio's signed webhook.

	// A config missing what the trigger needs is refused here, rather than
	// subscribed and then delivering nothing for ever.
	post(t, api.URL+"/v1/organizations/org_1/apps/triggers",
		fmt.Sprintf(`{"connection_id":%q,"trigger":"GITHUB_COMMIT_EVENT","config":{}}`, connectionID),
		http.StatusBadRequest, nil)
	// So is an event this app does not deliver.
	post(t, api.URL+"/v1/organizations/org_1/apps/triggers",
		fmt.Sprintf(`{"connection_id":%q,"trigger":"SLACK_RECEIVE_MESSAGE","config":{"channel":"C1"}}`, connectionID),
		http.StatusNotFound, nil)

	var trigger struct {
		ID            string `json:"id"`
		Kind          string `json:"kind"`
		DeliveryCount int64  `json:"delivery_count"`
	}
	post(t, api.URL+"/v1/organizations/org_1/apps/triggers",
		fmt.Sprintf(`{"connection_id":%q,"trigger":"GITHUB_COMMIT_EVENT","config":{"repo":"oryxa"}}`, connectionID),
		http.StatusCreated, &trigger)
	if trigger.Kind != "webhook" || trigger.DeliveryCount != 0 {
		t.Fatalf("a new trigger = %+v, want a pushed one that has delivered nothing", trigger)
	}
	subscribed := decodeEvent(t, waitForEvent(t, sink, appsv1.TriggerSubscribedDetailType))
	if subscribed["trigger"] != "GITHUB_COMMIT_EVENT" || subscribed["kind"] != "webhook" {
		t.Fatalf("subscribed event = %v", subscribed)
	}
	// The two refusals above changed nothing, so neither announced anything.
	if count := sink.count(appsv1.TriggerSubscribedDetailType); count != 1 {
		t.Fatalf("subscribed events = %d, want 1", count)
	}

	delivery := fmt.Sprintf(`{"id":"wh_1","type":"composio.trigger.message","timestamp":"2026-09-17T12:00:00Z",
		"metadata":{"trigger_id":%q,"trigger_slug":"GITHUB_COMMIT_EVENT","connected_account_id":%q},
		"data":{"commit":"abc123","message":"fix the thing"}}`, composio.triggerID, composio.accountID)
	if status := deliverWebhook(t, api.URL, delivery); status != http.StatusOK {
		t.Fatalf("delivery = %d", status)
	}
	received := waitForEvent(t, sink, appsv1.TriggerReceivedDetailType)
	payload, ok := decodeEvent(t, received)["data"].(map[string]any)
	if !ok || payload["commit"] != "abc123" {
		t.Fatalf("trigger event = %s", received.Data)
	}
	// Composio redelivers what it is not sure of; nothing is published twice.
	if status := deliverWebhook(t, api.URL, delivery); status != http.StatusOK {
		t.Fatalf("redelivery = %d", status)
	}
	if count := sink.count(appsv1.TriggerReceivedDetailType); count != 1 {
		t.Fatalf("trigger events = %d", count)
	}
	// An unsigned delivery is refused outright.
	if status := deliverUnsigned(t, api.URL, delivery); status != http.StatusUnauthorized {
		t.Fatalf("unsigned delivery = %d", status)
	}

	// The trigger now says it is live, counting the delivery once however
	// many times Composio sent it - the only way to tell a working
	// subscription from a silent one.
	var live struct {
		Triggers []struct {
			ID             string `json:"id"`
			Kind           string `json:"kind"`
			DeliveryCount  int64  `json:"delivery_count"`
			LastDeliveryAt string `json:"last_delivery_at"`
		} `json:"triggers"`
	}
	if err := json.NewDecoder(get(t, api.URL+"/v1/organizations/org_1/apps/triggers",
		http.StatusOK).Body).Decode(&live); err != nil {
		t.Fatalf("read the trigger listing: %v", err)
	}
	if len(live.Triggers) != 1 || live.Triggers[0].ID != trigger.ID {
		t.Fatalf("triggers listed = %+v", live.Triggers)
	}
	if live.Triggers[0].DeliveryCount != 1 || live.Triggers[0].LastDeliveryAt == "" {
		t.Fatalf("the trigger reports %d deliveries, last at %q; want one delivery",
			live.Triggers[0].DeliveryCount, live.Triggers[0].LastDeliveryAt)
	}

	// The payload is the app's own data: once published, the outbox forgets
	// it rather than keeping it for the retention window.
	waitFor(t, "the trigger's payload to be forgotten", func() bool {
		return outboxDetail(t, store, received.ID) == "{}"
	})
	if detail := outboxDetail(t, store, sink.first(appsv1.ActionCompletedDetailType).ID); detail == "{}" {
		t.Fatal("an action event was blanked, and only a trigger's payload should be")
	}

	// 6. The member leaves the organization: the connection goes, its trigger
	// goes, and Composio is told to revoke the credential.
	if err := service.Consumer().Handle(ctx, busMessage(t, authv1.Source, authv1.MemberRemovedDetailType,
		authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-1"})); err != nil {
		t.Fatal(err)
	}
	if connection, err := store.Connection(ctx, "org_1", connectionID); err != nil || connection.Status != domain.ConnectionRemoved {
		t.Fatalf("connection after the member left = %+v, %v", connection, err)
	}
	if !composio.deletedAccount() || !composio.revokedAccount() {
		t.Fatal("the credential was not revoked at Composio")
	}
	if left, err := store.Trigger(ctx, "org_1", trigger.ID); err != nil || left.Status != domain.TriggerRemoved {
		t.Fatalf("trigger after the member left = %+v, %v", left, err)
	}
	waitForEvent(t, sink, appsv1.ConnectionRemovedDetailType)

	// A delivery for what is gone is acknowledged and dropped: Composio would
	// otherwise retry it forever.
	late := strings.Replace(delivery, `"id":"wh_1"`, `"id":"wh_2"`, 1)
	if status := deliverWebhook(t, api.URL, late); status != http.StatusOK {
		t.Fatalf("late delivery = %d", status)
	}
	if count := sink.count(appsv1.TriggerReceivedDetailType); count != 1 {
		t.Fatalf("trigger events after removal = %d", count)
	}
}

const (
	webhookSecret = "whsec_integration"
	// The member's own token, and the one agents/primary mints under their
	// grant, which auth marks as an agent acting for them.
	memberToken = "Bearer member-token"
	agentToken  = "Bearer agent-token"
)

// composioClient points apps' Composio client at the stub.
func composioClient(t *testing.T, baseURL string) *composio.Client {
	t.Helper()
	client, err := composio.New(composio.Config{APIKey: "ak_integration", BaseURL: baseURL + "/api/v3.1"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// directoryClient points apps' auth client at the stub.
func directoryClient(t *testing.T, baseURL string) *authclient.Client {
	t.Helper()
	t.Cleanup(func() {})
	return authclient.New(baseURL)
}

// composioStub answers the Composio calls this path makes, and records them.
type composioStub struct {
	*httptest.Server
	accountID string
	triggerID string

	mu          sync.Mutex
	callbackURL string
	runs        map[string]int
	deleted     bool
	revoked     bool
}

func newComposioStub(t *testing.T) *composioStub {
	t.Helper()
	stub := &composioStub{accountID: "ca_integration", triggerID: "ti_integration", runs: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3.1/toolkits/{slug}", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, map[string]any{
			"slug": request.PathValue("slug"), "name": "GitHub",
			"auth_schemes": []string{"OAUTH2"}, "composio_managed_auth_schemes": []string{"OAUTH2"},
			"meta": map[string]any{"description": "GitHub", "tools_count": 120, "triggers_count": 8},
		})
	})
	mux.HandleFunc("GET /api/v3.1/auth_configs", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{"items": []any{}})
	})
	mux.HandleFunc("POST /api/v3.1/auth_configs", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{"auth_config": map[string]any{
			"id": "ac_integration", "toolkit": map[string]any{"slug": "github"},
			"auth_scheme": "OAUTH2", "is_composio_managed": true, "status": "ENABLED",
		}})
	})
	mux.HandleFunc("POST /api/v3.1/connected_accounts/link", func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			CallbackURL string `json:"callback_url"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		stub.mu.Lock()
		stub.callbackURL = body.CallbackURL
		stub.mu.Unlock()
		writeJSON(writer, map[string]any{
			"redirect_url": "https://connect.composio.dev/link/lt_integration", "connected_account_id": stub.accountID,
		})
	})
	mux.HandleFunc("GET /api/v3.1/connected_accounts/{id}", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, map[string]any{
			"id": request.PathValue("id"), "user_id": "oryxa.org_1.user-1",
			"toolkit":     map[string]any{"slug": "github"},
			"auth_config": map[string]any{"id": "ac_integration", "auth_scheme": "OAUTH2", "is_composio_managed": true},
			"status":      "ACTIVE", "state": map[string]any{"authScheme": "OAUTH2", "val": map[string]any{"access_token": "never-read"}},
			"toolkit_specific_data": map[string]any{}, "created_at": "2026-09-17T12:00:00Z",
		})
	})
	mux.HandleFunc("DELETE /api/v3.1/connected_accounts/{id}", func(writer http.ResponseWriter, request *http.Request) {
		stub.mu.Lock()
		stub.deleted = true
		stub.revoked = request.URL.Query().Get("revoke_on_delete") == "true"
		stub.mu.Unlock()
		writeJSON(writer, map[string]any{"success": true})
	})
	mux.HandleFunc("GET /api/v3.1/tools/{slug}", func(writer http.ResponseWriter, request *http.Request) {
		slug := request.PathValue("slug")
		tags := []string{"readOnlyHint"}
		if strings.Contains(slug, "CREATE") {
			tags = []string{"destructiveHint"}
		}
		writeJSON(writer, map[string]any{
			"slug": slug, "name": slug, "toolkit": map[string]any{"slug": "github"},
			"version": "20260901", "tags": tags, "input_parameters": map[string]any{"type": "object"},
		})
	})
	mux.HandleFunc("POST /api/v3.1/tools/execute/{slug}", func(writer http.ResponseWriter, request *http.Request) {
		stub.mu.Lock()
		stub.runs[request.PathValue("slug")]++
		stub.mu.Unlock()
		writeJSON(writer, map[string]any{
			"successful": true, "data": map[string]any{"number": 7}, "log_id": "log_integration",
		})
	})
	// The catalog a subscription is checked against, so a member cannot
	// subscribe to an event this app does not deliver or leave out a field
	// it needs.
	mux.HandleFunc("GET /api/v3.1/triggers_types", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{
			"items": []map[string]any{{
				"slug": "GITHUB_COMMIT_EVENT", "name": "New commit", "description": "A commit was pushed",
				"type": "webhook", "toolkit": map[string]any{"slug": "github"}, "version": "v1",
				"config": map[string]any{
					"type":       "object",
					"properties": map[string]any{"repo": map[string]any{"type": "string"}},
					"required":   []string{"repo"},
				},
				"payload": map[string]any{}, "requires_webhook_endpoint_setup": false,
			}},
			"next_cursor": nil,
		})
	})
	mux.HandleFunc("POST /api/v3.1/trigger_instances/{slug}/upsert", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{"trigger_id": stub.triggerID})
	})
	mux.HandleFunc("DELETE /api/v3.1/trigger_instances/manage/{id}", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{"trigger_id": stub.triggerID})
	})
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		t.Errorf("unexpected Composio call: %s %s", request.Method, request.URL.Path)
		http.Error(writer, "unexpected", http.StatusNotImplemented)
	})
	stub.Server = httptest.NewServer(mux)
	t.Cleanup(stub.Close)
	return stub
}

func (stub *composioStub) lastCallbackURL() string {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.callbackURL
}

func (stub *composioStub) executions(slug string) int {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.runs[slug]
}

func (stub *composioStub) deletedAccount() bool {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.deleted
}

func (stub *composioStub) revokedAccount() bool {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.revoked
}

// newDirectoryStub answers as auth does for one member of one organization.
func newDirectoryStub(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token := request.Header.Get("Authorization")
		if token != memberToken && token != agentToken {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(request.URL.Path, "/v1/organizations/org_1") {
			http.Error(writer, "forbidden", http.StatusForbidden)
			return
		}
		writeJSON(writer, map[string]any{
			"organization": map[string]any{"id": "org_1"},
			"membership":   map[string]any{"user_id": "user-1", "role": "member", "status": "active"},
			"agent":        token == agentToken,
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// collectingSink stands in for the bus.
type collectingSink struct {
	mu     sync.Mutex
	events []domain.Event
}

func (sink *collectingSink) PublishEvent(_ context.Context, event domain.Event) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.events = append(sink.events, event)
	return nil
}

func (sink *collectingSink) count(detailType string) int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	found := 0
	for _, event := range sink.events {
		if event.DetailType == detailType {
			found++
		}
	}
	return found
}

func (sink *collectingSink) first(detailType string) domain.Event {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, event := range sink.events {
		if event.DetailType == detailType {
			return event
		}
	}
	return domain.Event{}
}

// decodeEvent is an event's data, since Postgres reformats jsonb and a
// substring match on it proves nothing.
func decodeEvent(t *testing.T, event domain.Event) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(event.Data, &decoded); err != nil {
		t.Fatalf("decode %s: %v", event.DetailType, err)
	}
	return decoded
}

func waitForEvent(t *testing.T, sink *collectingSink, detailType string) domain.Event {
	t.Helper()
	waitFor(t, detailType, func() bool { return sink.count(detailType) > 0 })
	return sink.first(detailType)
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// outboxDetail is what the outbox still holds for an event, so a test can see
// a payload forgotten. A redacted one reads "{}".
func outboxDetail(t *testing.T, store *postgres.Store, eventID string) string {
	t.Helper()
	if eventID == "" {
		t.Fatal("no event id")
	}
	var detail *string
	err := store.Pool().QueryRow(context.Background(), "SELECT detail FROM apps_outbox WHERE id = $1", eventID).Scan(&detail)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	if detail == nil {
		return ""
	}
	return strings.TrimSpace(*detail)
}

// authFixture is the signing identity the messages this test feeds the
// consumer claim to be from: auth's, since a consumer verifies a signature
// against the source the event claims, and this fixture's events claim
// auth. One key for the whole test binary, as a running host mints one key
// per service for its process's life.
type authFixture struct {
	signer   *eventauth.Signer
	verifier *eventauth.Verifier
}

var testAuthFixture struct {
	once sync.Once
	auth authFixture
}

// authHarness returns auth's signing identity and the verifier that trusts
// it: the signer the bus messages are built with, and the verifier the
// service is mounted with.
func authHarness(t *testing.T) *authFixture {
	t.Helper()
	testAuthFixture.once.Do(func() {
		signer, key, err := eventauth.NewLocalSigner("auth", time.Now)
		if err != nil {
			panic(err)
		}
		testAuthFixture.auth = authFixture{
			signer:   signer,
			verifier: eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, time.Now),
		}
	})
	return &testAuthFixture.auth
}

func (fixture *authFixture) Signer() *eventauth.Signer { return fixture.signer }

func (fixture *authFixture) Verifier() *eventauth.Verifier { return fixture.verifier }

// busMessage is one SQS body carrying an EventBridge envelope, signed as
// auth would have signed it.
func busMessage(t *testing.T, source, detailType string, data any) string {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	eventID := "evt_" + randomHex(6)
	occurredAt := time.Now().UTC().Format(time.RFC3339Nano)
	signature, err := authHarness(t).Signer().Sign(source, detailType, eventID, 1, occurredAt, encoded)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"source": source, "detail-type": detailType, "time": time.Now().UTC().Format(time.RFC3339),
		"detail": map[string]any{
			"event_id": eventID, "version": 1,
			"occurred_at": occurredAt, "data": json.RawMessage(encoded),
			"sig": signature,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func post(t *testing.T, target, body string, wantStatus int, into any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", memberToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("POST %s = %d, want %d", target, response.StatusCode, wantStatus)
	}
	if into != nil {
		if err := json.NewDecoder(response.Body).Decode(into); err != nil {
			t.Fatalf("decode %s: %v", target, err)
		}
	}
}

func get(t *testing.T, target string, wantStatus int) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", memberToken)
	// A redirect is what a browser follows; the test wants to see it.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if wantStatus != 0 && response.StatusCode != wantStatus {
		t.Fatalf("GET %s = %d, want %d", target, response.StatusCode, wantStatus)
	}
	return response
}

// deliverWebhook signs a delivery the way Composio does.
func deliverWebhook(t *testing.T, baseURL, body string) int {
	t.Helper()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	var id struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &id); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write([]byte(id.ID + "." + timestamp + "." + body))
	request, err := http.NewRequest(http.MethodPost, baseURL+WebhookPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("webhook-id", id.ID)
	request.Header.Set("webhook-timestamp", timestamp)
	request.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func deliverUnsigned(t *testing.T, baseURL, body string) int {
	t.Helper()
	response, err := http.Post(baseURL+WebhookPath, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

// mcpSession connects an MCP client as the member's agent would.
func mcpSession(t *testing.T, baseURL string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "krude-test", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: baseURL + "/v1/mcp/apps",
		HTTPClient: &http.Client{Transport: headerTransport{values: map[string]string{
			"Authorization": agentToken, mcpserver.OrganizationHeader: "org_1",
		}}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type headerTransport struct{ values map[string]string }

func (transport headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	for name, value := range transport.values {
		request.Header.Set(name, value)
	}
	return http.DefaultTransport.RoundTrip(request)
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("%s answered nothing", name)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s content = %T", name, result.Content[0])
	}
	if result.IsError {
		t.Fatalf("%s failed: %s", name, text.Text)
	}
	return text.Text
}

// openTestStore migrates a fresh schema and drops it afterwards, so the test
// never leaves tables in a shared development database.
func openTestStore(t *testing.T) *postgres.Store {
	t.Helper()
	databaseURL := os.Getenv("APPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("APPS_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schemaName := "apps_e2e_" + randomHex(6)
	schema := pgx.Identifier{schemaName}.Sanitize()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = admin.Close(cleanup)
	})

	scoped, err := url.Parse(databaseURL)
	if err != nil || scoped.Scheme == "" {
		t.Fatal("APPS_TEST_DATABASE_URL must be a postgres:// URL")
	}
	query := scoped.Query()
	query.Set("search_path", schemaName+",public")
	scoped.RawQuery = query.Encode()

	store, err := postgres.Open(ctx, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return store
}

func randomHex(bytes int) string {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

// Compile-time proof that the stand-in publisher is what the service takes.
var _ appsevents.Sink = (*collectingSink)(nil)

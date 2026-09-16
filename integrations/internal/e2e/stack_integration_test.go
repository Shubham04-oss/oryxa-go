// Package e2e runs integrations end to end: the HTTP API, the application,
// PostgreSQL, envelope encryption, service identity and the provider adapters,
// against fake auth, a fake connections and fake providers. The live tests run
// the same stack against the real providers.
package e2e

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/jackc/pgx/v5"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/application"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

const (
	organization  = "org_e2e"
	adminToken    = "admin-token"
	memberToken   = "member-token"
	providerToken = "provider-token-e2e-0123456789"
	serviceKeyID  = "alias/integrations-identity"
)

// members are who fake auth knows, by access token.
var members = map[string]struct{ user, role string }{
	adminToken:  {"user-admin", "admin"},
	memberToken: {"user-1", "member"},
}

// openStore migrates a throwaway schema and drops it afterwards, so the test
// is safe against a shared development database. It returns the store and a
// connection that reads the same schema.
func openStore(t *testing.T) (*postgres.Store, *pgx.Conn) {
	t.Helper()
	databaseURL := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schemaName := "integrations_e2e_" + hex.EncodeToString(suffix)
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
	if _, err := admin.Exec(ctx, "SET search_path TO "+schema+", public"); err != nil {
		t.Fatal(err)
	}

	scoped, err := url.Parse(databaseURL)
	if err != nil || scoped.Scheme == "" {
		t.Fatal("INTEGRATIONS_TEST_DATABASE_URL must be a postgres:// URL")
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
	return store, admin
}

// localKeys stands in for KMS: it wraps each data key under a master key with
// the encryption context as associated data, so a key opens only in the
// context it was made for.
type localKeys struct{ master cipher.AEAD }

func newLocalKeys(t *testing.T) localKeys {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	master, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return localKeys{master: master}
}

func (keys localKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	dataKey := make([]byte, 32)
	nonce := make([]byte, keys.master.NonceSize())
	if _, err := rand.Read(dataKey); err != nil {
		return nil, nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	bound, _ := json.Marshal(encryptionContext)
	return dataKey, keys.master.Seal(nonce, nonce, dataKey, bound), nil
}

func (keys localKeys) DecryptDataKey(_ context.Context, encrypted []byte, encryptionContext map[string]string) ([]byte, error) {
	size := keys.master.NonceSize()
	if len(encrypted) < size {
		return nil, errors.New("wrapped key is too short")
	}
	bound, _ := json.Marshal(encryptionContext)
	return keys.master.Open(nil, encrypted[:size], encrypted[size:], bound)
}

// serviceKey stands in for integrations' KMS asymmetric key: it signs like
// kms:Sign and publishes its public key like kms:GetPublicKey.
type serviceKey struct{ key *ecdsa.PrivateKey }

func newServiceKey(t *testing.T) serviceKey {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return serviceKey{key: privateKey}
}

func (key serviceKey) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	digest := sha256.Sum256(input.Message)
	signature, err := ecdsa.SignASN1(rand.Reader, key.key, digest[:])
	return &kms.SignOutput{Signature: signature, KeyId: input.KeyId, SigningAlgorithm: input.SigningAlgorithm}, err
}

func (key serviceKey) GetPublicKey(_ context.Context, input *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, err := x509.MarshalPKIXPublicKey(&key.key.PublicKey)
	return &kms.GetPublicKeyOutput{PublicKey: der, KeyId: input.KeyId}, err
}

// clock is the stack's time, which a test moves forward.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

// fakeAuth answers GET /v1/organizations/{organizationID} as auth does.
func fakeAuth(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token, _ := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
		member, ok := members[token]
		switch {
		case !ok:
			writer.WriteHeader(http.StatusUnauthorized)
		case request.URL.Path != "/v1/organizations/"+organization:
			writer.WriteHeader(http.StatusNotFound)
		default:
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"organization": map[string]string{"id": organization},
				"membership":   map[string]string{"organization_id": organization, "user_id": member.user, "role": member.role},
			})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// placement is where connections says a provider credential goes.
type placement struct {
	In     string `json:"in"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

var bearerPlacement = placement{In: "header", Name: "Authorization", Prefix: "Bearer "}

// tokenCall is a credential integrations asked connections for, and who it
// was for.
type tokenCall struct {
	service, purpose, member string
}

// fakeConnections is the part of connections integrations calls: connection
// metadata, token issuance and holds on the internal API, checked with
// integrations' service identity, and the apps list on the public API.
type fakeConnections struct {
	t           *testing.T
	verifier    *serviceauth.Verifier
	mu          sync.Mutex
	connections map[string]domain.Connection
	apps        []connectionsclient.App
	token       string
	placement   placement
	tokens      []tokenCall
	holds       map[string]bool
}

func newFakeConnections(t *testing.T, key serviceKey) (*fakeConnections, *httptest.Server) {
	t.Helper()
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{
		Keys: key, Audience: connectionsclient.Audience,
		Callers: []serviceauth.Caller{{Service: "integrations", KeyID: serviceKeyID, Purposes: []string{"trigger", "cleanup"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeConnections{
		t: t, verifier: verifier, connections: map[string]domain.Connection{}, token: providerToken,
		placement: bearerPlacement, holds: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/internal/connections/{connectionID}", fake.connection)
	mux.HandleFunc("POST /v1/internal/connections/{connectionID}/token", fake.issue)
	mux.HandleFunc("POST /v1/internal/connections/{connectionID}/holds", fake.addHold)
	mux.HandleFunc("DELETE /v1/internal/connections/{connectionID}/holds/{holder}", fake.releaseHold)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/apps", fake.listApps)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return fake, server
}

func (fake *fakeConnections) add(connection domain.Connection) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	connection.OrganizationID = organization
	fake.connections[connection.ID] = connection
	fake.apps = append(fake.apps, connectionsclient.App{ID: connection.AppID, Provider: connection.Provider, Status: "active"})
}

func (fake *fakeConnections) setStatus(connectionID, status string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	connection := fake.connections[connectionID]
	connection.Status = status
	fake.connections[connectionID] = connection
}

func (fake *fakeConnections) held(connectionID string) bool {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.holds[connectionID]
}

func (fake *fakeConnections) issued() []tokenCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]tokenCall(nil), fake.tokens...)
}

func (fake *fakeConnections) fail(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
}

// service verifies a request's service token and answers its claims.
func (fake *fakeConnections) service(writer http.ResponseWriter, request *http.Request) (serviceauth.Claims, bool) {
	claims, err := fake.verifier.Verify(request.Context(), request.Header.Get(serviceauth.Header))
	if err != nil {
		fake.fail(writer, http.StatusUnauthorized, "unauthorized")
		return serviceauth.Claims{}, false
	}
	return claims, true
}

// visible is the connection a request names, if the forwarded member, when
// there is one, may see it.
func (fake *fakeConnections) visible(writer http.ResponseWriter, request *http.Request, organizationID string) (domain.Connection, bool) {
	fake.mu.Lock()
	connection, ok := fake.connections[request.PathValue("connectionID")]
	fake.mu.Unlock()
	if !ok || connection.OrganizationID != organizationID {
		fake.fail(writer, http.StatusNotFound, "not_found")
		return domain.Connection{}, false
	}
	if authorization := request.Header.Get("Authorization"); authorization != "" {
		member, known := members[strings.TrimPrefix(authorization, "Bearer ")]
		if !known {
			fake.fail(writer, http.StatusUnauthorized, "unauthorized")
			return domain.Connection{}, false
		}
		if connection.Scope == "member" && connection.MemberID != member.user && member.role != "admin" {
			fake.fail(writer, http.StatusNotFound, "not_found")
			return domain.Connection{}, false
		}
	}
	return connection, true
}

func (fake *fakeConnections) connection(writer http.ResponseWriter, request *http.Request) {
	if _, ok := fake.service(writer, request); !ok {
		return
	}
	connection, ok := fake.visible(writer, request, request.URL.Query().Get("organization_id"))
	if !ok {
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(connection)
}

func (fake *fakeConnections) issue(writer http.ResponseWriter, request *http.Request) {
	claims, ok := fake.service(writer, request)
	if !ok {
		return
	}
	var body struct {
		OrganizationID string `json:"organization_id"`
		Purpose        string `json:"purpose"`
		ActingMemberID string `json:"acting_member_id"`
	}
	if json.NewDecoder(request.Body).Decode(&body) != nil || body.Purpose != claims.Purpose {
		fake.fail(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	connection, ok := fake.visible(writer, request, body.OrganizationID)
	if !ok {
		return
	}
	member := body.ActingMemberID
	if authorization := request.Header.Get("Authorization"); authorization != "" {
		member = members[strings.TrimPrefix(authorization, "Bearer ")].user
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.tokens = append(fake.tokens, tokenCall{service: claims.Service, purpose: body.Purpose, member: member})
	// As connections does, only cleanup works on a disconnected connection,
	// and only while it is held.
	if connection.Status != "active" && (body.Purpose != "cleanup" || !fake.holds[connection.ID]) {
		fake.fail(writer, http.StatusNotFound, "not_found")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": fake.token, "placement": fake.placement})
}

func (fake *fakeConnections) addHold(writer http.ResponseWriter, request *http.Request) {
	if _, ok := fake.service(writer, request); !ok {
		return
	}
	var body struct {
		OrganizationID string `json:"organization_id"`
		Holder         string `json:"holder"`
	}
	if json.NewDecoder(request.Body).Decode(&body) != nil || body.Holder != domain.Holder || body.OrganizationID != organization {
		fake.fail(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.holds[request.PathValue("connectionID")] = true
	writer.WriteHeader(http.StatusNoContent)
}

func (fake *fakeConnections) releaseHold(writer http.ResponseWriter, request *http.Request) {
	if _, ok := fake.service(writer, request); !ok {
		return
	}
	if request.PathValue("holder") != domain.Holder || request.URL.Query().Get("organization_id") != organization {
		fake.fail(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	delete(fake.holds, request.PathValue("connectionID"))
	writer.WriteHeader(http.StatusNoContent)
}

func (fake *fakeConnections) listApps(writer http.ResponseWriter, request *http.Request) {
	if _, known := members[strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")]; !known || request.Header.Get(serviceauth.Header) != "" {
		fake.fail(writer, http.StatusUnauthorized, "unauthorized")
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"apps": fake.apps})
}

// toProviders sends every provider API call to a fake provider server,
// whatever host the adapter names.
type toProviders struct{ target *url.URL }

func (transport toProviders) RoundTrip(request *http.Request) (*http.Response, error) {
	rewritten := request.Clone(request.Context())
	rewritten.URL.Scheme, rewritten.URL.Host, rewritten.Host = transport.target.Scheme, transport.target.Host, ""
	return http.DefaultTransport.RoundTrip(rewritten)
}

// stack is integrations serving its API and webhooks over HTTP, on
// PostgreSQL, with fake auth and connections.
type stack struct {
	t           *testing.T
	store       *postgres.Store
	database    *pgx.Conn
	service     *application.Service
	server      *httptest.Server
	connections *fakeConnections
	clock       *clock
	// apiURL is where a test calls the member API, and publicURL what
	// providers are told to deliver to: the same server, named as the test
	// reaches it and as the provider does.
	apiURL    string
	publicURL string
	// sealer is the envelope encryption the service runs with, so a test can
	// store a secret the way the service does.
	sealer secrets.Envelope
	// deliveries counts what reached the webhook routes.
	deliveries atomic.Int64
}

// stackOptions are the differences between the in-process stack and the live
// one: where provider calls go, where integrations listens, and what URL
// providers are given.
type stackOptions struct {
	// providerAPI takes every provider API call; without it they go to the
	// real providers.
	providerAPI *httptest.Server
	// listen and publicURL serve integrations on a real address behind a
	// public URL, such as a tunnel, instead of a test server.
	listen, publicURL string
	// realClock follows the wall clock, as the live tests need, instead of a
	// clock the test moves.
	realClock bool
}

// newStack serves integrations over a test server. Provider API calls go to
// providerAPI, when set, and otherwise to the real providers.
func newStack(t *testing.T, providerAPI *httptest.Server) *stack {
	t.Helper()
	return newStackWith(t, stackOptions{providerAPI: providerAPI})
}

// newStackWith serves integrations as options ask.
func newStackWith(t *testing.T, options stackOptions) *stack {
	t.Helper()
	store, database := openStore(t)
	key := newServiceKey(t)
	connections, connectionsServer := newFakeConnections(t, key)
	auth := fakeAuth(t)
	providerHTTP := providers.NewHTTPClient()
	if options.providerAPI != nil {
		target, _ := url.Parse(options.providerAPI.URL)
		providerHTTP = &http.Client{Timeout: 10 * time.Second, Transport: toProviders{target: target}}
	}
	s := &stack{
		t: t, store: store, database: database, connections: connections,
		clock:  &clock{now: time.Now().UTC().Truncate(time.Second)},
		sealer: secrets.Envelope{Keys: newLocalKeys(t), KeyID: "alias/integrations-e2e"},
	}
	now := s.clock.Now
	if options.realClock {
		now = func() time.Time { return time.Now().UTC() }
	}
	var handler http.Handler
	counted := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/v1/webhooks/") {
			s.deliveries.Add(1)
		}
		handler.ServeHTTP(writer, request)
	})
	if options.publicURL != "" {
		listener, err := net.Listen("tcp", options.listen)
		if err != nil {
			t.Fatalf("listen on %s: %v", options.listen, err)
		}
		server := &http.Server{Handler: counted, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		})
		s.apiURL, s.publicURL = "http://"+listener.Addr().String(), strings.TrimRight(options.publicURL, "/")
	} else {
		s.server = httptest.NewServer(counted)
		t.Cleanup(s.server.Close)
		s.apiURL, s.publicURL = s.server.URL, s.server.URL
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s.service = application.New(application.Dependencies{
		Store:       store,
		Directory:   authclient.New(auth.URL),
		Connections: connectionsclient.New(connectionsServer.URL, serviceauth.Signer{Client: key, KeyID: serviceKeyID, Service: "integrations"}),
		Sealer:      s.sealer,
		Providers:   providers.Builtin(),
		HTTP:        providerHTTP,
		Logger:      logger,
		Clock:       now,
	}, application.Config{PublicBaseURL: s.publicURL})
	handler = httpserver.New(s.service, httpserver.Config{}, logger)
	return s
}

// do calls the member API as a member.
func (s *stack) do(method, path, token string, body any) (int, []byte) {
	s.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, s.apiURL+"/v1/organizations/"+organization+path, reader)
	if err != nil {
		s.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	status, _, payload := send(s.t, request)
	return status, payload
}

// deliver posts a delivery to a webhook URL as a provider does.
func deliver(t *testing.T, webhookURL string, header http.Header, body []byte) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range header {
		request.Header[name] = values
	}
	return send(t, request)
}

func send(t *testing.T, request *http.Request) (int, http.Header, []byte) {
	t.Helper()
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, payload
}

// published is one event in the outbox.
type published struct {
	DetailType string
	Data       json.RawMessage
}

// outbox is every event integrations wrote, in the order the dispatcher
// publishes them.
func (s *stack) outbox() []published {
	s.t.Helper()
	rows, err := s.database.Query(context.Background(), `SELECT detail_type, detail FROM integrations_outbox ORDER BY occurred_at, id`)
	if err != nil {
		s.t.Fatal(err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (published, error) {
		var event published
		return event, row.Scan(&event.DetailType, &event.Data)
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return events
}

// eventsOf decodes the outbox's events of one detail type.
func eventsOf[T any](s *stack, detailType string) []T {
	s.t.Helper()
	var decoded []T
	for _, event := range s.outbox() {
		if event.DetailType != detailType {
			continue
		}
		var value T
		if err := json.Unmarshal(event.Data, &value); err != nil {
			s.t.Fatal(err)
		}
		decoded = append(decoded, value)
	}
	return decoded
}

// subscription is a subscription as the member API shows it.
type subscription struct {
	ID           string            `json:"id"`
	ConnectionID string            `json:"connection_id"`
	Provider     string            `json:"provider"`
	Target       map[string]string `json:"target"`
	Events       []string          `json:"events"`
	Status       string            `json:"status"`
	Reason       string            `json:"reason"`
	CreatedBy    string            `json:"created_by"`
	ExpiresAt    *time.Time        `json:"expires_at"`
	StoppedBy    string            `json:"stopped_by"`
}

// subscribe creates a subscription as the member and fails the test unless
// integrations answers 201.
func (s *stack) subscribe(connectionID string, target map[string]string, events []string) subscription {
	s.t.Helper()
	status, body := s.do(http.MethodPost, "/subscriptions", memberToken, map[string]any{"connection_id": connectionID, "target": target, "events": events})
	var created subscription
	if status != http.StatusCreated || json.Unmarshal(body, &created) != nil {
		s.t.Fatalf("POST subscriptions = %d %s", status, body)
	}
	for _, hidden := range []string{"route", "secret", "registration", "account_key", "renew", "cleanup"} {
		if strings.Contains(string(body), hidden) {
			s.t.Fatalf("POST subscriptions answered %s, which shows its %s", body, hidden)
		}
	}
	return created
}

// stored reads a subscription as the store keeps it.
func (s *stack) stored(subscriptionID string) domain.Subscription {
	s.t.Helper()
	stored, err := s.store.Subscription(context.Background(), organization, subscriptionID)
	if err != nil {
		s.t.Fatal(err)
	}
	return stored
}

// noSecrets fails the test if any outbox event or stored row carries one of
// the values in the clear.
func (s *stack) noSecrets(values ...string) {
	s.t.Helper()
	for _, value := range values {
		var leaked bool
		if err := s.database.QueryRow(context.Background(), `
			SELECT EXISTS (SELECT 1 FROM integrations_outbox WHERE strpos(detail::text, $1) > 0)
			    OR EXISTS (SELECT 1 FROM integrations_subscriptions WHERE strpos(encrypted_secret::text, $1) > 0 OR strpos(target::text, $1) > 0)
			    OR EXISTS (SELECT 1 FROM integrations_app_webhooks WHERE strpos(encrypted_secret::text, $1) > 0)`, value).Scan(&leaked); err != nil {
			s.t.Fatal(err)
		}
		if leaked {
			s.t.Fatalf("a secret of %d bytes is stored or published in the clear", len(value))
		}
	}
}

// connectionDisconnected is the event connections publishes when a member
// disconnects a connection.
func connectionDisconnected(connectionID, disconnectedBy string) connectionseventsv1.ConnectionDisconnected {
	return connectionseventsv1.ConnectionDisconnected{
		ConnectionID: connectionID, OrganizationID: organization, DisconnectedBy: disconnectedBy, Reason: "disconnected", DisconnectedAt: time.Now().UTC(),
	}
}

// hmacHex is the hex HMAC-SHA256 providers sign deliveries with.
func hmacHex(secret, message []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

// issues sets the credential connections hands out, and where it goes.
func (fake *fakeConnections) issues(token string, where placement) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.token, fake.placement = token, where
}

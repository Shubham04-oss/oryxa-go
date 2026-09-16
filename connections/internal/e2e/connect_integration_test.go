// Package e2e runs connections end to end: the HTTP API, the application,
// PostgreSQL, envelope encryption and the OAuth client, against fake auth and
// a fake GitHub.
package e2e

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/application"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

const (
	organization  = "org_e2e"
	adminToken    = "admin-token"
	memberToken   = "member-token"
	agentToken    = "agent-token"
	returnURI     = "http://localhost:3000/connected"
	accessToken   = "gho_e2e_access_token"
	personalToken = "ghp_e2e_personal_token"
)

// openStore migrates a throwaway schema and drops it afterwards, so the test
// is safe against a shared development database. It returns the store and a
// connection that reads the same schema.
func openStore(t *testing.T) (*postgres.Store, *pgx.Conn) {
	t.Helper()
	databaseURL := os.Getenv("CONNECTIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CONNECTIONS_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schemaName := "connections_e2e_" + hex.EncodeToString(suffix)
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
		t.Fatal("CONNECTIONS_TEST_DATABASE_URL must be a postgres:// URL")
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

// fakeAuth answers GET /v1/organizations/{organizationID} as auth does.
func fakeAuth(t *testing.T) *httptest.Server {
	t.Helper()
	members := map[string]struct {
		user, role string
		agent      bool
	}{
		"Bearer " + adminToken:  {"user-admin", "admin", false},
		"Bearer " + memberToken: {"user-1", "member", false},
		// The agent token names the same member as memberToken, with the
		// custom agent client: an agent acts as the member it works for,
		// never as a separate identity.
		"Bearer " + agentToken: {"user-1", "member", true},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		member, ok := members[request.Header.Get("Authorization")]
		switch {
		case !ok:
			writer.WriteHeader(http.StatusUnauthorized)
		case request.URL.Path != "/v1/organizations/"+organization:
			writer.WriteHeader(http.StatusNotFound)
		default:
			response := map[string]any{
				"organization": map[string]string{"id": organization},
				"membership":   map[string]string{"organization_id": organization, "user_id": member.user, "role": member.role},
			}
			if member.agent {
				response["agent"] = true
			}
			_ = json.NewEncoder(writer).Encode(response)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// fakeGitHub issues one code per consent and checks every token request the
// way GitHub would.
type fakeGitHub struct {
	t      *testing.T
	server *httptest.Server

	mu        sync.Mutex
	codes     map[string]string
	exchanges int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	github := &fakeGitHub{t: t, codes: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", github.token)
	mux.HandleFunc("GET /user", func(writer http.ResponseWriter, request *http.Request) {
		switch request.Header.Get("Authorization") {
		case "Bearer " + accessToken, "Bearer " + personalToken:
			_, _ = io.WriteString(writer, `{"id":42,"login":"octocat"}`)
		default:
			writer.WriteHeader(http.StatusUnauthorized)
		}
	})
	github.server = httptest.NewServer(mux)
	t.Cleanup(github.server.Close)
	return github
}

// consent plays the member approving the app on GitHub: it issues a code for
// the authorization URL's redirect.
func (github *fakeGitHub) consent(authorizationURL string) (callback string) {
	github.t.Helper()
	parsed, err := url.Parse(authorizationURL)
	query := parsed.Query()
	if err != nil || !strings.HasPrefix(authorizationURL, github.server.URL+"/login/oauth/authorize?") ||
		query.Get("client_id") != "client-e2e" || query.Get("response_type") != "code" || query.Get("state") == "" ||
		query.Get("scope") != "read:user repo" {
		github.t.Fatalf("authorization URL = %s", authorizationURL)
	}
	code := "code-" + query.Get("state")[:8]
	github.mu.Lock()
	github.codes[code] = query.Get("redirect_uri")
	github.mu.Unlock()
	return query.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {query.Get("state")}}.Encode()
}

func (github *fakeGitHub) token(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	github.mu.Lock()
	defer github.mu.Unlock()
	github.exchanges++
	redirect, known := github.codes[request.PostForm.Get("code")]
	delete(github.codes, request.PostForm.Get("code"))
	if !known || request.PostForm.Get("grant_type") != "authorization_code" || request.PostForm.Get("client_id") != "client-e2e" ||
		request.PostForm.Get("client_secret") != "secret-e2e" || request.PostForm.Get("redirect_uri") != redirect {
		_, _ = io.WriteString(writer, `{"error":"bad_verification_code"}`)
		return
	}
	_, _ = io.WriteString(writer, `{"access_token":"`+accessToken+`","token_type":"bearer","scope":"read:user,repo"}`)
}

type client struct {
	t       *testing.T
	baseURL string
	http    *http.Client
}

// do sends a request with a member's bearer token, when token is not empty.
func (c client) do(method, path, token string, body any) (int, http.Header, []byte) {
	c.t.Helper()
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	return c.send(method, path, headers, body)
}

// send sends a request with headers and a JSON body. A path starting with a
// slash is relative to the client's base URL.
func (c client) send(method, path string, headers http.Header, body any) (int, http.Header, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	target := path
	if strings.HasPrefix(path, "/") {
		target = c.baseURL + path
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := c.http.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return response.StatusCode, response.Header, payload
}

// stack is connections served over HTTP against a fake GitHub and fake auth.
type stack struct {
	service  *application.Service
	envelope secrets.Envelope
	server   *httptest.Server
	api      client
	internal client
}

// newStack points the built-in GitHub provider at the fake. The provider
// client is a plain HTTP client, because the real one refuses loopback
// addresses such as the fakes'.
func newStack(t *testing.T, store *postgres.Store, auth *httptest.Server, github *fakeGitHub, services httpserver.ServiceVerifier) stack {
	t.Helper()
	registry := providers.Builtin()
	provider := registry["github"]
	oauth := *provider.OAuth2
	oauth.AuthorizationURL, oauth.TokenURL, oauth.APIURL = github.server.URL+"/login/oauth/authorize", github.server.URL+"/login/oauth/access_token", github.server.URL
	provider.OAuth2 = &oauth
	for credentialType, keys := range provider.Keys {
		keys.APIURL = github.server.URL
		provider.Keys[credentialType] = keys
	}
	registry["github"] = provider
	return serveStack(t, store, auth, registry, &http.Client{Timeout: 10 * time.Second}, services)
}

// serveStack serves connections over HTTP with registry's providers, calling
// them through providerHTTP.
func serveStack(t *testing.T, store *postgres.Store, auth *httptest.Server, registry providers.Registry, providerHTTP *http.Client, services httpserver.ServiceVerifier) stack {
	t.Helper()
	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	envelope := secrets.Envelope{Keys: newLocalKeys(t), KeyID: "alias/oryxa-connections-e2e"}
	service := application.New(application.Dependencies{
		Store: store, Directory: authclient.New(auth.URL), Sealer: envelope,
		ProviderClient: providers.Client{HTTP: providerHTTP}, Providers: registry,
	}, application.Config{PublicBaseURL: server.URL, AllowedOrigins: []string{"http://localhost:3000"}})
	handler = httpserver.New(service, httpserver.Config{AllowedOrigins: []string{"http://localhost:3000"}, Services: services}, nil)
	noRedirects := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return stack{
		service: service, envelope: envelope, server: server,
		api:      client{t: t, baseURL: server.URL + "/v1/organizations/" + organization, http: noRedirects},
		internal: client{t: t, baseURL: server.URL + "/v1/internal/connections", http: noRedirects},
	}
}

func TestConnectGitHubEndToEnd(t *testing.T) {
	store, database := openStore(t)
	ctx := context.Background()
	auth := fakeAuth(t)
	github := newFakeGitHub(t)
	identity := newServiceIdentity(t)
	stack := newStack(t, store, auth, github, identity.verifier)
	connections, envelope, api := stack.server, stack.envelope, stack.api

	// An admin enables GitHub.
	status, _, body := api.do(http.MethodPost, "/apps", adminToken, map[string]any{
		"provider": "github", "client_id": "client-e2e", "client_secret": "secret-e2e", "scopes": []string{"repo", "read:user"},
	})
	var app struct {
		ID       string   `json:"id"`
		ClientID string   `json:"client_id"`
		Scopes   []string `json:"scopes"`
	}
	if status != http.StatusCreated || json.Unmarshal(body, &app) != nil || app.ClientID != "client-e2e" || strings.Contains(string(body), "secret-e2e") {
		t.Fatalf("POST apps = %d %s", status, body)
	}
	if status, _, body := api.do(http.MethodPost, "/apps", memberToken, map[string]any{"provider": "github"}); status != http.StatusForbidden {
		t.Fatalf("member POST apps = %d %s", status, body)
	}

	// A member connects their GitHub account.
	status, _, body = api.do(http.MethodPost, "/connections/authorize", memberToken, map[string]any{"app_id": app.ID, "return_uri": returnURI})
	var authorization application.Authorization
	if status != http.StatusOK || json.Unmarshal(body, &authorization) != nil {
		t.Fatalf("POST authorize = %d %s", status, body)
	}
	callback := github.consent(authorization.URL)
	if !strings.HasPrefix(callback, connections.URL+"/v1/oauth/callback/github?") {
		t.Fatalf("GitHub redirects to %s", callback)
	}
	status, header, _ := api.do(http.MethodGet, callback, "", nil)
	location, err := url.Parse(header.Get("Location"))
	if status != http.StatusSeeOther || err != nil || !strings.HasPrefix(location.String(), returnURI+"?") || location.Query().Get("session_id") == "" ||
		header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("callback = %d %v", status, header)
	}
	sessionID := location.Query().Get("session_id")
	if status, _, _ := api.do(http.MethodGet, callback, "", nil); status != http.StatusSeeOther {
		t.Fatalf("second callback = %d", status)
	}

	if status, _, body := api.do(http.MethodPost, "/connections/complete", adminToken, map[string]string{"session_id": sessionID}); status != http.StatusNotFound {
		t.Fatalf("admin completing the member's session = %d %s", status, body)
	}
	status, _, body = api.do(http.MethodPost, "/connections/complete", memberToken, map[string]string{"session_id": sessionID})
	var connection domain.Connection
	if status != http.StatusCreated || json.Unmarshal(body, &connection) != nil || connection.ProviderAccount != (domain.ProviderAccount{ID: "42", Name: "octocat"}) ||
		connection.MemberID != "user-1" || !connection.Default || !slices.Equal(connection.Scopes, []string{"read:user", "repo"}) ||
		connection.Health.State != domain.HealthUnknown || strings.Contains(string(body), accessToken) {
		t.Fatalf("POST complete = %d %s", status, body)
	}
	if status, _, body := api.do(http.MethodPost, "/connections/complete", memberToken, map[string]string{"session_id": sessionID}); status != http.StatusBadRequest ||
		!strings.Contains(string(body), "session_expired") {
		t.Fatalf("replayed complete = %d %s", status, body)
	}
	if github.exchanges != 1 {
		t.Fatalf("GitHub saw %d code exchanges", github.exchanges)
	}

	// The grant is stored only as envelope ciphertext bound to the connection.
	var rawCredentials []byte
	var sessionCompleted bool
	if err := database.QueryRow(ctx, `SELECT encrypted_credentials::text::bytea FROM connections WHERE id = $1`, connection.ID).Scan(&rawCredentials); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT completed_at IS NOT NULL FROM connections_sessions WHERE handle_hash IS NOT NULL`).Scan(&sessionCompleted); err != nil || !sessionCompleted {
		t.Fatalf("session completed = %v %v", sessionCompleted, err)
	}
	var sealed domain.SealedData
	if bytes.Contains(rawCredentials, []byte(accessToken)) || json.Unmarshal(rawCredentials, &sealed) != nil {
		t.Fatalf("stored credentials = %s", rawCredentials)
	}
	plaintext, err := envelope.Open(ctx, secrets.Binding{OrganizationID: organization, ResourceType: secrets.ResourceConnection, ResourceID: connection.ID}, sealed)
	if err != nil || !bytes.Contains(plaintext, []byte(accessToken)) {
		t.Fatalf("open stored grant = %s %v", plaintext, err)
	}
	if _, err := envelope.Open(ctx, secrets.Binding{OrganizationID: "org_other", ResourceType: secrets.ResourceConnection, ResourceID: connection.ID}, sealed); err == nil {
		t.Fatal("the grant opened for another organization")
	}

	// A member cannot pull the raw token with their own bearer token: tokens go
	// only to services.
	tokenBody := map[string]any{"organization_id": organization, "purpose": "action", "scopes": []string{"repo"}}
	if status, _, body := stack.internal.do(http.MethodPost, "/"+connection.ID+"/token", memberToken, tokenBody); status != http.StatusUnauthorized {
		t.Fatalf("member's own token request = %d %s", status, body)
	}
	// Integrations carries out the member's action: its own identity, and the
	// member's bearer token so connections checks that member's access.
	forward := func(bearer string) http.Header {
		return http.Header{serviceauth.Header: {identity.token("action")}, "Authorization": {"Bearer " + bearer}}
	}
	status, header, body = stack.internal.send(http.MethodPost, "/"+connection.ID+"/token", forward(memberToken), tokenBody)
	var issued domain.IssuedToken
	if status != http.StatusOK || json.Unmarshal(body, &issued) != nil || issued.AccessToken != accessToken || issued.TokenType != "Bearer" ||
		header.Get("Cache-Control") != "no-store" || header.Get("Pragma") != "no-cache" {
		t.Fatalf("token for a forwarded member = %d %v %s", status, header, body)
	}
	if status, _, body := stack.internal.send(http.MethodPost, "/"+connection.ID+"/token", forward(adminToken), tokenBody); status != http.StatusForbidden {
		t.Fatalf("token for a forwarded admin on a member's connection = %d %s", status, body)
	}

	// The member sees it, the admin can disconnect it, and it stays visible
	// as disconnected.
	status, _, body = api.do(http.MethodGet, "/connections", memberToken, nil)
	var listed struct {
		Connections []domain.Connection `json:"connections"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &listed) != nil || len(listed.Connections) != 1 || listed.Connections[0].ID != connection.ID {
		t.Fatalf("GET connections = %d %s", status, body)
	}
	if status, _, body := api.do(http.MethodDelete, "/connections/"+connection.ID, adminToken, nil); status != http.StatusNoContent {
		t.Fatalf("admin DELETE connection = %d %s", status, body)
	}
	status, _, body = api.do(http.MethodGet, "/connections/"+connection.ID, memberToken, nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"status":"disconnected"`) {
		t.Fatalf("GET disconnected connection = %d %s", status, body)
	}

	var detailTypes []string
	rows, err := database.Query(ctx, `SELECT detail_type FROM connections_outbox ORDER BY occurred_at, detail_type`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var detailType string
		if err := rows.Scan(&detailType); err != nil {
			t.Fatal(err)
		}
		detailTypes = append(detailTypes, detailType)
	}
	rows.Close()
	want := []string{"connections.app.created.v1", "connections.connection.connected.v1", "connections.connection.disconnected.v1"}
	if !slices.Equal(detailTypes, want) {
		t.Fatalf("outbox = %v, want %v", detailTypes, want)
	}
	var leaked bool
	if err := database.QueryRow(ctx, `SELECT bool_or(detail::text LIKE '%' || $1 || '%' OR detail::text LIKE '%secret-e2e%') FROM connections_outbox`, accessToken).Scan(&leaked); err != nil || leaked {
		t.Fatalf("outbox leaked a secret = %v %v", leaked, err)
	}
}

// serviceKey stands in for a caller's KMS asymmetric key: it signs like
// kms:Sign and publishes its public key like kms:GetPublicKey.
type serviceKey struct{ key *ecdsa.PrivateKey }

func (key serviceKey) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	digest := sha256.Sum256(input.Message)
	signature, err := ecdsa.SignASN1(rand.Reader, key.key, digest[:])
	return &kms.SignOutput{Signature: signature, KeyId: input.KeyId, SigningAlgorithm: input.SigningAlgorithm}, err
}

func (key serviceKey) GetPublicKey(_ context.Context, input *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, err := x509.MarshalPKIXPublicKey(&key.key.PublicKey)
	return &kms.GetPublicKeyOutput{PublicKey: der, KeyId: input.KeyId}, err
}

// serviceCall sends a request to the internal API with only a service token.
func (c client) serviceCall(method, path, serviceToken string, body any) (int, []byte) {
	c.t.Helper()
	status, _, payload := c.send(method, path, http.Header{serviceauth.Header: {serviceToken}}, body)
	return status, payload
}

// serviceIdentity is integrations' signing key, and the verifier connections
// checks its tokens with. Integrations may use action and cleanup.
type serviceIdentity struct {
	t        *testing.T
	verifier *serviceauth.Verifier
	signer   serviceauth.Signer
}

func newServiceIdentity(t *testing.T) serviceIdentity {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := serviceKey{key: privateKey}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{
		Keys: key, Audience: "connections",
		Callers: []serviceauth.Caller{{Service: "integrations", KeyID: "alias/integrations-identity", Purposes: []string{"action", "cleanup"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return serviceIdentity{t: t, verifier: verifier, signer: serviceauth.Signer{Client: key, KeyID: "alias/integrations-identity", Service: "integrations"}}
}

// token signs a token for purpose, whether or not integrations may use it.
func (identity serviceIdentity) token(purpose string) string {
	identity.t.Helper()
	token, err := identity.signer.Token(context.Background(), "connections", purpose)
	if err != nil {
		identity.t.Fatal(err)
	}
	return token
}

func TestServiceUsesAKeyConnectionEndToEnd(t *testing.T) {
	store, database := openStore(t)
	ctx := context.Background()
	identity := newServiceIdentity(t)
	stack := newStack(t, store, fakeAuth(t), newFakeGitHub(t), identity.verifier)

	// An admin enables GitHub personal access tokens, and a member connects one.
	status, _, body := stack.api.do(http.MethodPost, "/apps", adminToken, map[string]any{"provider": "github", "credential_type": "bearer"})
	var app domain.App
	if status != http.StatusCreated || json.Unmarshal(body, &app) != nil || app.CredentialType != domain.CredentialBearer {
		t.Fatalf("POST apps = %d %s", status, body)
	}
	status, _, body = stack.api.do(http.MethodPost, "/connections", memberToken, map[string]any{
		"app_id": app.ID, "label": "Personal token", "credential": map[string]string{"token": personalToken},
	})
	var connection domain.Connection
	if status != http.StatusCreated || json.Unmarshal(body, &connection) != nil || connection.ProviderAccount.ID != "42" ||
		connection.Label != "Personal token" || strings.Contains(string(body), personalToken) {
		t.Fatalf("POST connections = %d %s", status, body)
	}
	if status, _, body := stack.api.do(http.MethodPost, "/connections", memberToken, map[string]any{
		"app_id": app.ID, "credential": map[string]string{"token": "ghp_not_accepted"},
	}); status != http.StatusUnprocessableEntity || !strings.Contains(string(body), "credential_rejected") {
		t.Fatalf("POST connections with a refused token = %d %s", status, body)
	}

	// Integrations asks for the token as itself, acting for the member.
	tokenBody := map[string]any{"organization_id": organization, "purpose": "action", "acting_member_id": "user-1"}
	status, body = stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/token", identity.token("action"), tokenBody)
	var issued domain.IssuedToken
	if status != http.StatusOK || json.Unmarshal(body, &issued) != nil || issued.AccessToken != personalToken || issued.TokenType != "bearer" ||
		issued.Placement != (domain.TokenPlacement{In: "header", Name: "Authorization", Prefix: "Bearer "}) || issued.ExpiresAt != nil {
		t.Fatalf("service token issuance = %d %s", status, body)
	}
	if status, body := stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/token", identity.token("cleanup"), tokenBody); status != http.StatusForbidden {
		t.Fatalf("service token naming another purpose = %d %s", status, body)
	}
	if status, body := stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/token", identity.token("trigger"),
		map[string]any{"organization_id": organization, "purpose": "trigger", "acting_member_id": "user-1"}); status != http.StatusForbidden {
		t.Fatalf("service purpose outside its allowlist = %d %s", status, body)
	}
	tampered := identity.token("action")
	tampered = tampered[:len(tampered)-4] + "AAAA"
	if status, body := stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/token", tampered, tokenBody); status != http.StatusUnauthorized {
		t.Fatalf("tampered service token = %d %s", status, body)
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	forger := serviceauth.Signer{Client: serviceKey{key: otherKey}, KeyID: "alias/forged", Service: "integrations"}
	if forged, err := forger.Token(ctx, "connections", "action"); err != nil {
		t.Fatal(err)
	} else if status, body := stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/token", forged, tokenBody); status != http.StatusUnauthorized {
		t.Fatalf("token signed with another key = %d %s", status, body)
	}

	// It holds the connection while its webhooks exist, so disconnecting
	// keeps the key until the hold is released.
	holdBody := map[string]any{"organization_id": organization, "holder": "integrations:webhooks"}
	if status, body := stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/holds", identity.token("cleanup"), holdBody); status != http.StatusNoContent {
		t.Fatalf("POST holds = %d %s", status, body)
	}
	if status, _, body := stack.api.do(http.MethodDelete, "/connections/"+connection.ID, memberToken, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE connection = %d %s", status, body)
	}
	if claimed, err := stack.service.CleanUp(ctx); err != nil || claimed != 0 {
		t.Fatalf("CleanUp() with a hold = %d %v", claimed, err)
	}
	cleanupBody := map[string]any{"organization_id": organization, "purpose": "cleanup", "acting_member_id": "user-1"}
	if status, body := stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/token", identity.token("cleanup"), cleanupBody); status != http.StatusOK ||
		!strings.Contains(string(body), personalToken) {
		t.Fatalf("cleanup token while held = %d %s", status, body)
	}
	if status, body := stack.internal.serviceCall(http.MethodDelete, "/"+connection.ID+"/holds/integrations:webhooks?organization_id="+organization,
		identity.token("cleanup"), nil); status != http.StatusNoContent {
		t.Fatalf("DELETE hold = %d %s", status, body)
	}
	if claimed, err := stack.service.CleanUp(ctx); err != nil || claimed != 1 {
		t.Fatalf("CleanUp() after release = %d %v", claimed, err)
	}
	if status, body := stack.internal.serviceCall(http.MethodPost, "/"+connection.ID+"/token", identity.token("cleanup"), cleanupBody); status != http.StatusNotFound {
		t.Fatalf("cleanup token after deletion = %d %s", status, body)
	}
	var credentialsDeleted bool
	var revoked string
	if err := database.QueryRow(ctx, `SELECT encrypted_credentials IS NULL AND credentials_deleted_at IS NOT NULL FROM connections WHERE id = $1`,
		connection.ID).Scan(&credentialsDeleted); err != nil || !credentialsDeleted {
		t.Fatalf("credentials deleted = %v %v", credentialsDeleted, err)
	}
	if err := database.QueryRow(ctx, `SELECT detail->>'revoked' FROM connections_outbox WHERE detail_type = 'connections.connection.credentials-deleted.v1'`).Scan(&revoked); err != nil || revoked != "false" {
		t.Fatalf("credentials deleted event revoked = %q %v", revoked, err)
	}
	var leaked bool
	if err := database.QueryRow(ctx, `SELECT bool_or(detail::text LIKE '%' || $1 || '%') FROM connections_outbox`, personalToken).Scan(&leaked); err != nil || leaked {
		t.Fatalf("outbox leaked the token = %v %v", leaked, err)
	}
}

// TestAgentTokenIsReadOnlyEndToEnd runs the whole stack — the HTTP API, the
// application and PostgreSQL — against fake auth answering an agent token's
// organization request with the top-level "agent" field. It proves the public
// API enforces the rule connections/README.md documents: an agent token can
// list connections but cannot disconnect one, while the member it acts for
// still can.
func TestAgentTokenIsReadOnlyEndToEnd(t *testing.T) {
	store, _ := openStore(t)
	stack := newStack(t, store, fakeAuth(t), newFakeGitHub(t), nil)

	status, _, body := stack.api.do(http.MethodPost, "/apps", adminToken, map[string]any{"provider": "github", "credential_type": "bearer"})
	var app domain.App
	if status != http.StatusCreated || json.Unmarshal(body, &app) != nil {
		t.Fatalf("POST apps = %d %s", status, body)
	}
	status, _, body = stack.api.do(http.MethodPost, "/connections", memberToken, map[string]any{
		"app_id": app.ID, "label": "Personal token", "credential": map[string]string{"token": personalToken},
	})
	var connection domain.Connection
	if status != http.StatusCreated || json.Unmarshal(body, &connection) != nil {
		t.Fatalf("POST connections = %d %s", status, body)
	}

	status, _, body = stack.api.do(http.MethodGet, "/connections", agentToken, nil)
	var listed struct {
		Connections []domain.Connection `json:"connections"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &listed) != nil || len(listed.Connections) != 1 || listed.Connections[0].ID != connection.ID {
		t.Fatalf("GET connections with an agent token = %d %s", status, body)
	}

	status, _, body = stack.api.do(http.MethodDelete, "/connections/"+connection.ID, agentToken, nil)
	if status != http.StatusForbidden || !strings.Contains(string(body), `"an agent cannot change this"`) {
		t.Fatalf("DELETE connection with an agent token = %d %s", status, body)
	}

	// The member it acts for can still disconnect it themselves.
	if status, _, body := stack.api.do(http.MethodDelete, "/connections/"+connection.ID, memberToken, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE connection with the member's own token = %d %s", status, body)
	}
}

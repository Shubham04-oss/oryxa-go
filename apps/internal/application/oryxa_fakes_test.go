package application

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// fakeOryxaStore is apps' tables in memory with what Oryxa's sign-in adds:
// connections without a state hash, the sweep of abandoned sign-ins,
// relabelling, bare events, and OAuth clients as signin keeps them.
type fakeOryxaStore struct {
	*fakeStore
	clients map[string]signin.OAuthClient // organization/client → live client
}

func newFakeOryxaStore() *fakeOryxaStore {
	return &fakeOryxaStore{fakeStore: newFakeStore(), clients: map[string]signin.OAuthClient{}}
}

// CreateConnection records an Oryxa connection, which carries no state hash,
// under the same one-current-connection rule; a Composio one goes through
// the fake the Composio tests use.
func (store *fakeOryxaStore) CreateConnection(ctx context.Context, connection domain.Connection, stateHash string, event domain.Event) error {
	if stateHash != "" {
		return store.fakeStore.CreateConnection(ctx, connection, stateHash, event)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.connections {
		if existing.OrganizationID == connection.OrganizationID && existing.MemberID == connection.MemberID &&
			existing.Toolkit == connection.Toolkit &&
			(existing.Status == domain.ConnectionPending || existing.Status == domain.ConnectionActive) {
			return domain.ErrConflict
		}
	}
	store.connections[connection.ID] = connection
	store.events = append(store.events, event)
	return nil
}

func (store *fakeOryxaStore) FailPendingOryxaConnectionsBefore(_ context.Context, cutoff, now time.Time, events func([]domain.Connection) ([]domain.Event, error)) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var swept []domain.Connection
	for id, connection := range store.connections {
		if connection.Oryxa() && connection.Status == domain.ConnectionPending && connection.CreatedAt.Before(cutoff) {
			connection.Status, connection.StatusReason, connection.UpdatedAt = domain.ConnectionFailed, "link expired", now
			store.connections[id] = connection
			swept = append(swept, connection)
		}
	}
	if len(swept) == 0 {
		return 0, nil
	}
	built, err := events(swept)
	if err != nil {
		return 0, err
	}
	store.events = append(store.events, built...)
	return int64(len(swept)), nil
}

func (store *fakeOryxaStore) RelabelConnection(_ context.Context, id, label string, now time.Time, event domain.Event) (domain.Connection, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	connection, ok := store.connections[id]
	if !ok || connection.Status != domain.ConnectionActive {
		return domain.Connection{}, false, nil
	}
	connection.Label, connection.UpdatedAt = label, now
	store.connections[id] = connection
	store.events = append(store.events, event)
	return connection, true, nil
}

func (store *fakeOryxaStore) RecordEvents(_ context.Context, events ...domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.events = append(store.events, events...)
	return nil
}

func (store *fakeOryxaStore) LiveOAuthClient(_ context.Context, organizationID, client string) (signin.OAuthClient, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	live, ok := store.clients[organizationID+"/"+client]
	if !ok {
		return signin.OAuthClient{}, signin.ErrNoClient
	}
	return live, nil
}

// setKind records the kind of credential signin kept for a connection, as
// the store reads it back beside the connection.
func (store *fakeOryxaStore) setKind(id, kind string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if connection, ok := store.connections[id]; ok {
		connection.CredentialKind = kind
		store.connections[id] = connection
	}
}

func (store *fakeOryxaStore) connection(id string) domain.Connection {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.connections[id]
}

// fakeSignin answers what a test sets, records what it was asked, and keeps
// what signin keeps for a connection only as far as the store reads it.
type fakeSignin struct {
	mu    sync.Mutex
	store *fakeOryxaStore

	starts   []signin.StartRequest
	startErr error

	callbacks   []signin.Callback
	completion  signin.Completion
	completeErr error

	saves   []signin.CredentialRequest
	saveErr error
	account signin.Account

	revoked    []string
	revocation signin.Revocation
	revokeErr  error

	checks       []signin.Check
	checkCalls   int
	retryCalls   int
	clients      map[string]signin.ClientView
	puts         []signin.ClientInput
	putErr       error
	removedCalls []string

	// sessions records what a tool, reader or poll asked signin for, and
	// answer builds the session it is answered. Without one, a session with
	// no client is answered, so a test that only checks what was asked need
	// not build one.
	sessions   []string
	sessionErr error
	answer     func(ctx context.Context, connectionID string) (integration.Session, error)
}

func (fake *fakeSignin) Session(ctx context.Context, connectionID string, _ ...signin.SessionOption) (integration.Session, error) {
	fake.mu.Lock()
	fake.sessions = append(fake.sessions, connectionID)
	answer, err := fake.answer, fake.sessionErr
	fake.mu.Unlock()
	switch {
	case err != nil:
		return integration.Session{}, err
	case answer != nil:
		return answer(ctx, connectionID)
	default:
		return integration.Session{BaseURL: "https://api.acme.test", Settings: map[string]string{}}, nil
	}
}

// sessionCalls is the connection each Session call was for, in order.
func (fake *fakeSignin) sessionCalls() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return slices.Clone(fake.sessions)
}

func (fake *fakeSignin) StartOAuth(_ context.Context, request signin.StartRequest) (signin.Started, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.starts = append(fake.starts, request)
	if fake.startErr != nil {
		return signin.Started{}, fake.startErr
	}
	return signin.Started{
		AuthorizationURL: "https://github.com/login/oauth/authorize?client_id=Iv1.oryxa&state=state-" + request.ConnectionID,
		State:            "state-" + request.ConnectionID, ExpiresAt: testClock.Add(signin.FlowLifetime),
	}, nil
}

// CompleteOAuth runs the callback's Verify as signin does: an error it
// answers ends the sign-in, a *Failure keeping its reason and anything else
// as wrong_account.
func (fake *fakeSignin) CompleteOAuth(ctx context.Context, callback signin.Callback) (signin.Completion, error) {
	fake.mu.Lock()
	fake.callbacks = append(fake.callbacks, callback)
	completion, err := fake.completion, fake.completeErr
	fake.mu.Unlock()
	if completion.ConnectionID == "" {
		return signin.Completion{}, err
	}
	if callback.Verify != nil {
		if verifyErr := callback.Verify(ctx, completion.OrganizationID, completion.MemberID); verifyErr != nil {
			if failure, ok := verifyErr.(*signin.Failure); ok {
				return completion, failure
			}
			return completion, &signin.Failure{Reason: signin.ReasonWrongAccount}
		}
	}
	if err == nil {
		fake.store.setKind(completion.ConnectionID, integration.KindOAuth2Code)
	}
	return completion, err
}

func (fake *fakeSignin) SaveCredential(_ context.Context, request signin.CredentialRequest) (signin.Saved, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.saves = append(fake.saves, request)
	if fake.saveErr != nil {
		return signin.Saved{}, fake.saveErr
	}
	fake.store.setKind(request.ConnectionID, request.Kind)
	return signin.Saved{Kind: request.Kind, Account: fake.account, Version: int64(len(fake.saves))}, nil
}

func (fake *fakeSignin) Revoke(_ context.Context, connectionID string) (signin.Revocation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.revoked = append(fake.revoked, connectionID)
	return fake.revocation, fake.revokeErr
}

func (fake *fakeSignin) CheckDue(context.Context, int) ([]signin.Check, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.checkCalls++
	checks := fake.checks
	fake.checks = nil
	return checks, nil
}

func (fake *fakeSignin) RetryRevocations(context.Context, int) (int, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.retryCalls++
	return 0, nil
}

func (fake *fakeSignin) PutClient(_ context.Context, input signin.ClientInput) (signin.ClientView, bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.puts = append(fake.puts, input)
	if fake.putErr != nil {
		return signin.ClientView{}, false, fake.putErr
	}
	key := input.OrganizationID + "/" + input.Client
	current, existed := fake.clients[key]
	view := signin.ClientView{
		ID: "aoc_" + input.Client, OrganizationID: input.OrganizationID, Client: input.Client, ClientID: input.ClientID,
		SecretSet: input.ClientSecret != "" || current.SecretSet, RefuseDefault: input.RefuseDefault,
		Fields: map[string]string{}, SecretFields: map[string]string{}, CreatedBy: input.ActorID,
		CreatedAt: testClock, UpdatedAt: testClock,
	}
	if secret := input.ClientSecret.Reveal(); len(secret) >= 16 {
		view.SecretHint = secret[len(secret)-4:]
	} else if existed {
		view.SecretHint = current.SecretHint
	}
	changed := !existed || current.ClientID != view.ClientID || input.ClientSecret != "" || current.RefuseDefault != view.RefuseDefault
	fake.clients[key] = view
	fake.store.mu.Lock()
	fake.store.clients[key] = signin.OAuthClient{ID: view.ID, OrganizationID: input.OrganizationID, Client: input.Client, ClientID: input.ClientID}
	fake.store.mu.Unlock()
	return view, changed, nil
}

func (fake *fakeSignin) GetClient(_ context.Context, organizationID, client string) (signin.ClientView, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	view, ok := fake.clients[organizationID+"/"+client]
	if !ok {
		return signin.ClientView{}, signin.ErrClientNotFound
	}
	return view, nil
}

func (fake *fakeSignin) RemoveClient(_ context.Context, organizationID, client string) (bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	key := organizationID + "/" + client
	fake.removedCalls = append(fake.removedCalls, key)
	if _, ok := fake.clients[key]; !ok {
		return false, nil
	}
	delete(fake.clients, key)
	fake.store.mu.Lock()
	delete(fake.store.clients, key)
	fake.store.mu.Unlock()
	return true, nil
}

// fakeHooks keeps endpoints in memory, each with its toolkit's webhook
// section as the registry declares it.
type fakeHooks struct {
	mu        sync.Mutex
	registry  *integration.Registry
	endpoints map[string]hooks.Endpoint
	serial    int
	created   []hooks.CreateInput
	rotated   []string
	deleted   []string // connections whose endpoints were deleted
	// deletedEndpoints are endpoints deleted one by one.
	deletedEndpoints []string
	revealed         []string
	revealErr        error
	token            secrets.Secret
	swept            int
}

func (fake *fakeHooks) Create(_ context.Context, input hooks.CreateInput) (hooks.Created, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.created = append(fake.created, input)
	registered, ok := fake.registry.Lookup(input.Toolkit)
	if !ok || registered.Manifest().Webhook == nil {
		return hooks.Created{}, fmt.Errorf("no webhook: %w", domain.ErrInvalid)
	}
	fake.serial++
	endpoint := hooks.Endpoint{
		ID: fmt.Sprintf("aph_%024d", fake.serial), Toolkit: input.Toolkit, Scope: input.Scope,
		OrganizationID: input.OrganizationID, ConnectionID: input.ConnectionID, Scheme: *registered.Manifest().Webhook,
		Status: hooks.StatusActive, CreatedAt: testClock, UpdatedAt: testClock,
	}
	if input.Secret != "" {
		endpoint.Secret = hooks.Sealed{Current: &secrets.SealedData{Version: 1}, Version: 1}
	}
	fake.endpoints[endpoint.ID] = endpoint
	return hooks.Created{Endpoint: endpoint, URL: fake.url(endpoint.ID)}, nil
}

func (fake *fakeHooks) Rotate(_ context.Context, id string, _ secrets.Secret) (hooks.Created, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.rotated = append(fake.rotated, id)
	endpoint := fake.endpoints[id]
	endpoint.Secret.Version++
	fake.endpoints[id] = endpoint
	return hooks.Created{Endpoint: endpoint, URL: fake.url(id)}, nil
}

func (fake *fakeHooks) find(scope, organizationID, toolkit string) (hooks.Endpoint, bool) {
	for _, endpoint := range fake.endpoints {
		if endpoint.Scope == scope && endpoint.OrganizationID == organizationID && endpoint.Toolkit == toolkit {
			return endpoint, true
		}
	}
	return hooks.Endpoint{}, false
}

func (fake *fakeHooks) OrganizationEndpoint(_ context.Context, organizationID, toolkit string) (hooks.Endpoint, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	endpoint, ok := fake.find(integration.HookScopeOrganization, organizationID, toolkit)
	if !ok {
		return hooks.Endpoint{}, fmt.Errorf("hook endpoint: %w", domain.ErrNotFound)
	}
	return endpoint, nil
}

func (fake *fakeHooks) DeploymentEndpoint(ctx context.Context, toolkit string) (hooks.Endpoint, string, error) {
	fake.mu.Lock()
	endpoint, ok := fake.find(integration.HookScopeDeployment, "", toolkit)
	fake.mu.Unlock()
	if ok {
		return endpoint, fake.url(endpoint.ID), nil
	}
	created, err := fake.Create(ctx, hooks.CreateInput{Toolkit: toolkit, Scope: integration.HookScopeDeployment})
	return created.Endpoint, created.URL, err
}

func (fake *fakeHooks) Delete(_ context.Context, id string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if _, ok := fake.endpoints[id]; !ok {
		return fmt.Errorf("webhook endpoint: %w", domain.ErrNotFound)
	}
	delete(fake.endpoints, id)
	fake.deletedEndpoints = append(fake.deletedEndpoints, id)
	return nil
}

func (fake *fakeHooks) DeleteForConnection(_ context.Context, connectionID string) ([]hooks.Endpoint, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.deleted = append(fake.deleted, connectionID)
	var deleted []hooks.Endpoint
	for id, endpoint := range fake.endpoints {
		if endpoint.ConnectionID == connectionID {
			deleted = append(deleted, endpoint)
			delete(fake.endpoints, id)
		}
	}
	return deleted, nil
}

func (fake *fakeHooks) RevealVerificationToken(_ context.Context, id string) (secrets.Secret, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.revealed = append(fake.revealed, id)
	if fake.revealErr != nil {
		return "", fake.revealErr
	}
	return fake.token, nil
}

func (fake *fakeHooks) SweepExpired(context.Context) (int64, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.swept++
	return 0, nil
}

func (fake *fakeHooks) URL(id string) string { return fake.url(id) }

func (fake *fakeHooks) url(id string) string { return "https://api.oryxa.test" + hooks.PathPrefix + id }

// sessionDirectory answers auth's membership by bearer token or by access
// cookie, so a callback's cookie can be told apart.
type sessionDirectory struct {
	mu          sync.Mutex
	members     map[string]domain.OrgMembership
	unavailable bool
}

func (directory *sessionDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	directory.mu.Lock()
	defer directory.mu.Unlock()
	if directory.unavailable && credentials.Authorization == "" {
		return domain.OrgMembership{}, fmt.Errorf("auth did not answer: %w", domain.ErrUnavailable)
	}
	key := credentials.Authorization
	if key == "" {
		key = "cookie:" + credentials.AccessCookie
	}
	member, ok := directory.members[key]
	if !ok {
		return domain.OrgMembership{}, domain.ErrUnauthorized
	}
	if member.OrganizationID != organizationID {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	return member, nil
}

// redirectURI is where every test's sign-in comes back.
const redirectURI = "https://app.oryxa.test/v1/apps/oauth/callback"

type oryxaHarness struct {
	service   *Service
	store     *fakeOryxaStore
	signin    *fakeSignin
	hooks     *fakeHooks
	composio  *fakeComposio
	directory *sessionDirectory
}

// newOryxaHarness is the application with Oryxa's own sign-in switched on
// for toolkits, over the integrations this build ships.
func newOryxaHarness(t *testing.T, toolkits ...string) oryxaHarness {
	t.Helper()
	registry, err := integrations.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return newOryxaHarnessWith(t, registry, toolkits...)
}

// newOryxaHarnessWith is newOryxaHarness over a registry a test built, so a
// test can register an integration that carries tools, readers and triggers
// of its own. The integrations this build ships declare how to sign in, and
// no tools yet, so nothing about the Oryxa route can be exercised without
// one.
func newOryxaHarnessWith(t *testing.T, registry *integration.Registry, toolkits ...string) oryxaHarness {
	t.Helper()
	store := newFakeOryxaStore()
	fakeSign := &fakeSignin{store: store, clients: map[string]signin.ClientView{}, account: signin.Account{Label: "octocat", Ref: "583231"}}
	fakeHook := &fakeHooks{registry: registry, endpoints: map[string]hooks.Endpoint{}, token: "notion-verification-token-1234"}
	directory := &sessionDirectory{members: map[string]domain.OrgMembership{
		memberToken.Authorization:     {OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember},
		otherToken.Authorization:      {OrganizationID: "org_1", UserID: "user-2", Role: domain.OrgRoleMember},
		adminToken.Authorization:      {OrganizationID: "org_1", UserID: "user-3", Role: domain.OrgRoleAdmin},
		agentToken.Authorization:      {OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember, Agent: true},
		adminAgentToken.Authorization: {OrganizationID: "org_1", UserID: "user-3", Role: domain.OrgRoleAdmin, Agent: true},
		"cookie:member-session":       {OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember},
		"cookie:other-session":        {OrganizationID: "org_1", UserID: "user-2", Role: domain.OrgRoleMember},
		"cookie:elsewhere-session":    {OrganizationID: "org_2", UserID: "user-1", Role: domain.OrgRoleMember},
	}}
	composioFake := newFakeComposio()
	service, err := New(Dependencies{
		Store: store, Directory: directory, Composio: composioFake,
		Oryxa: &Oryxa{
			Store: store, Signin: fakeSign, Hooks: fakeHook, Integrations: registry,
			Toolkits: toolkits, RedirectURI: redirectURI,
		},
		Clock: func() time.Time { return testClock },
	}, Config{
		PublicBaseURL: "https://api.oryxa.test", ReturnOrigins: []string{"https://app.oryxa.test"},
		WebhookSecret: "whsec_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	service.wait = func(context.Context, time.Duration) error { return nil }
	return oryxaHarness{service: service, store: store, signin: fakeSign, hooks: fakeHook, composio: composioFake, directory: directory}
}

// oryxaConnection records a member's Oryxa connection to toolkit, holding a
// credential of kind when it is not pending.
func (h oryxaHarness) oryxaConnection(id, memberID, toolkit string, status domain.ConnectionStatus, kind string) domain.Connection {
	connected := testClock
	connection := domain.Connection{
		ID: id, OrganizationID: "org_1", MemberID: memberID, Toolkit: toolkit, Broker: domain.BrokerOryxa,
		CredentialKind: kind, Status: status, Label: "octocat", CreatedAt: testClock, UpdatedAt: testClock,
	}
	if status != domain.ConnectionPending {
		connection.ConnectedAt = &connected
	}
	h.store.connections[id] = connection
	return connection
}

// composioConnection records a member's active Composio connection.
func (h oryxaHarness) composioConnection(id, memberID, toolkit string) domain.Connection {
	connected := testClock
	connection := domain.Connection{
		ID: id, OrganizationID: "org_1", MemberID: memberID, Toolkit: toolkit, AuthConfigID: "ac_1",
		ComposioUserID: domain.ComposioUserID("org_1", memberID), ComposioAccountID: "ca_" + id,
		Status: domain.ConnectionActive, CreatedAt: testClock, UpdatedAt: testClock, ConnectedAt: &connected,
	}
	h.store.connections[id] = connection
	return connection
}

// eventTypes lists the detail types published, in order.
func (h oryxaHarness) eventTypes() []string {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	types := make([]string, 0, len(h.store.events))
	for _, event := range h.store.events {
		types = append(types, event.DetailType)
	}
	return types
}

// allEvents is every event's data, joined, for a test that must find no
// secret anywhere in them.
func (h oryxaHarness) allEvents() string {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	var parts []string
	for _, event := range h.store.events {
		parts = append(parts, string(event.Data))
	}
	return strings.Join(parts, "\n")
}

func containsAll(haystack []string, needles ...string) bool {
	for _, needle := range needles {
		if !slices.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

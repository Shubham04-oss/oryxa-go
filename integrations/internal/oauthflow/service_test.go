package oauthflow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

var errStateUnavailable = errors.New("oauth state unavailable")

type testCatalog struct{ definition Definition }

func (c testCatalog) OAuthDefinition(provider string) (Definition, bool) {
	return c.definition, provider == c.definition.Provider
}

type testAuthorizer struct {
	manage  bool
	connect bool
}

func (a *testAuthorizer) CanManageOAuthApps(context.Context, string, string) error {
	if !a.manage {
		return errors.New("not an admin")
	}
	return nil
}

func (a *testAuthorizer) CanCreateConnection(context.Context, string, string) error {
	if !a.connect {
		return errors.New("not a member")
	}
	return nil
}

type memoryStore struct {
	profiles     map[string]domain.OAuthAppProfile
	states       map[[32]byte]domain.OAuthAuthorizationState
	connection   *domain.Connection
	event        *domain.Event
	profileEvent *domain.Event
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		profiles: make(map[string]domain.OAuthAppProfile),
		states:   make(map[[32]byte]domain.OAuthAuthorizationState),
	}
}

func (s *memoryStore) CreateOAuthApp(_ context.Context, profile domain.OAuthAppProfile) error {
	s.profiles[profile.OrganizationID+"/"+profile.ID] = profile
	return nil
}

func (s *memoryStore) CreateOAuthAppWithEvent(ctx context.Context, profile domain.OAuthAppProfile, event domain.Event) error {
	if err := s.CreateOAuthApp(ctx, profile); err != nil {
		return err
	}
	s.profileEvent = &event
	return nil
}

func (s *memoryStore) OAuthApp(_ context.Context, organizationID, profileID string) (domain.OAuthAppProfile, error) {
	profile, ok := s.profiles[organizationID+"/"+profileID]
	if !ok {
		return domain.OAuthAppProfile{}, errors.New("profile not found")
	}
	return profile, nil
}

func (s *memoryStore) UpdateOAuthAppCredentialsWithEvent(_ context.Context, profile domain.OAuthAppProfile, event domain.Event) error {
	s.profiles[profile.OrganizationID+"/"+profile.ID] = profile
	s.profileEvent = &event
	return nil
}

func (s *memoryStore) DisableOAuthAppWithEvent(_ context.Context, organizationID, profileID string, now time.Time, event domain.Event) error {
	profile, ok := s.profiles[organizationID+"/"+profileID]
	if !ok {
		return domain.ErrNotFound
	}
	profile.Status = domain.OAuthAppStatusDisabled
	profile.UpdatedAt = now
	s.profiles[organizationID+"/"+profileID] = profile
	s.profileEvent = &event
	return nil
}

func (s *memoryStore) CreateOAuthState(_ context.Context, state domain.OAuthAuthorizationState) error {
	if _, exists := s.states[state.StateHash]; exists {
		return errors.New("state collision")
	}
	s.states[state.StateHash] = state
	return nil
}

func (s *memoryStore) ConsumeOAuthState(_ context.Context, hash [32]byte, _ time.Time) (domain.OAuthAuthorizationState, error) {
	state, ok := s.states[hash]
	if !ok {
		return domain.OAuthAuthorizationState{}, errStateUnavailable
	}
	delete(s.states, hash)
	return state, nil
}

func (s *memoryStore) CreateConnectionWithEvent(_ context.Context, connection domain.Connection, event domain.Event) error {
	s.connection = &connection
	s.event = &event
	return nil
}

func (s *memoryStore) ActiveConnection(_ context.Context, organizationID, memberID, profileID string) (domain.Connection, bool, error) {
	if s.connection == nil || s.connection.OrganizationID != organizationID || s.connection.MemberID != memberID || s.connection.ProfileID != profileID || s.connection.Status != domain.ConnectionStatusActive {
		return domain.Connection{}, false, nil
	}
	return *s.connection, true, nil
}

func (s *memoryStore) ConnectionForExecution(_ context.Context, organizationID, connectionID string) (domain.Connection, error) {
	if s.connection == nil || s.connection.OrganizationID != organizationID || s.connection.ID != connectionID || s.connection.Status != domain.ConnectionStatusActive {
		return domain.Connection{}, domain.ErrNotFound
	}
	return *s.connection, nil
}

func (s *memoryStore) ListMemberIntegrations(_ context.Context, organizationID, memberID string) ([]domain.MemberIntegration, error) {
	result := make([]domain.MemberIntegration, 0, len(s.profiles))
	for _, profile := range s.profiles {
		if profile.OrganizationID != organizationID || profile.Status != domain.OAuthAppStatusActive {
			continue
		}
		item := domain.MemberIntegration{
			ProfileID: profile.ID, Provider: profile.Provider, PieceName: profile.PieceName,
			PieceVersion: profile.PieceVersion, Mode: profile.Mode,
			Scopes: append([]string(nil), profile.Scopes...), State: domain.MemberIntegrationAvailable,
		}
		if connection, ok, _ := s.ActiveConnection(context.Background(), organizationID, memberID, profile.ID); ok {
			item.State = domain.MemberIntegrationConnected
			item.Connection = &domain.MemberConnection{ID: connection.ID, Identity: connection.Identity, ConnectedAt: connection.CreatedAt}
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *memoryStore) DisconnectConnectionWithEvent(_ context.Context, organizationID, memberID, connectionID string, now time.Time, event domain.Event) error {
	if s.connection == nil || s.connection.OrganizationID != organizationID || s.connection.MemberID != memberID || s.connection.ID != connectionID {
		return domain.ErrNotFound
	}
	s.connection.Status = domain.ConnectionStatusDisconnected
	s.connection.UpdatedAt = now
	s.event = &event
	return nil
}

type boxedValue struct {
	binding secrets.Binding
	value   []byte
}

type memoryBox struct {
	values map[string]boxedValue
}

func newMemoryBox() *memoryBox { return &memoryBox{values: make(map[string]boxedValue)} }

func (b *memoryBox) Seal(_ context.Context, binding secrets.Binding, plaintext []byte) (domain.SealedData, error) {
	digest := sha256.Sum256(append([]byte(binding.ResourceType+"/"+binding.ResourceID+":"), plaintext...))
	key := hex.EncodeToString(digest[:])
	b.values[key] = boxedValue{binding: binding, value: append([]byte(nil), plaintext...)}
	return domain.SealedData{Version: 1, Nonce: []byte("nonce"), Ciphertext: digest[:], EncryptedDataKey: []byte("wrapped")}, nil
}

func (b *memoryBox) Open(_ context.Context, binding secrets.Binding, sealed domain.SealedData) ([]byte, error) {
	value, ok := b.values[hex.EncodeToString(sealed.Ciphertext)]
	if !ok || value.binding != binding {
		return nil, errors.New("sealed value or binding not found")
	}
	return append([]byte(nil), value.value...), nil
}

type testProvider struct {
	authorization AuthorizationRequest
	exchange      ExchangeRequest
	exchangeCalls int
	grant         Grant
	identity      domain.ProviderIdentity
}

func (p *testProvider) AuthorizationURL(request AuthorizationRequest) (string, error) {
	p.authorization = request
	query := url.Values{
		"client_id":             {request.ClientID},
		"redirect_uri":          {request.RedirectURI},
		"scope":                 {strings.Join(request.Scopes, " ")},
		"state":                 {request.State},
		"code_challenge":        {request.PKCEChallenge},
		"code_challenge_method": {"S256"},
	}
	return "https://provider.example/authorize?" + query.Encode(), nil
}

func (p *testProvider) Exchange(_ context.Context, request ExchangeRequest) (Grant, error) {
	p.exchange = request
	p.exchangeCalls++
	return p.grant, nil
}

func (p *testProvider) ResolveIdentity(context.Context, Grant) (domain.ProviderIdentity, error) {
	return p.identity, nil
}

type testRegistry struct{ provider Provider }

func (r testRegistry) Provider(provider string) (Provider, bool) {
	return r.provider, provider == "slack"
}

type testReturnURLs struct{}

func (testReturnURLs) Allows(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "app.oryxa.test"
}

func TestRegisterBYOCEnforcesRoleAndCatalogScopes(t *testing.T) {
	t.Parallel()
	service, store, _, authorizer, _ := newTestService()
	request := RegisterBYOCRequest{
		OrganizationID: "org-1",
		ActorID:        "user-admin",
		Provider:       "slack",
		ClientID:       "client-id",
		ClientSecret:   "client-secret",
		Scopes:         []string{"channels:history", "chat:write"},
	}

	authorizer.manage = false
	if _, err := service.RegisterBYOC(context.Background(), request); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin error = %v, want ErrForbidden", err)
	}
	authorizer.manage = true
	outside := request
	outside.Scopes = append(outside.Scopes, "admin")
	if _, err := service.RegisterBYOC(context.Background(), outside); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("outside scope error = %v, want ErrInvalidInput", err)
	}
	profile, err := service.RegisterBYOC(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Mode != domain.OAuthAppModeBYOC || profile.Status != domain.OAuthAppStatusActive {
		t.Fatalf("unexpected profile mode/status: %s/%s", profile.Mode, profile.Status)
	}
	stored := store.profiles["org-1/"+profile.ID]
	encoded, _ := json.Marshal(stored)
	if strings.Contains(string(encoded), "client-secret") {
		t.Fatal("stored profile contains plaintext client secret")
	}
}

func TestRotateAndDisableBYOCStayAdminOnlyAndEmitSafeEvents(t *testing.T) {
	t.Parallel()
	service, store, box, authorizer, _ := newTestService()
	profile, err := service.RegisterBYOC(context.Background(), RegisterBYOCRequest{OrganizationID: "org-1", ActorID: "admin-1", Provider: "slack", ClientID: "old-client", ClientSecret: "old-secret", Scopes: []string{"channels:history", "chat:write"}})
	if err != nil {
		t.Fatal(err)
	}
	authorizer.manage = false
	if _, err := service.RotateBYOC(context.Background(), RotateBYOCRequest{OrganizationID: "org-1", ActorID: "member-1", ProfileID: profile.ID, ClientID: "new-client", ClientSecret: "new-secret"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member rotation = %v", err)
	}
	authorizer.manage = true
	rotated, err := service.RotateBYOC(context.Background(), RotateBYOCRequest{OrganizationID: "org-1", ActorID: "admin-1", ProfileID: profile.ID, ClientID: "new-client", ClientSecret: "new-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.CredentialRevision != 2 || rotated.ClientID != "new-client" {
		t.Fatalf("rotated profile = %+v", rotated)
	}
	plain, err := box.Open(context.Background(), profileBinding("org-1", profile.ID), rotated.EncryptedSecret)
	if err != nil || string(plain) != "new-secret" {
		t.Fatalf("rotated secret/error = %q/%v", plain, err)
	}
	eventJSON, _ := json.Marshal(store.profileEvent)
	if strings.Contains(string(eventJSON), "new-secret") || strings.Contains(string(eventJSON), "new-client") {
		t.Fatalf("rotation event leaked credentials: %s", eventJSON)
	}
	if err := service.DisableOAuthApp(context.Background(), DisableOAuthAppRequest{OrganizationID: "org-1", ActorID: "admin-1", ProfileID: profile.ID}); err != nil {
		t.Fatal(err)
	}
	items, err := service.MemberIntegrations(context.Background(), "org-1", "member-1")
	if err != nil || len(items) != 0 {
		t.Fatalf("catalog after disable = %+v/%v", items, err)
	}
}

func TestOAuthLifecycleUsesHashedSingleUseStatePKCEAndDomainEvent(t *testing.T) {
	t.Parallel()
	service, store, box, _, provider := newTestService()
	profile, err := service.RegisterBYOC(context.Background(), RegisterBYOCRequest{
		OrganizationID: "org-1", ActorID: "admin-1", Provider: "slack",
		ClientID: "client-id", ClientSecret: "client-secret",
		Scopes: []string{"chat:write", "channels:history"},
	})
	if err != nil {
		t.Fatal(err)
	}
	available, err := service.MemberIntegrations(context.Background(), "org-1", "member-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 1 || available[0].State != domain.MemberIntegrationAvailable || available[0].Connection != nil {
		t.Fatalf("member catalog before consent = %+v", available)
	}
	begin, err := service.Begin(context.Background(), BeginRequest{
		OrganizationID: "org-1", MemberID: "member-1", ProfileID: profile.ID,
		ReturnURI: "https://app.oryxa.test/settings/integrations",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(begin.AuthorizationURL)
	rawState := parsed.Query().Get("state")
	if rawState == "" || parsed.Query().Get("code_challenge") == "" {
		t.Fatalf("authorization URL is missing state or PKCE: %s", begin.AuthorizationURL)
	}
	stateHash := sha256.Sum256([]byte(rawState))
	state, ok := store.states[stateHash]
	if !ok {
		t.Fatal("state is not stored by its SHA-256 hash")
	}
	for key := range store.states {
		if strings.Contains(hex.EncodeToString(key[:]), rawState) {
			t.Fatal("raw state leaked into state key")
		}
	}
	verifier, err := box.Open(context.Background(), stateBinding("org-1", stateHash), state.EncryptedVerifier)
	if err != nil {
		t.Fatal(err)
	}
	challengeHash := sha256.Sum256(verifier)
	wantChallenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])
	if got := parsed.Query().Get("code_challenge"); got != wantChallenge {
		t.Fatalf("challenge = %q, want %q", got, wantChallenge)
	}

	completed, err := service.Complete(context.Background(), CompleteRequest{Provider: "slack", State: rawState, Code: "auth-code"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.exchange.ClientSecret != "client-secret" || provider.exchange.PKCEVerifier != string(verifier) {
		t.Fatal("provider exchange did not receive the scoped app secret and PKCE verifier")
	}
	if completed.ReturnURI != "https://app.oryxa.test/settings/integrations" {
		t.Fatalf("return URI = %q", completed.ReturnURI)
	}
	if store.connection == nil || store.event == nil {
		t.Fatal("connection and event were not persisted together")
	}
	connected, err := service.MemberIntegrations(context.Background(), "org-1", "member-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(connected) != 1 || connected[0].State != domain.MemberIntegrationConnected || connected[0].Connection == nil || connected[0].Connection.ID != store.connection.ID {
		t.Fatalf("member catalog after consent = %+v", connected)
	}
	if _, err := service.Begin(context.Background(), BeginRequest{
		OrganizationID: "org-1", MemberID: "member-1", ProfileID: profile.ID,
		ReturnURI: "https://app.oryxa.test/settings/integrations",
	}); !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("second authorization error = %v, want ErrAlreadyConnected", err)
	}
	if store.event.Source != "oryxa.integrations" || store.event.DetailType != "integrations.connection.connected.v1" {
		t.Fatalf("unexpected event identity: %s/%s", store.event.Source, store.event.DetailType)
	}
	safeEvent, _ := json.Marshal(store.event)
	if strings.Contains(string(safeEvent), "access-token") || strings.Contains(string(safeEvent), "client-secret") {
		t.Fatal("domain event contains provider credentials")
	}
	credentialJSON, err := box.Open(context.Background(), connectionBinding("org-1", store.connection.ID), store.connection.EncryptedCredentials)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(credentialJSON), "access-token") {
		t.Fatal("encrypted connection grant cannot be recovered")
	}
	if err := service.Disconnect(context.Background(), DisconnectRequest{OrganizationID: "org-1", MemberID: "member-1", ConnectionID: store.connection.ID}); err != nil {
		t.Fatal(err)
	}
	afterDisconnect, err := service.MemberIntegrations(context.Background(), "org-1", "member-1")
	if err != nil || len(afterDisconnect) != 1 || afterDisconnect[0].State != domain.MemberIntegrationAvailable {
		t.Fatalf("catalog after disconnect = %+v/%v", afterDisconnect, err)
	}
	disconnectedEvent, _ := json.Marshal(store.event)
	if store.event.DetailType != "integrations.connection.disconnected.v1" || strings.Contains(string(disconnectedEvent), "access-token") {
		t.Fatalf("disconnect event = %s", disconnectedEvent)
	}
	if _, err := service.Complete(context.Background(), CompleteRequest{Provider: "slack", State: rawState, Code: "second-code"}); !errors.Is(err, errStateUnavailable) {
		t.Fatalf("second callback error = %v, want consumed state", err)
	}
}

func TestCallbackProviderMismatchConsumesStateBeforeCredentialsAreOpened(t *testing.T) {
	t.Parallel()
	service, _, _, _, provider := newTestService()
	profile, _ := service.RegisterBYOC(context.Background(), RegisterBYOCRequest{
		OrganizationID: "org-1", ActorID: "admin-1", Provider: "slack",
		ClientID: "client-id", ClientSecret: "client-secret",
	})
	begin, _ := service.Begin(context.Background(), BeginRequest{
		OrganizationID: "org-1", MemberID: "member-1", ProfileID: profile.ID,
		ReturnURI: "https://app.oryxa.test/settings/integrations",
	})
	parsed, _ := url.Parse(begin.AuthorizationURL)
	rawState := parsed.Query().Get("state")
	if _, err := service.Complete(context.Background(), CompleteRequest{Provider: "github", State: rawState, Code: "code"}); !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	if provider.exchangeCalls != 0 {
		t.Fatal("provider exchange ran for a mismatched callback")
	}
	if _, err := service.Complete(context.Background(), CompleteRequest{Provider: "slack", State: rawState, Code: "code"}); !errors.Is(err, errStateUnavailable) {
		t.Fatalf("mismatched callback did not consume state: %v", err)
	}
}

func TestCallbackRejectsPartialGrantWithoutSavingConnection(t *testing.T) {
	t.Parallel()
	service, store, _, _, provider := newTestService()
	provider.grant.Scopes = []string{"chat:write"}
	profile, _ := service.RegisterBYOC(context.Background(), RegisterBYOCRequest{
		OrganizationID: "org-1", ActorID: "admin-1", Provider: "slack",
		ClientID: "client-id", ClientSecret: "client-secret",
		Scopes: []string{"chat:write", "channels:history"},
	})
	begin, _ := service.Begin(context.Background(), BeginRequest{
		OrganizationID: "org-1", MemberID: "member-1", ProfileID: profile.ID,
		ReturnURI: "https://app.oryxa.test/settings/integrations",
	})
	parsed, _ := url.Parse(begin.AuthorizationURL)
	_, err := service.Complete(context.Background(), CompleteRequest{
		Provider: "slack", State: parsed.Query().Get("state"), Code: "code",
	})
	if !errors.Is(err, ErrRequiredScope) {
		t.Fatalf("partial grant error = %v, want ErrRequiredScope", err)
	}
	if store.connection != nil || store.event != nil {
		t.Fatal("partial grant created a connection or event")
	}
}

func TestProviderDenialConsumesOAuthStateWithoutExchangingCode(t *testing.T) {
	t.Parallel()
	service, store, _, _, provider := newTestService()
	profile, err := service.RegisterBYOC(context.Background(), RegisterBYOCRequest{OrganizationID: "org-1", ActorID: "admin-1", Provider: "slack", ClientID: "client", ClientSecret: "secret", Scopes: []string{"channels:history", "chat:write"}})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := service.Begin(context.Background(), BeginRequest{OrganizationID: "org-1", MemberID: "member-1", ProfileID: profile.ID, ReturnURI: "https://app.oryxa.test/settings/integrations"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(begin.AuthorizationURL)
	state := parsed.Query().Get("state")
	_, err = service.Complete(context.Background(), CompleteRequest{Provider: "slack", State: state, ProviderError: "access_denied"})
	if !errors.Is(err, ErrProviderDenied) || provider.exchangeCalls != 0 {
		t.Fatalf("denial/error exchanges = %v/%d", err, provider.exchangeCalls)
	}
	if len(store.states) != 0 {
		t.Fatal("denied OAuth state remained reusable")
	}
}

func newTestService() (Service, *memoryStore, *memoryBox, *testAuthorizer, *testProvider) {
	store := newMemoryStore()
	box := newMemoryBox()
	authorizer := &testAuthorizer{manage: true, connect: true}
	provider := &testProvider{
		grant: Grant{
			AccessToken: "access-token", RefreshToken: "refresh-token", TokenType: "bearer",
			Scopes: []string{"channels:history", "chat:write"},
		},
		identity: domain.ProviderIdentity{SubjectID: "U123", TenantID: "T123", TenantName: "Test Workspace"},
	}
	definition := Definition{
		Provider: "slack", PieceName: "@activepieces/piece-slack", PieceVersion: "0.17.10",
		RedirectURI:   "https://connect.oryxa.test/v1/oauth/callback/slack",
		AllowedScopes: []string{"channels:history", "chat:write"}, RequiredScopes: []string{"chat:write"},
		SupportsPKCE: true,
	}
	counters := make(map[string]int)
	return Service{
		Catalog: testCatalog{definition: definition}, Authorizer: authorizer,
		Profiles: store, States: store, Connections: store, Secrets: box,
		Providers: testRegistry{provider: provider}, ReturnURLs: testReturnURLs{},
		Now:    func() time.Time { return time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC) },
		Random: strings.NewReader(strings.Repeat("r", 256)),
		IDs: func(prefix string) (string, error) {
			counters[prefix]++
			return fmt.Sprintf("%s-%d", prefix, counters[prefix]), nil
		},
	}, store, box, authorizer, provider
}

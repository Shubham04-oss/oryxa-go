package application

import (
	"cmp"
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/secrets"
)

// fakeStore keeps what the PostgreSQL store keeps, with the same rules for
// sessions, uniqueness, defaults and versions.
type fakeStore struct {
	// staleHandles makes SessionByHandle answer as if the session had not been
	// claimed yet, like a request that read it just before another claimed it.
	staleHandles bool

	mu          sync.Mutex
	apps        map[string]domain.App
	deletedApps map[string]bool
	sessions    map[string]domain.Session
	connections map[string]domain.Connection
	nextChecks  map[string]time.Time
	events      []domain.Event
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		apps: map[string]domain.App{}, deletedApps: map[string]bool{}, sessions: map[string]domain.Session{},
		connections: map[string]domain.Connection{}, nextChecks: map[string]time.Time{},
	}
}

func (store *fakeStore) CreateApp(_ context.Context, app domain.App, event domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.apps[app.ID]; ok {
		return domain.ErrConflict
	}
	store.apps[app.ID] = app
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) App(_ context.Context, organizationID, appID string) (domain.App, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	app, ok := store.apps[appID]
	if !ok || app.OrganizationID != organizationID || store.deletedApps[appID] {
		return domain.App{}, domain.ErrNotFound
	}
	return app, nil
}

func (store *fakeStore) Apps(_ context.Context, organizationID string) ([]domain.App, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	apps := []domain.App{}
	for _, app := range store.apps {
		if app.OrganizationID == organizationID && !store.deletedApps[app.ID] {
			apps = append(apps, app)
		}
	}
	slices.SortFunc(apps, func(a, b domain.App) int { return cmp.Compare(a.ID, b.ID) })
	return apps, nil
}

func (store *fakeStore) UpdateApp(_ context.Context, app domain.App, expectedRevision int64, reconnect domain.ReconnectReason,
	event domain.Event, connectionEvent func(domain.Connection) (domain.Event, error)) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := store.apps[app.ID]
	if !ok || store.deletedApps[app.ID] || current.OrganizationID != app.OrganizationID || current.Revision != expectedRevision {
		return domain.ErrConflict
	}
	store.apps[app.ID] = app
	if reconnect != "" {
		if err := store.change(app.ID, func(connection *domain.Connection) bool {
			if connection.Status != domain.StatusActive {
				return false
			}
			connection.Status, connection.Reason = domain.StatusNeedsReconnect, string(reconnect)
			connection.StatusChangedAt, connection.UpdatedAt = app.UpdatedAt, app.UpdatedAt
			return true
		}, connectionEvent); err != nil {
			return err
		}
	}
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) DeleteApp(_ context.Context, organizationID, appID string, at time.Time, event domain.Event,
	connectionEvent func(domain.Connection) (domain.Event, error)) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	app, ok := store.apps[appID]
	if !ok || store.deletedApps[appID] || app.OrganizationID != organizationID {
		return domain.ErrNotFound
	}
	app.Status, app.UpdatedAt = domain.AppStatusDisabled, at
	store.apps[appID], store.deletedApps[appID] = app, true
	if err := store.change(appID, func(connection *domain.Connection) bool {
		if connection.Status == domain.StatusDisconnected {
			return false
		}
		connection.Status, connection.Reason, connection.Default = domain.StatusDisconnected, string(domain.DisconnectAppDeleted), false
		connection.StatusChangedAt, connection.UpdatedAt = at, at
		return true
	}, connectionEvent); err != nil {
		return err
	}
	store.events = append(store.events, event)
	return nil
}

// change applies update to each connection of an app, recording an event for
// every one it changed. The caller holds the lock.
func (store *fakeStore) change(appID string, update func(*domain.Connection) bool, connectionEvent func(domain.Connection) (domain.Event, error)) error {
	for id, connection := range store.connections {
		if connection.AppID != appID || !update(&connection) {
			continue
		}
		connection.Version++
		event, err := connectionEvent(connection)
		if err != nil {
			return err
		}
		store.connections[id] = connection
		store.events = append(store.events, event)
	}
	return nil
}

func (store *fakeStore) CreateSession(_ context.Context, session domain.Session) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.sessions[session.StateHash]; ok {
		return domain.ErrConflict
	}
	if session.Usage == "" {
		session.Usage = domain.UsageMembers
	}
	store.sessions[session.StateHash] = session
	return nil
}

func (store *fakeStore) SessionByState(_ context.Context, stateHash string) (domain.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.sessions[stateHash]
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	return session, nil
}

func (store *fakeStore) SessionByHandle(_ context.Context, handleHash string) (domain.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, session := range store.sessions {
		if session.HandleHash != "" && session.HandleHash == handleHash {
			if store.staleHandles {
				session.ClaimedAt = nil
			}
			return session, nil
		}
	}
	return domain.Session{}, domain.ErrNotFound
}

func (store *fakeStore) RecordCallback(_ context.Context, stateHash, handleHash string, result *domain.SealedData, providerError string, at time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.sessions[stateHash]
	if !ok || session.CallbackAt != nil {
		return domain.ErrNotFound
	}
	session.HandleHash, session.EncryptedResult, session.ProviderError, session.CallbackAt = handleHash, result, providerError, &at
	store.sessions[stateHash] = session
	return nil
}

func (store *fakeStore) ClaimSession(_ context.Context, stateHash string, at time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.sessions[stateHash]
	if !ok || session.HandleHash == "" || session.ProviderError != "" || session.ClaimedAt != nil ||
		session.CompletedAt != nil || !at.Before(session.ExpiresAt) {
		return domain.ErrSessionExpired
	}
	session.ClaimedAt = &at
	store.sessions[stateHash] = session
	return nil
}

func (store *fakeStore) BurnSession(_ context.Context, stateHash string, at time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if session, ok := store.sessions[stateHash]; ok && session.CompletedAt == nil {
		session.CompletedAt = &at
		store.sessions[stateHash] = session
	}
	return nil
}

// consumable reports whether a session can be consumed. The caller holds the
// lock.
func (store *fakeStore) consumable(stateHash string) error {
	session, ok := store.sessions[stateHash]
	if !ok || session.ClaimedAt == nil || session.CompletedAt != nil {
		return domain.ErrSessionExpired
	}
	return nil
}

func (store *fakeStore) consume(stateHash string, at time.Time) {
	session := store.sessions[stateHash]
	session.CompletedAt = &at
	store.sessions[stateHash] = session
}

func (store *fakeStore) Connection(_ context.Context, organizationID, connectionID string) (domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	connection, ok := store.connections[connectionID]
	if !ok || connection.OrganizationID != organizationID {
		return domain.Connection{}, domain.ErrNotFound
	}
	return store.listed(connection), nil
}

func (store *fakeStore) Connections(_ context.Context, organizationID string, query domain.ConnectionQuery) ([]domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := []domain.Connection{}
	for _, connection := range store.connections {
		if connection.OrganizationID != organizationID || connection.Status == domain.StatusDisconnected ||
			(query.AppID != "" && connection.AppID != query.AppID) || !matches(query, connection) {
			continue
		}
		result = append(result, store.listed(connection))
	}
	slices.SortFunc(result, func(a, b domain.Connection) int { return cmp.Compare(a.ID, b.ID) })
	return result, nil
}

func matches(query domain.ConnectionQuery, connection domain.Connection) bool {
	ownMember := connection.Scope == domain.ScopeMember && (query.Admin || connection.MemberID == query.MemberID)
	switch query.Scope {
	case domain.ScopeTeam:
		return connection.Scope == domain.ScopeTeam && connection.TeamID == query.TeamID
	case domain.ScopeOrganization:
		return connection.Scope == domain.ScopeOrganization
	case domain.ScopeMember:
		return ownMember
	default:
		return connection.Scope == domain.ScopeOrganization || ownMember
	}
}

// listed adds what a listing shows: health, and whether the app blocks the
// connection. The caller holds the lock.
func (store *fakeStore) listed(connection domain.Connection) domain.Connection {
	connection.Health = domain.ConnectionHealth{State: domain.HealthUnknown}
	if store.apps[connection.AppID].Status != domain.AppStatusActive && connection.Status != domain.StatusDisconnected {
		connection.Blocked = "app_disabled"
	}
	return connection
}

func (store *fakeStore) CompleteConnect(_ context.Context, stateHash string, connection domain.Connection, event domain.Event, nextCheck time.Time) (domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.consumable(stateHash); err != nil {
		return domain.Connection{}, err
	}
	connection.Default = true
	for _, other := range store.connections {
		sameOwner := other.OrganizationID == connection.OrganizationID && other.AppID == connection.AppID &&
			other.Scope == connection.Scope && other.TeamID == connection.TeamID && other.MemberID == connection.MemberID
		if !sameOwner || other.Status == domain.StatusDisconnected {
			continue
		}
		if other.ProviderAccount.ID == connection.ProviderAccount.ID {
			return domain.Connection{}, domain.ErrAlreadyConnected
		}
		if other.Default {
			connection.Default = false
		}
	}
	if connection.Usage == "" {
		connection.Usage = domain.UsageMembers
	}
	connection.Version = 1
	store.connections[connection.ID] = connection
	store.nextChecks[connection.ID] = nextCheck
	store.consume(stateHash, connection.UpdatedAt)
	store.events = append(store.events, event)
	return store.listed(connection), nil
}

func (store *fakeStore) CompleteReconnect(_ context.Context, stateHash string, connection domain.Connection, expectedVersion int64, event domain.Event) (domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.consumable(stateHash); err != nil {
		return domain.Connection{}, err
	}
	current, ok := store.connections[connection.ID]
	if !ok || current.Version != expectedVersion || current.Status == domain.StatusDisconnected {
		return domain.Connection{}, domain.ErrConflict
	}
	current.EncryptedCredentials, current.Scopes = connection.EncryptedCredentials, connection.Scopes
	current.Status, current.Reason, current.StatusChangedAt = domain.StatusActive, "", connection.StatusChangedAt
	current.ProviderAccount.Name, current.UpdatedAt = connection.ProviderAccount.Name, connection.UpdatedAt
	current.Version++
	store.connections[connection.ID] = current
	store.consume(stateHash, connection.UpdatedAt)
	store.events = append(store.events, event)
	return store.listed(current), nil
}

func (store *fakeStore) DisconnectConnection(_ context.Context, organizationID, connectionID string, reason domain.DisconnectReason,
	at time.Time, connectionEvent func(domain.Connection) (domain.Event, error)) (bool, error) {
	if !reason.Valid() {
		return false, domain.ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	connection, ok := store.connections[connectionID]
	if !ok || connection.OrganizationID != organizationID || connection.Status == domain.StatusDisconnected {
		return false, nil
	}
	connection.Status, connection.Reason, connection.Default = domain.StatusDisconnected, string(reason), false
	connection.StatusChangedAt, connection.UpdatedAt = at, at
	connection.Version++
	event, err := connectionEvent(connection)
	if err != nil {
		return false, err
	}
	store.connections[connectionID] = connection
	store.events = append(store.events, event)
	return true, nil
}

func (store *fakeStore) session(stateHash string) domain.Session {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.sessions[stateHash]
}

func (store *fakeStore) connection(id string) domain.Connection {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.connections[id]
}

// eventsOf returns the recorded events of one detail type.
func (store *fakeStore) eventsOf(detailType string) []domain.Event {
	store.mu.Lock()
	defer store.mu.Unlock()
	var result []domain.Event
	for _, event := range store.events {
		if event.DetailType == detailType {
			result = append(result, event)
		}
	}
	return result
}

// fakeDirectory answers for a caller's token in each organization it belongs
// to.
type fakeDirectory map[string][]domain.OrgMembership

func (directory fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	memberships, ok := directory[credentials.Authorization]
	if !ok {
		return domain.OrgMembership{}, domain.ErrUnauthorized
	}
	for _, membership := range memberships {
		if membership.OrganizationID == organizationID {
			return membership, nil
		}
	}
	return domain.OrgMembership{}, domain.ErrNotFound
}

// fakeTeams answers for a caller's token and a team ID. Anything else is a
// team the caller cannot see.
type fakeTeams map[string]domain.TeamAccess

func (teams fakeTeams) Team(_ context.Context, _ string, teamID string, credentials authclient.Credentials) (domain.TeamAccess, error) {
	access, ok := teams[credentials.Authorization+"/"+teamID]
	if !ok {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	return access, nil
}

// fakeSealer leaves plaintext readable, but opens a value only with the
// binding it was sealed under, so a test fails when a secret is opened as
// the wrong resource.
type fakeSealer struct{}

func (fakeSealer) Seal(_ context.Context, binding secrets.Binding, plaintext []byte) (domain.SealedData, error) {
	bound, err := json.Marshal(binding.Context())
	if err != nil {
		return domain.SealedData{}, err
	}
	return domain.SealedData{Version: 1, Nonce: []byte("nonce"), Ciphertext: slices.Clone(plaintext), EncryptedDataKey: bound}, nil
}

func (fakeSealer) Open(_ context.Context, binding secrets.Binding, sealed domain.SealedData) ([]byte, error) {
	bound, err := json.Marshal(binding.Context())
	if err != nil {
		return nil, err
	}
	if string(bound) != string(sealed.EncryptedDataKey) {
		return nil, secrets.ErrInvalidEnvelope
	}
	return slices.Clone(sealed.Ciphertext), nil
}

type fakeOAuth struct {
	mu             sync.Mutex
	authorizations []providers.AuthorizationRequest
	exchanges      []providers.ExchangeRequest
	grant          providers.Grant
	account        domain.ProviderAccount
	exchangeErr    error
	identityErr    error
	// onExchange runs inside Exchange, before it answers.
	onExchange func()

	refreshes    []providers.RefreshRequest
	refreshGrant providers.Grant
	refreshErr   error
	// refreshDelay holds each refresh open, so concurrent callers overlap.
	refreshDelay time.Duration

	keyChecks  []domain.CredentialInput
	keyAPIURLs []string
	keyAccount domain.ProviderAccount
	keyErr     error

	mints     []providers.MintRequest
	mintGrant providers.Grant
	mintErr   error

	userInstallations   []string
	installationAccount domain.ProviderAccount
	installationErr     error
	installationTokens  int
	installationToken   providers.Grant
	installationTokErr  error

	revocations []providers.RevokeRequest
	revokeErr   error

	identities int
}

func (oauth *fakeOAuth) AuthorizationURL(provider providers.OAuth2, request providers.AuthorizationRequest) (string, error) {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	oauth.authorizations = append(oauth.authorizations, request)
	return provider.AuthorizationURL + "?state=" + url.QueryEscape(request.State), nil
}

func (oauth *fakeOAuth) Exchange(_ context.Context, _ providers.OAuth2, request providers.ExchangeRequest) (providers.Grant, error) {
	oauth.mu.Lock()
	oauth.exchanges = append(oauth.exchanges, request)
	hook, grant, err := oauth.onExchange, oauth.grant, oauth.exchangeErr
	oauth.mu.Unlock()
	if hook != nil {
		hook()
	}
	return grant, err
}

func (oauth *fakeOAuth) Identity(context.Context, providers.OAuth2, providers.Grant) (domain.ProviderAccount, error) {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	oauth.identities++
	return oauth.account, oauth.identityErr
}

func (oauth *fakeOAuth) set(change func(*fakeOAuth)) {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	change(oauth)
}

func (oauth *fakeOAuth) lastAuthorization() providers.AuthorizationRequest {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	return oauth.authorizations[len(oauth.authorizations)-1]
}

func (oauth *fakeOAuth) exchangeRequests() []providers.ExchangeRequest {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	return slices.Clone(oauth.exchanges)
}

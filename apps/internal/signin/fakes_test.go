package signin

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// memStore is Store in memory, keeping the rules apps' Postgres store
// keeps. Its own tests in apps/internal/postgres hold that store to the same
// rules.
type memStore struct {
	mu          sync.Mutex
	connections map[string]*memConnection
	flows       map[string]*memFlow
	credentials map[string]*memCredential
	clients     map[string]OAuthClient

	// crashOnSave makes SaveRefreshedCredential fail as a process dying
	// after the app answered would: nothing saved, and the lease never
	// released.
	crashOnSave bool
	crashed     map[string]bool
	// failures makes a method fail with an error, by name.
	failures map[string]error
}

type memConnection struct {
	organizationID, memberID, toolkit, status string
}

type memFlow struct {
	flow     Flow
	consumed bool
}

type memCredential struct {
	credential Credential
	leaseToken string
	leaseUntil time.Time
}

func newMemStore() *memStore {
	return &memStore{
		connections: map[string]*memConnection{}, flows: map[string]*memFlow{},
		credentials: map[string]*memCredential{}, clients: map[string]OAuthClient{},
		crashed: map[string]bool{}, failures: map[string]error{},
	}
}

func (m *memStore) addConnection(id, organizationID, memberID, toolkit, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connections[id] = &memConnection{organizationID: organizationID, memberID: memberID, toolkit: toolkit, status: status}
}

func (m *memStore) setStatus(id, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connections[id].status = status
}

func (m *memStore) fail(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures[method] = err
}

// stored answers a copy of a connection's stored credential, and whether
// it has one.
func (m *memStore) stored(connectionID string) (Credential, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.credentials[connectionID]
	if !ok {
		return Credential{}, false
	}
	return m.withConnection(held.credential), true
}

func (m *memStore) lease(connectionID string) (string, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held := m.credentials[connectionID]
	return held.leaseToken, held.leaseUntil
}

func (m *memStore) setNextCheck(connectionID string, next time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.credentials[connectionID].credential.NextCheckAt = next
}

func (m *memStore) flow(stateHash string) (Flow, bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.flows[stateHash]
	if !ok {
		return Flow{}, false, false
	}
	return copyFlow(held.flow), true, held.consumed
}

func (m *memStore) withConnection(credential Credential) Credential {
	credential = copyCredential(credential)
	if connection, ok := m.connections[credential.ConnectionID]; ok {
		credential.OrganizationID, credential.MemberID = connection.organizationID, connection.memberID
		credential.Toolkit, credential.Status = connection.toolkit, connection.status
	}
	return credential
}

func (m *memStore) InsertSigninFlow(_ context.Context, flow Flow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failures["InsertSigninFlow"]; err != nil {
		return err
	}
	if _, ok := m.connections[flow.ConnectionID]; !ok {
		return domain.ErrNotFound
	}
	if _, ok := m.clients[flow.OAuthClientID]; !ok {
		return domain.ErrNotFound
	}
	if _, taken := m.flows[flow.StateHash]; taken {
		return domain.ErrConflict
	}
	m.flows[flow.StateHash] = &memFlow{flow: copyFlow(flow)}
	return nil
}

func (m *memStore) ConsumeSigninFlow(_ context.Context, stateHash string, now time.Time) (Flow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.flows[stateHash]
	if !ok || held.consumed || !held.flow.ExpiresAt.After(now) {
		return Flow{}, ErrFlowNotFound
	}
	held.consumed = true
	return copyFlow(held.flow), nil
}

func (m *memStore) Credential(_ context.Context, connectionID string) (Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failures["Credential"]; err != nil {
		return Credential{}, err
	}
	held, ok := m.credentials[connectionID]
	if !ok {
		return Credential{}, ErrNoCredential
	}
	return m.withConnection(held.credential), nil
}

func (m *memStore) InsertCredential(_ context.Context, credential Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	connection, ok := m.connections[credential.ConnectionID]
	if !ok || connection.organizationID != credential.OrganizationID || connection.status != "pending" {
		return domain.ErrConflict
	}
	if _, held := m.credentials[credential.ConnectionID]; held {
		return domain.ErrConflict
	}
	credential = copyCredential(credential)
	credential.Version, credential.CheckFailures = 1, 0
	m.credentials[credential.ConnectionID] = &memCredential{credential: credential}
	return nil
}

func (m *memStore) ReplaceCredential(_ context.Context, credential Credential) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	connection, ok := m.connections[credential.ConnectionID]
	if !ok || connection.organizationID != credential.OrganizationID ||
		(connection.status != "pending" && connection.status != "active") {
		return 0, domain.ErrConflict
	}
	credential = copyCredential(credential)
	credential.CheckFailures = 0
	held, exists := m.credentials[credential.ConnectionID]
	if !exists {
		credential.Version = 1
		m.credentials[credential.ConnectionID] = &memCredential{credential: credential}
		return 1, nil
	}
	if held.credential.Kind != credential.Kind {
		return 0, domain.ErrConflict
	}
	credential.Version = held.credential.Version + 1
	*held = memCredential{credential: credential}
	return credential.Version, nil
}

func (m *memStore) AcquireCredentialLease(_ context.Context, connectionID string, version int64, token string, now, until time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.credentials[connectionID]
	if !ok || held.credential.Version != version || (held.leaseToken != "" && held.leaseUntil.After(now)) {
		return false, nil
	}
	held.leaseToken, held.leaseUntil = token, until
	return true, nil
}

func (m *memStore) SaveRefreshedCredential(_ context.Context, connectionID, token string, version int64, refreshed Refreshed) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.crashOnSave {
		m.crashed[token] = true
		return 0, errors.New("the process died before it saved")
	}
	held, ok := m.credentials[connectionID]
	if !ok || held.leaseToken != token || held.credential.Version != version {
		return 0, domain.ErrConflict
	}
	next := held.credential
	next.Sealed, next.Settings = maps.Clone(refreshed.Sealed), maps.Clone(refreshed.Settings)
	next.Scopes, next.ExpiresAt = slices.Clone(refreshed.Scopes), copyTime(refreshed.ExpiresAt)
	next.UpdatedAt, next.Version = refreshed.UpdatedAt, held.credential.Version+1
	*held = memCredential{credential: next}
	return next.Version, nil
}

func (m *memStore) ReleaseCredentialLease(_ context.Context, connectionID, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.crashed[token] {
		// A process that died releases nothing.
		return nil
	}
	if held, ok := m.credentials[connectionID]; ok && held.leaseToken == token {
		held.leaseToken, held.leaseUntil = "", time.Time{}
	}
	return nil
}

func (m *memStore) DeleteCredential(_ context.Context, connectionID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.credentials[connectionID]
	delete(m.credentials, connectionID)
	return ok, nil
}

func (m *memStore) MarkCredentialUsed(_ context.Context, connectionID string, notBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.credentials[connectionID]
	if !ok || m.connections[connectionID].status != "active" {
		return nil
	}
	if notBefore.After(held.credential.NextCheckAt) {
		held.credential.NextCheckAt = notBefore
	}
	held.credential.CheckFailures = 0
	return nil
}

func (m *memStore) ClaimCredentialChecks(_ context.Context, now, claimUntil time.Time, limit int) ([]Credential, error) {
	return m.claim(now, claimUntil, limit, "active")
}

func (m *memStore) ClaimRevocations(_ context.Context, now, claimUntil time.Time, limit int) ([]Credential, error) {
	return m.claim(now, claimUntil, limit, "removed", "failed")
}

func (m *memStore) claim(now, claimUntil time.Time, limit int, statuses ...string) ([]Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []*memCredential
	for id, held := range m.credentials {
		if slices.Contains(statuses, m.connections[id].status) && !held.credential.NextCheckAt.After(now) {
			due = append(due, held)
		}
	}
	slices.SortFunc(due, func(a, b *memCredential) int {
		return cmp.Or(a.credential.NextCheckAt.Compare(b.credential.NextCheckAt),
			cmp.Compare(a.credential.ConnectionID, b.credential.ConnectionID))
	})
	claimed := []Credential{}
	for _, held := range due[:min(limit, len(due))] {
		held.credential.NextCheckAt = claimUntil
		claimed = append(claimed, m.withConnection(held.credential))
	}
	return claimed, nil
}

func (m *memStore) RecordCredentialCheck(_ context.Context, connectionID string, next time.Time, failures int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if held, ok := m.credentials[connectionID]; ok {
		held.credential.NextCheckAt, held.credential.CheckFailures = next, failures
	}
	return nil
}

func (m *memStore) GrantShared(_ context.Context, connectionID, clientID, label string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, held := range m.credentials {
		status := m.connections[id].status
		if id == connectionID || (status != "pending" && status != "active") {
			continue
		}
		client, ok := m.clients[held.credential.OAuthClientID]
		if ok && client.ClientID == clientID && held.credential.Settings["account.label"] == label {
			return true, nil
		}
	}
	return false, nil
}

func (m *memStore) LiveOAuthClient(_ context.Context, organizationID, client string) (OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.clients {
		if row.OrganizationID == organizationID && row.Client == client && row.Status == ClientActive {
			return copyClient(row), nil
		}
	}
	return OAuthClient{}, ErrNoClient
}

func (m *memStore) OAuthClient(_ context.Context, id string) (OAuthClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.clients[id]
	if !ok {
		return OAuthClient{}, ErrNoClient
	}
	return copyClient(row), nil
}

func (m *memStore) InsertOAuthClient(_ context.Context, client OAuthClient) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, row := range m.clients {
		if row.OrganizationID == client.OrganizationID && row.Client == client.Client && row.Status == ClientActive {
			row.Status, row.UpdatedAt = ClientRemoved, client.UpdatedAt
			m.clients[id] = row
		}
	}
	client = copyClient(client)
	client.Status = ClientActive
	m.clients[client.ID] = client
	return nil
}

func (m *memStore) UpdateOAuthClient(_ context.Context, client OAuthClient) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.clients[client.ID]
	if !ok || row.Status != ClientActive {
		return false, nil
	}
	row.SealedSecret, row.Settings = copySealed(client.SealedSecret), maps.Clone(client.Settings)
	row.SealedFields, row.UpdatedAt = maps.Clone(client.SealedFields), client.UpdatedAt
	m.clients[client.ID] = row
	return true, nil
}

func (m *memStore) RemoveOAuthClient(_ context.Context, organizationID, client string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, row := range m.clients {
		if row.OrganizationID == organizationID && row.Client == client && row.Status == ClientActive {
			row.Status, row.UpdatedAt = ClientRemoved, now
			m.clients[id] = row
			return true, nil
		}
	}
	return false, nil
}

func copyFlow(flow Flow) Flow {
	flow.Settings, flow.Sealed = maps.Clone(flow.Settings), maps.Clone(flow.Sealed)
	return flow
}

func copyCredential(credential Credential) Credential {
	credential.Sealed, credential.Settings = maps.Clone(credential.Sealed), maps.Clone(credential.Settings)
	credential.Scopes, credential.ExpiresAt = slices.Clone(credential.Scopes), copyTime(credential.ExpiresAt)
	return credential
}

func copyClient(client OAuthClient) OAuthClient {
	client.SealedSecret = copySealed(client.SealedSecret)
	client.Settings, client.SealedFields = maps.Clone(client.Settings), maps.Clone(client.SealedFields)
	return client
}

func copySealed(sealed *secrets.SealedData) *secrets.SealedData {
	if sealed == nil {
		return nil
	}
	copied := *sealed
	return &copied
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// fakeKeys stands in for KMS: a wrapped key unwraps only with exactly the
// encryption context it was generated for. It counts what it does, so tests
// can see when a value was opened.
type fakeKeys struct {
	mu        sync.Mutex
	generated int
	decrypted int
}

type wrappedKey struct {
	Key     []byte            `json:"key"`
	Context map[string]string `json:"context"`
}

func (k *fakeKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, nil, err
	}
	wrapped, err := json.Marshal(wrappedKey{Key: key, Context: encryptionContext})
	if err != nil {
		return nil, nil, err
	}
	k.mu.Lock()
	k.generated++
	k.mu.Unlock()
	return key, wrapped, nil
}

func (k *fakeKeys) DecryptDataKey(_ context.Context, wrapped []byte, encryptionContext map[string]string) ([]byte, error) {
	var unwrapped wrappedKey
	if err := json.Unmarshal(wrapped, &unwrapped); err != nil {
		return nil, err
	}
	if !maps.Equal(unwrapped.Context, encryptionContext) {
		return nil, fmt.Errorf("kms: the encryption context does not match")
	}
	k.mu.Lock()
	k.decrypted++
	k.mu.Unlock()
	return unwrapped.Key, nil
}

func (k *fakeKeys) decrypts() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.decrypted
}

// testClock is a clock tests move by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

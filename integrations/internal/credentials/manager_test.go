package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

func TestManagerRefreshesExpiredGrantUnderLease(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	store := &refreshStore{acquire: true, profile: domain.OAuthAppProfile{ID: "app-1", OrganizationID: "org-1",
		Provider: "google", ClientID: "client", EncryptedSecret: domain.SealedData{Ciphertext: []byte("client-secret")}, Status: domain.OAuthAppStatusActive}}
	provider := &refreshProvider{grant: oauthflow.Grant{AccessToken: "new-access", Expiry: now.Add(time.Hour)}}
	manager := Manager{Store: store, Secrets: plaintextBox{}, Providers: oauthflow.StaticProviders{"google": provider},
		Now: func() time.Time { return now }, IDs: func(string) (string, error) { return "lease-1", nil }}
	connection := refreshConnection(now.Add(-time.Minute))

	value, err := manager.ForExecution(context.Background(), connection)
	if err != nil {
		t.Fatal(err)
	}
	var grant oauthflow.Grant
	if err := json.Unmarshal(value, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.AccessToken != "new-access" || grant.RefreshToken != "refresh-1" || len(grant.Scopes) != 1 || store.updated == nil || store.released != 0 || provider.calls != 1 {
		t.Fatalf("grant/store/provider = %+v/%+v/%d", grant, store.updated, provider.calls)
	}
}

func TestManagerDoesNotRefreshFreshOrConcurrentGrant(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	provider := &refreshProvider{}
	store := &refreshStore{acquire: false, fresh: refreshConnection(now.Add(-time.Minute))}
	manager := Manager{Store: store, Secrets: plaintextBox{}, Providers: oauthflow.StaticProviders{"google": provider},
		Now: func() time.Time { return now }, IDs: func(string) (string, error) { return "lease-1", nil }}

	value, err := manager.ForExecution(context.Background(), refreshConnection(now.Add(time.Hour)))
	if err != nil || len(value) == 0 || store.acquireCalls != 0 {
		t.Fatalf("fresh = %s/%v/%d", value, err, store.acquireCalls)
	}
	if _, err := manager.ForExecution(context.Background(), refreshConnection(now.Add(-time.Minute))); !errors.Is(err, ErrRefreshInProgress) {
		t.Fatalf("concurrent error = %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
}

func TestManagerMarksPermanentRefreshFailureForReauthorization(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	store := &refreshStore{acquire: true, profile: domain.OAuthAppProfile{ID: "app-1", OrganizationID: "org-1",
		Provider: "google", ClientID: "client", EncryptedSecret: domain.SealedData{Ciphertext: []byte("client-secret")}, Status: domain.OAuthAppStatusActive}}
	provider := &refreshProvider{err: oauthflow.RefreshError{Code: "invalid_grant", Permanent: true}}
	manager := Manager{Store: store, Secrets: plaintextBox{}, Providers: oauthflow.StaticProviders{"google": provider},
		Now: func() time.Time { return now }, IDs: func(prefix string) (string, error) { return prefix + "-1", nil }}

	if _, err := manager.ForExecution(context.Background(), refreshConnection(now.Add(-time.Minute))); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("error = %v", err)
	}
	if store.marked.DetailType == "" || store.marked.Data == nil {
		t.Fatalf("reauthorization event = %+v", store.marked)
	}
}

func refreshConnection(expiry time.Time) domain.Connection {
	grant, _ := json.Marshal(oauthflow.Grant{AccessToken: "old-access", RefreshToken: "refresh-1",
		Expiry: expiry, Scopes: []string{"scope"}, Data: map[string]any{"tenant": "T1"}})
	return domain.Connection{ID: "connection-1", OrganizationID: "org-1", MemberID: "member-1",
		ProfileID: "app-1", Provider: "google", GrantedScopes: []string{"scope"},
		EncryptedCredentials: domain.SealedData{Ciphertext: grant}, Status: domain.ConnectionStatusActive, Version: 1}
}

type plaintextBox struct{}

func (plaintextBox) Open(_ context.Context, _ secrets.Binding, sealed domain.SealedData) ([]byte, error) {
	return append([]byte(nil), sealed.Ciphertext...), nil
}
func (plaintextBox) Seal(_ context.Context, _ secrets.Binding, value []byte) (domain.SealedData, error) {
	return domain.SealedData{Ciphertext: append([]byte(nil), value...)}, nil
}

type refreshStore struct {
	acquire      bool
	acquireCalls int
	profile      domain.OAuthAppProfile
	fresh        domain.Connection
	updated      *domain.SealedData
	released     int
	marked       domain.Event
}

func (s *refreshStore) OAuthApp(context.Context, string, string) (domain.OAuthAppProfile, error) {
	return s.profile, nil
}
func (s *refreshStore) ConnectionForExecution(context.Context, string, string) (domain.Connection, error) {
	return s.fresh, nil
}
func (s *refreshStore) AcquireConnectionRefreshLease(context.Context, string, string, int64, string, time.Time, time.Time) (bool, error) {
	s.acquireCalls++
	return s.acquire, nil
}
func (s *refreshStore) ReleaseConnectionRefreshLease(context.Context, string, string, string) error {
	s.released++
	return nil
}
func (s *refreshStore) UpdateConnectionCredentials(_ context.Context, _, _, _ string, _ int64, sealed domain.SealedData, _ []string, _ time.Time) error {
	s.updated = &sealed
	return nil
}
func (s *refreshStore) MarkConnectionReauthorizationWithEvent(_ context.Context, _, _, _ string, _ int64, _ time.Time, event domain.Event) error {
	s.marked = event
	return nil
}

type refreshProvider struct {
	grant oauthflow.Grant
	err   error
	calls int
}

func (*refreshProvider) AuthorizationURL(oauthflow.AuthorizationRequest) (string, error) {
	return "", nil
}
func (*refreshProvider) Exchange(context.Context, oauthflow.ExchangeRequest) (oauthflow.Grant, error) {
	return oauthflow.Grant{}, nil
}
func (*refreshProvider) ResolveIdentity(context.Context, oauthflow.Grant) (domain.ProviderIdentity, error) {
	return domain.ProviderIdentity{}, nil
}
func (p *refreshProvider) Refresh(context.Context, oauthflow.RefreshRequest) (oauthflow.Grant, error) {
	p.calls++
	return p.grant, p.err
}

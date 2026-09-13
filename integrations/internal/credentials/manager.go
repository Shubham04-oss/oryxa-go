package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

var (
	ErrRefreshInProgress       = errors.New("connection credential refresh is in progress")
	ErrReauthorizationRequired = errors.New("connection requires reauthorization")
	ErrCredentialRefreshFailed = errors.New("connection credential refresh failed")
)

type RefreshStore interface {
	OAuthApp(context.Context, string, string) (domain.OAuthAppProfile, error)
	ConnectionForExecution(context.Context, string, string) (domain.Connection, error)
	AcquireConnectionRefreshLease(context.Context, string, string, int64, string, time.Time, time.Time) (bool, error)
	ReleaseConnectionRefreshLease(context.Context, string, string, string) error
	UpdateConnectionCredentials(context.Context, string, string, string, int64, domain.SealedData, []string, time.Time) error
	MarkConnectionReauthorizationWithEvent(context.Context, string, string, string, int64, time.Time, domain.Event) error
}

type RefreshSecretBox interface {
	Open(context.Context, secrets.Binding, domain.SealedData) ([]byte, error)
	Seal(context.Context, secrets.Binding, []byte) (domain.SealedData, error)
}

type Manager struct {
	Store     RefreshStore
	Secrets   RefreshSecretBox
	Providers oauthflow.ProviderRegistry
	Now       func() time.Time
	IDs       func(string) (string, error)
	Skew      time.Duration
	Lease     time.Duration
}

func (m Manager) ForExecution(ctx context.Context, connection domain.Connection) ([]byte, error) {
	return m.forExecution(ctx, connection, true)
}

func (m Manager) forExecution(ctx context.Context, connection domain.Connection, allowReload bool) ([]byte, error) {
	plaintext, grant, err := m.openGrant(ctx, connection)
	if err != nil {
		return nil, err
	}
	skew := m.Skew
	if skew == 0 {
		skew = 2 * time.Minute
	}
	if grant.Expiry.IsZero() || m.now().Add(skew).Before(grant.Expiry) {
		return plaintext, nil
	}
	wipe(plaintext)
	leaseToken, err := m.newID("lease")
	if err != nil {
		return nil, err
	}
	lease := m.Lease
	if lease == 0 {
		lease = 30 * time.Second
	}
	now := m.now()
	acquired, err := m.Store.AcquireConnectionRefreshLease(ctx, connection.OrganizationID,
		connection.ID, connection.Version, leaseToken, now, now.Add(lease))
	if err != nil {
		return nil, fmt.Errorf("%w: acquire lease", ErrCredentialRefreshFailed)
	}
	if !acquired {
		if allowReload {
			fresh, reloadErr := m.Store.ConnectionForExecution(ctx, connection.OrganizationID, connection.ID)
			if reloadErr == nil && fresh.Version != connection.Version {
				return m.forExecution(ctx, fresh, false)
			}
		}
		return nil, ErrRefreshInProgress
	}
	release := true
	defer func() {
		if release {
			_ = m.Store.ReleaseConnectionRefreshLease(context.WithoutCancel(ctx), connection.OrganizationID, connection.ID, leaseToken)
		}
	}()
	if grant.RefreshToken == "" {
		return nil, m.reauthorization(ctx, connection, leaseToken, "refresh_token_missing")
	}
	profile, err := m.Store.OAuthApp(ctx, connection.OrganizationID, connection.ProfileID)
	if err != nil || profile.Status != domain.OAuthAppStatusActive || profile.Provider != connection.Provider {
		return nil, m.reauthorization(ctx, connection, leaseToken, "oauth_app_unavailable")
	}
	provider, ok := m.Providers.Provider(connection.Provider)
	if !ok {
		return nil, m.reauthorization(ctx, connection, leaseToken, "provider_unavailable")
	}
	refresher, ok := provider.(oauthflow.Refresher)
	if !ok {
		return nil, m.reauthorization(ctx, connection, leaseToken, "refresh_not_supported")
	}
	clientSecret, err := m.Secrets.Open(ctx, secrets.Binding{OrganizationID: profile.OrganizationID,
		ResourceType: "oauth-app", ResourceID: profile.ID}, profile.EncryptedSecret)
	if err != nil {
		return nil, fmt.Errorf("%w: open OAuth application", ErrCredentialRefreshFailed)
	}
	defer wipe(clientSecret)
	refreshed, err := refresher.Refresh(ctx, oauthflow.RefreshRequest{
		ClientID: profile.ClientID, ClientSecret: string(clientSecret),
		RefreshToken: grant.RefreshToken, Scopes: append([]string(nil), connection.GrantedScopes...),
	})
	if err != nil {
		var refreshError oauthflow.RefreshError
		if errors.As(err, &refreshError) && refreshError.Permanent {
			return nil, m.reauthorization(ctx, connection, leaseToken, safeReason(refreshError.Code))
		}
		return nil, ErrCredentialRefreshFailed
	}
	if refreshed.AccessToken == "" {
		return nil, m.reauthorization(ctx, connection, leaseToken, "access_token_missing")
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = grant.RefreshToken
	}
	if refreshed.TokenType == "" {
		refreshed.TokenType = grant.TokenType
	}
	if len(refreshed.Scopes) == 0 {
		refreshed.Scopes = append([]string(nil), connection.GrantedScopes...)
	}
	if len(missingScopes(connection.GrantedScopes, refreshed.Scopes)) > 0 {
		return nil, m.reauthorization(ctx, connection, leaseToken, "scope_reduced")
	}
	if refreshed.Data == nil {
		refreshed.Data = grant.Data
	}
	encoded, err := json.Marshal(refreshed)
	if err != nil {
		return nil, fmt.Errorf("%w: encode refreshed grant", ErrCredentialRefreshFailed)
	}
	sealed, err := m.Secrets.Seal(ctx, secrets.Binding{OrganizationID: connection.OrganizationID,
		ResourceType: "connection", ResourceID: connection.ID}, encoded)
	if err != nil {
		wipe(encoded)
		return nil, fmt.Errorf("%w: seal refreshed grant", ErrCredentialRefreshFailed)
	}
	if err := m.Store.UpdateConnectionCredentials(ctx, connection.OrganizationID, connection.ID,
		leaseToken, connection.Version, sealed, refreshed.Scopes, m.now()); err != nil {
		wipe(encoded)
		return nil, fmt.Errorf("%w: persist refreshed grant", ErrCredentialRefreshFailed)
	}
	release = false
	return encoded, nil
}

func (m Manager) openGrant(ctx context.Context, connection domain.Connection) ([]byte, oauthflow.Grant, error) {
	if m.Store == nil || m.Secrets == nil || m.Providers == nil || connection.ID == "" || connection.OrganizationID == "" {
		return nil, oauthflow.Grant{}, errors.New("credential manager is not configured")
	}
	plaintext, err := m.Secrets.Open(ctx, secrets.Binding{OrganizationID: connection.OrganizationID,
		ResourceType: "connection", ResourceID: connection.ID}, connection.EncryptedCredentials)
	if err != nil {
		return nil, oauthflow.Grant{}, fmt.Errorf("open connection credentials: %w", err)
	}
	var grant oauthflow.Grant
	if err := json.Unmarshal(plaintext, &grant); err != nil || grant.AccessToken == "" {
		wipe(plaintext)
		return nil, oauthflow.Grant{}, errors.New("connection credential payload is invalid")
	}
	return plaintext, grant, nil
}

func (m Manager) reauthorization(ctx context.Context, connection domain.Connection, leaseToken, reason string) error {
	eventID, err := m.newID("evt")
	if err != nil {
		return errors.Join(ErrReauthorizationRequired, err)
	}
	now := m.now()
	event := domain.Event{EventID: eventID, Source: integrationevents.Source,
		DetailType: integrationevents.ConnectionReauthorizationRequiredDetailType,
		Version:    1, OrganizationID: connection.OrganizationID, OccurredAt: now,
		Data: integrationevents.ConnectionReauthorizationRequired{ConnectionID: connection.ID,
			Provider: connection.Provider, Reason: safeReason(reason), RequiredAt: now}}
	if err := m.Store.MarkConnectionReauthorizationWithEvent(ctx, connection.OrganizationID,
		connection.ID, leaseToken, connection.Version, now, event); err != nil {
		return errors.Join(ErrReauthorizationRequired, err)
	}
	return ErrReauthorizationRequired
}

func missingScopes(required, actual []string) []string {
	set := make(map[string]struct{}, len(actual))
	for _, scope := range actual {
		set[scope] = struct{}{}
	}
	var missing []string
	for _, scope := range required {
		if _, ok := set[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	slices.Sort(missing)
	return missing
}

func safeReason(value string) string {
	if value == "" || len(value) > 80 {
		return "provider_rejected"
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && !strings.ContainsRune("_-", character) {
			return "provider_rejected"
		}
	}
	return value
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m Manager) newID(prefix string) (string, error) {
	if m.IDs == nil {
		return "", errors.New("ID generator is required")
	}
	return m.IDs(prefix)
}

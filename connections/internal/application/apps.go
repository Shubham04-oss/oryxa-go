package application

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
)

var (
	githubAppIDPattern   = regexp.MustCompile(`^[0-9]{1,20}$`)
	githubAppSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)
)

type CreateAppInput struct {
	Provider string
	// CredentialType defaults to the provider's first supported type.
	CredentialType domain.CredentialType
	ClientID       string
	ClientSecret   string
	Scopes         []string
	// GitHubAppID, GitHubAppSlug and PrivateKey name an installation app's
	// GitHub App. Its ClientID and ClientSecret authorize the installing user.
	GitHubAppID   string
	GitHubAppSlug string
	PrivateKey    string
}

// UpdateAppInput changes an app. Nil Scopes and empty strings each leave that
// part alone. A new ClientID needs its secret.
type UpdateAppInput struct {
	Scopes       []string
	ClientID     string
	ClientSecret string
	// PrivateKey rotates an installation app's GitHub App private key.
	PrivateKey string
}

// AppList is an organization's apps and whether the caller administers them.
type AppList struct {
	Apps  []domain.App
	Admin bool
}

func (s *Service) ListApps(ctx context.Context, credentials authclient.Credentials, organizationID string) (AppList, error) {
	membership, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return AppList{}, err
	}
	apps, err := s.store.Apps(ctx, organizationID)
	if err != nil {
		return AppList{}, err
	}
	return AppList{Apps: apps, Admin: membership.Role.IsOrgAdmin()}, nil
}

func (s *Service) CreateApp(ctx context.Context, credentials authclient.Credentials, organizationID string, input CreateAppInput) (domain.App, error) {
	membership, err := s.admin(ctx, credentials, organizationID)
	if err != nil {
		return domain.App{}, err
	}
	provider, ok := s.providers.Lookup(input.Provider)
	if !ok {
		return domain.App{}, fmt.Errorf("provider is not available: %w", domain.ErrInvalid)
	}
	credentialType := input.CredentialType
	if credentialType == "" && len(provider.CredentialTypes) > 0 {
		credentialType = provider.CredentialTypes[0]
	}
	if !provider.Supports(credentialType) {
		return domain.App{}, fmt.Errorf("%s does not support %q credentials: %w", provider.Name, credentialType, domain.ErrInvalid)
	}
	rules := provider.ScopesFor(credentialType)
	scopes, err := domain.AppScopes(input.Scopes, rules.Ceiling, rules.Required)
	if err != nil {
		return domain.App{}, err
	}
	id, err := s.newID("app")
	if err != nil {
		return domain.App{}, err
	}
	now := s.clock()
	app := domain.App{
		ID: id, OrganizationID: organizationID, Provider: provider.Name, CredentialType: credentialType,
		Scopes: scopes, Status: domain.AppStatusActive, Revision: 1, CreatedBy: membership.UserID,
		CreatedAt: now, UpdatedAt: now,
	}
	githubAppGiven := input.GitHubAppID != "" || input.GitHubAppSlug != "" || input.PrivateKey != ""
	switch {
	case usesClient(credentialType):
		if githubAppGiven && credentialType != domain.CredentialInstallation {
			return domain.App{}, fmt.Errorf("only installation apps take a GitHub App: %w", domain.ErrInvalid)
		}
		if app.ClientID, err = clientValue("client_id", input.ClientID); err != nil {
			return domain.App{}, err
		}
		sealed := appSecrets{}
		if sealed.ClientSecret, err = clientValue("client_secret", input.ClientSecret); err != nil {
			return domain.App{}, err
		}
		if credentialType == domain.CredentialInstallation {
			if app.Settings, err = githubAppSettings(input.GitHubAppID, input.GitHubAppSlug); err != nil {
				return domain.App{}, err
			}
			if sealed.PrivateKey, err = privateKey(input.PrivateKey); err != nil {
				return domain.App{}, err
			}
		}
		if app.EncryptedSecret, err = s.seal(ctx, appSecretBinding(app), sealed); err != nil {
			return domain.App{}, err
		}
	case input.ClientID != "" || input.ClientSecret != "" || githubAppGiven:
		return domain.App{}, fmt.Errorf("%q apps take no client: %w", credentialType, domain.ErrInvalid)
	}
	event, err := s.event(connectionseventsv1.AppCreatedDetailType, connectionseventsv1.AppCreated{
		AppID: app.ID, OrganizationID: app.OrganizationID, Provider: app.Provider,
		CredentialType: string(app.CredentialType), Scopes: app.Scopes, CreatedBy: app.CreatedBy, CreatedAt: now,
	}, now)
	if err != nil {
		return domain.App{}, err
	}
	if err := s.store.CreateApp(ctx, app, event); err != nil {
		return domain.App{}, err
	}
	return app, nil
}

// UpdateApp changes an app's allowed scopes, secret, private key or client.
// Changing the client moves its connections to needs_reconnect, because
// grants belong to the client that issued them.
func (s *Service) UpdateApp(ctx context.Context, credentials authclient.Credentials, organizationID, appID string, input UpdateAppInput) (domain.App, error) {
	membership, err := s.admin(ctx, credentials, organizationID)
	if err != nil {
		return domain.App{}, err
	}
	app, err := s.store.App(ctx, organizationID, appID)
	if err != nil {
		return domain.App{}, err
	}
	provider, ok := s.providers.Lookup(app.Provider)
	if !ok {
		return domain.App{}, fmt.Errorf("provider is not available: %w", domain.ErrInvalid)
	}
	updated := app
	var changed []string
	var reconnect domain.ReconnectReason
	if input.Scopes != nil {
		rules := provider.ScopesFor(app.CredentialType)
		scopes, err := domain.AppScopes(input.Scopes, rules.Ceiling, rules.Required)
		if err != nil {
			return domain.App{}, err
		}
		if !slices.Equal(scopes, app.Scopes) {
			updated.Scopes = scopes
			changed = append(changed, connectionseventsv1.AppChangedScopes)
		}
	}
	if (input.ClientID != "" || input.ClientSecret != "") && !usesClient(app.CredentialType) {
		return domain.App{}, fmt.Errorf("%q apps take no client: %w", app.CredentialType, domain.ErrInvalid)
	}
	if input.PrivateKey != "" && app.CredentialType != domain.CredentialInstallation {
		return domain.App{}, fmt.Errorf("only installation apps take a private key: %w", domain.ErrInvalid)
	}
	if input.ClientID != "" {
		clientID, err := clientValue("client_id", input.ClientID)
		if err != nil {
			return domain.App{}, err
		}
		if clientID != app.ClientID {
			if strings.TrimSpace(input.ClientSecret) == "" {
				return domain.App{}, fmt.Errorf("a new client_id needs its client_secret: %w", domain.ErrInvalid)
			}
			updated.ClientID = clientID
			reconnect = domain.ReconnectClientChanged
			changed = append(changed, connectionseventsv1.AppChangedClient)
		}
	}
	if input.ClientSecret != "" || input.PrivateKey != "" {
		sealed, err := s.appSecrets(ctx, app)
		if err != nil {
			return domain.App{}, err
		}
		if input.ClientSecret != "" {
			if sealed.ClientSecret, err = clientValue("client_secret", input.ClientSecret); err != nil {
				return domain.App{}, err
			}
		}
		if input.PrivateKey != "" {
			if sealed.PrivateKey, err = privateKey(input.PrivateKey); err != nil {
				return domain.App{}, err
			}
		}
		if updated.EncryptedSecret, err = s.seal(ctx, appSecretBinding(app), sealed); err != nil {
			return domain.App{}, err
		}
		changed = append(changed, connectionseventsv1.AppChangedSecret)
	}
	if len(changed) == 0 {
		return app, nil
	}
	now := s.clock()
	updated.Revision, updated.UpdatedAt = app.Revision+1, now
	event, err := s.event(connectionseventsv1.AppUpdatedDetailType, connectionseventsv1.AppUpdated{
		AppID: app.ID, OrganizationID: app.OrganizationID, Provider: app.Provider, Scopes: updated.Scopes,
		Changed: changed, UpdatedBy: membership.UserID, UpdatedAt: now,
	}, now)
	if err != nil {
		return domain.App{}, err
	}
	if err := s.store.UpdateApp(ctx, updated, app.Revision, reconnect, event, s.needsReconnectEvent(now)); err != nil {
		return domain.App{}, err
	}
	return updated, nil
}

// SetAppStatus disables or enables an app. A disabled app blocks its
// connections without changing their status.
func (s *Service) SetAppStatus(ctx context.Context, credentials authclient.Credentials, organizationID, appID string, status domain.AppStatus) (domain.App, error) {
	if status != domain.AppStatusActive && status != domain.AppStatusDisabled {
		return domain.App{}, fmt.Errorf("app status is not allowed: %w", domain.ErrInvalid)
	}
	membership, err := s.admin(ctx, credentials, organizationID)
	if err != nil {
		return domain.App{}, err
	}
	app, err := s.store.App(ctx, organizationID, appID)
	if err != nil {
		return domain.App{}, err
	}
	if app.Status == status {
		return app, nil
	}
	now := s.clock()
	updated := app
	updated.Status, updated.Revision, updated.UpdatedAt = status, app.Revision+1, now
	detailType := connectionseventsv1.AppEnabledDetailType
	var data any = connectionseventsv1.AppEnabled{AppID: app.ID, OrganizationID: app.OrganizationID, Provider: app.Provider,
		EnabledBy: membership.UserID, EnabledAt: now}
	if status == domain.AppStatusDisabled {
		detailType = connectionseventsv1.AppDisabledDetailType
		data = connectionseventsv1.AppDisabled{AppID: app.ID, OrganizationID: app.OrganizationID, Provider: app.Provider,
			DisabledBy: membership.UserID, DisabledAt: now}
	}
	event, err := s.event(detailType, data, now)
	if err != nil {
		return domain.App{}, err
	}
	if err := s.store.UpdateApp(ctx, updated, app.Revision, "", event, nil); err != nil {
		return domain.App{}, err
	}
	return updated, nil
}

// DeleteApp takes an app out of use and disconnects its connections.
func (s *Service) DeleteApp(ctx context.Context, credentials authclient.Credentials, organizationID, appID string) error {
	membership, err := s.admin(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	app, err := s.store.App(ctx, organizationID, appID)
	if err != nil {
		return err
	}
	now := s.clock()
	event, err := s.event(connectionseventsv1.AppDeletedDetailType, connectionseventsv1.AppDeleted{
		AppID: app.ID, OrganizationID: app.OrganizationID, Provider: app.Provider, DeletedBy: membership.UserID,
		Reason: connectionseventsv1.AppDeletedReasonDeleted, DeletedAt: now,
	}, now)
	if err != nil {
		return err
	}
	return s.store.DeleteApp(ctx, organizationID, app.ID, now, event, s.disconnectedEvent(membership.UserID, now))
}

func githubAppSettings(appID, slug string) (domain.AppSettings, error) {
	appID, slug = strings.TrimSpace(appID), strings.TrimSpace(slug)
	if !githubAppIDPattern.MatchString(appID) || !githubAppSlugPattern.MatchString(slug) {
		return domain.AppSettings{}, fmt.Errorf("installation apps need a numeric github_app_id and a github_app_slug: %w", domain.ErrInvalid)
	}
	return domain.AppSettings{GitHubAppID: appID, GitHubAppSlug: slug}, nil
}

func privateKey(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("private_key is required: %w", domain.ErrInvalid)
	}
	if err := providers.ParsePrivateKey(value); err != nil {
		return "", fmt.Errorf("private_key must be the GitHub App's RSA private key in PEM: %w", domain.ErrInvalid)
	}
	return value, nil
}

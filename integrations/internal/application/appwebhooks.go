package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

// AppWebhookView is an app's webhook as org admins see it: the URL to set on
// the provider's app, and never its secret.
type AppWebhookView struct {
	domain.AppWebhook
	URL string `json:"url"`
}

func (s *Service) GetAppWebhook(ctx context.Context, credentials authclient.Credentials, organizationID, appID string) (AppWebhookView, error) {
	if _, err := s.admin(ctx, credentials, organizationID); err != nil {
		return AppWebhookView{}, err
	}
	webhook, err := s.store.AppWebhook(ctx, organizationID, appID)
	if err != nil {
		return AppWebhookView{}, err
	}
	return AppWebhookView{AppWebhook: webhook, URL: s.WebhookURL(webhook.Provider, webhook.Route)}, nil
}

// PutAppWebhook sets the signing secret of an app whose provider delivers
// through its app. Replacing the secret keeps the URL.
func (s *Service) PutAppWebhook(ctx context.Context, credentials authclient.Credentials, organizationID, appID, secret string) (AppWebhookView, error) {
	membership, err := s.admin(ctx, credentials, organizationID)
	if err != nil {
		return AppWebhookView{}, err
	}
	if secret, err = domain.CheckWebhookSecret(secret); err != nil {
		return AppWebhookView{}, err
	}
	app, err := s.connections.App(ctx, organizationID, appID, credentials.AccessToken())
	if err != nil {
		return AppWebhookView{}, err
	}
	provider, err := s.provider(app.Provider)
	if err != nil {
		return AppWebhookView{}, err
	}
	if provider.Delivery != providers.DeliveryApp {
		return AppWebhookView{}, fmt.Errorf("%s subscriptions register their own webhooks: %w", provider.Name, domain.ErrInvalid)
	}
	route := ""
	switch existing, err := s.store.AppWebhook(ctx, organizationID, appID); {
	case err == nil:
		route = existing.Route
	case errors.Is(err, domain.ErrNotFound):
		if route, err = newRoute(); err != nil {
			return AppWebhookView{}, err
		}
	default:
		return AppWebhookView{}, err
	}
	now := s.now()
	webhook := domain.AppWebhook{
		AppID: appID, OrganizationID: organizationID, Provider: provider.Name, Route: route,
		UpdatedBy: membership.UserID, CreatedAt: now, UpdatedAt: now,
	}
	if webhook.EncryptedSecret, err = s.sealer.Seal(ctx, appWebhookBinding(webhook), []byte(secret)); err != nil {
		return AppWebhookView{}, err
	}
	saved, err := s.store.PutAppWebhook(ctx, webhook)
	if err != nil {
		return AppWebhookView{}, err
	}
	return AppWebhookView{AppWebhook: saved, URL: s.WebhookURL(saved.Provider, saved.Route)}, nil
}

// DeleteAppWebhook forgets an app's signing secret. Its deliveries are then
// refused until one is set again.
func (s *Service) DeleteAppWebhook(ctx context.Context, credentials authclient.Credentials, organizationID, appID string) error {
	if _, err := s.admin(ctx, credentials, organizationID); err != nil {
		return err
	}
	return s.store.DeleteAppWebhooks(ctx, organizationID, appID)
}

func (s *Service) admin(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	membership, err := s.member(ctx, credentials, organizationID)
	if err != nil {
		return domain.OrgMembership{}, err
	}
	// An app's signing secret is organization configuration, which an agent
	// never changes even for an admin it acts for.
	if !membership.Role.IsOrgAdmin() || membership.Agent {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	return membership, nil
}

func appWebhookBinding(webhook domain.AppWebhook) secrets.Binding {
	return secrets.Binding{OrganizationID: webhook.OrganizationID, ResourceType: secrets.ResourceAppWebhook, ResourceID: webhook.AppID}
}

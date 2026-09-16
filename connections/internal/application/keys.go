package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// CreateConnectionInput is POST …/connections: a key-based or
// client-credentials connection, made without a redirect.
type CreateConnectionInput struct {
	AppID      string
	Scope      domain.Scope
	TeamID     string
	Usage      domain.Usage
	Label      string
	Credential domain.CredentialInput
}

// CreateConnection saves a key-based connection once the provider's identity
// check accepts the key, or a client-credentials connection once a token
// mints in its tenant.
func (s *Service) CreateConnection(ctx context.Context, credentials authclient.Credentials, organizationID string, input CreateConnectionInput) (domain.Connection, error) {
	membership, err := s.callerForWrite(ctx, credentials, organizationID)
	if err != nil {
		return domain.Connection{}, err
	}
	scope, err := domain.ParseScope(string(input.Scope))
	if err != nil {
		return domain.Connection{}, err
	}
	usage, err := domain.ParseUsage(string(input.Usage), scope)
	if err != nil {
		return domain.Connection{}, err
	}
	placement, err := domain.Place(scope, input.TeamID, membership.UserID)
	if err != nil {
		return domain.Connection{}, err
	}
	caller, err := s.callerAt(ctx, credentials, membership, placement.Scope, placement.TeamID)
	if err != nil {
		return domain.Connection{}, err
	}
	if err := domain.MayCreate(caller, placement); err != nil {
		return domain.Connection{}, err
	}
	if strings.TrimSpace(input.AppID) == "" {
		return domain.Connection{}, fmt.Errorf("app_id is required: %w", domain.ErrInvalid)
	}
	app, err := s.store.App(ctx, organizationID, strings.TrimSpace(input.AppID))
	if err != nil {
		return domain.Connection{}, err
	}
	if !app.CredentialType.KeyBased() && app.CredentialType != domain.CredentialOAuth2ClientCredentials {
		return domain.Connection{}, fmt.Errorf("%s apps are connected through authorize: %w", app.CredentialType, domain.ErrInvalid)
	}
	if app.Status != domain.AppStatusActive {
		return domain.Connection{}, domain.ErrAppDisabled
	}
	if err := app.CredentialType.Fits(placement.Scope); err != nil {
		return domain.Connection{}, err
	}
	provider, err := s.providerFor(app)
	if err != nil {
		return domain.Connection{}, err
	}
	credential, err := input.Credential.Validate(app.CredentialType)
	if err != nil {
		return domain.Connection{}, err
	}
	label, err := domain.Label(input.Label)
	if err != nil {
		return domain.Connection{}, err
	}

	var got obtained
	if app.CredentialType == domain.CredentialOAuth2ClientCredentials {
		opened, err := s.appSecrets(ctx, app)
		if err != nil {
			return domain.Connection{}, err
		}
		if got, err = s.mintConsented(ctx, app, provider, opened, callbackResult{Tenant: credential.Tenant}); err != nil {
			return domain.Connection{}, err
		}
	} else {
		account, err := s.oauth.KeyIdentity(ctx, provider, app.CredentialType, credential)
		if err != nil {
			return domain.Connection{}, err
		}
		got = obtained{account: account, secrets: connectionSecrets{Key: &credential}}
	}
	connection, event, err := s.newConnection(ctx, app, placement, usage, label, got, membership.UserID)
	if err != nil {
		return domain.Connection{}, err
	}
	return s.store.CreateConnection(ctx, connection, event, s.clock().Add(s.heartbeatInterval))
}

// ReplaceCredential replaces a key-based connection's secret in place. The new
// key must belong to the same provider account.
func (s *Service) ReplaceCredential(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string, credential domain.CredentialInput) (domain.Connection, error) {
	membership, err := s.callerForWrite(ctx, credentials, organizationID)
	if err != nil {
		return domain.Connection{}, err
	}
	current, err := s.manageableConnection(ctx, credentials, membership, connectionID)
	if err != nil {
		return domain.Connection{}, err
	}
	if !current.CredentialType.KeyBased() {
		return domain.Connection{}, fmt.Errorf("only key-based connections take a new credential; reconnect this one instead: %w", domain.ErrInvalid)
	}
	app, err := s.store.App(ctx, organizationID, current.AppID)
	if err != nil {
		return domain.Connection{}, err
	}
	if app.Status != domain.AppStatusActive {
		return domain.Connection{}, domain.ErrAppDisabled
	}
	provider, err := s.providerFor(app)
	if err != nil {
		return domain.Connection{}, err
	}
	validated, err := credential.Validate(current.CredentialType)
	if err != nil {
		return domain.Connection{}, err
	}
	account, err := s.oauth.KeyIdentity(ctx, provider, current.CredentialType, validated)
	if err != nil {
		return domain.Connection{}, err
	}
	restored, event, err := s.restoredConnection(ctx, current, obtained{account: account, secrets: connectionSecrets{Key: &validated}}, membership.UserID)
	if err != nil {
		return domain.Connection{}, err
	}
	return s.store.ReplaceCredentials(ctx, restored, current.Version, event)
}

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/secrets"
)

// appSecrets is what an app keeps encrypted.
type appSecrets struct {
	ClientSecret string `json:"client_secret,omitempty"`
	// PrivateKey is an installation app's GitHub App private key, in PEM.
	PrivateKey string `json:"private_key,omitempty"`
}

// connectionSecrets is what a connection keeps encrypted: an oauth2 grant; the
// last token minted for a client-credentials or installation connection, with
// the tenant or installation it was minted for; or a key-based secret.
type connectionSecrets struct {
	Grant          *providers.Grant        `json:"grant,omitempty"`
	Tenant         string                  `json:"tenant,omitempty"`
	InstallationID string                  `json:"installation_id,omitempty"`
	Key            *domain.CredentialInput `json:"key,omitempty"`
}

// callbackResult is what a provider's redirect left on a session.
type callbackResult struct {
	Code           string `json:"code,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`
	Tenant         string `json:"tenant,omitempty"`
}

// seal encrypts value as JSON under binding.
func (s *Service) seal(ctx context.Context, binding secrets.Binding, value any) (*domain.SealedData, error) {
	plaintext, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", binding.ResourceType, err)
	}
	defer secrets.Wipe(plaintext)
	sealed, err := s.sealer.Seal(ctx, binding, plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal %s: %w", binding.ResourceType, err)
	}
	return &sealed, nil
}

// open decrypts a JSON value sealed under binding into target. Its errors
// never quote the plaintext.
func (s *Service) open(ctx context.Context, binding secrets.Binding, sealed *domain.SealedData, target any) error {
	if sealed == nil {
		return fmt.Errorf("open %s: nothing is sealed", binding.ResourceType)
	}
	plaintext, err := s.sealer.Open(ctx, binding, *sealed)
	if err != nil {
		return fmt.Errorf("open %s: %w", binding.ResourceType, err)
	}
	defer secrets.Wipe(plaintext)
	if json.Unmarshal(plaintext, target) != nil {
		return fmt.Errorf("open %s: %w", binding.ResourceType, errors.New("sealed value is not valid"))
	}
	return nil
}

func (s *Service) appSecrets(ctx context.Context, app domain.App) (appSecrets, error) {
	var opened appSecrets
	if err := s.open(ctx, appSecretBinding(app), app.EncryptedSecret, &opened); err != nil {
		return appSecrets{}, err
	}
	return opened, nil
}

func (s *Service) connectionSecrets(ctx context.Context, connection domain.Connection) (connectionSecrets, error) {
	var opened connectionSecrets
	if err := s.open(ctx, connectionBinding(connection.OrganizationID, connection.ID), connection.EncryptedCredentials, &opened); err != nil {
		return connectionSecrets{}, err
	}
	return opened, nil
}

func (s *Service) sealConnectionSecrets(ctx context.Context, connection domain.Connection, value connectionSecrets) (*domain.SealedData, error) {
	return s.seal(ctx, connectionBinding(connection.OrganizationID, connection.ID), value)
}

// githubApp is what minting an installation app's tokens needs.
func githubApp(app domain.App, opened appSecrets) providers.GitHubApp {
	return providers.GitHubApp{AppID: app.Settings.GitHubAppID, PrivateKey: opened.PrivateKey}
}

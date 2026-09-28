package signin

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// CredentialRequest is a member giving a connection a key, a password or a
// client's credentials: as it connects, or to replace what it holds.
type CredentialRequest struct {
	OrganizationID string
	ConnectionID   string
	Toolkit        string
	// Kind picks the manifest's method by its kind; "" is the manifest's
	// first. It must be api_token, basic or oauth2_client_credentials.
	Kind string
	// Fields are the kind's standard fields (token; username and password;
	// client_id and client_secret) and the method's connection-level ones.
	Fields map[string]string
}

// Saved is a credential SaveCredential kept.
type Saved struct {
	Kind    string
	Account Account
	Scopes  []string
	Version int64
}

// SaveCredential checks what a member typed against the manifest, proves it
// with the test call (minting a token first for a client's credentials),
// and keeps it sealed: each secret value on its own. Nothing is kept unless
// the test call passes. A *Failure says why it was not. The connection must
// be pending or active, and a credential it holds already must be of the
// same kind; this replaces it.
func (s *Service) SaveCredential(ctx context.Context, request CredentialRequest) (Saved, error) {
	if request.OrganizationID == "" || request.ConnectionID == "" {
		return Saved{}, fmt.Errorf("signin: an organization and a connection are required: %w", domain.ErrInvalid)
	}
	manifest, method, err := s.method(request.Toolkit, request.Kind)
	if err != nil {
		return Saved{}, err
	}
	switch method.Kind {
	case integration.KindAPIToken, integration.KindBasic, integration.KindOAuth2ClientCredentials:
	default:
		return Saved{}, fmt.Errorf("%w: it signs in with %s, through StartOAuth", ErrUnsupportedKind, method.Kind)
	}
	settings, secretValues, err := memberFields(method, request.Fields)
	if err != nil {
		return Saved{}, err
	}
	var client *OAuthClient
	if hasClientFields(method) {
		if method.OAuth2 == nil || method.OAuth2.Client == "" {
			return Saved{}, fmt.Errorf("%w: the method has client-level fields and no client", ErrNoClient)
		}
		row, err := s.resolveClient(ctx, request.OrganizationID, method.OAuth2.Client)
		if err != nil {
			return Saved{}, err
		}
		if err := requireClientFields(method, &row); err != nil {
			return Saved{}, err
		}
		client = &row
	}

	held := material{}
	scopes := []string{}
	switch method.Kind {
	case integration.KindAPIToken:
		held.token = secretValues["token"]
	case integration.KindBasic:
		held.username, held.password = settings["username"], secretValues["password"]
	case integration.KindOAuth2ClientCredentials:
		values := merged(clientValues(method, client), settings)
		tokenURL, err := fillURL(method.OAuth2.TokenURL, values)
		if err != nil {
			return Saved{}, &Failure{Reason: ReasonInvalidCredentials, cause: err}
		}
		requestSecrets, err := s.clientFieldSecrets(ctx, method, client)
		if err != nil {
			return Saved{}, err
		}
		maps.Copy(requestSecrets, secretValues)
		token, err := s.mintToken(ctx, method.OAuth2, tokenURL.String(), settings["client_id"], secretValues["client_secret"],
			tokenParams(method, values, requestSecrets))
		switch {
		case errors.Is(err, errGrantRefused):
			return Saved{}, &Failure{Reason: ReasonInvalidCredentials, Rule: "the app refused the client's credentials", cause: err}
		case errors.Is(err, errClientRefused):
			return Saved{}, &Failure{Reason: ReasonInvalidCredentials, Rule: "the app refused the token request", cause: err}
		case err != nil:
			return Saved{}, &Failure{Reason: ReasonUnavailable, cause: err}
		}
		minted := s.grantFrom(token)
		discovered, discoveredSecrets, err := discover(method, token)
		if err != nil {
			return Saved{}, err
		}
		maps.Copy(settings, discovered)
		maps.Copy(secretValues, discoveredSecrets)
		secretValues[sealedAccessToken] = minted.access
		held.token, held.expires = minted.access, minted.expiresAt
		if minted.scopes != nil {
			scopes = minted.scopes
		} else {
			scopes = append(scopes, method.OAuth2.Scopes...)
		}
	}
	clientSecrets, err := s.clientFieldSecrets(ctx, method, client)
	if err != nil {
		return Saved{}, err
	}
	held.fields = placedValues(method, mergedSecrets(clientSecrets, secretValues))

	p, err := s.prepare(manifest, method, client, settings)
	if err != nil {
		return Saved{}, &Failure{Reason: ReasonInvalidCredentials, cause: err}
	}
	account, err := s.runTest(ctx, p, held)
	if err != nil {
		return Saved{}, testFailure(err)
	}
	settings[integration.SettingAccountLabel] = account.Label
	if account.Ref != "" {
		settings[integration.SettingAccountRef] = account.Ref
	}
	sealed, err := s.sealEach(ctx, func(name string) secrets.Binding {
		return credentialBinding(request.ConnectionID, name)
	}, secretValues)
	if err != nil {
		return Saved{}, err
	}
	now := s.now().UTC()
	credential := Credential{
		ConnectionID: request.ConnectionID, OrganizationID: request.OrganizationID, Toolkit: request.Toolkit,
		Kind: method.Kind, Sealed: sealed, Settings: settings, Scopes: scopes, ExpiresAt: held.expires,
		NextCheckAt: nextCheck(now), UpdatedAt: now,
	}
	if client != nil {
		credential.OAuthClientID = client.ID
	}
	version, err := s.store.ReplaceCredential(ctx, credential)
	if err != nil {
		return Saved{}, err
	}
	s.cache.put(request.ConnectionID, version, held, cacheUntil(held, now), now)
	s.logger.InfoContext(ctx, "app credential saved", "connection_id", request.ConnectionID,
		"toolkit", request.Toolkit, "kind", method.Kind)
	return Saved{Kind: method.Kind, Account: account, Scopes: scopes, Version: version}, nil
}

func mergedSecrets(sources ...map[string]secrets.Secret) map[string]secrets.Secret {
	out := map[string]secrets.Secret{}
	for _, source := range sources {
		maps.Copy(out, source)
	}
	return out
}

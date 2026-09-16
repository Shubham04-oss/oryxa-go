package application

import (
	"context"
	"net/url"
	"slices"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
)

func (oauth *fakeOAuth) Refresh(_ context.Context, _ providers.OAuth2, request providers.RefreshRequest) (providers.Grant, error) {
	oauth.mu.Lock()
	oauth.refreshes = append(oauth.refreshes, request)
	grant, err, delay := oauth.refreshGrant, oauth.refreshErr, oauth.refreshDelay
	oauth.mu.Unlock()
	time.Sleep(delay)
	// Like providers.Client, a provider that does not rotate the refresh
	// token keeps the old one, and metadata the refresh leaves out is kept.
	if err == nil && grant.RefreshToken == "" {
		grant.RefreshToken = request.RefreshToken
	}
	if err == nil && grant.Metadata == nil {
		grant.Metadata = request.Metadata
	}
	return grant, err
}

func (oauth *fakeOAuth) KeyIdentity(_ context.Context, provider providers.Provider, credentialType domain.CredentialType, credential domain.CredentialInput) (domain.ProviderAccount, error) {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	oauth.keyChecks = append(oauth.keyChecks, credential)
	oauth.keyAPIURLs = append(oauth.keyAPIURLs, provider.Keys[credentialType].APIURL)
	return oauth.keyAccount, oauth.keyErr
}

func (oauth *fakeOAuth) Mint(_ context.Context, _ providers.Provider, request providers.MintRequest) (providers.Grant, error) {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	oauth.mints = append(oauth.mints, request)
	return oauth.mintGrant, oauth.mintErr
}

func (oauth *fakeOAuth) AdminConsentURL(_ providers.Provider, request providers.AdminConsentRequest) (string, error) {
	return "https://login.example.test/adminconsent?" + url.Values{"client_id": {request.ClientID}, "state": {request.State}}.Encode(), nil
}

func (oauth *fakeOAuth) InstallationURL(_ providers.Provider, request providers.InstallationRequest) (string, error) {
	return "https://github.example.test/apps/" + request.AppSlug + "/installations/new?" + url.Values{"state": {request.State}}.Encode(), nil
}

func (oauth *fakeOAuth) UserInstallations(context.Context, providers.Provider, providers.Grant) ([]string, error) {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	return slices.Clone(oauth.userInstallations), nil
}

func (oauth *fakeOAuth) Installation(context.Context, providers.Provider, providers.GitHubApp, string) (domain.ProviderAccount, error) {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	return oauth.installationAccount, oauth.installationErr
}

func (oauth *fakeOAuth) InstallationToken(context.Context, providers.Provider, providers.GitHubApp, string) (providers.Grant, error) {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	oauth.installationTokens++
	return oauth.installationToken, oauth.installationTokErr
}

func (oauth *fakeOAuth) Revoke(_ context.Context, _ providers.Provider, request providers.RevokeRequest) (bool, error) {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	oauth.revocations = append(oauth.revocations, request)
	return oauth.revokeErr == nil, oauth.revokeErr
}

func (oauth *fakeOAuth) refreshCount() int {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	return len(oauth.refreshes)
}

func (oauth *fakeOAuth) refreshRequests() []providers.RefreshRequest {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	return slices.Clone(oauth.refreshes)
}

func (oauth *fakeOAuth) revokeRequests() []providers.RevokeRequest {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	return slices.Clone(oauth.revocations)
}

func (oauth *fakeOAuth) identityCount() int {
	oauth.mu.Lock()
	defer oauth.mu.Unlock()
	return oauth.identities
}

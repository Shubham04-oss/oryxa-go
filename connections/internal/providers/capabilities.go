package providers

// This file declares the shared types for provider capabilities beyond the
// authorization-code flow. Their functions live in keys.go,
// clientcredentials.go, installations.go and revoke.go.

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// ErrInstallationNotFound means a GitHub App installation no longer exists.
// Errors carrying it also match domain.ErrCredentialRejected.
var ErrInstallationNotFound = errors.New("installation not found")

// ClientCredentials is a provider's client-credentials grant, with the admin
// consent that must come first.
type ClientCredentials struct {
	// TokenURL contains {tenant}.
	TokenURL string
	// AdminConsentURL is where an admin grants the app access to a tenant.
	AdminConsentURL string
	// Scopes are sent with every mint, such as Microsoft Graph's .default.
	Scopes []string
	// PermanentErrors are token endpoint error codes meaning the client or
	// its consent is gone for good.
	PermanentErrors []string
}

// Installation is a GitHub App's installation flow.
type Installation struct {
	// InstallURL contains {slug}.
	InstallURL string
	APIURL     string
}

// KeyAuth is how a key-based credential type is sent to a provider, and how
// its account is checked.
type KeyAuth struct {
	Placement domain.TokenPlacement
	APIURL    string
	// Identity returns the account a credential belongs to. apply puts the
	// credential on the request per Placement, the way KeyToken describes it.
	Identity func(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error)
}

type MintRequest struct {
	ClientID     string
	ClientSecret string
	Tenant       string
}

type AdminConsentRequest struct {
	ClientID    string
	RedirectURI string
	State       string
}

type InstallationRequest struct {
	AppSlug string
	State   string
}

// GitHubApp is what minting installation tokens needs: the app's ID and its
// PEM private key.
type GitHubApp struct {
	AppID      string
	PrivateKey string
}

type RevokeRequest struct {
	ClientID     string
	ClientSecret string
	Grant        Grant
}

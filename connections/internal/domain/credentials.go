package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	// RenewBefore is how close to expiry a token is refreshed or minted
	// before it is issued.
	RenewBefore = 2 * time.Minute
	// RenewWait bounds how long a caller waits for another caller's refresh.
	RenewWait = 10 * time.Second
	// MaxOpenSessions bounds the unexpired authorization sessions one member
	// may hold in an organization.
	MaxOpenSessions = 20
	maxSecretLength = 4096
)

// CredentialInput is what a member supplies to create or replace a
// connection without a provider redirect: an API key, a bearer token, basic
// credentials, or the tenant a client-credentials app mints tokens in.
type CredentialInput struct {
	APIKey   string `json:"api_key,omitempty"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Tenant   string `json:"tenant,omitempty"`
}

var tenantPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,99}$`)

// Validate checks that the input carries exactly what a credential type needs,
// with surrounding space trimmed.
func (input CredentialInput) Validate(credentialType CredentialType) (CredentialInput, error) {
	trimmed := CredentialInput{
		APIKey: strings.TrimSpace(input.APIKey), Token: strings.TrimSpace(input.Token),
		Username: strings.TrimSpace(input.Username), Password: strings.TrimSpace(input.Password),
		Tenant: strings.TrimSpace(input.Tenant),
	}
	var want CredentialInput
	switch credentialType {
	case CredentialAPIKey:
		want.APIKey = trimmed.APIKey
		if want.APIKey == "" {
			return CredentialInput{}, fmt.Errorf("credential.api_key is required: %w", ErrInvalid)
		}
	case CredentialBearer:
		want.Token = trimmed.Token
		if want.Token == "" {
			return CredentialInput{}, fmt.Errorf("credential.token is required: %w", ErrInvalid)
		}
	case CredentialBasic:
		want.Username, want.Password = trimmed.Username, trimmed.Password
		if want.Username == "" || want.Password == "" || strings.Contains(want.Username, ":") {
			return CredentialInput{}, fmt.Errorf("credential.username without a colon and credential.password are required: %w", ErrInvalid)
		}
	case CredentialOAuth2ClientCredentials:
		want.Tenant = trimmed.Tenant
		if !tenantPattern.MatchString(want.Tenant) {
			return CredentialInput{}, fmt.Errorf("credential.tenant must be a tenant ID or domain: %w", ErrInvalid)
		}
	case CredentialOAuth2, CredentialInstallation:
		return CredentialInput{}, fmt.Errorf("%s connections are made through authorize: %w", credentialType, ErrInvalid)
	default:
		return CredentialInput{}, fmt.Errorf("credential type is not allowed: %w", ErrInvalid)
	}
	if trimmed != want {
		return CredentialInput{}, fmt.Errorf("credential has fields %s connections do not take: %w", credentialType, ErrInvalid)
	}
	for _, value := range []string{want.APIKey, want.Token, want.Username, want.Password} {
		if len(value) > maxSecretLength || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return CredentialInput{}, fmt.Errorf("credential values must be one line of at most %d bytes: %w", maxSecretLength, ErrInvalid)
		}
	}
	return want, nil
}

// TokenPlacement says where an issued credential goes on a provider request.
type TokenPlacement struct {
	// In is "header" or "query".
	In     string `json:"in"`
	Name   string `json:"name"`
	Prefix string `json:"prefix,omitempty"`
}

// IssuedToken is the only response that carries a credential.
type IssuedToken struct {
	TokenType       string          `json:"token_type"`
	AccessToken     string          `json:"access_token"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	Placement       TokenPlacement  `json:"placement"`
	Scopes          []string        `json:"scopes"`
	ProviderAccount ProviderAccount `json:"provider_account"`
	// Domain is the app's domain, for providers reached through the
	// organization's own address, such as an Atlassian site.
	Domain string `json:"domain,omitempty"`
	// Metadata carries what the provider returned with the token that callers
	// need and that is not secret, such as Salesforce's instance_url.
	Metadata map[string]string `json:"metadata,omitempty"`
}

var holderPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,79}$`)

// ValidHolder checks a hold's holder name: lowercase letters, digits, dots,
// underscores, colons and hyphens, at most 80 characters.
func ValidHolder(holder string) error {
	if !holderPattern.MatchString(holder) {
		return fmt.Errorf("holder must be 1 to 80 lowercase letters, digits, or . _ : -: %w", ErrInvalid)
	}
	return nil
}

// OwnerFilter selects live connections to end together: a member's own
// connections, a team's, or with neither set, all of an organization's.
type OwnerFilter struct {
	MemberID string
	TeamID   string
}

// ConnectionChange is what PATCH changes on a connection.
type ConnectionChange struct {
	Label *string
	// MakeDefault makes the connection its app's default in its scope.
	MakeDefault bool
}

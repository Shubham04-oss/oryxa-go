package memberauth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"google.golang.org/adk/v2/auth"
)

// TokenSource mints a short-lived access token for a member when the member
// is not present to carry their own — a trigger or a schedule acting on
// their behalf. The token is still the member's own identity (its subject is
// the member), issued through the agent app client under their standing
// grant; AuthClient implements it against auth's internal endpoint.
type TokenSource interface {
	AgentToken(ctx context.Context, userID string) (token string, expiresAt time.Time, err error)
}

// CredentialProvider is the ADK auth.CredentialProvider every integrations
// MCP call authenticates with: the member's own access token when the run
// carries one (WithAccessToken), otherwise a short-lived agent token for the
// session's user, minted from Tokens.
//
// It only ever sees an HTTP request's plain context.Context — auth.Transport
// hands it req.Context() — so it can authenticate only what WrapToolset
// already attached to that context before the request was built.
type CredentialProvider struct {
	Tokens TokenSource
}

// NewCredentialProvider returns a CredentialProvider that falls back to
// tokens when a run carries no member access token of its own. tokens may be
// nil for a caller that only ever runs with the member present.
func NewCredentialProvider(tokens TokenSource) *CredentialProvider {
	return &CredentialProvider{Tokens: tokens}
}

// Credential implements auth.CredentialProvider.
func (p *CredentialProvider) Credential(ctx context.Context) (auth.Credential, error) {
	member, ok := memberFromContext(ctx)
	if !ok {
		return nil, ErrNoMember
	}
	if token := AccessToken(ctx); token != "" {
		return memberCredential{token: token, organizationID: member.OrganizationID}, nil
	}
	if p.Tokens == nil {
		return nil, errors.New("memberauth: this run carries no member access token, and no agent token source is configured")
	}
	token, _, err := p.Tokens.AgentToken(ctx, member.UserID)
	if err != nil {
		return nil, err
	}
	return memberCredential{token: token, organizationID: member.OrganizationID}, nil
}

// memberCredential applies the member's bearer token and organization to a
// request headed for integrations.
type memberCredential struct {
	token          string
	organizationID string
}

// Apply implements auth.Credential.
func (c memberCredential) Apply(header http.Header) error {
	if c.token == "" {
		return errors.New("memberauth: empty access token")
	}
	header.Set("Authorization", "Bearer "+c.token)
	header.Set("X-Oryxa-Organization", c.organizationID)
	return nil
}

var _ auth.CredentialProvider = (*CredentialProvider)(nil)

package memberauth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/auth"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
)

// TokenSource mints a short-lived access token for a member when the member
// is not present to carry their own — a trigger or a schedule acting on
// their behalf. The token is still the member's own identity (its subject is
// the member), issued through the agent app client under their standing
// grant; AuthClient implements it against auth's internal endpoint. The
// token names agent, the one of the member's agents it acts as, so apps and
// rag can tell a specialist from the primary; a caller that is not a run of
// one agent passes the zero Agent, the primary.
type TokenSource interface {
	AgentToken(ctx context.Context, userID string, agent Agent) (token string, expiresAt time.Time, err error)
}

// CredentialProvider is the ADK auth.CredentialProvider a member-authenticated
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
	// A specialist always presents its own agent token, even in its member's
	// own turn: the member's token would reach past its grant (I3).
	if token := AccessToken(ctx); token != "" && member.Agent.Role != runprofile.Specialist {
		return memberCredential{token: token, organizationID: member.OrganizationID}, nil
	}
	if p.Tokens == nil {
		return nil, errors.New("memberauth: this run cannot present an agent token: no agent token source is configured")
	}
	token, _, err := p.Tokens.AgentToken(ctx, member.UserID, member.Agent)
	if err != nil {
		return nil, err
	}
	return memberCredential{token: token, organizationID: member.OrganizationID}, nil
}

// Bearer returns the member a tool call acts for and the token it presents
// as them: the member's own when the run carries it, otherwise an agent
// token for the session's user, minted from tokens. It is for native tools
// that make their own HTTP calls: they see the session, so, unlike an MCP
// toolset's calls, they need no WrapToolset to find the member.
func Bearer(ctx agent.ReadonlyContext, tokens TokenSource) (Member, string, error) {
	member, err := MemberFromSession(ctx)
	if err != nil {
		return Member{}, "", err
	}
	if token := AccessToken(ctx); token != "" {
		return member, token, nil
	}
	if tokens == nil {
		return Member{}, "", errors.New("this run cannot act for the member: it carries no token of theirs, and none can be minted here")
	}
	token, _, err := tokens.AgentToken(ctx, member.UserID, member.Agent)
	if err != nil {
		return Member{}, "", err
	}
	return member, token, nil
}

// memberCredential applies the member's bearer token and organization to a
// request made as that member.
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

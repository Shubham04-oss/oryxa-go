package v1

import "time"

// The edge token (contract §2) is an ES256 JWT that agents signs with its
// internal/serviceauth KMS key and the edge verifies with that key's public
// half. The edge→agents token (§4) has the same shape with the issuer and
// audience swapped and the edge's own key.
const (
	// IssuerAgents and AudienceEdge are a Go→edge token's iss and aud.
	IssuerAgents = "agents"
	AudienceEdge = "edge"
	// IssuerEdge and AudienceAgents are an edge→Go token's iss and aud.
	IssuerEdge     = "edge"
	AudienceAgents = "agents"

	// MaxTokenLifetime is the longest an edge token may live. Tokens are
	// issued per call, so they normally live far less.
	MaxTokenLifetime = 5 * time.Minute

	// AuthorizationScheme prefixes the token in the Authorization header.
	AuthorizationScheme = "Bearer "
)

// Scope is one thing a token lets its bearer do in its room.
type Scope string

const (
	ScopeSandboxExec  Scope = "sandbox:exec"
	ScopeSandboxFiles Scope = "sandbox:files"
	ScopeBrowserDrive Scope = "browser:drive"
	ScopeCallJoin     Scope = "call:join"
	ScopeCallSpeak    Scope = "call:speak"
	ScopeLogWrite     Scope = "log:write"
	// ScopeLogRead lets Go read a run's events back for the agent's context.
	ScopeLogRead Scope = "log:read"
)

// KnownScopes lists every scope, in a fixed order.
var KnownScopes = []Scope{
	ScopeSandboxExec, ScopeSandboxFiles, ScopeBrowserDrive,
	ScopeCallJoin, ScopeCallSpeak, ScopeLogWrite, ScopeLogRead,
}

// TokenClaims is the edge token's payload.
//
// Team is empty for thread and dm rooms. Member is whom the agent acts for.
// Private is allowed only for that member's private-{member} sandbox.
type TokenClaims struct {
	Issuer   string  `json:"iss"`
	Audience string  `json:"aud"`
	Org      string  `json:"org"`
	Team     string  `json:"team"`
	Room     Room    `json:"room"`
	Member   string  `json:"member"`
	Agent    string  `json:"agent"`
	Run      string  `json:"run"`
	Scopes   []Scope `json:"scopes"`
	Private  bool    `json:"private"`
	IssuedAt int64   `json:"iat"`
	Expiry   int64   `json:"exp"`
	ID       string  `json:"jti"`
}

// HasScope reports whether claims carry scope.
func (claims TokenClaims) HasScope(scope Scope) bool {
	for _, held := range claims.Scopes {
		if held == scope {
			return true
		}
	}
	return false
}

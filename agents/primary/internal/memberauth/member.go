// Package memberauth lets primary's agents act as the member they are
// assisting, with that member's own identity, rather than as a service
// impersonating them.
//
// A run started while the member is present (a chat turn) forwards the
// member's own access token: WithAccessToken puts it on the context and
// AccessToken reads it back. A run with no member present (a trigger or a
// schedule) has no such token; the member has instead, once, granted "agents
// may act for me", and TokenSource exchanges that standing grant for a
// short-lived access token on request.
//
// Either way, the acting Member — the session's user, in the organization
// its state names — has to reach an ADK auth.CredentialProvider, and a
// CredentialProvider only ever sees an outgoing HTTP request's plain
// context.Context, never ADK's session state. WrapToolset (toolset.go) is
// what carries the member from session state onto that context, and
// CredentialProvider (credential.go) is what reads it back off.
package memberauth

import (
	"context"
	"errors"

	"google.golang.org/adk/v2/agent"
)

// organizationStateKey is the ADK session state key internal/adksession
// seeds with the member's organization when a session is created for them
// (mirrored as agents.OrganizationIDKey and adksession.StateOrganizationID).
// Kept as a literal, rather than imported from either package, so this
// low-level package pulls in neither's dependencies for one string.
const organizationStateKey = "organization_id"

// Member is who an agent is acting for: a session's own user, in the
// organization its state names.
type Member struct {
	UserID         string
	OrganizationID string
}

// The errors below reach the model as a tool's error, so each says in plain
// words what went wrong for the member; Explain picks them out of a longer
// error.

// ErrNoMember reports that a session names no user, or no organization for
// that user, so there is no member to act for. A development session for a
// user who belongs to no organization (internal/adksession) hits this.
var ErrNoMember = errors.New("this conversation has no member of an organization to act for")

// ErrGrantRequired reports that the member has not granted agents
// permission to act for them while they are absent: auth answered 404 (no
// grant), or 409 (Cognito refused the stored refresh token, so auth deleted
// the grant) — either way the member has to grant again.
var ErrGrantRequired = errors.New("the member has not allowed agents to act for them while they are away")

// ErrUnavailable reports that auth's agent-token endpoint could not be
// reached, or answered with a server error; the caller may retry.
var ErrUnavailable = errors.New("the sign-in service did not answer; try again shortly")

// Explain returns the failure to act for the member that err carries —
// ErrNoMember, ErrGrantRequired or ErrUnavailable — without the transport
// detail around it, or nil when it carries none of them.
func Explain(err error) error {
	for _, known := range []error{ErrGrantRequired, ErrNoMember, ErrUnavailable} {
		if errors.Is(err, known) {
			return known
		}
	}
	return nil
}

// MemberFromSession resolves the acting member from an ADK session: its own
// user ID, and the organization its organization_id state names, exactly as
// internal/adksession seeds a session when it is created for a member. It
// reports ErrNoMember when either is missing.
func MemberFromSession(ctx agent.ReadonlyContext) (Member, error) {
	userID := ctx.UserID()
	if userID == "" {
		return Member{}, ErrNoMember
	}
	value, err := ctx.ReadonlyState().Get(organizationStateKey)
	if err != nil {
		return Member{}, ErrNoMember
	}
	organizationID, _ := value.(string)
	if organizationID == "" {
		return Member{}, ErrNoMember
	}
	return Member{UserID: userID, OrganizationID: organizationID}, nil
}

// contextKey namespaces this package's context values so they cannot
// collide with another package's.
type contextKey int

const (
	accessTokenContextKey contextKey = iota
	memberContextKey
	runOriginContextKey
)

// WithAccessToken attaches the member's own access token to ctx: the same
// bearer token chat auth already issues, set by whatever starts a run while
// the member is present. Never written to session state or logs — it lives
// only on the context of the run that carries it.
func WithAccessToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, accessTokenContextKey, token)
}

// AccessToken returns the token WithAccessToken attached to ctx, or "" when
// the run carries none — a member-absent run, such as a trigger or a
// schedule.
func AccessToken(ctx context.Context) string {
	token, _ := ctx.Value(accessTokenContextKey).(string)
	return token
}

// WithRunOrigin records whether this execution was started by the member, an
// external trigger, or their primary agent's team coordinator. It is runtime
// context only and is never persisted as a credential.
func WithRunOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, runOriginContextKey, origin)
}

// RunOrigin returns the execution origin attached by the threads runtime.
func RunOrigin(ctx context.Context) string {
	origin, _ := ctx.Value(runOriginContextKey).(string)
	return origin
}

// withMember attaches member to ctx, so a CredentialProvider — which only
// ever sees an outgoing HTTP request's plain context.Context — can still
// tell who it is authenticating. WrapToolset calls this on the context an
// MCP call is about to use; see toolset.go.
func withMember(ctx context.Context, member Member) context.Context {
	return context.WithValue(ctx, memberContextKey, member)
}

// memberFromContext reads back what withMember attached to ctx.
func memberFromContext(ctx context.Context) (Member, bool) {
	member, ok := ctx.Value(memberContextKey).(Member)
	return member, ok
}

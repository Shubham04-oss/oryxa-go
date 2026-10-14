package edgeclient

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"google.golang.org/adk/v2/agent"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// The ADK session state keys this package reads. They are literals, as in
// memberauth, mirrored from internal/agents (OrganizationIDKey, AgentIDKey)
// so this low-level package pulls in neither's dependencies.
const (
	organizationStateKey = "organization_id"
	managedAgentStateKey = "managed_agent_id"
)

// ErrNoOrganization reports a session with no member of an organization to
// act for, so there is no room on the edge it could use.
var ErrNoOrganization = errors.New("this conversation has no member of an organization, so it has no sandbox or browser")

// Target is where on the edge a tool call acts, and for whom: everything an
// edge token names except its scopes.
type Target struct {
	Org    string
	Room   edgev1.Room
	Member string
	Agent  string
	Run    string
}

// Claims are target's token claims with scopes.
func (target Target) Claims(scopes ...edgev1.Scope) edgev1.TokenClaims {
	return edgev1.TokenClaims{
		Org: target.Org, Room: target.Room, Member: target.Member,
		Agent: target.Agent, Run: target.Run, Scopes: scopes,
	}
}

// TargetFromContext places a tool call: the member and organization of its
// ADK session, and that session as a thread room, so each conversation has
// its own sandbox and browser, as with AgentCore. The run is the
// invocation. Ids that can't be used in an edge name are hashed into ones
// that can.
func TargetFromContext(ctx agent.ReadonlyContext) (Target, error) {
	member := ctx.UserID()
	org := stateString(ctx, organizationStateKey)
	if member == "" || org == "" {
		return Target{}, ErrNoOrganization
	}
	agentID := stateString(ctx, managedAgentStateKey)
	if agentID == "" {
		agentID = ctx.AgentName()
	}
	return Target{
		Org:    org,
		Room:   edgev1.Room{Kind: edgev1.RoomThread, ID: SafeID(ctx.SessionID())},
		Member: member,
		Agent:  agentID,
		Run:    SafeID(ctx.InvocationID()),
	}, nil
}

// SafeID returns id when the edge accepts it in a name, and otherwise a
// stable hash of it that it does.
func SafeID(id string) string {
	if edgev1.ValidID(id) {
		return id
	}
	digest := sha256.Sum256([]byte(id))
	return "h" + hex.EncodeToString(digest[:16])
}

func stateString(ctx agent.ReadonlyContext, key string) string {
	state := ctx.ReadonlyState()
	if state == nil {
		return ""
	}
	value, err := state.Get(key)
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

package authz

import (
	"context"
	"errors"
	"time"

	cedar "github.com/cedar-policy/cedar-go"
)

// Action is one of the things a Request can ask to do. It is also the id of
// the Cedar action entity the request names, Oryxa::Action::"<action>".
type Action string

const (
	Read       Action = "read"
	Search     Action = "search"
	Administer Action = "administer" // see and change a resource's policies
)

// Principal is who asks: a member, or an agent acting for that member. An
// agent has no identity of its own here; Agent is enough to tell the two
// apart, because every baseline rule that treats an agent differently — it
// never inherits acts_as_admin, but it does inherit its member's ownership —
// only needs to know which one the caller is, not which agent.
type Principal struct {
	UserID string
	Agent  bool
}

// Scope names what a stored policy is attached to: the whole organization
// (Type "organization", ID "") or one resource (e.g. Type "knowledge_base",
// ID a corpus id). It is the unit Store keys policies by and the unit an
// admin manages them by; which scopes apply to a given request is decided by
// the Resource being asked about (see Resource).
type Scope struct {
	Type string
	ID   string
}

// organizationScope is every request's own scope: a stored policy attached
// to the organization applies to every resource in it.
func organizationScope() Scope { return Scope{Type: "organization"} }

// Resource is anything the model governs. Its method set is unexported, so
// only this package can satisfy it — every resource type is defined here,
// which is what lets Engine build the right Cedar entities and know which
// stored policies apply without a type switch at every call site.
type Resource interface {
	// entityUID is the Cedar entity this resource is, e.g.
	// Oryxa::KnowledgeBase::"<id>".
	entityUID() cedar.EntityUID
	// entities is this resource's own Cedar entity, attributes included, plus
	// one entity per ancestor up to and including its Organization. A policy
	// can only read what is in this set: a Document's baseline read rule
	// reaches its knowledge base's scope and owner through the knowledge_base_id
	// attribute set here, precisely because that entity is included alongside
	// the document's own.
	entities() []cedar.Entity
	// scopes lists the Scopes whose stored policies apply to this resource:
	// the organization's, plus the resource's own and its ancestors'.
	scopes() []Scope
}

// Context carries facts the calling service has already established, because
// they need information Cedar cannot look up for itself: team membership
// comes from the teams service, and admin reach depends on whether the
// caller is a person or an agent acting for one.
type Context struct {
	// ActsAsAdmin is true for an organization owner or admin acting as
	// themselves. It is false for an agent acting for an admin — an agent
	// never gets an admin's reach, only its own member's — and the caller
	// computes it accordingly rather than this package inferring it from
	// Principal.Agent alone, since only the caller knows the member's role.
	ActsAsAdmin bool
	// TeamMember is true when the member belongs to the team a team-scoped
	// resource is placed on. It is meaningless, and ignored, for a resource
	// of any other scope.
	TeamMember bool
}

// Request is one authorization question: can Principal take Action on
// Resource, within OrganizationID, given the facts in Context.
type Request struct {
	OrganizationID string
	Principal      Principal
	Action         Action
	Resource       Resource
	Context        Context
}

// Decision is the answer to a Request.
type Decision struct {
	Allowed bool
	// Policies holds the ids of the policies that determined the decision:
	// the baseline permits if nothing narrowed them, or the stored forbids
	// that denied it otherwise. Empty when nothing matched, including when a
	// baseline evaluation error forced a denial (see Engine.Authorize).
	Policies []string
}

// Policy is one organization's stored narrowing rule: Cedar text, attached to
// a Scope, that may only forbid (see ValidatePolicy). Text may hold more than
// one forbid statement.
type Policy struct {
	ID, OrganizationID    string
	Scope                 Scope
	Name, Text, CreatedBy string
	CreatedAt, UpdatedAt  time.Time
}

// PolicyStore is what an Engine reads stored policies from. *Store, backed by
// PostgreSQL, is the one implementation today; Engine asks for every policy
// an organization has, once per cache fill, rather than one Scope at a time,
// because a single request or a search result list may need several Scopes'
// worth at once (see Engine.AuthorizeEach).
type PolicyStore interface {
	OrganizationPolicies(ctx context.Context, organizationID string) ([]Policy, error)
}

// Errors ValidatePolicy, Store and Engine return. Engine.Authorize itself
// never returns ErrInvalid or ErrConflict — those belong to writing a policy,
// not evaluating one — but it passes through whatever PolicyStore returns,
// which for *Store also draws from this set.
var (
	ErrInvalid  = errors.New("authz: invalid policy")
	ErrNotFound = errors.New("authz: not found")
	ErrConflict = errors.New("authz: conflict")
)

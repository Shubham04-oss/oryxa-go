package authz

import (
	cedar "github.com/cedar-policy/cedar-go"
)

// Cedar entity types, namespace Oryxa. Kept as untyped constants of
// cedar.EntityType so every reference to "the KnowledgeBase entity type"
// goes through one name instead of a repeated string literal; see
// schema.cedarschema for the model these describe.
const (
	entityTypeOrganization  cedar.EntityType = "Oryxa::Organization"
	entityTypeMember        cedar.EntityType = "Oryxa::Member"
	entityTypeAgent         cedar.EntityType = "Oryxa::Agent"
	entityTypeKnowledgeBase cedar.EntityType = "Oryxa::KnowledgeBase"
	entityTypeDocument      cedar.EntityType = "Oryxa::Document"
	entityTypeAction        cedar.EntityType = "Oryxa::Action"

	scopeKnowledgeBase = "knowledge_base"
)

// actionUID is the Cedar entity a Request's Action names.
func actionUID(action Action) cedar.EntityUID {
	return cedar.NewEntityUID(entityTypeAction, cedar.String(action))
}

// organizationEntity is the Cedar entity every Member, KnowledgeBase and
// Document is ultimately parented to. It carries no attributes: nothing in
// the baseline or in a stored policy needs more than the organization's
// identity, and org membership itself is established by the caller before
// Engine.Authorize is ever asked (see Request.OrganizationID).
func organizationEntity(organizationID string) cedar.Entity {
	return cedar.Entity{UID: cedar.NewEntityUID(entityTypeOrganization, cedar.String(organizationID))}
}

// principalEntities builds the Cedar entity a Request's Principal is, plus
// every entity needed to resolve it: a plain member is Oryxa::Member with the
// organization as its parent; an agent is Oryxa::Agent with the member it
// acts for as its parent. Parenting the agent to the member, rather than
// giving it the member's own id, is what lets a baseline rule like "member_id
// is the caller" be written once, as `principal in resource.member_id`: that
// check is reflexive for the member itself and walks the one parent edge for
// an agent acting for them (see KnowledgeBase.entities).
func principalEntities(organizationID string, principal Principal) (uid cedar.EntityUID, entities []cedar.Entity) {
	organization := organizationEntity(organizationID)
	member := cedar.Entity{
		UID:     cedar.NewEntityUID(entityTypeMember, cedar.String(principal.UserID)),
		Parents: cedar.NewEntityUIDSet(organization.UID),
	}
	if !principal.Agent {
		return member.UID, []cedar.Entity{member, organization}
	}
	agent := cedar.Entity{
		UID:     cedar.NewEntityUID(entityTypeAgent, cedar.String(principal.UserID)),
		Parents: cedar.NewEntityUIDSet(member.UID),
	}
	return agent.UID, []cedar.Entity{agent, member, organization}
}

// KnowledgeBase is a rag corpus as this package sees it: enough to place it
// (Scope, TeamID, MemberID) and to attribute it (OrganizationID, CreatedBy).
type KnowledgeBase struct {
	ID, OrganizationID string
	Scope              string // "member" | "team" | "organization"
	TeamID, MemberID   string
	CreatedBy          string
}

func (kb KnowledgeBase) entityUID() cedar.EntityUID {
	return cedar.NewEntityUID(entityTypeKnowledgeBase, cedar.String(kb.ID))
}

// entities is this knowledge base and its Organization. member_id is set as
// a reference to the Oryxa::Member entity it names, not as a string, so a
// policy can test ownership with `principal in resource.member_id` rather
// than needing an id comparison Cedar has no operator for; team_id stays a
// plain string because teams are not a Cedar entity type yet (team
// membership is decided by the caller and arrives as Context.TeamMember
// instead). Both are omitted, rather than sent empty, when the knowledge
// base is not placed on a member or team, so a policy can guard access to
// them with has instead of a comparison against "".
func (kb KnowledgeBase) entities() []cedar.Entity {
	organization := organizationEntity(kb.OrganizationID)
	attributes := cedar.RecordMap{
		"organization_id": cedar.String(kb.OrganizationID),
		"scope":           cedar.String(kb.Scope),
		"created_by":      cedar.String(kb.CreatedBy),
	}
	if kb.TeamID != "" {
		attributes["team_id"] = cedar.String(kb.TeamID)
	}
	if kb.MemberID != "" {
		attributes["member_id"] = cedar.NewEntityUID(entityTypeMember, cedar.String(kb.MemberID))
	}
	self := cedar.Entity{
		UID:        kb.entityUID(),
		Parents:    cedar.NewEntityUIDSet(organization.UID),
		Attributes: cedar.NewRecord(attributes),
	}
	return []cedar.Entity{self, organization}
}

// scopes is the organization's policies plus this knowledge base's own.
func (kb KnowledgeBase) scopes() []Scope {
	return []Scope{organizationScope(), {Type: scopeKnowledgeBase, ID: kb.ID}}
}

// Document is one document inside a KnowledgeBase. It embeds the knowledge
// base it belongs to, rather than just its id, because deciding whether a
// document is readable needs the knowledge base's placement (see
// policies/knowledge_bases.cedar's baseline/documents/read), and this is the
// only way this package's entity-building code — which never does I/O — can
// reach it.
type Document struct {
	KnowledgeBase KnowledgeBase
	ID            string
	Access        string // "", "members" or "admins"; "" means members
	Metadata      map[string]string
}

func (document Document) entityUID() cedar.EntityUID {
	return cedar.NewEntityUID(entityTypeDocument, cedar.String(document.ID))
}

// entities is this document, its knowledge base and the knowledge base's
// Organization. knowledge_base_id is a reference to the Oryxa::KnowledgeBase
// entity, not its id as a string, so that a policy on the document can chain
// through it to the knowledge base's own attributes: resource.knowledge_base_id.scope,
// for instance, is how the baseline read rule reaches placement without
// duplicating it onto every document. access and metadata are always
// present, even when empty, so a policy never needs `has access`: "" is
// itself a meaningful value (see VisibleTo's rules), and an always-present
// empty metadata record makes `resource.metadata has x` false rather than an
// error when a document was never tagged.
func (document Document) entities() []cedar.Entity {
	knowledgeBaseEntities := document.KnowledgeBase.entities()
	metadata := make(cedar.RecordMap, len(document.Metadata))
	for key, value := range document.Metadata {
		metadata[cedar.String(key)] = cedar.String(value)
	}
	self := cedar.Entity{
		UID:     document.entityUID(),
		Parents: cedar.NewEntityUIDSet(document.KnowledgeBase.entityUID()),
		Attributes: cedar.NewRecord(cedar.RecordMap{
			"organization_id":   cedar.String(document.KnowledgeBase.OrganizationID),
			"knowledge_base_id": document.KnowledgeBase.entityUID(),
			"access":            cedar.String(document.Access),
			"metadata":          cedar.NewRecord(metadata),
		}),
	}
	return append([]cedar.Entity{self}, knowledgeBaseEntities...)
}

// scopes is the organization's policies plus this document's knowledge
// base's — a document never carries stored policies of its own (see Scope).
func (document Document) scopes() []Scope {
	return []Scope{organizationScope(), {Type: scopeKnowledgeBase, ID: document.KnowledgeBase.ID}}
}

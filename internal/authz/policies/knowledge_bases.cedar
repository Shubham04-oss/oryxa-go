// Baseline authorization for knowledge bases and their documents.
//
// These statements are the whole ceiling on what any request against a
// knowledge base or a document can be granted: they are the only permits
// Engine ever evaluates (see engine.go), so a stored organization policy can
// only forbid something these already allow, never grant something they do
// not. They reproduce, exactly, the rules rag enforced before this package
// existed — see rag/internal/application/service.go's mayRead and
// rag/internal/domain/document.go's VisibleTo.
//
// Every statement carries an @id annotation. That annotation, not the
// statement's position in this file, is the id Engine reports in
// Decision.Policies, so statements below may be reordered freely without
// changing what a caller sees.

// A knowledge base placed on the whole organization is readable and
// searchable by anyone in it; the caller has already established that the
// principal belongs to the organization before asking (see Request).
@id("baseline/knowledge_bases/read_search_organization")
permit (
    principal,
    action in [Oryxa::Action::"read", Oryxa::Action::"search"],
    resource is Oryxa::KnowledgeBase
) when {
    resource.scope == "organization"
};

// A knowledge base placed on a member is readable and searchable by that
// member, by an agent acting for them (principal in resource.member_id is
// reflexive for the member and walks the one parent edge for their agent —
// see principalEntities), or by anyone acting with an org admin's reach.
@id("baseline/knowledge_bases/read_search_member")
permit (
    principal,
    action in [Oryxa::Action::"read", Oryxa::Action::"search"],
    resource is Oryxa::KnowledgeBase
) when {
    resource.scope == "member" &&
    ((resource has member_id && principal in resource.member_id) || context.acts_as_admin)
};

// A knowledge base placed on a team is readable and searchable by an org
// admin or by a member of that team. Team membership is not a Cedar entity
// yet, so it arrives as Context.TeamMember, computed by the caller from the
// teams service rather than looked up here.
@id("baseline/knowledge_bases/read_search_team")
permit (
    principal,
    action in [Oryxa::Action::"read", Oryxa::Action::"search"],
    resource is Oryxa::KnowledgeBase
) when {
    resource.scope == "team" &&
    (context.acts_as_admin || context.team_member)
};

// Seeing and changing a knowledge base's own stored policies is an org
// admin's job regardless of placement.
@id("baseline/knowledge_bases/administer")
permit (
    principal,
    action == Oryxa::Action::"administer",
    resource is Oryxa::KnowledgeBase
) when {
    context.acts_as_admin
};

// A document is readable when its knowledge base would be readable under the
// three rules above — reached through knowledge_base_id, a reference to the
// Oryxa::KnowledgeBase entity rather than a plain id, precisely so this
// condition can read the knowledge base's own scope, member_id and team_id —
// and its own access does not narrow it further: "" and "members" are
// readable by anyone the knowledge base already let in, "admins" only by an
// org admin, and any other value is refused rather than guessed at.
@id("baseline/documents/read")
permit (
    principal,
    action == Oryxa::Action::"read",
    resource is Oryxa::Document
) when {
    (
        resource.knowledge_base_id.scope == "organization" ||
        (resource.knowledge_base_id.scope == "member" &&
            ((resource.knowledge_base_id has member_id && principal in resource.knowledge_base_id.member_id) ||
                context.acts_as_admin)) ||
        (resource.knowledge_base_id.scope == "team" &&
            (context.acts_as_admin || context.team_member))
    ) &&
    (resource.access == "" || resource.access == "members" ||
        (resource.access == "admins" && context.acts_as_admin))
};

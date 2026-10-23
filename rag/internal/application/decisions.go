package application

import (
	"context"
	"errors"

	"github.com/Shubham04-oss/oryxa-go/internal/access"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// Recorder keeps the access decisions rag makes, so a member can see on their
// Activity page who reached their corpora and documents, and why
// (docs/design/permissions, §6 step 3). *access.Log is the one rag uses.
type Recorder interface {
	Record(ctx context.Context, decision access.Decision)
}

// WithDecisions sets where the service records its access decisions. Without
// one it records nothing.
func (service *Service) WithDecisions(decisions Recorder) *Service {
	service.decisions = decisions
	return service
}

// WithShadow sets where the service's decisions are compared with SpiceDB.
// Without one it compares nothing.
func (service *Service) WithShadow(shadow access.Comparer) *Service {
	service.shadow = shadow
	return service
}

// compare asks SpiceDB in shadow what mayRead, mayWrite or mayDelete just
// decided about a corpus; their answer stands. A failure that decided
// nothing, such as teams being unreachable, isn't compared.
func (service *Service) compare(ctx context.Context, member domain.OrgMembership, corpus domain.Corpus, action string, err error) {
	if service.shadow == nil {
		return
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrForbidden) {
		return
	}
	ask := access.Ask{
		OrganizationID: corpus.OrganizationID, Service: "rag", Action: action,
		ResourceKind: resourceCorpus, ResourceID: corpus.ID, UserID: member.UserID,
		Actor:   access.Actor{Role: member.Role, Agent: member.Agent, AgentRole: member.AgentRole},
		Limited: member.CorporaLimited(),
	}
	if member.Agent {
		ask.AgentID = member.AgentID
	}
	service.shadow.Compare(ctx, ask, err == nil)
}

// Resource kinds in the decision log.
const (
	resourceCorpus   = "corpus"
	resourceDocument = "document"
)

// The rule a decision names is the most specific one the check actually
// needed, in this order:
//
//   - grant: a specialist whose grant names some corpora, this one among
//     them. Its grant is what narrowed it to this corpus, so it says more than
//     whatever its member's place would.
//   - owner: the corpus is the caller's own member corpus, or, to delete a
//     team corpus, one they made.
//   - org_admin: an org owner's or admin's own session reaching what is not
//     theirs: another member's corpus, any team's, writing to or deleting an
//     organization corpus, or reading an admin-only document.
//   - team_admin: deleting a team corpus they did not make, which takes
//     managing the team.
//   - team_member: reading or writing a team corpus, which being on the team
//     is enough for.
//   - organization: reading an organization corpus, open to every member.
//
// granted applies the first of these: a limited specialist's reach is its
// grant whatever placement let its member in.
func granted(member domain.OrgMembership, rule string) string {
	if member.CorporaLimited() {
		return access.ReasonGrant
	}
	return rule
}

// record keeps one allow. Recording never fails the request: the allow was
// already made (see access.Log.Record).
func (service *Service) record(ctx context.Context, member domain.OrgMembership, corpus domain.Corpus, action, kind, resourceID, rule string) {
	if service.decisions == nil {
		return
	}
	decision := access.Decision{
		OrganizationID: corpus.OrganizationID,
		Service:        "rag",
		Action:         action,
		ResourceKind:   kind,
		ResourceID:     resourceID,
		ActorUserID:    member.UserID,
		Reason:         rule,
	}
	// A member corpus is its member's; a team's or the organization's is
	// nobody's in particular.
	if corpus.Scope == domain.ScopeMember {
		decision.OwnerID = corpus.MemberID
	}
	if member.Agent {
		decision.ActorAgentID, decision.ActorAgentRole = member.AgentID, member.AgentRole
	}
	service.decisions.Record(ctx, decision)
}

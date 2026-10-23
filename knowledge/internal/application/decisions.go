package application

import (
	"context"
	"errors"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// Recorder keeps the access decisions knowledge makes, so a member can see on
// their Activity page who reached their sources and documents, and why
// (docs/design/permissions, §6 step 3). *access.Log is one; it keeps only
// what is worth keeping and never fails the caller.
type Recorder interface {
	Record(context.Context, access.Decision)
}

// Resource kinds knowledge records decisions about.
const (
	resourceSource   = "source"
	resourceDocument = "document"
)

// record notes an allow that mayRead (and, for a document, its access label)
// already made. It is called at single decisions only: Sources decides each
// item it lists with mayRead too, and a listing is not a member reaching
// each thing in it, so mayRead itself records nothing.
func (s *Service) record(ctx context.Context, caller domain.OrgMembership, action, kind, id string, source domain.Source) {
	if s.recorder == nil {
		return
	}
	decision := access.Decision{
		OrganizationID: source.OrganizationID,
		Service:        s.config.ServiceName,
		Action:         action,
		ResourceKind:   kind,
		ResourceID:     id,
		OwnerID:        source.CreatedBy,
		ActorUserID:    caller.UserID,
		Reason:         reason(caller, source),
	}
	if caller.Agent {
		decision.ActorAgentID, decision.ActorAgentRole = caller.AgentID, caller.AgentRole
	}
	s.recorder.Record(ctx, decision)
}

// compare asks SpiceDB in shadow what mayRead just decided about a source.
func (s *Service) compare(ctx context.Context, caller domain.OrgMembership, source domain.Source, action string, err error) {
	if s.shadow == nil {
		return
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrForbidden) {
		return
	}
	ask := access.Ask{
		OrganizationID: source.OrganizationID, Service: s.config.ServiceName, Action: action,
		ResourceKind: resourceSource, ResourceID: source.ID, UserID: caller.UserID,
		Actor:   access.Actor{Role: caller.Role, Agent: caller.Agent, AgentRole: caller.AgentRole},
		Limited: caller.KnowledgeLimited(),
	}
	if caller.Agent {
		ask.AgentID = caller.AgentID
	}
	s.shadow.Compare(ctx, ask, err == nil)
}

// reason is the rule that let caller reach source, the most specific one
// actually needed, in this order:
//
//   - grant: a specialist whose knowledge grant names the source. Without the
//     name it would have been refused, whoever made the source, so the grant
//     is what decided.
//   - owner: the caller made it (a specialist whose grant covers everything
//     is its member here, so ownership decides).
//   - org_admin: an owner's or admin's own session reaching someone else's.
//
// mayRead admits nobody else, so these three cover every allow.
func reason(caller domain.OrgMembership, source domain.Source) string {
	switch {
	case caller.KnowledgeLimited():
		return access.ReasonGrant
	case source.CreatedBy == caller.UserID:
		return access.ReasonOwner
	default:
		return access.ReasonOrgAdmin
	}
}

package application

import (
	"context"
	"errors"
	"fmt"

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

// Relationships writes a source's relationships to SpiceDB as it is
// registered. Knowledge publishes no event for a source, so without this the
// nightly reconcile is the only writer. A removed source keeps them: its row
// stays, and its creator and admins can still read it and why it went.
type Relationships interface {
	SourceRegistered(ctx context.Context, source domain.Source) error
}

func (s *Service) sourceRegistered(ctx context.Context, source domain.Source) {
	if s.relationships == nil {
		return
	}
	// The source stands either way: the nightly reconcile writes it too.
	if err := s.relationships.SourceRegistered(ctx, source); err != nil {
		s.logger.WarnContext(ctx, "source relationships not written to spicedb; the reconcile will", "source_id", source.ID, "error", err)
	}
}

// decide returns what the caller gets for action on a source, given the
// code's answer, as files and rag do. With no engine the code's answer stands
// and SpiceDB is asked in shadow. With one, SpiceDB decides but may only
// narrow: the caller gets in only when both allow. Where SpiceDB has no answer
// (an agent changing a source, the org agent) or the code decided nothing, the
// code's answer stands, and a SpiceDB failure refuses. Decide records every
// difference.
func (s *Service) decide(ctx context.Context, caller domain.OrgMembership, source domain.Source, action string, code error) error {
	decided := code == nil || errors.Is(code, domain.ErrNotFound) || errors.Is(code, domain.ErrForbidden)
	if s.engine == nil || !decided {
		if decided && s.shadow != nil {
			s.shadow.Compare(ctx, s.ask(caller, source, action), code == nil)
		}
		return code
	}
	allowed, err := s.engine.Decide(ctx, s.ask(caller, source, action), code == nil)
	switch {
	case errors.Is(err, access.ErrNoAnswer):
		return code
	case err != nil:
		return fmt.Errorf("decide access to source %s: %w", source.ID, err)
	case code != nil:
		return code
	case allowed:
		return nil
	default:
		return domain.ErrNotFound
	}
}

// decideMany is decide for a listing, with one round trip to SpiceDB for all
// of it: codes are the code's answers, and each comes back as decide would
// answer it.
func (s *Service) decideMany(ctx context.Context, caller domain.OrgMembership, sources []domain.Source, action string, codes []error) ([]error, error) {
	if s.engine == nil {
		for i, source := range sources {
			codes[i] = s.decide(ctx, caller, source, action, codes[i])
		}
		return codes, nil
	}
	var (
		asks    []access.Ask
		allowed []bool
		rows    []int
	)
	for i, source := range sources {
		if codes[i] == nil || errors.Is(codes[i], domain.ErrNotFound) || errors.Is(codes[i], domain.ErrForbidden) {
			asks, allowed, rows = append(asks, s.ask(caller, source, action)), append(allowed, codes[i] == nil), append(rows, i)
		}
	}
	allowed, err := s.engine.DecideMany(ctx, asks, allowed)
	if err != nil {
		return nil, fmt.Errorf("decide access to %d sources: %w", len(asks), err)
	}
	for j, i := range rows {
		if codes[i] == nil && !allowed[j] {
			codes[i] = domain.ErrNotFound
		}
	}
	return codes, nil
}

// ask is a decision on a source in the terms SpiceDB is asked about it.
func (s *Service) ask(caller domain.OrgMembership, source domain.Source, action string) access.Ask {
	ask := access.Ask{
		OrganizationID: source.OrganizationID, Service: s.config.ServiceName, Action: action,
		ResourceKind: resourceSource, ResourceID: source.ID, UserID: caller.UserID,
		Actor:   access.Actor{Role: caller.Role, Agent: caller.Agent, AgentRole: caller.AgentRole},
		Limited: caller.KnowledgeLimited(),
	}
	if caller.Agent {
		ask.AgentID = caller.AgentID
	}
	return ask
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

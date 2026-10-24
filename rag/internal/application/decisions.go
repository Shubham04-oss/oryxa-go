package application

import (
	"context"
	"errors"
	"fmt"

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

// Relationships keeps a corpus's relationships in SpiceDB as the corpus comes
// and goes. The projector also writes them from the corpus's events; this
// only makes them there sooner, so a corpus can be read the moment it exists.
type Relationships interface {
	CorpusCreated(ctx context.Context, corpus domain.Corpus) error
	CorpusDeleted(ctx context.Context, organizationID, corpusID string) error
}

// WithEngine makes SpiceDB decide corpus access, and writes each corpus's
// relationships as it is created or deleted.
func (service *Service) WithEngine(engine access.Decider, relationships Relationships) *Service {
	service.engine, service.relationships = engine, relationships
	return service
}

func (service *Service) corpusCreated(ctx context.Context, corpus domain.Corpus) {
	if service.relationships == nil {
		return
	}
	// The corpus stands either way: its event brings the projector here too.
	if err := service.relationships.CorpusCreated(ctx, corpus); err != nil {
		service.logger.WarnContext(ctx, "corpus relationships not written to spicedb; the projector will", "corpus_id", corpus.ID, "error", err)
	}
}

func (service *Service) corpusDeleted(ctx context.Context, organizationID, corpusID string) {
	if service.relationships == nil {
		return
	}
	if err := service.relationships.CorpusDeleted(ctx, organizationID, corpusID); err != nil {
		service.logger.WarnContext(ctx, "corpus relationships not deleted from spicedb; the projector will", "corpus_id", corpusID, "error", err)
	}
}

// decide returns what the caller gets for action on a corpus, given the
// code's answer, as files does. With no engine the code's answer stands and
// SpiceDB is asked in shadow. With one, SpiceDB decides but may only narrow:
// a revoke reaches SpiceDB a moment after the code sees it, so the caller gets
// in only when both allow. Where SpiceDB has no answer (the org agent) or the
// code decided nothing (teams unreachable) the code's answer stands, and a
// SpiceDB failure refuses. Decide records every difference.
func (service *Service) decide(ctx context.Context, member domain.OrgMembership, corpus domain.Corpus, action string, code error) error {
	decided := code == nil || errors.Is(code, domain.ErrNotFound) || errors.Is(code, domain.ErrForbidden)
	if service.engine == nil || !decided {
		if decided && service.shadow != nil {
			service.shadow.Compare(ctx, ask(member, corpus, action), code == nil)
		}
		return code
	}
	allowed, err := service.engine.Decide(ctx, ask(member, corpus, action), code == nil)
	switch {
	case errors.Is(err, access.ErrNoAnswer):
		return code
	case err != nil:
		return fmt.Errorf("decide access to corpus %s: %w", corpus.ID, err)
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
func (service *Service) decideMany(ctx context.Context, member domain.OrgMembership, corpora []domain.Corpus, action string, codes []error) ([]error, error) {
	if service.engine == nil {
		for i, corpus := range corpora {
			codes[i] = service.decide(ctx, member, corpus, action, codes[i])
		}
		return codes, nil
	}
	var (
		asks    []access.Ask
		allowed []bool
		rows    []int
	)
	for i, corpus := range corpora {
		if codes[i] == nil || errors.Is(codes[i], domain.ErrNotFound) || errors.Is(codes[i], domain.ErrForbidden) {
			asks, allowed, rows = append(asks, ask(member, corpus, action)), append(allowed, codes[i] == nil), append(rows, i)
		}
	}
	allowed, err := service.engine.DecideMany(ctx, asks, allowed)
	if err != nil {
		return nil, fmt.Errorf("decide access to %d corpora: %w", len(asks), err)
	}
	for j, i := range rows {
		if codes[i] == nil && !allowed[j] {
			codes[i] = domain.ErrNotFound
		}
	}
	return codes, nil
}

// ask is a decision on a corpus in the terms SpiceDB is asked about it.
func ask(member domain.OrgMembership, corpus domain.Corpus, action string) access.Ask {
	ask := access.Ask{
		OrganizationID: corpus.OrganizationID, Service: "rag", Action: action,
		ResourceKind: resourceCorpus, ResourceID: corpus.ID, UserID: member.UserID,
		Actor:   access.Actor{Role: member.Role, Agent: member.Agent, AgentRole: member.AgentRole},
		Limited: member.CorporaLimited(),
	}
	if member.Agent {
		ask.AgentID = member.AgentID
	}
	return ask
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

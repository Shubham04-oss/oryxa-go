package application

import (
	"context"
	"errors"

	"github.com/Shubham04-oss/oryxa-go/internal/access"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// reads decides, for one request, which corpora a caller may read. A single
// read and a listing both ask it, so the two cannot disagree about any corpus
// (docs/design/permissions, G3); a listing filters in SQL first only to read
// fewer rows, and what it returns is whatever survives mayRead here.
//
// Teams is asked once per team for the whole request, so a listing of many
// corpora on one team makes one call, not one per corpus.
type reads struct {
	service     *Service
	credentials authclient.Credentials
	member      domain.OrgMembership
	teams       map[string]teamAnswer
}

type teamAnswer struct {
	access domain.TeamAccess
	err    error
}

func (service *Service) reads(credentials authclient.Credentials, member domain.OrgMembership) *reads {
	return &reads{service: service, credentials: credentials, member: member, teams: map[string]teamAnswer{}}
}

// mayRead decides who can search a corpus. Placement mirrors files exactly.
// An org admin's reach is a person's own: an agent acting for one reaches
// what a plain member would (see domain.OrgMembership.ActsAsAdmin).
//
// It answers the rule that let the caller in (see granted), which a single
// read records and a listing ignores.
func (reads *reads) mayRead(ctx context.Context, corpus domain.Corpus) (string, error) {
	rule, err := reads.decide(ctx, corpus)
	if err = reads.service.decide(ctx, reads.member, corpus, access.ActionRead, err); err != nil {
		return "", err
	}
	return rule, nil
}

func (reads *reads) decide(ctx context.Context, corpus domain.Corpus) (string, error) {
	member := reads.member
	// Every read, and every write, reaches a corpus through here, so a
	// specialist's grant is enforced once.
	if err := member.MayReadCorpus(corpus.ID); err != nil {
		return "", err
	}
	switch corpus.Scope {
	case domain.ScopeOrganization:
		return granted(member, access.ReasonOrganization), nil
	case domain.ScopeMember:
		if corpus.MemberID == member.UserID {
			return granted(member, access.ReasonOwner), nil
		}
		if member.ActsAsAdmin() {
			return access.ReasonOrgAdmin, nil
		}
		return "", domain.ErrNotFound
	case domain.ScopeTeam:
		if member.ActsAsAdmin() {
			return access.ReasonOrgAdmin, nil
		}
		if _, err := reads.team(ctx, corpus.OrganizationID, corpus.TeamID); err != nil {
			return "", err
		}
		return granted(member, access.ReasonTeamMember), nil
	default:
		return "", domain.ErrNotFound
	}
}

// team is teamAccess, asked at most once per team in this request.
func (reads *reads) team(ctx context.Context, organizationID, teamID string) (domain.TeamAccess, error) {
	if answer, ok := reads.teams[teamID]; ok {
		return answer.access, answer.err
	}
	access, err := reads.service.teamAccess(ctx, reads.credentials, reads.member, organizationID, teamID)
	reads.teams[teamID] = teamAnswer{access: access, err: err}
	return access, err
}

// readable keeps the corpora mayRead lets the caller read, in order. A corpus
// it refuses reads as missing and is left out; any other failure, such as
// teams being unreachable, fails the listing rather than quietly shortening
// it.
//
// SpiceDB is asked about the whole listing in one round trip.
func (reads *reads) readable(ctx context.Context, corpora []domain.Corpus) ([]domain.Corpus, error) {
	codes := make([]error, len(corpora))
	for i, corpus := range corpora {
		_, codes[i] = reads.decide(ctx, corpus)
	}
	codes, err := reads.service.decideMany(ctx, reads.member, corpora, access.ActionRead, codes)
	if err != nil {
		return nil, err
	}
	kept := corpora[:0]
	for i, corpus := range corpora {
		switch err := codes[i]; {
		case err == nil:
			kept = append(kept, corpus)
		case errors.Is(err, domain.ErrNotFound):
		default:
			return nil, err
		}
	}
	return kept, nil
}

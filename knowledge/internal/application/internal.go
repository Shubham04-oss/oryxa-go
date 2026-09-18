package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// A document too large to travel is fetched rather than lost.
//
// An event carries a body up to MaxBodyBytes and flags the rest as omitted.
// A retrieval pipeline that could not read those had two bad choices: index
// nothing for the document, or drop the version it already held because that
// version no longer matches. Both mean the largest documents an organization
// has — the handbook, the specification, the long thread — are the ones it
// cannot search.
//
// So there is a way in for a service. It is deliberately narrow: one
// document's body, named by an id nothing guesses, to a caller that proved
// which service it is. It answers no listing, no search and nothing about
// who may read the document, because a service asking this has no member to
// be, and a route that answered more would be a way around the rules that
// decide what a member can see.

// DocumentBody answers one document's body to a service that has already
// been identified by its token. The caller decides nothing about access:
// this is only reachable from the internal route, and that route admits only
// the services a deployment names.
func (s *Service) DocumentBody(ctx context.Context, organizationID, documentID string) (domain.Document, error) {
	organizationID = strings.TrimSpace(organizationID)
	documentID = strings.TrimSpace(documentID)
	if organizationID == "" || documentID == "" {
		return domain.Document{}, fmt.Errorf("an organization and a document are required: %w", domain.ErrInvalid)
	}
	// Scoped by organization as well as id, so a service that had the wrong
	// organization reads nothing rather than another's document.
	return s.store.Document(ctx, organizationID, documentID)
}

package interactions

import (
	"context"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// A lease is what approving a request with access gives its asker: for
// LeaseTTL, or less where the organization expires consent sooner, the same
// member's follow-ups to the same owner in the same Slack thread are created
// approved, with no click, decided as the lease's (DecidedByLease, naming the
// owner who gave it and the lease). A follow-up is otherwise an ordinary
// request: the router's screen has already read it, and the answer guard reads
// what answers it as it reads any other. It is only the owner's ask that is
// answered ahead of time.
//
// The owner ends a lease with RevokeLease. From then on their follow-ups ask
// again, and no answer to a request approved under it goes out: an answer that
// comes after a revoke is not delivered (Store.Respond closes the request
// instead, ErrRevoked). A lease also ends when it runs out.

// RevokeLease ends a lease its owner gave, as that member, in their own
// signed-in session, and records the revoked event. The actor must be marked
// agents.SessionMember, which only the HTTP API's member path does. Revoking a
// lease that already ended, by expiring or by being revoked, changes nothing and
// records nothing, and returns it as it is. A lease that is not the actor's to
// revoke, whoever's it is, is ErrNotFound.
func (service *Service) RevokeLease(ctx context.Context, actor agents.InteractionActor, id string) (Lease, error) {
	if actor.Session != agents.SessionMember {
		return Lease{}, fmt.Errorf("%w: only the member who gave a lease, in their own signed-in session, revokes it", ErrForbidden)
	}
	if err := validateActor(actor); err != nil {
		return Lease{}, err
	}
	if service.consents == nil {
		return Lease{}, ErrConsentUnavailable
	}
	id = strings.TrimSpace(id)
	if id == "" || !validID(id) {
		return Lease{}, ErrNotFound
	}
	lease, revoked, err := service.consents.RevokeLease(ctx, actor.OrganizationID, id, actor.UserID, service.now().UTC())
	if err != nil {
		return Lease{}, err
	}
	if revoked {
		service.logger.InfoContext(ctx, "an owner revoked a lease", "lease_id", lease.ID, "organization_id", lease.OrganizationID,
			"owner_user_id", lease.OwnerUserID, "asker_user_id", lease.AskerUserID)
	}
	return lease, nil
}

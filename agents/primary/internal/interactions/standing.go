package interactions

import (
	"context"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

// A request made in a teamspace is approved as its target's owner chose
// there when it was made, and that can change: the owner can take deciding
// back from a specialist, the member who asked can leave the team, the space
// can be disabled, an agent can lose its seat. So where the request stands
// is read again, live, before anything acts on it: when an agent decides it,
// when its owner approves it, and when the waker is about to wake an agent
// for it. A request whose owner has since taken deciding back is held for
// them; one that no longer stands at all is closed by Oryxa, with the
// reason: rejected while pending, failed once approved.

// ErrClosed reports a request made in a teamspace that no longer stands
// there, which Oryxa closed, with the reason, instead of letting it be
// decided.
var ErrClosed = errors.New("agent request no longer stands in its teamspace, so Oryxa closed it")

// errNoMemberToken is no token of the addressed member's to be had: they
// have not allowed agents to act for them while they are away, or this
// server cannot mint one.
var errNoMemberToken = errors.New("no token of the addressed member's can be had")

// Verdict is what re-reading a request made in a teamspace found, before the
// waker wakes an agent for it.
type Verdict string

const (
	// VerdictStands: the request still stands as it was decided: wake its
	// agent as usual.
	VerdictStands Verdict = "stands"
	// VerdictHeld: it is held for its owner's own decision: wake nobody.
	VerdictHeld Verdict = "held"
	// VerdictClosed: it no longer stands, and Oryxa closed it with the
	// reason: wake nobody. The closing is itself an event, which tells the
	// member who asked.
	VerdictClosed Verdict = "closed"
)

// standing is where a request made in a teamspace was found to stand.
type standing struct {
	seat agents.TeamspaceSeat
	// lapsed is why the request no longer stands, when it does not.
	lapsed string
	// unchecked is a request that could not be read again: no token of the
	// addressed member's can be had, or this server has no teamspaces.
	unchecked bool
}

// standingOf reads where a request made in a teamspace stands now, through
// the organization as its addressed member sees it: with token, their own
// session's, when the caller has it, and otherwise with memberToken.
func (service *Service) standingOf(ctx context.Context, request agents.Interaction, token string) (standing, error) {
	if service.teamspaces == nil {
		return standing{unchecked: true}, nil
	}
	if token == "" {
		var err error
		token, err = service.memberToken(ctx, request.Target.UserID)
		if errors.Is(err, errNoMemberToken) {
			return standing{unchecked: true}, nil
		}
		if err != nil {
			return standing{}, fmt.Errorf("get the addressed member's token to read the request's teamspace: %w", err)
		}
	}
	directory, err := service.directory.Load(ctx, request.OrganizationID, token)
	if err != nil {
		return standing{}, fmt.Errorf("load organization directory: %w", err)
	}
	now, err := service.teamspaces.RequestStanding(ctx, request.OrganizationID, directory, request.TeamspaceID, request.Requester, request.Target)
	if err != nil {
		return standing{}, fmt.Errorf("read the request's teamspace: %w", err)
	}
	return standing{seat: now.Seat, lapsed: now.Lapsed}, nil
}

// memberToken is a token of the addressed member's: their own, when the run
// acting for them carries it because they are here, and otherwise an agent
// token minted under their standing grant.
func (service *Service) memberToken(ctx context.Context, userID string) (string, error) {
	if token := memberauth.AccessToken(ctx); token != "" {
		return token, nil
	}
	if service.tokens == nil {
		return "", errNoMemberToken
	}
	token, _, err := service.tokens.AgentToken(ctx, userID, memberauth.Agent{})
	if errors.Is(err, memberauth.ErrGrantRequired) {
		return "", fmt.Errorf("%w: %w", errNoMemberToken, err)
	}
	return token, err
}

// ownersToDecide is whether a seat leaves deciding what is asked of it to
// its owner: every seat but a specialist its owner contributed with
// automatic delegation.
func ownersToDecide(seat agents.TeamspaceSeat) bool {
	return seat.Kind != "specialist" || seat.DelegationMode != "automatic"
}

// agentMayDecide reads again, before an agent decides a request made in a
// teamspace, whether deciding it is still the agent's. When its owner has
// since taken deciding back, or it cannot be read at all because no token of
// the member's can be had, the request is held for them and the agent told
// so; when it no longer stands, Oryxa closes it. Either way the agent's
// decision is refused.
func (service *Service) agentMayDecide(ctx context.Context, request agents.Interaction) error {
	found, err := service.standingOf(ctx, request, "")
	switch {
	case err != nil:
		return err
	case found.lapsed != "":
		return service.close(ctx, request, found.lapsed)
	case found.unchecked:
		return service.hold(ctx, request, "Oryxa could not check that its teamspace still leaves deciding it to you: your member has not allowed agents to act for them while they are away")
	case ownersToDecide(found.seat):
		return service.hold(ctx, request, "your member no longer leaves deciding it to you in its teamspace")
	}
	return nil
}

// ownerMayApprove reads again, before the member approves a request made in
// a teamspace in their own session, whether it still stands there. Their
// own decision works whatever standing they gave their agent, as long as the
// space and both seats remain; a request that no longer stands is closed.
func (service *Service) ownerMayApprove(ctx context.Context, request agents.Interaction, token string) error {
	if service.teamspaces == nil {
		return fmt.Errorf("%w: requests inside a teamspace are not available on this server", ErrInvalid)
	}
	found, err := service.standingOf(ctx, request, token)
	switch {
	case err != nil:
		return err
	case found.lapsed != "":
		return service.close(ctx, request, found.lapsed)
	}
	return nil
}

// Recheck reads again, before the waker wakes an agent for a request made in
// a teamspace, where the request stands. The waker calls it for the
// request's own delivery and for its approval's.
//
//   - A request that no longer stands is closed: VerdictClosed.
//   - A pending request is held, VerdictHeld, when it already is, when its
//     owner has since taken deciding back, and when it cannot be read again,
//     since no agent then may decide it; otherwise VerdictStands, and the
//     agent decides it, reading it again itself.
//   - An approved request stands when its member approved it in person, and
//     when an agent approved it that may still decide it. Otherwise — an
//     agent's approval of a request its owner has since taken back, or an
//     approval of a held request that records no decider, as an older
//     build's did — the approval is withdrawn and the request held for its
//     owner: VerdictHeld. An approval that cannot be read again stands: it
//     was read live when it was made.
//
// Anything else — a request outside any teamspace, or one no longer pending
// or approved — stands as it is.
func (service *Service) Recheck(ctx context.Context, request agents.Interaction) (agents.Interaction, Verdict, error) {
	if request.TeamspaceID == "" || request.Status != StatusPending && request.Status != StatusApproved {
		return request, VerdictStands, nil
	}
	found, err := service.standingOf(ctx, request, "")
	if err != nil {
		return request, "", err
	}
	now := service.now().UTC()
	if found.lapsed != "" {
		closed, err := service.repository.Close(ctx, request.ID, closedBecause(found.lapsed), now)
		if err != nil {
			return request, "", err
		}
		return closed, VerdictClosed, nil
	}
	takenBack := !found.unchecked && ownersToDecide(found.seat)
	if request.Status == StatusPending {
		if HeldForOwner(request) {
			return request, VerdictHeld, nil
		}
		if found.unchecked || takenBack {
			held, err := service.repository.Hold(ctx, request.ID, now)
			if err != nil {
				return request, "", err
			}
			return held, VerdictHeld, nil
		}
		return request, VerdictStands, nil
	}
	if DecidedByItsOwner(request) || !HeldForOwner(request) && !takenBack {
		return request, VerdictStands, nil
	}
	held, err := service.repository.Hold(ctx, request.ID, now)
	if err != nil {
		return request, "", err
	}
	return held, VerdictHeld, nil
}

// hold keeps a request for its owner's own decision and refuses the agent
// that asked to decide it, saying why.
func (service *Service) hold(ctx context.Context, request agents.Interaction, why string) error {
	if _, err := service.repository.Hold(ctx, request.ID, service.now().UTC()); err != nil {
		return err
	}
	return fmt.Errorf("%w: this request is now held for your member's own decision in Oryxa, because %s; no agent can approve or reject it, so tell your member it is waiting for them", ErrForbidden, why)
}

// close ends a request that no longer stands in its teamspace, and reports
// that it did.
func (service *Service) close(ctx context.Context, request agents.Interaction, lapsed string) error {
	if _, err := service.repository.Close(ctx, request.ID, closedBecause(lapsed), service.now().UTC()); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrClosed, lapsed)
}

// closedBecause is what a request Oryxa closed says, to both members, in its
// rejection or its failed result.
func closedBecause(lapsed string) string {
	return "Oryxa closed this request because " + lapsed + "."
}

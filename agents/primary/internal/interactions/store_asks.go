package interactions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// The Slack consent loop's side of the ledger.
//
// Locks are taken in one order, so that no two transactions wait for each
// other: an ask's row, then its requests' rows, then a lease's. Store.Respond,
// Store.Review, Store.Bypass and CreateAsk take the ask first; a request's own
// decisions (Decide, Consent, Close) take one request and, for access, one
// lease; and revoking a lease takes the lease alone.

const askColumns = `id, organization_id, origin, team_id, channel_id, thread_ts, trigger_ts,
	asker_user_id, kind, winner_request_id, bypass, created_at, settled_at`

const leaseColumns = `id, organization_id, asker_user_id, owner_user_id, channel_id, thread_ts,
	request_id, created_at, expires_at, revoked_at`

func scanAsk(row scanner, ask *Ask) error {
	return row.Scan(&ask.ID, &ask.OrganizationID, &ask.Origin, &ask.TeamID, &ask.ChannelID, &ask.ThreadTS, &ask.TriggerTS,
		&ask.AskerUserID, &ask.Kind, &ask.WinnerRequestID, &ask.Bypass, &ask.CreatedAt, &ask.SettledAt)
}

func scanLease(row scanner, lease *Lease) error {
	return row.Scan(&lease.ID, &lease.OrganizationID, &lease.AskerUserID, &lease.OwnerUserID, &lease.ChannelID, &lease.ThreadTS,
		&lease.RequestID, &lease.CreatedAt, &lease.ExpiresAt, &lease.RevokedAt)
}

// CreateAsk stores an ask and a request for each owner, in one transaction.
// An ask made again, which is how a retried Slack delivery makes it, stores
// nothing: it finds the ask by the message it was made from, and each request
// by its idempotency key. Each new request is recorded as requested, and, when
// an owner's live lease covers the asker in this thread, or a bypass was
// asked for, approved in the same transaction, under that lease or as that
// bypass, recorded as decided right after: the requested event comes first,
// as it does for a request an owner decides later, so the rest of the system
// sees the same two events either way.
func (store *Store) CreateAsk(ctx context.Context, ask Ask, requests []AskRequest, at time.Time) (Ask, []AskedRequest, error) {
	var stored Ask
	var created []AskedRequest
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		var err error
		if stored, err = insertAsk(ctx, tx, ask); err != nil {
			return err
		}
		created = make([]AskedRequest, 0, len(requests))
		bypassed := false
		for _, item := range requests {
			request := item.Request
			request.AskID = stored.ID
			row, inserted, err := insertRequest(ctx, tx, request, item.Key)
			if err != nil {
				return err
			}
			if !inserted {
				created = append(created, AskedRequest{Request: row})
				continue
			}
			if err := recordEvent(ctx, tx, agentsv1.InteractionRequestedDetailType, row); err != nil {
				return err
			}
			decided, viaBypass, err := decideAtCreation(ctx, tx, stored, row, item, at)
			if err != nil {
				return err
			}
			bypassed = bypassed || viaBypass
			created = append(created, AskedRequest{Request: decided, Created: true})
		}
		if bypassed && !stored.Bypass {
			stored.Bypass = true
			if _, err := tx.Exec(ctx, `UPDATE agents_primary.interaction_asks SET bypass=true WHERE id=$1`, stored.ID); err != nil {
				return fmt.Errorf("mark an ask bypassed: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Ask{}, nil, fmt.Errorf("create ask: %w", err)
	}
	return stored, created, nil
}

// insertAsk stores an ask, or finds the one the same message already made,
// and returns it locked: a row this transaction inserted is locked by the
// insert, and one found is locked now.
func insertAsk(ctx context.Context, tx pgx.Tx, ask Ask) (Ask, error) {
	var stored Ask
	err := scanAsk(tx.QueryRow(ctx, `INSERT INTO agents_primary.interaction_asks
			(id, organization_id, origin, team_id, channel_id, thread_ts, trigger_ts, asker_user_id, kind, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (organization_id, origin, team_id, channel_id, trigger_ts, asker_user_id) DO NOTHING
		RETURNING `+askColumns,
		ask.ID, ask.OrganizationID, ask.Origin, ask.TeamID, ask.ChannelID, ask.ThreadTS, ask.TriggerTS, ask.AskerUserID, ask.Kind, ask.CreatedAt), &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		err = scanAsk(tx.QueryRow(ctx, `SELECT `+askColumns+` FROM agents_primary.interaction_asks
			WHERE organization_id=$1 AND origin=$2 AND team_id=$3 AND channel_id=$4 AND trigger_ts=$5 AND asker_user_id=$6
			FOR UPDATE`,
			ask.OrganizationID, ask.Origin, ask.TeamID, ask.ChannelID, ask.TriggerTS, ask.AskerUserID), &stored)
	}
	if err != nil {
		return Ask{}, fmt.Errorf("store the ask: %w", err)
	}
	return stored, nil
}

// decideAtCreation approves a request created a moment ago, in the same
// transaction, when its owner has a live lease covering the asker in this
// thread (recorded as the lease's decision) or the router asked for a bypass.
// A lease wins over a bypass, since it is the owner's own permission. A
// request neither covers is returned as it is, pending.
//
// The decision is made a microsecond after the request, the least the database
// keeps, so that the two events, requested and then decided, are in that order
// by when they happened, as a request an owner decides later has them.
func decideAtCreation(ctx context.Context, tx pgx.Tx, ask Ask, request agents.Interaction, item AskRequest, created time.Time) (agents.Interaction, bool, error) {
	at := created.Add(time.Microsecond)
	if item.OnLease != nil {
		lease, found, err := liveLease(ctx, tx, request, ask, created)
		if err != nil {
			return agents.Interaction{}, false, err
		}
		if found {
			decider := agents.InteractionDecider{Kind: DecidedByLease, UserID: request.Target.UserID, Surface: SurfaceLease, Reason: lease.ID}
			decided, err := decideLocked(ctx, tx, request, *item.OnLease, decider, at)
			return decided, false, err
		}
	}
	if item.Bypass != nil {
		decider := agents.InteractionDecider{Kind: DecidedByBypass, Surface: SurfaceBypass, Reason: item.Bypass.Reason}
		decided, err := decideLocked(ctx, tx, request, item.Bypass.Input, decider, at)
		return decided, true, err
	}
	return request, false, nil
}

// liveLease finds the lease of the request's owner that covers its asker in
// the ask's thread at at, and holds it, so that a revoke that comes after
// waits for this transaction and finds the request it approved.
func liveLease(ctx context.Context, tx pgx.Tx, request agents.Interaction, ask Ask, at time.Time) (Lease, bool, error) {
	var lease Lease
	err := scanLease(tx.QueryRow(ctx, `SELECT `+leaseColumns+` FROM agents_primary.interaction_leases
		WHERE organization_id=$1 AND asker_user_id=$2 AND owner_user_id=$3 AND channel_id=$4 AND thread_ts=$5
		  AND revoked_at IS NULL AND expires_at > $6
		FOR SHARE`,
		request.OrganizationID, request.Requester.UserID, request.Target.UserID, ask.ChannelID, ask.ThreadTS, at), &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("find the owner's lease: %w", err)
	}
	return lease, true, nil
}

// Ask reads one ask.
func (store *Store) Ask(ctx context.Context, id string) (Ask, error) {
	var ask Ask
	err := scanAsk(store.database.Pool.QueryRow(ctx, `SELECT `+askColumns+` FROM agents_primary.interaction_asks WHERE id=$1`, id), &ask)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ask{}, ErrNotFound
	}
	if err != nil {
		return Ask{}, fmt.Errorf("read the ask: %w", err)
	}
	return ask, nil
}

// CountOpenAsks is how many asks the member asked are still waiting: no
// winner yet, and a request that is pending, approved or in review, unexpired
// at at.
func (store *Store) CountOpenAsks(ctx context.Context, organizationID, askerUserID string, at time.Time) (int, error) {
	var open int
	err := store.database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.interaction_asks ask
		WHERE ask.organization_id=$1 AND ask.asker_user_id=$2 AND ask.winner_request_id=''
		  AND EXISTS (SELECT 1 FROM agents_primary.agent_interactions request
			WHERE request.ask_id=ask.id AND request.status IN ('pending', 'approved', 'review') AND request.expires_at > $3)`,
		organizationID, askerUserID, at).Scan(&open)
	if err != nil {
		return 0, fmt.Errorf("count open asks: %w", err)
	}
	return open, nil
}

// Consent records a pending request's decision and, for access, gives the
// asker a lease in the ask's thread, in one transaction. A request already
// decided, or decided meanwhile, is ErrConflict; one past its expiry,
// ErrExpired. A live lease of the same owner and asker in the thread is
// renewed, keeping its id.
func (store *Store) Consent(ctx context.Context, id string, record ConsentRecord, at time.Time) (agents.Interaction, *Lease, error) {
	var result agents.Interaction
	var lease *Lease
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Status != StatusPending {
			return ErrConflict
		}
		if !current.ExpiresAt.After(at) {
			return ErrExpired
		}
		result, err = decideLocked(ctx, tx, current, record.Input, record.Decider, at)
		if err != nil {
			return err
		}
		if record.LeaseTTL <= 0 {
			return nil
		}
		given, err := grantLease(ctx, tx, result, record.LeaseID, at, at.Add(record.LeaseTTL))
		if err != nil {
			return err
		}
		lease = &given
		return nil
	})
	if err != nil {
		return agents.Interaction{}, nil, err
	}
	return result, lease, nil
}

// grantLease gives the asker of a request a lease from its owner in the
// thread of the request's ask, or renews the one they hold.
func grantLease(ctx context.Context, tx pgx.Tx, request agents.Interaction, id string, at, expires time.Time) (Lease, error) {
	var lease Lease
	err := scanLease(tx.QueryRow(ctx, `INSERT INTO agents_primary.interaction_leases
			(id, organization_id, asker_user_id, owner_user_id, channel_id, thread_ts, request_id, created_at, expires_at)
		SELECT $1, request.organization_id, request.requester_user_id, request.target_user_id,
			ask.channel_id, ask.thread_ts, request.id, $2, $3
		FROM agents_primary.agent_interactions request
		JOIN agents_primary.interaction_asks ask ON ask.id = request.ask_id
		WHERE request.id = $4
		ON CONFLICT (organization_id, asker_user_id, owner_user_id, channel_id, thread_ts) WHERE revoked_at IS NULL
		DO UPDATE SET expires_at = EXCLUDED.expires_at, created_at = EXCLUDED.created_at, request_id = EXCLUDED.request_id
		RETURNING `+leaseColumns, id, at, expires, request.ID), &lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, fmt.Errorf("%w: access is given for a Slack thread, and this request was not made in one", ErrInvalid)
	}
	if err != nil {
		return Lease{}, fmt.Errorf("give the asker a lease: %w", err)
	}
	return lease, nil
}

// Bypass records a pending request's approval as a bypass, and marks its ask
// as one that was. The ask's lock comes first, as Respond takes it.
func (store *Store) Bypass(ctx context.Context, id string, decision BypassDecision, at time.Time) (agents.Interaction, error) {
	askID, err := store.askOf(ctx, id)
	if err != nil {
		return agents.Interaction{}, err
	}
	var result agents.Interaction
	err = pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := lockAsk(ctx, tx, askID); err != nil {
			return err
		}
		current, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Status != StatusPending {
			return ErrConflict
		}
		if !current.ExpiresAt.After(at) {
			return ErrExpired
		}
		decider := agents.InteractionDecider{Kind: DecidedByBypass, Surface: SurfaceBypass, Reason: decision.Reason}
		result, err = decideLocked(ctx, tx, current, decision.Input, decider, at)
		if err != nil {
			return err
		}
		if askID == "" {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE agents_primary.interaction_asks SET bypass=true WHERE id=$1`, askID)
		return err
	})
	if err != nil {
		return agents.Interaction{}, err
	}
	return result, nil
}

// RevokeLease ends a live lease of its owner's, at at, and records the
// revoked event. It reports whether this call revoked it. A lease already
// revoked, or already run out, is returned as it is, with no event: revoking
// again changes nothing, so it is not news. A lease that is not the owner's,
// or not in the organization, is ErrNotFound.
func (store *Store) RevokeLease(ctx context.Context, organizationID, id, ownerUserID string, at time.Time) (Lease, bool, error) {
	var lease Lease
	revoked := false
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		err := scanLease(tx.QueryRow(ctx, `SELECT `+leaseColumns+` FROM agents_primary.interaction_leases
			WHERE id=$1 AND organization_id=$2 AND owner_user_id=$3 FOR UPDATE`, id, organizationID, ownerUserID), &lease)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !lease.Live(at) {
			return nil
		}
		if err := scanLease(tx.QueryRow(ctx, `UPDATE agents_primary.interaction_leases SET revoked_at=$2
			WHERE id=$1 RETURNING `+leaseColumns, id, at), &lease); err != nil {
			return err
		}
		revoked = true
		return recordRawEvent(ctx, tx, agentsv1.InteractionLeaseRevokedDetailType, agentsv1.InteractionLeaseRevoked{
			LeaseID: lease.ID, OrganizationID: lease.OrganizationID, OwnerUserID: lease.OwnerUserID, AskerUserID: lease.AskerUserID,
			ChannelID: lease.ChannelID, ThreadTS: lease.ThreadTS, RequestID: lease.RequestID,
			ExpiresAt: lease.ExpiresAt.UTC(), RevokedAt: at.UTC(),
		}, at)
	})
	if err != nil {
		return Lease{}, false, err
	}
	return lease, revoked, nil
}

// Leases lists, newest first, the leases the member gave and the ones they
// hold.
func (store *Store) Leases(ctx context.Context, organizationID, userID string, limit int) ([]Lease, error) {
	rows, err := store.database.Pool.Query(ctx, `SELECT `+leaseColumns+` FROM agents_primary.interaction_leases
		WHERE organization_id=$1 AND (owner_user_id=$2 OR asker_user_id=$2)
		ORDER BY created_at DESC, id DESC LIMIT $3`, organizationID, userID, clamp(limit))
	if err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	defer rows.Close()
	leases := []Lease{}
	for rows.Next() {
		var lease Lease
		if err := scanLease(rows, &lease); err != nil {
			return nil, fmt.Errorf("scan lease: %w", err)
		}
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	return leases, nil
}

// askOf is the ask a request belongs to, empty for one that belongs to none,
// read before the transaction that locks the ask first and then the request.
// A request never changes its ask, so what it finds cannot go stale.
func (store *Store) askOf(ctx context.Context, id string) (string, error) {
	var askID string
	err := store.database.Pool.QueryRow(ctx, `SELECT ask_id FROM agents_primary.agent_interactions WHERE id=$1`, id).Scan(&askID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read a request's ask: %w", err)
	}
	return askID, nil
}

// lockAsk takes an ask's row lock for the rest of the transaction and says
// which request won it, empty while none has. No ask, or one that is gone, is
// no lock and no winner.
func lockAsk(ctx context.Context, tx pgx.Tx, askID string) (string, error) {
	if askID == "" {
		return "", nil
	}
	var winner string
	err := tx.QueryRow(ctx, `SELECT winner_request_id FROM agents_primary.interaction_asks WHERE id=$1 FOR UPDATE`, askID).Scan(&winner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lock the ask: %w", err)
	}
	return winner, nil
}

// settleAsk makes a request that delivered its answer the winner of its ask,
// and closes the ones that did not answer, saying who did. The caller holds
// the ask and the winner locked. The winner is set with a compare-and-set, so
// an ask is won once whatever else happens; each request still waiting is
// then locked in id order and closed as it stands: a pending one is
// rejected, an approved one fails before its work is done, and one whose
// answer is held for review has that answer withheld. One past its expiry is
// left as it is.
func settleAsk(ctx context.Context, tx pgx.Tx, winner agents.Interaction, at time.Time) error {
	if winner.AskID == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE agents_primary.interaction_asks SET winner_request_id=$2, settled_at=$3
		WHERE id=$1 AND winner_request_id=''`, winner.AskID, winner.ID, at)
	if err != nil {
		return fmt.Errorf("settle the ask: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM agents_primary.agent_interactions
		WHERE ask_id=$1 AND id<>$2 AND status IN ('pending', 'approved', 'review') ORDER BY id`, winner.AskID, winner.ID)
	if err != nil {
		return fmt.Errorf("find the ask's other requests: %w", err)
	}
	siblings, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read the ask's other requests: %w", err)
	}
	reason := answeredByOwner(winner.Target)
	for _, id := range siblings {
		sibling, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := closeAnswered(ctx, tx, sibling, reason, at); err != nil {
			return err
		}
	}
	return nil
}

// closeAnswered closes a request of an ask that another owner answered.
func closeAnswered(ctx context.Context, tx pgx.Tx, sibling agents.Interaction, reason string, at time.Time) error {
	if !sibling.ExpiresAt.After(at) {
		return nil
	}
	switch sibling.Status {
	case StatusPending, StatusApproved:
		_, err := closeLocked(ctx, tx, sibling, reason, ReasonAnsweredElsewhere, at)
		return err
	case StatusReview:
		_, err := settleReview(ctx, tx, sibling.ID, nil, agents.InteractionReview{
			Action: ReviewWithhold, DecidedBy: agents.InteractionDecider{Kind: DecidedBySystem, Reason: ReasonAnsweredElsewhere},
			DecidedAt: at, Reason: reason,
		}, at)
		return err
	}
	return nil
}

// answeredByOwner says who answered an ask, for the requests it closes: by
// the name the member chose, else their sign-in address.
func answeredByOwner(owner agents.InteractionParty) string {
	switch {
	case cleanLine(owner.UserDisplayName) != "":
		return "Answered by " + cleanLine(owner.UserDisplayName)
	case cleanLine(owner.UserEmail) != "":
		return "Answered by " + cleanLine(owner.UserEmail)
	}
	return "Answered by another owner"
}

// answeredBy says who won an ask, from its winner's request.
func answeredBy(ctx context.Context, tx pgx.Tx, winnerID string) string {
	var owner agents.InteractionParty
	err := tx.QueryRow(ctx, `SELECT target_user_display_name, target_user_email FROM agents_primary.agent_interactions WHERE id=$1`,
		winnerID).Scan(&owner.UserDisplayName, &owner.UserEmail)
	if err != nil {
		return answeredByOwner(agents.InteractionParty{})
	}
	return answeredByOwner(owner)
}

// accessRevokedReason is why Oryxa closes a request whose owner revoked the
// access they gave before its answer was sent.
const accessRevokedReason = "The owner revoked their access before this request was answered."

// accessRevoked is whether the owner of a request approved with access has
// revoked their lease in its thread since approving it: what lets a request's
// answer go out is an access that is still theirs to give. Every lease of the
// pair in that thread is read and held, so a revoke that comes after waits for
// the answer, and an answer that comes after a revoke does not go out.
func accessRevoked(ctx context.Context, tx pgx.Tx, request agents.Interaction) (bool, error) {
	decidedAt := request.CreatedAt
	if request.Decision != nil {
		decidedAt = request.Decision.DecidedAt
	}
	rows, err := tx.Query(ctx, `SELECT lease.revoked_at FROM agents_primary.interaction_leases lease
		JOIN agents_primary.interaction_asks ask ON ask.id=$1
		WHERE lease.organization_id=$2 AND lease.asker_user_id=$3 AND lease.owner_user_id=$4
		  AND lease.channel_id=ask.channel_id AND lease.thread_ts=ask.thread_ts
		FOR SHARE OF lease`, request.AskID, request.OrganizationID, request.Requester.UserID, request.Target.UserID)
	if err != nil {
		return false, fmt.Errorf("read the owner's leases: %w", err)
	}
	revocations, err := pgx.CollectRows(rows, pgx.RowTo[*time.Time])
	if err != nil {
		return false, fmt.Errorf("read the owner's leases: %w", err)
	}
	for _, revokedAt := range revocations {
		if revokedAt != nil && !revokedAt.Before(decidedAt) {
			return true, nil
		}
	}
	return false, nil
}

// RecordSecretViewed records, in the caller's transaction, that the member
// who asked opened the one-time secret their request's owner shared: when,
// on the request (secret_viewed_at, first view only) and as the secret-viewed
// event, which says that and by whom and never what the secret held. The
// secret's store calls it in the transaction that deletes the secret, so a
// secret cannot be opened without the record, nor recorded without having been
// opened. A request that is not viewerUserID's own is ErrNotFound.
func RecordSecretViewed(ctx context.Context, tx pgx.Tx, requestID, viewerUserID string, at time.Time) error {
	var viewed agentsv1.InteractionSecretViewed
	err := tx.QueryRow(ctx, `UPDATE agents_primary.agent_interactions
		SET secret_viewed_at=COALESCE(secret_viewed_at, $3)
		WHERE id=$1 AND requester_user_id=$2
		RETURNING id, organization_id, ask_id, target_user_id, requester_user_id`, requestID, viewerUserID, at).
		Scan(&viewed.RequestID, &viewed.OrganizationID, &viewed.AskID, &viewed.OwnerUserID, &viewed.AskerUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("record that a secret was viewed: %w", err)
	}
	viewed.ViewedAt = at.UTC()
	return recordRawEvent(ctx, tx, agentsv1.InteractionSecretViewedDetailType, viewed, at)
}

// recordRawEvent writes an event to the outbox in the caller's transaction.
func recordRawEvent(ctx context.Context, tx pgx.Tx, detailType string, detail any, at time.Time) error {
	event, err := events.NewEvent(detailType, detail, at)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err = tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	return err
}

var _ Consents = (*Store)(nil)

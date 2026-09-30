package interactions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// Reply records an owner's own answer to a request, in one transaction, under
// the locks every answer takes: the request's ask first, as Respond and Review
// take it, then the request. It lands only on a request that is still in the
// status the reply was made for (replyLands), unexpired, and not closed by
// another owner's answer to its ask; anything else is ErrConflict, which the
// service reads again to say who won.
//
//   - A pending request is decided and answered at once: approved by its owner,
//     with ConsentReply and nothing allowed or granted to their agent, recorded
//     as the decided event, and then, a microsecond later, completed with the
//     reply, recorded as the completed event. Nothing in between is ever read
//     by anyone, so no agent is ever woken for the approval: its request is
//     answered by the time anything reads it.
//   - A request in review has the answer held for its owner dropped, never to
//     be sent, and the reply delivered in its place, with the review recorded as
//     ReviewReply, the owner's own; that is the completed event.
//
// Either way the reply is what the guards made of it, and is delivered: it is
// the ask's first answer if none was, and settles it (settleAsk).
func (store *Store) Reply(ctx context.Context, id string, reply ReplyRecord, at time.Time) (agents.Interaction, error) {
	if err := reply.valid(); err != nil {
		return agents.Interaction{}, err
	}
	askID, err := store.askOf(ctx, id)
	if err != nil {
		return agents.Interaction{}, err
	}
	var result agents.Interaction
	err = pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		winner, err := lockAsk(ctx, tx, askID)
		if err != nil {
			return err
		}
		current, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case !replyLands(current, reply.Stage), winner != "" && winner != current.ID:
			return ErrConflict
		case !current.ExpiresAt.After(at):
			return ErrExpired
		}
		if result, err = replyLocked(ctx, tx, current, reply, at); err != nil {
			return err
		}
		if delivers(result) {
			// The ask is settled when the reply was delivered, which for a
			// pending request is a microsecond after its approval.
			return settleAsk(ctx, tx, result, result.UpdatedAt)
		}
		return nil
	})
	if err != nil {
		return agents.Interaction{}, err
	}
	return result, nil
}

// replyLocked records a reply on a request the caller holds locked, which
// replyLands has let it land on.
func replyLocked(ctx context.Context, tx pgx.Tx, current agents.Interaction, reply ReplyRecord, at time.Time) (agents.Interaction, error) {
	guardJSON, err := nullableJSON(reply.Answer.Guard)
	if err != nil {
		return agents.Interaction{}, err
	}
	answered := *reply.Answer.Result
	var result agents.Interaction
	switch current.Status {
	case StatusPending:
		decided, err := decideLockedAs(ctx, tx, current, replyDecision(), reply.Decider, ConsentReply, at)
		if err != nil {
			return agents.Interaction{}, err
		}
		// The answer comes a microsecond after the approval, the least the
		// database keeps, so that the two events are in that order by when they
		// happened, as they are for a request whose agent answers it.
		completedAt := at.Add(time.Microsecond)
		answered.CompletedAt = completedAt
		resultJSON, err := json.Marshal(answered)
		if err != nil {
			return agents.Interaction{}, err
		}
		err = scanInteraction(tx.QueryRow(ctx, `UPDATE agents_primary.agent_interactions
			SET status='completed', result=$2, guard=$3, revision=revision+1, updated_at=$4
			WHERE id=$1 RETURNING `+selectColumns, decided.ID, resultJSON, guardJSON, completedAt), &result)
		if err != nil {
			return agents.Interaction{}, err
		}
	case StatusReview:
		answered.CompletedAt = at
		resultJSON, err := json.Marshal(answered)
		if err != nil {
			return agents.Interaction{}, err
		}
		reviewJSON, err := json.Marshal(agents.InteractionReview{Action: ReviewReply, DecidedBy: reply.Decider, DecidedAt: at})
		if err != nil {
			return agents.Interaction{}, err
		}
		// What the guard found in the held answer goes with it: the guard is
		// what was found in the answer the member who asked reads, which is now
		// the reply.
		err = scanInteraction(tx.QueryRow(ctx, `UPDATE agents_primary.agent_interactions
			SET status='completed', result=$2, held_result=NULL, review=$3, guard=$4, revision=revision+1, updated_at=$5
			WHERE id=$1 RETURNING `+selectColumns, current.ID, resultJSON, reviewJSON, guardJSON, at), &result)
		if err != nil {
			return agents.Interaction{}, err
		}
	default:
		return agents.Interaction{}, fmt.Errorf("%w: a reply lands on a request pending or in review", ErrConflict)
	}
	return result, recordEvent(ctx, tx, agentsv1.InteractionCompletedDetailType, result)
}

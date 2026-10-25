package rooms

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// busyRetries is how many pauses a turn's budget is divided into for asking
// a seat whose thread is busy again: about two seconds each, by default.
const busyRetries = 20

// errSaidNothing is a turn that ended without saying anything, not even
// [Pass]: a run that stopped to wait for its owner says nothing, and so does
// a model that returned no text.
var errSaidNothing = errors.New("rooms: the turn ended without an answer")

// turn is how one seat's turn ended: with what it said, or with why it could
// not say anything.
type turn struct {
	reply string
	err   error
	// slow says the turn's budget had run out when it ended, which is what
	// ended it when it said nothing, or failed.
	slow bool
}

// takeTurn is one seat's turn, within a budget of its own.
//
// A seat whose thread is busy with a turn from another message, as two
// people asking the same seat at once bring about, is asked again until its
// thread is free or its budget is spent. Nothing of the turn has run when
// the thread is busy, so nothing is said twice.
//
// It runs in a goroutine of the relay's own, where nothing else would
// recover a panic, so a panic is that turn's failure and not the process's.
func (relay *Relay) takeTurn(ctx context.Context, seat Seat, room Situation, transcript []Message, summons Summons) (taken turn) {
	defer func() {
		if recovered := recover(); recovered != nil {
			relay.logger().ErrorContext(ctx, "rooms: relay: a seat's turn panicked",
				"room_id", room.RoomID, "seat", seat.Handle, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			taken = turn{err: fmt.Errorf("rooms: %s's turn panicked", seat.Handle)}
		}
	}()

	own, cancel := context.WithTimeout(ctx, relay.perTurn())
	defer cancel()
	for {
		reply, err := relay.Speaker.Speak(own, seat, room, transcript, summons)
		slow := errors.Is(own.Err(), context.DeadlineExceeded)
		if !errors.Is(err, threads.ErrRunInProgress) {
			return turn{reply: reply, err: err, slow: slow}
		}
		again := time.NewTimer(max(relay.perTurn()/busyRetries, time.Millisecond))
		select {
		case <-own.Done():
			again.Stop()
			return turn{err: err, slow: errors.Is(own.Err(), context.DeadlineExceeded)}
		case <-again.C:
		}
	}
}

// unanswered is what a room hears in place of the answer of a seat that was
// asked and could not give one: a line that names it, and says why in plain
// words. It never carries what went wrong, only which of a few things did,
// because what went wrong is not for the whole team to read.
//
// These lines name a seat as the room shows it, by its display name, never
// by its handle: a handle is for typing an @mention, and it changes with a
// rename or a namesake joining (T8).
func unanswered(seat Seat, taken turn) string {
	name := seatName(seat)
	switch {
	case errors.Is(taken.err, threads.ErrConfirmationPending):
		return fmt.Sprintf("%s is waiting on its owner's confirmation, so it can't answer yet.", name)
	case errors.Is(taken.err, threads.ErrRunInProgress):
		return fmt.Sprintf("%s is busy answering something else, so it couldn't answer this.", name)
	case taken.slow:
		return fmt.Sprintf("%s is taking too long to answer, so the room stopped waiting.", name)
	default:
		return fmt.Sprintf("%s couldn't answer right now.", name)
	}
}

// seatName is what the room calls seat: its display name, or its handle
// when it has none.
func seatName(seat Seat) string {
	if name := strings.TrimSpace(seat.DisplayName); name != "" {
		return name
	}
	return seat.Handle
}

// passedFor is the line for seat, standing in for the away person whose
// handle is away and whose name is awayName, when it had nothing to say in
// their place: they were asked, so the room is told the question waits for
// them rather than hearing nothing. The last mention stays an @handle: it is
// what tells them they were asked.
func passedFor(seat Seat, away, awayName string) string {
	return fmt.Sprintf("%s is away, and %s has nothing to add for them, so this waits for @%s.", awayName, seatName(seat), away)
}

// noTurn is the line for a seat a person named that the turns one message
// may cause ran out before.
func noTurn(seat Seat) string {
	return fmt.Sprintf("%s wasn't given a turn: one message wakes at most %d agents.", seatName(seat), maxAgentTurns)
}

// note says, as seat, that it has no answer for trigger: line, which the
// guard reads like anything else an agent says in a room. It is stored under
// a context of its own, bounded, because a request that has gone away is
// often why a seat has no answer, and the room is still owed being told. It
// returns why the line was not stored, which the relay only acts on when it
// is ErrPaused: what failed is logged here.
func (relay *Relay) note(ctx context.Context, room Situation, trigger Message, seat Seat, line string) (Message, error) {
	settling, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleBudget)
	defer cancel()
	said := relay.guarded(settling, trigger.RoomID, seat, line)
	if said == unchecked {
		// A line that is a handle and a fixed reason holds nothing the guard
		// could have found. That it could not be read is no reason to say an
		// answer is being held, when there is none.
		said = line
	}
	message, err := relay.say(settling, room, trigger, seat, said)
	if err != nil {
		relay.logger().WarnContext(ctx, "rooms: relay: could not record that a seat has no answer",
			"room_id", trigger.RoomID, "seat", seat.Handle, "in_reply_to_seq", trigger.Seq, "error", err)
		return Message{}, err
	}
	return message, nil
}

package rooms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"
)

// A relay is bounded three ways, each guarding against a different way a
// room could stop feeling like a conversation.
const (
	// maxAgentTurns caps how many times one posted message may wake an
	// agent, across every reply that answering it goes on to cause. Four is
	// room enough for a question, an answer and a follow-up or two — enough
	// to feel like a conversation rather than a single Q&A — while staying
	// small enough that agents addressing one another back and forth can
	// never turn one message into an unbounded thread.
	maxAgentTurns = 4

	// maxConsecutivePasses ends a relay once this many turns in a row came
	// back PASS. One PASS only means "not my turn"; a second one right
	// after it means nobody left has anything to add, and spending a third
	// turn would only confirm silence that is already obvious.
	maxConsecutivePasses = 2

	// relayBudget caps the wall-clock time one Post call may spend running
	// agents. The decider a room's service asks is built to answer in a few
	// hundred milliseconds, but a Speaker turn is a model call and can run
	// to several seconds. Twenty seconds covers maxAgentTurns worst-case
	// turns with room to spare, while still bounding how long one Post call
	// — an HTTP request, ultimately — is left waiting on a room that will
	// not settle.
	relayBudget = 20 * time.Second

	// transcriptWindow is how many of the room's own recent messages a
	// Speaker is shown alongside the message that woke it. The brief stands
	// in for everything older, so this only has to cover the immediate
	// back-and-forth; keeping it fixed means a long-lived room's prompts do
	// not grow with the room's age.
	transcriptWindow = 12
)

// mentionPattern finds "@handle" tokens in a message body. Handles are what
// [Service]'s own handle function produces: lowercase letters, digits and
// hyphens, so that is all this looks for.
var mentionPattern = regexp.MustCompile(`@([A-Za-z0-9-]+)`)

// Relay is what happens after somebody posts a message in a room: append it,
// work out which agent seats should answer, run them in order, and append
// what they say.
//
// Whether an agent should answer at all when nobody was addressed, and which
// one, is a judgment — the same judgment that also decides whether the
// message is worth keeping and whether the room has moved on. Asking it once
// and handing the answer to whoever needs it is what keeps those three from
// disagreeing about the same message, so a relay does not make that
// judgment itself: [Relay.Post] takes it in as verdict.
type Relay struct {
	// Store is where a relay appends the posted message and what the agents
	// it runs go on to say.
	Store Store
	// Speaker takes an agent seat's turn. One implementation serves every
	// seat; which agent answers is carried in the Seat it is given.
	Speaker Speaker
	// Logger records an agent turn that failed so the room can keep moving
	// without losing why. Nil discards it.
	Logger *slog.Logger
}

// NewRelay returns the relay a room service runs every posted message
// through.
func NewRelay(store Store, speaker Speaker) (*Relay, error) {
	if store == nil || speaker == nil {
		return nil, errors.New("rooms: a relay needs a store and a speaker")
	}
	return &Relay{Store: store, Speaker: speaker}, nil
}

// relayItem is one message waiting to be checked for a responder: either the
// message a person posted, carrying the verdict the caller judged it with,
// or a reply an agent went on to give, which carries no verdict of its own
// because nothing judged it — only addressing can wake a responder to it.
type relayItem struct {
	message Message
	// priorLen is how many messages of history came before message. history
	// only ever grows by appending, so re-slicing it to this length keeps
	// meaning what it meant when this item was queued, no matter what has
	// been appended to history since.
	priorLen int
	verdict  *Verdict
}

// Post appends message, works out which agent seats should answer it, runs
// them in order, and appends what they say. It returns everything it added:
// the posted message first, then each reply in the order the relay produced
// it.
//
// Addressing decides first and is a rule, not a judgment: a seat named in
// the body by "@handle", the author excluded, is who answers — an addressed
// agent seat answers; an addressed person who is present answers for
// themselves, so nothing runs; an addressed person who is not present has
// their own agent answer in their place. Only when a message addresses
// nobody does verdict get a say, and a Speaker handle it names is ignored
// unless it is an agent seat in this room.
//
// An agent's own reply can itself address another seat, and that seat
// answers in turn — this is how agents answer agents — but never past
// maxAgentTurns, maxConsecutivePasses or relayBudget, and never a seat
// answering a message it wrote itself.
func (relay *Relay) Post(ctx context.Context, room Situation, message Message, verdict Verdict) ([]Message, error) {
	stored, err := relay.Store.Append(ctx, message)
	if err != nil {
		return nil, fmt.Errorf("rooms: relay: append the posted message: %w", err)
	}
	results := []Message{stored}

	history := make([]Message, 0, len(room.Recent)+1)
	history = append(history, room.Recent...)
	history = append(history, stored)

	queue := []relayItem{{message: stored, priorLen: len(history) - 1, verdict: &verdict}}

	deadline := time.Now().Add(relayBudget)
	turns := 0
	consecutivePasses := 0

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		trigger := item.message
		prior := history[:item.priorLen]

		responders := resolveResponders(room.Seats, trigger, item.verdict)
		if len(responders) == 0 {
			continue
		}

		situation := room
		situation.Recent = tail(prior, transcriptWindow)
		transcript := make([]Message, 0, len(situation.Recent)+1)
		transcript = append(transcript, situation.Recent...)
		transcript = append(transcript, trigger)

		for _, seat := range responders {
			if turns >= maxAgentTurns || time.Now().After(deadline) {
				return results, nil
			}
			turns++

			reply, err := relay.Speaker.Speak(ctx, seat, situation, transcript)
			if err != nil {
				relay.logger().WarnContext(ctx, "rooms: relay: a seat failed to answer; skipping it",
					"room_id", trigger.RoomID, "seat", seat.Handle, "in_reply_to_seq", trigger.Seq, "error", err)
				continue
			}
			if reply == Pass {
				consecutivePasses++
				if consecutivePasses >= maxConsecutivePasses {
					return results, nil
				}
				continue
			}
			consecutivePasses = 0

			said, err := relay.Store.Append(ctx, Message{
				RoomID:        trigger.RoomID,
				AuthorHandle:  seat.Handle,
				AuthorKind:    seat.Kind,
				AuthorUserID:  seat.UserID,
				AuthorAgentID: seat.AgentID,
				Body:          reply,
				AddressedTo:   addressedHandles(reply, seat.Handle),
				InReplyToSeq:  trigger.Seq,
				CreatedAt:     time.Now().UTC(),
			})
			if err != nil {
				relay.logger().WarnContext(ctx, "rooms: relay: could not record a seat's answer; skipping it",
					"room_id", trigger.RoomID, "seat", seat.Handle, "in_reply_to_seq", trigger.Seq, "error", err)
				continue
			}
			results = append(results, said)
			history = append(history, said)
			queue = append(queue, relayItem{message: said, priorLen: len(history) - 1})
		}
	}

	return results, nil
}

// resolveResponders is rules 1 and 2: addressing decides first, and only
// when trigger addresses nobody real does verdict get a say — and verdict
// only has a say at all when it is non-nil, which is true for the message a
// person posted and false for every reply answering it goes on to cause,
// since nothing judged those.
func resolveResponders(seats []Seat, trigger Message, verdict *Verdict) []Seat {
	if handles := addressedHandles(trigger.Body, trigger.AuthorHandle); len(handles) > 0 {
		if responders, addressed := addressedSeats(seats, handles); addressed {
			return excludeAuthor(responders, trigger.AuthorHandle)
		}
		// None of the @mentions named a real seat: nobody was really
		// addressed, so fall through to verdict below.
	}

	if verdict == nil || !verdict.Answer || verdict.Speaker == "" {
		return nil
	}
	seat, ok := findSeat(seats, verdict.Speaker)
	if !ok || seat.Kind != SeatAgent {
		// A named seat that is not an agent seat in this room — including
		// no seat at all — is ignored, not substituted for.
		return nil
	}
	return excludeAuthor([]Seat{seat}, trigger.AuthorHandle)
}

// addressedSeats resolves rule 1 for the handles a message named. addressed
// is false when none of them matched a real seat in the room, which means
// the message did not really address anyone and rule 2 should run instead.
func addressedSeats(seats []Seat, handles []string) (responders []Seat, addressed bool) {
	for _, handle := range handles {
		seat, ok := findSeat(seats, handle)
		if !ok {
			continue
		}
		addressed = true
		switch {
		case seat.Kind == SeatAgent:
			responders = append(responders, seat)
		case seat.Present:
			// Addressed and here: they answer for themselves. Their agent
			// talking over them would be worse than silence.
		default:
			if agent, ok := agentForUser(seats, seat.UserID); ok {
				responders = append(responders, agent)
			}
		}
	}
	return responders, addressed
}

// excludeAuthor drops a seat that would otherwise answer the very message it
// wrote. Addressing already leaves the author out of AddressedTo, and
// verdict is asked to route to somebody, not to the speaker — but this is
// the backstop that keeps either one from turning into an agent replying to
// itself forever.
func excludeAuthor(seats []Seat, authorHandle string) []Seat {
	var out []Seat
	for _, seat := range seats {
		if seat.Handle == authorHandle {
			continue
		}
		out = append(out, seat)
	}
	return out
}

// addressedHandles returns the handles trigger's body names with "@handle",
// the author excluded and duplicates removed, in the order they first
// appear. A body that only names its own author returns nothing: that is
// not addressing anybody, so it is treated exactly like a message that
// addressed nobody at all.
func addressedHandles(body, authorHandle string) []string {
	matches := mentionPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		handle := match[1]
		if handle == authorHandle || seen[handle] {
			continue
		}
		seen[handle] = true
		out = append(out, handle)
	}
	return out
}

func findSeat(seats []Seat, handle string) (Seat, bool) {
	for _, seat := range seats {
		if seat.Handle == handle {
			return seat, true
		}
	}
	return Seat{}, false
}

// agentForUser is the agent seat that speaks for userID, if this room has
// one — how an absent person's own agent is found to stand in for them.
func agentForUser(seats []Seat, userID string) (Seat, bool) {
	for _, seat := range seats {
		if seat.Kind == SeatAgent && seat.UserID == userID {
			return seat, true
		}
	}
	return Seat{}, false
}

// tail returns the last n messages of history, oldest first, or history
// unchanged when there are n or fewer.
func tail(history []Message, n int) []Message {
	if len(history) <= n {
		return history
	}
	return history[len(history)-n:]
}

func (relay *Relay) logger() *slog.Logger {
	if relay.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return relay.Logger
}

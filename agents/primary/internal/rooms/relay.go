package rooms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
)

// A relay is bounded four ways, each guarding against a different way a
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

	// turnBudget is how long the relay waits on one seat's turn. Every turn
	// has a budget of its own, started when that turn starts, so a slow seat
	// neither costs another its time nor is cut short by one. A turn is a
	// model call, or a few, and under load one runs to tens of seconds; a
	// seat that has not answered by then is said to have no answer, in the
	// room, rather than waited on for as long as a run may last.
	turnBudget = 40 * time.Second

	// relayBudget caps the wall-clock time one Post call may spend running
	// agents, so that an HTTP request is not left waiting on a room that will
	// not settle: the edge in front of the API gives up on one that takes
	// much longer. It is two turns' worth. The seats a person's message
	// names always start, together, and each has its whole turn; what their
	// answers go on to cause starts only while a whole turn still fits in
	// what is left, so the call ends within the budget.
	relayBudget = 80 * time.Second

	// settleBudget bounds saying that a seat has no answer, which goes on
	// after the request that asked for it has gone away.
	settleBudget = 10 * time.Second

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
// work out which agent seats should answer, run them together, and append
// what they say, in the order the seats were named.
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
	// Guard reads every agent's answer before it is stored: the whole team
	// reads a room, so what one member's agent says there it tells the
	// others. Nil stores answers as they were said.
	Guard Guard
	// TurnBudget and Budget replace turnBudget and relayBudget when they are
	// set: how long one seat's turn may take, and how long a whole Post may
	// spend running agents.
	TurnBudget time.Duration
	Budget     time.Duration
}

// Guard checks what an agent says in a room before anybody reads it.
// *answerguard.Guard implements it; it never fails, holding what it cannot
// check.
type Guard interface {
	Check(ctx context.Context, texts []string) answerguard.Verdict
}

// unchecked is what a room hears in place of an agent's answer the guard
// could not check: a room has nobody to review an answer before the team
// reads it, so it is not said at all.
const unchecked = "I have an answer, but it could not be checked for personal data, so I have not posted it."

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
// them together, and appends what they say, as the answer guard leaves it
// (guarded). It returns everything it added: the posted message first, then
// each reply, and each line saying a seat had none, in the order the relay
// stored them.
//
// Addressing decides first and is a rule, not a judgment: a seat named in
// the body by "@handle", the author excluded, is who answers — an addressed
// agent seat answers; an addressed person who is present answers for
// themselves, so nothing runs; an addressed person who is not present has
// their own agent answer in their place. Only when a message addresses
// nobody does verdict get a say, and a Speaker handle it names is ignored
// unless it is an agent seat in this room.
//
// The seats one message names run together, and each has a turn budget of
// its own, so the second of "@ada @bilbo" is neither left waiting on the
// first nor cut off by how long the first took. A seat answers a message
// once, however many ways it was named. Answers are stored in the order the
// seats were named, each as soon as it and every seat named before it has
// finished.
//
// A seat asked to answer that cannot says so in the room, in a line that
// names it and gives a plain reason, rather than staying silent: its turn
// failed or ran out of its budget, it is waiting on its owner's confirmation,
// or the turns this message may cause ran out before its came. Nothing about
// a seat is remembered from one message to the next, so a seat that was
// waiting on its owner answers again as soon as its owner has answered, and
// is told so again by every message that names it until then.
//
// An agent's own reply can itself address another seat, and that seat
// answers in turn — this is how agents answer agents — but never past
// maxAgentTurns, maxConsecutivePasses or relayBudget, and never a seat
// answering a message it wrote itself.
//
// Every message the relay stores, the posted one and each reply, is stored
// addressing exactly the room's seats it named (seatHandles), whatever
// message.AddressedTo held when it came in. That list is what the store keeps
// and what the message's event publishes, so it is worked out here, where
// every message a room stores passes through.
func (relay *Relay) Post(ctx context.Context, room Situation, message Message, verdict Verdict) ([]Message, error) {
	message.AddressedTo = seatHandles(room.Seats, message.Body, message.AuthorHandle)
	stored, err := relay.Store.Append(ctx, message)
	if err != nil {
		return nil, fmt.Errorf("rooms: relay: append the posted message: %w", err)
	}
	results := []Message{stored}

	history := make([]Message, 0, len(room.Recent)+1)
	history = append(history, room.Recent...)
	history = append(history, stored)

	queue := []relayItem{{message: stored, priorLen: len(history) - 1, verdict: &verdict}}

	deadline := time.Now().Add(relay.perPost())
	turns := 0
	consecutivePasses := 0

	// A request that has gone away starts nothing more. The turns it had
	// running have each said in the room that they could not answer.
	for len(queue) > 0 && ctx.Err() == nil {
		item := queue[0]
		queue = queue[1:]
		trigger := item.message
		prior := history[:item.priorLen]

		responders := distinctSeats(resolveResponders(room.Seats, trigger, item.verdict))
		if len(responders) == 0 {
			continue
		}
		// What a person posted always gets its turns. What an agent's reply
		// goes on to cause only starts while a whole turn still fits in the
		// budget, and otherwise ends there without a word: it was never
		// asked for.
		if item.verdict == nil && time.Until(deadline) < relay.perTurn() {
			return results, nil
		}
		left := maxAgentTurns - turns
		if left <= 0 {
			return results, nil
		}
		asked := responders[:min(len(responders), left)]
		var unreached []Seat
		if item.verdict != nil {
			// A person is owed an answer, or a line saying why not, from every
			// seat they named. An agent's chain just ends where its turns do.
			unreached = responders[len(asked):]
		}
		turns += len(asked)

		situation := room
		situation.Recent = tail(prior, transcriptWindow)
		transcript := make([]Message, 0, len(situation.Recent)+1)
		transcript = append(transcript, situation.Recent...)
		transcript = append(transcript, trigger)

		var added []addition
		var paused bool
		added, consecutivePasses, paused = relay.round(ctx, situation, trigger, transcript, asked, unreached, consecutivePasses)
		for _, said := range added {
			results = append(results, said.Message)
			history = append(history, said.Message)
			if said.answer {
				queue = append(queue, relayItem{message: said.Message, priorLen: len(history) - 1})
			}
		}
		// A room paused while its turns ran took nothing more from the moment
		// it was paused, and the relay starts no turn whose answer it would
		// refuse.
		if paused || consecutivePasses >= maxConsecutivePasses {
			return results, nil
		}
	}

	return results, nil
}

// addition is one message a round added to the room. answer is set for a
// seat's answer, which may name others who then answer in turn; a line saying
// a seat has none names nobody and wakes nobody.
type addition struct {
	Message
	answer bool
}

// round runs asked together, each on a turn of its own, and stores what they
// come to in the order they are given: an answer, nothing for a seat that
// passes, and for one that could not answer a line saying so. unreached are
// seats the message named that had no turn left, which say that too. It
// returns what it stored, and how many turns in a row have passed by now,
// counting on from passes, and whether the room refused something because it
// had been paused by then. Whether that ends the relay is for the caller to
// say once the whole round is in: a seat that ran has its answer heard.
func (relay *Relay) round(ctx context.Context, room Situation, trigger Message, transcript []Message, asked, unreached []Seat, passes int) ([]addition, int, bool) {
	running := make([]chan turn, len(asked))
	for index, seat := range asked {
		running[index] = make(chan turn, 1)
		go func() { running[index] <- relay.takeTurn(ctx, seat, room, transcript) }()
	}

	var added []addition
	paused := false
	for index, seat := range asked {
		taken := <-running[index]
		if taken.err == nil && strings.TrimSpace(taken.reply) == "" {
			taken.err = errSaidNothing
		}
		switch {
		case taken.err != nil:
			relay.logger().WarnContext(ctx, "rooms: relay: a seat could not answer; saying so in the room",
				"room_id", trigger.RoomID, "seat", seat.Handle, "in_reply_to_seq", trigger.Seq, "slow", taken.slow, "error", taken.err)
			line, err := relay.note(ctx, room, trigger, seat, unanswered(seat, taken))
			if err == nil {
				added = append(added, addition{Message: line})
			}
			paused = paused || errors.Is(err, ErrPaused)
		case taken.reply == Pass:
			passes++
		default:
			passes = 0
			said, err := relay.say(ctx, room, trigger, seat, relay.guarded(ctx, trigger.RoomID, seat, taken.reply))
			if err != nil {
				relay.logger().WarnContext(ctx, "rooms: relay: could not record a seat's answer; skipping it",
					"room_id", trigger.RoomID, "seat", seat.Handle, "in_reply_to_seq", trigger.Seq, "error", err)
				paused = paused || errors.Is(err, ErrPaused)
				continue
			}
			added = append(added, addition{Message: said, answer: true})
		}
	}
	for _, seat := range unreached {
		relay.logger().WarnContext(ctx, "rooms: relay: a seat was named past the turns one message may cause; saying so in the room",
			"room_id", trigger.RoomID, "seat", seat.Handle, "in_reply_to_seq", trigger.Seq)
		line, err := relay.note(ctx, room, trigger, seat, noTurn(seat))
		if err == nil {
			added = append(added, addition{Message: line})
		}
		paused = paused || errors.Is(err, ErrPaused)
	}
	return added, passes, paused
}

// say stores what seat says in reply to trigger, addressing exactly the
// room's seats it names.
func (relay *Relay) say(ctx context.Context, room Situation, trigger Message, seat Seat, body string) (Message, error) {
	return relay.Store.Append(ctx, Message{
		RoomID:        trigger.RoomID,
		AuthorHandle:  seat.Handle,
		AuthorKind:    seat.Kind,
		AuthorUserID:  seat.UserID,
		AuthorAgentID: seat.AgentID,
		Body:          body,
		AddressedTo:   seatHandles(room.Seats, body, seat.Handle),
		InReplyToSeq:  trigger.Seq,
		CreatedAt:     time.Now().UTC(),
	})
}

// guarded is what the room hears of an agent's answer, as the guard found
// it: nothing found, the answer as said; only kinds that are always masked,
// the answer with them masked; a kind held for review (an email address, a
// phone number, an address), the answer with every such value replaced by
// its kind, as {EMAIL}, since a room has nobody to review it before the team
// reads it; and an answer that could not be checked, or a verdict that does
// not add up, unchecked in its place. Which of those a verdict allows is
// answerguard.Verdict.Shareable's rule, the one the teamspace ledger and the
// check route apply too. What was found is logged by kind, never by value.
func (relay *Relay) guarded(ctx context.Context, roomID string, seat Seat, reply string) string {
	if relay.Guard == nil {
		return reply
	}
	verdict := relay.Guard.Check(ctx, []string{reply})
	said := unchecked
	if shareable := verdict.Shareable(1); shareable != nil {
		said = shareable[0]
	}
	if verdict.State != answerguard.StateClear || said == unchecked {
		relay.logger().InfoContext(ctx, "rooms: relay: the answer guard found something in a seat's answer",
			"room_id", roomID, "seat", seat.Handle, "state", verdict.State, "kinds", verdict.Kinds)
	}
	return said
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

// distinctSeats drops a seat listed twice, as naming a person and their own
// agent both does: a seat answers a message once, and two turns of one agent
// at the same time would only collide on its thread.
func distinctSeats(seats []Seat) []Seat {
	var out []Seat
	seen := make(map[string]bool, len(seats))
	for _, seat := range seats {
		key := "agent:" + seat.AgentID
		if seat.AgentID == "" {
			key = "handle:" + seat.Handle
		}
		if seen[key] {
			continue
		}
		seen[key] = true
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

// seatHandles is addressedHandles narrowed to the handles findSeat resolves
// among seats: whom a message named, as the room resolves a name when it
// works out who answers. An "@" before anything that is no seat here — the
// domain of an email address, a handle from another room, a word — names
// nobody, so it is left out. This is the list a message is stored as
// addressing and its event publishes, so what reaches the bus is the room's
// own seat handles, never a word of what was said. nil when it named no seat.
func seatHandles(seats []Seat, body, authorHandle string) []string {
	var named []string
	for _, handle := range addressedHandles(body, authorHandle) {
		if _, ok := findSeat(seats, handle); ok {
			named = append(named, handle)
		}
	}
	return named
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

// perTurn is how long one seat's turn may take.
func (relay *Relay) perTurn() time.Duration {
	if relay.TurnBudget > 0 {
		return relay.TurnBudget
	}
	return turnBudget
}

// perPost is how long a whole Post may spend running agents.
func (relay *Relay) perPost() time.Duration {
	if relay.Budget > 0 {
		return relay.Budget
	}
	return relayBudget
}

// Package rooms is a teamspace as the conversation it always wanted to be:
// the people on a team and their agents talking in one place, rather than
// publishing finished work at each other.
//
// A room is a teamspace. Everything here hangs off one: who may read it is
// the team's own membership, resolved live, and what the room concludes is
// lifted into that teamspace's ledger rather than published by hand.
//
// Three things keep a room from becoming either a wall of noise or a wall of
// silence, and all three are judgments rather than rules:
//
//   - Who answers. A message addressed to someone is theirs; a message
//     addressed to nobody may still be work, and the agent whose role fits it
//     is the one that should pick it up.
//   - What is worth keeping. A room says a great deal and decides a little.
//     Only the little goes to the ledger and to the organization's context.
//   - When to catch up. A rolling brief stands in for the scrollback: it is
//     what an agent reads instead of the whole room, and what a person who
//     joins late is handed.
//
// [Decider] makes those judgments. In production it is Jev, which answers
// typed questions in a few hundred milliseconds for a fraction of what a
// model turn costs; without a key it falls back to rules that are honest
// about being rules.
package rooms

import (
	"context"
	"time"
)

// SeatKind is what a participant in a room is.
type SeatKind string

const (
	// SeatPerson is a member of the team, present or not.
	SeatPerson SeatKind = "person"
	// SeatAgent is a member's own agent, which speaks for them and is never
	// mistaken for them.
	SeatAgent SeatKind = "agent"
)

// Seat is one participant, as the room shows them to everybody else.
type Seat struct {
	// Handle is the seat's name without the "@", "ada", which a message
	// addresses as "@ada".
	Handle string   `json:"handle"`
	Kind   SeatKind `json:"kind"`
	// UserID is the person, or for an agent the person it speaks for.
	UserID string `json:"user_id"`
	// AgentID and AgentName are set for an agent seat.
	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	// SpeaksFor is the person an agent seat is the agent OF, set only for
	// somebody's own agent and never for a contributed specialist. It is
	// what lets a reader tell "Ada's agent, standing in while she is away"
	// from "the pricing specialist the team shares" — two very different
	// things to be answered by, which look identical without it.
	SpeaksFor string `json:"speaks_for,omitempty"`
	// DisplayName is the person's name, or the agent's.
	DisplayName string `json:"display_name"`
	// Role is what this seat is here for, in the member's own words — "sales",
	// "keeps the launch checklist". It is what routing reads, so it is worth
	// asking people for.
	Role string `json:"role,omitempty"`
	// Present says the person is reading the room now. A present person's
	// agent does not answer over them.
	Present bool `json:"present"`
}

// Message is one thing said in a room, by a person or by an agent.
type Message struct {
	ID           string   `json:"id"`
	RoomID       string   `json:"room_id"`
	Seq          int64    `json:"seq"`
	AuthorHandle string   `json:"author_handle"`
	AuthorKind   SeatKind `json:"author_kind"`
	AuthorUserID string   `json:"author_user_id"`
	// AuthorAgentID is set when an agent said it.
	AuthorAgentID string `json:"author_agent_id,omitempty"`
	Body          string `json:"body"`
	// AddressedTo holds the handles of the room's seats the body named with
	// "@handle", the author excluded, in the order it first named them, as the
	// relay resolved them when it stored the message: never an "@" before
	// anything that was not a seat, such as an email address's domain. The
	// message's event publishes the same list as addressed_handles.
	AddressedTo []string `json:"addressed_to,omitempty"`
	// InReplyToSeq is the message this one answers, when an agent was woken
	// by one in particular.
	InReplyToSeq int64     `json:"in_reply_to_seq,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Brief is what the room amounts to so far: what an agent is given instead of
// the whole scrollback, and what somebody who joins late is handed.
type Brief struct {
	RoomID string `json:"room_id"`
	// Revision increases every time the brief is rewritten.
	Revision int64 `json:"revision"`
	// ThroughSeq is the last message the brief accounts for.
	ThroughSeq int64 `json:"through_seq"`
	// Summary is the state of the conversation in prose: what it is about and
	// where it has got to.
	Summary string `json:"summary"`
	// Decided, Open and Owed are the parts a person scanning the room needs:
	// what was settled, what is still a question, and who owes what to whom.
	Decided   []string  `json:"decided"`
	Open      []string  `json:"open"`
	Owed      []string  `json:"owed"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Verdict is what the [Decider] made of one message. Every field is a
// judgment the code acts on; none of them is an instruction from the model.
type Verdict struct {
	// Answer says an agent should take a turn. False for people talking
	// among themselves and for acknowledgement.
	Answer bool `json:"answer"`
	// Speaker is the handle of the agent seat that should answer, when the
	// message named nobody. Empty means the decider had nobody to suggest.
	Speaker string `json:"speaker,omitempty"`
	// Keep says this message changed what the organization should remember,
	// and Kind says what it was: "decision", "commitment" or "fact".
	Keep bool   `json:"keep"`
	Kind string `json:"kind,omitempty"`
	// Compact says the room has moved far enough that the brief no longer
	// describes it.
	Compact bool `json:"compact"`
	// Certainty is how sure the decider was about answering, from 0 to 1. It
	// is recorded so a room's behaviour can be tuned against what really
	// happened rather than against an opinion.
	Certainty float64 `json:"certainty"`
	// Why is one line for the log and for the record, never shown as though
	// the room said it.
	Why string `json:"why,omitempty"`
	// Model names what judged, so a change in behaviour can be attributed.
	Model string `json:"model,omitempty"`
}

// Decider judges one message against the room it was said in. It is asked on
// every message, so it has to be cheap and it has to be quick: this is the
// difference between a room that feels alive and one that costs a model turn
// to say good morning.
//
// It is never asked to decide anything a rule could decide. A message that
// addresses somebody by name has already chosen its speaker.
type Decider interface {
	Decide(ctx context.Context, room Situation, message Message) (Verdict, error)
}

// Situation is what the decider is shown: the room, who is in it, what it
// amounts to so far, and the last few things said. Deliberately small — the
// brief is what stands in for everything older.
type Situation struct {
	// RoomID and OrganizationID say which teamspace this is, and whose.
	// An agent taking a turn is looked up in that organization, so a room
	// that did not carry it could not run anybody.
	RoomID         string `json:"room_id"`
	OrganizationID string `json:"organization_id"`
	// Name and Purpose are the teamspace's own.
	Name    string `json:"name"`
	Purpose string `json:"purpose,omitempty"`
	Seats   []Seat `json:"seats"`
	Brief   Brief  `json:"brief"`
	// Recent holds the last few messages, oldest first, the new one excluded.
	Recent []Message `json:"recent"`
}

// Compactor rewrites a room's brief. It reads what the brief already said and
// what has been said since, and returns the brief that replaces it — the same
// consolidate-rather-than-append contract the organization synthesis follows.
type Compactor interface {
	Compact(ctx context.Context, room Situation, since []Message) (Brief, error)
}

// Speaker takes one agent's turn in a room. The transcript it is given is
// rendered from that agent's own seat: its own messages are its own, and
// everyone else's — person or agent — arrive as somebody else speaking, so an
// agent hears a colleague's agent exactly as it hears a colleague.
//
// An agent with nothing to add answers [Pass], which is how a relay ends
// quietly instead of on a turn limit.
type Speaker interface {
	Speak(ctx context.Context, seat Seat, room Situation, transcript []Message) (string, error)
}

// Pass is what an agent says when it has nothing worth adding. Agents
// answering agents will otherwise fill a room with acknowledgement forever.
const Pass = "PASS"

// Store keeps a room's messages, its brief, and who is reading it.
type Store interface {
	// Append adds a message and gives it the room's next sequence number.
	Append(ctx context.Context, message Message) (Message, error)
	// Messages returns the room's messages after seq, oldest first, at most
	// limit of them.
	Messages(ctx context.Context, roomID string, afterSeq int64, limit int) ([]Message, error)
	// Recent returns the last limit messages, oldest first.
	Recent(ctx context.Context, roomID string, limit int) ([]Message, error)
	// Brief returns the room's brief, or the zero brief when it has none.
	Brief(ctx context.Context, roomID string) (Brief, error)
	// SaveBrief replaces the brief when expected matches the revision it
	// holds, so two compactions cannot overwrite one another.
	SaveBrief(ctx context.Context, brief Brief, expected int64) (Brief, error)
	// Watch records that a person is reading the room now.
	Watch(ctx context.Context, roomID, userID string, at time.Time) error
	// Watching lists the people who have read the room since at.
	Watching(ctx context.Context, roomID string, since time.Time) ([]string, error)
	// MarkRead moves userID's read cursor in roomID forward to seq, never
	// back and never past the room's last message, and returns where it
	// stands.
	MarkRead(ctx context.Context, roomID, userID string, seq int64) (int64, error)
	// Activity is, for each of roomIDs in the organization, how much of it
	// userID has not read and the last thing said there, read in one go
	// however many rooms are asked about.
	Activity(ctx context.Context, organizationID, userID string, roomIDs []string) (map[string]Activity, error)
	// Forget removes a room's messages, brief and presence.
	Forget(ctx context.Context, organizationID, userID string) error
}

// Activity is one member's standing in one room: how far they have read, how
// many messages after that somebody else said, and the last message.
type Activity struct {
	LastReadSeq int64
	// Unread counts the messages after LastReadSeq, leaving out what the
	// member said themself. What their own agent said counts: they were not
	// the one saying it.
	Unread int64
	// Last is the room's newest message, nil for a room nobody has spoken in.
	Last *Message
}

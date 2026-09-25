// Package v1 holds the event contracts realtime publishes on EventBridge.
// Every Oryxa publisher puts the same envelope around one of these details:
// event_id, version, occurred_at and data.
//
// Direct messages and teamspace side conversations live in the web app's
// Durable Objects. The Worker tells realtime what happened in them
// (POST /v1/realtime/events, realtime/README.md), and realtime publishes it
// here, so the rest of the platform can react. As in agents' request events,
// EventBridge carries identities and lifecycle state, never what was said: no
// bodies, no previews, no titles.
//
// Events arrive in no particular order. Realtime's outbox publishes a batch in
// parallel, and a failed event later, and EventBridge keeps no order either,
// so a conversation's seq 2 can reach a consumer before its seq 1. A consumer
// orders a conversation's messages by Message.Seq, never by arrival.
package v1

import "time"

// Source identifies realtime on the bus.
const Source = "oryxa.realtime"

const (
	// MessagePostedDetailType is published for a stored, finished message: a
	// person's message, or an agent's final answer in a side conversation.
	// It never covers streaming, typing, request cards or failures.
	MessagePostedDetailType = "realtime.message.posted.v1"
	// ConversationReadDetailType is published when a member's read position
	// in a conversation advances. The Worker coalesces them: only the newest
	// position it had not yet sent for a reader is published.
	ConversationReadDetailType = "realtime.conversation.read.v1"
	// PurgeCompletedDetailType is published once the Worker has purged what
	// a member kept in an organization, or everything the organization kept,
	// as the audit trail of that deletion. Realtime publishes it itself; the
	// Worker cannot send it.
	PurgeCompletedDetailType = "realtime.purge.completed.v1"
)

// The kinds of conversation, as Conversation.Kind.
const (
	// ConversationDirect is a direct message between two members.
	ConversationDirect = "direct"
	// ConversationSide is a side conversation: two teammates and their
	// agents, in one teamspace.
	ConversationSide = "side"
)

// Who wrote a message, as Author.Kind.
const (
	AuthorMember = "member"
	AuthorAgent  = "agent"
)

// What a purge took, as PurgeCompleted.Scope.
const (
	// PurgeScopeMember is what one member kept in the organization.
	PurgeScopeMember = "member"
	// PurgeScopeOrganization is everything the organization kept.
	PurgeScopeOrganization = "organization"
)

// Conversation names one conversation.
type Conversation struct {
	Kind string `json:"kind"`
	// ID is the name of the Durable Object that holds the conversation:
	// "{organization}~{user}~{user}" for a direct message and
	// "{organization}~{teamspace}~{user}~{user}" for a side conversation, the
	// two users' ids sorted.
	ID string `json:"id"`
	// TeamspaceID is set exactly when Kind is ConversationSide.
	TeamspaceID string `json:"teamspace_id,omitempty"`
}

// Author is who wrote a message.
type Author struct {
	Kind string `json:"kind"`
	// UserID is the member who wrote it, or the member whose agent did.
	UserID string `json:"user_id"`
	// AgentID is set exactly when an agent wrote it.
	AgentID string `json:"agent_id,omitempty"`
}

// Message names one message in its conversation.
type Message struct {
	// ID is the conversation's stable id for the message.
	ID string `json:"id"`
	// Seq is the message's place in its conversation, counting from 1: the
	// order to put the conversation's messages in, whatever order their
	// events arrive in.
	Seq int64 `json:"seq"`
}

// MessagePosted announces a stored, finished message, without what it said.
type MessagePosted struct {
	OrganizationID string       `json:"organization_id"`
	Conversation   Conversation `json:"conversation"`
	// ParticipantUserIDs are the conversation's two members, sorted: the last
	// two segments of its id.
	ParticipantUserIDs []string  `json:"participant_user_ids"`
	Message            Message   `json:"message"`
	Author             Author    `json:"author"`
	PostedAt           time.Time `json:"posted_at"`
}

// ConversationRead announces that a member has read a conversation through a
// message.
type ConversationRead struct {
	OrganizationID string       `json:"organization_id"`
	Conversation   Conversation `json:"conversation"`
	// ReaderUserID is one of the conversation's two members.
	ReaderUserID string `json:"reader_user_id"`
	// ReadThroughSeq is the newest message the reader has read. Reads, too,
	// can arrive out of order: the reader has read through the highest seen.
	ReadThroughSeq int64     `json:"read_through_seq"`
	ReadAt         time.Time `json:"read_at"`
}

// PurgeCompleted records that the Worker purged the conversations a member
// kept in an organization, or all the organization's, written up to
// RemovedAt.
type PurgeCompleted struct {
	OrganizationID string `json:"organization_id"`
	Scope          string `json:"scope"`
	// UserID is the member whose conversations went, set exactly when Scope
	// is PurgeScopeMember.
	UserID string `json:"user_id,omitempty"`
	// RemovedAt is when the membership, or the organization, ended: the purge
	// took what was written then or before.
	RemovedAt time.Time `json:"removed_at"`
	// SourceEventID is the id of auth's event that asked for the purge.
	SourceEventID string    `json:"source_event_id"`
	CompletedAt   time.Time `json:"completed_at"`
}

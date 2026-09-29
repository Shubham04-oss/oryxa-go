// Package surfaces says, in Slack and in the asker's agent chat, what the
// ledger says of a request that was asked in Slack.
//
// A member mentions the Oryxa app and names the people whose agents they want
// to ask. The router files the ask (slackrouter, interactions) and tells the
// asker, alone, that it did. Everything that follows is this package's, and all
// of it is a view of the ledger:
//
//   - the owner is sent a notice in a direct message, with three buttons
//     (interactions.ConsentTickets), and the notice is replaced with what became
//     of it, whichever surface the owner decided on;
//   - an answer is posted in the thread it was asked in, saying how far its owner
//     let it go, or in the asker's own direct message where a bypass says so;
//   - an answer held for its owner's review is a status with no word of the
//     answer in it, and a refusal, or a request that ended without an answer, is
//     said to the asker alone;
//   - an owner who was not asked is told that their agent answered anyway;
//   - the asker's own agent chat gets a note without a model (threads.Note).
//
// It consumes agents.interaction.* events, and believes none of them: an event
// says which request to look at and nothing else, and every pass re-reads the
// request, its ask and its parties from the ledger and says whatever Slack does
// not yet say. So a delivery that arrives twice, late or out of order is a look
// at the ledger and nothing said twice; and what a pass posts is only ever what
// the ledger holds now. A request is worked on by one pass at a time, and events
// that arrive during one leave it due again, which is what stops a notice being
// posted after the decision that settled it.
//
// What a message says is decided in apps from a template and typed params;
// this package sends the params. Every post carries an idempotency key of the
// request and the message's role, so a pass that failed after posting and is
// tried again sends nothing twice.
//
// The rules it keeps:
//
//   - it never posts the router's acknowledgement (ask_ack): the router does;
//   - it never posts a secret. A secret is shared as a one-time view in Oryxa
//     and is in no ledger, event or model context; for an ask of a secret the
//     thread is told only that the answer was given privately, whatever the
//     answer holds. What an answer that is held, or withheld, says is never read;
//   - a bypass posts where consentpolicy.AnswerFor says, and reads the channel's
//     mark again first: an answer goes into a thread only while the channel is
//     still private, unshared and marked, and otherwise goes to the asker alone;
//   - it logs ids and states, never what anyone asked, answered or clicked.
package surfaces

// The roles of a request's messages. A request has at most one of each, and its
// link (Store.Link) says where it is and what it says.
const (
	// roleNotice is the owner's notice, in a direct message.
	roleNotice = "notice"
	// roleBypassNotice tells the owner their agent answered without asking them.
	roleBypassNotice = "bypass_notice"
	// roleHeld is the thread's status that an answer is held for review.
	roleHeld = "held"
	// roleAnswer is the answer itself, in the thread or the asker's direct message.
	roleAnswer = "answer"
	// rolePrivateNote is the thread's note that the answer went to one person.
	rolePrivateNote = "private_note"
	// roleDenied and roleClosed are what the asker is told alone, in the thread:
	// that the owner chose not to share, and that the request ended unanswered.
	roleDenied = "denied"
	roleClosed = "closed"
	// roleNotePrefix begins the role of a note in the asker's agent chat, which
	// ends in the state it says.
	roleNotePrefix = "note:"
)

// What a link says. A notice says which of its states it is in; the rest say
// only that they were sent or given up on.
const (
	// statePending is a notice that still has its buttons.
	statePending = "pending"
	// stateSent is a message that was posted, and stateSentPrivately an answer
	// that was posted to the asker's direct message and not to the thread.
	stateSent          = "sent"
	stateSentPrivately = "sent_private"
	// stateFailed is a message that could not be sent and never will be, so
	// that no pass tries again.
	stateFailed = "failed"
)

// The states a settled notice says, which are the decisions apps' notice_settled
// template knows.
const (
	stateApprovedSummary = "approved_summary"
	stateApprovedAccess  = "approved_access"
	stateDenied          = "denied"
	stateAnsweredByOther = "answered_by_other"
	stateExpired         = "expired"
)

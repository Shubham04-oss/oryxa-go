// What a page is handed about a side conversation: the contract its views
// build against (useSideConversation). REALTIME.md says where each part
// comes from.
import type {
  SideAgentMessageStatus,
  SideApproval,
  SideConfirmation,
  SideDecision,
  SideGuard,
  SideHeldAnswer,
  SideOutcome,
  SideRequestStatus,
  SideReview,
  SideReviewAction,
  SideVia,
} from "./protocol"

export type {
  SideAgentMessageStatus,
  SideApproval,
  SideConfirmation,
  SideDecision,
  SideGuard,
  SideHeldAnswer,
  SideOutcome,
  SideRequestStatus,
  SideReview,
  SideReviewAction,
  SideVia,
}

/**
 * A person's message, oldest first in a view with every other item.
 *
 * - `seq` is the conversation's own order over every kind of item: gapless
 *   from 1, each one newer than any before, and never used twice, not even
 *   after a purge takes items away (the next run goes on from the
 *   highest). It is 0 while the object has not confirmed the message
 *   (`status`).
 * - `clientId` is the page's own id for a message it sent, kept once it is
 *   confirmed so a view can keep its key. Only the author's own carry it.
 * - `asksAgentId` marks a message that asked its author's own agent
 *   (`askMyAgent`); the answer follows as an agent message whose `replyTo`
 *   is this message's seq.
 * - `status` is set only on the viewer's own messages the object has not
 *   confirmed: `sending`, or `failed` with `error` (the object refused it,
 *   or never answered); `retry(clientId)` sends a failed one again.
 */
export type SideMessage = {
  kind: "message"
  id: string
  seq: number
  authorId: string
  body: string
  createdAt: string
  clientId?: string
  asksAgentId?: string
  status?: "sending" | "failed"
  error?: string
}

/**
 * An agent's message: its owner's agent answering them (`replyTo`, the
 * question's seq), or a teammate's agent's result for a request (`via`).
 * Attributed to the agent (`agentId`, `agentName`) and its owner
 * (`ownerId`).
 *
 * - `status`: `streaming` while its run answers (`body` grows live, for
 *   both people), `waiting` while the run waits for its owner to confirm a
 *   tool call, `done`, or `failed` with `error` (and `code`: Go's refusal
 *   code, or `session_expired`).
 * - `confirmations` is what a `waiting` run waits on, on the owner's view
 *   only; the owner answers every one with `confirm`. The other person sees
 *   only that it waits.
 * - `via` is a request's result: `onBehalfOf` the agent's owner, approved by
 *   `approvedBy` (the owner), or by nobody when the agent decided itself
 *   (automatic delegation), and `reviewedBy` the owner when the answer
 *   guard held the answer for them first. A view says so in a quiet line:
 *   "on behalf of Priya · approved by Priya · reviewed by Priya".
 */
export type SideAgentMessage = {
  kind: "agent-message"
  id: string
  seq: number
  agentId: string
  agentName: string
  ownerId: string
  body: string
  createdAt: string
  updatedAt: string
  status: SideAgentMessageStatus
  error?: string
  code?: string
  replyTo?: number
  via?: SideVia
  confirmations?: SideConfirmation[]
}

/**
 * A request to a teammate's agent (an agent request in Go), as a card both
 * people see, its status kept live while it is open.
 *
 * - `requesterId` asked `targetAgentId`, the agent of `targetId`.
 * - `approval: "owner"` holds it for `targetId`, who approves or declines it
 *   on the card (`decide`), from their own session; `"agent"` lets the agent
 *   decide (a specialist contributed with automatic delegation).
 * - `status` is Go's: `pending`, `approved`, then `completed` (the result
 *   follows as an agent message) or `failed`, or `rejected`, or `expired`.
 *   An answer the answer guard holds is `review` until `targetId` reviews
 *   it (`review`): then `completed`, or `withheld` when they chose not to
 *   share it or it expired first. Before Go has it, the viewer's own
 *   request is `sending`, or `not_filed` with `error` (retry(clientId) asks
 *   again); its `requestId` is "" until then.
 * - `requestedTools` is what it asks to use: toolbox names (`apps`, the
 *   owner's connected apps; `knowledge`, the team's knowledge). Approving
 *   it allows them, or none (`decide`'s `allowedTools`).
 * - `guard` and `heldAnswer` are on the target owner's view only: what the
 *   answer guard found, and, while `review`, the answer held for them as it
 *   would be shared and fully masked (`review(requestId, action)`).
 * - `deciding` is set on the target owner's view while their decision or
 *   review is on its way; `decisionError` says why the last one was
 *   refused.
 */
export type SideRequestCard = {
  kind: "request-card"
  id: string
  seq: number
  requestId: string
  requesterId: string
  requesterAgentId: string
  requesterAgentName: string
  targetId: string
  targetAgentId: string
  targetAgentName: string
  approval: SideApproval
  status: SideRequestStatus | "sending" | "not_filed"
  task: string
  createdAt: string
  updatedAt: string
  expiresAt: string
  decision?: SideDecision
  clientId?: string
  requestedTools?: string[]
  guard?: SideGuard
  heldAnswer?: SideHeldAnswer
  review?: SideReview
  error?: string
  deciding?: boolean
  decisionError?: string
}

/**
 * A quiet line in the transcript: a request declined (`rejected`), failed
 * or expired, or an answer its owner did not share (`withheld`). `text`
 * says it in the object's words; `authorId` is the target agent's owner.
 */
export type SideStatusLine = {
  kind: "status"
  id: string
  seq: number
  requestId: string
  outcome: SideOutcome
  authorId: string
  text: string
  createdAt: string
}

/** One item of a side conversation, oldest first; the viewer's own not yet confirmed last. */
export type SideItem =
  SideMessage | SideAgentMessage | SideRequestCard | SideStatusLine

/**
 * An agent in the conversation: a person's primary agent, or a specialist
 * they contributed to the teamspace.
 *
 * - `approval` is who approves a request to it: its owner, unless it is a
 *   specialist contributed with automatic delegation (`agent`).
 * - `role` is a specialist's role in the teamspace.
 */
export type AgentRef = {
  id: string
  name: string
  ownerId: string
  kind: "primary" | "specialist"
  approval: SideApproval
  role?: string
}

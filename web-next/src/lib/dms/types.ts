// What a page is handed about direct messages: the contract the DM views
// build against (useDirectMessages, useInbox). REALTIME.md says where each
// part comes from.

/**
 * One message in a DM, oldest first in a view.
 *
 * - `seq` is the conversation's own order: gapless from 1, each one newer
 *   than any before, and never used twice, not even after a purge takes
 *   messages away (the next run goes on from the highest). It is 0 while
 *   the object has not confirmed the message (`status` set).
 * - `createdAt` is when the object stored it (ISO 8601, UTC, milliseconds),
 *   or when the page sent it while it is not confirmed: the day separators
 *   read it.
 * - `clientId` is the page's own id for a message it sent, kept on the
 *   confirmed message too so a view can keep its key across confirmation.
 *   Only the author's own messages carry it.
 * - `status` is set only on the viewer's messages the object has not
 *   confirmed: `sending`, or `failed` (the object refused it, or never
 *   answered); `retry(clientId)` sends a failed one again.
 */
export type DmMessage = {
  id: string
  seq: number
  authorId: string
  body: string
  createdAt: string
  clientId?: string
  status?: "sending" | "failed"
}

/**
 * One conversation in a member's inbox: a DM (`dm`), or a side conversation
 * in a teamspace (`side`, which carries the teamspace).
 *
 * - `name` is the conversation object's name (`directMessageName`).
 * - `lastMessage.body` is a preview: at most 160 characters on one line.
 * - `unread` counts the messages from others after what the member has read,
 *   up to 999.
 * - `updatedAt` is its last message's time, or when it was listed; the
 *   inbox lists the latest first.
 */
export type Conversation = {
  kind: "dm" | "side"
  name: string
  peerId: string
  teamspaceId?: string
  teamspaceName?: string
  lastMessage?: { authorId: string; body: string; createdAt: string }
  unread: number
  updatedAt: string
}

export type ConversationKind = Conversation["kind"]

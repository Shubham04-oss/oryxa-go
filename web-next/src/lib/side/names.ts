// The name of a side conversation's Durable Object (REALTIME.md): a page
// builds it, and the Worker (src/server.ts) and the object parse it back
// before trusting it. The builder and parser live beside the DM's, since an
// inbox checks both kinds of name (src/inbox-agent.ts).
export {
  isNameId,
  parseSideConversationName,
  peerIn,
  sideConversationName,
} from "@/lib/dms/names"

/** The side conversation's class and binding, useAgent's `agent`: the Worker serves it at `/agents/side-conversation-agent/{name}`. */
export const SIDE_CONVERSATION_AGENT = "SideConversationAgent"

/** The ids of a side conversation's name. */
export type SideConversationIds = {
  organizationId: string
  teamspaceId: string
  /** The two people, sorted. */
  userA: string
  userB: string
}

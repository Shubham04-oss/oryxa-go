import type { DirectMessageAgent } from "./direct-message-agent"
import type { InboxAgent } from "./inbox-agent"
import type { RealtimeRegistryAgent } from "./realtime-registry-agent"
import type { SideConversationAgent } from "./side-conversation-agent"

/**
 * What the conversation objects (a DM, a side conversation, a member's
 * inbox, the organization's registry) read from the Worker's environment:
 * each other's bindings, Go's origin, and the secret Go and the Worker sign
 * with, both ways (src/realtime-purge.ts, src/realtime-events.ts).
 */
export interface ConversationEnv extends Cloudflare.Env {
  DirectMessageAgent: DurableObjectNamespace<DirectMessageAgent>
  SideConversationAgent: DurableObjectNamespace<SideConversationAgent>
  InboxAgent: DurableObjectNamespace<InboxAgent>
  RealtimeRegistryAgent: DurableObjectNamespace<RealtimeRegistryAgent>
  /**
   * Where the Go API answers, such as `https://api.oryxa.in`: a DM and a
   * side conversation send their events there (`POST /v1/realtime/events`).
   */
  ORYXA_API_ORIGIN: string
  /**
   * The HMAC-SHA256 key shared with Go: it signs `POST
   * /internal/realtime/purge` (Go to the Worker) and the conversation events
   * (the Worker to Go). A Worker secret (`wrangler secret put
   * REALTIME_PURGE_SECRET`, or `.dev.vars` locally), trimmed of surrounding
   * whitespace, as Go trims it. A purge needs at least 32 characters, or it
   * is refused; without it, no conversation records events.
   */
  REALTIME_PURGE_SECRET?: string
}

/**
 * The headers the Worker sets on a request it routes to a DM or an inbox,
 * replacing any `X-Oryxa-*` header the browser sent, once Go has said who
 * the member is and that they may open it. The objects never call Go, so
 * they are not handed the member's cookie.
 */
export const conversationRequestHeaders = {
  organization: "X-Oryxa-Organization",
  /** The member's user id, as Go answered the Worker for their cookie. */
  user: "X-Oryxa-User",
} as const

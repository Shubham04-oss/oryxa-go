import type { DirectMessageAgent } from "./direct-message-agent"
import type { InboxAgent } from "./inbox-agent"
import type { RealtimeRegistryAgent } from "./realtime-registry-agent"

/**
 * What the conversation objects (a DM, a member's inbox, the organization's
 * registry) read from the Worker's environment: each other's bindings, and
 * the secret Go signs a purge with (src/realtime-purge.ts).
 */
export interface ConversationEnv extends Cloudflare.Env {
  DirectMessageAgent: DurableObjectNamespace<DirectMessageAgent>
  InboxAgent: DurableObjectNamespace<InboxAgent>
  RealtimeRegistryAgent: DurableObjectNamespace<RealtimeRegistryAgent>
  /**
   * The HMAC-SHA256 key of `POST /internal/realtime/purge`, shared with Go:
   * a Worker secret (`wrangler secret put REALTIME_PURGE_SECRET`, or
   * `.dev.vars` locally), at least 32 characters. Without it every purge is
   * refused.
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

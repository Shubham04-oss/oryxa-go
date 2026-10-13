// Side conversations, as a page builds on them: a teamspace's two
// teammates and both their agents (REALTIME.md). The views are
// components/side-conversation.tsx; this is what they build against.
export { SIDE_CONVERSATION_AGENT, sideConversationName } from "./names"
export { SIDE_MAX_BODY_LENGTH } from "./protocol"
export { sideConversationPath } from "./client"
export type {
  AgentRef,
  SideAgentMessage,
  SideItem,
  SideMessage,
  SideRequestCard,
  SideReviewAction,
  SideStatusLine,
} from "./types"
export { SideActionError, useSideConversation } from "./use-side-conversation"
export type {
  SideConversation,
  SideConversationStatus,
} from "./use-side-conversation"

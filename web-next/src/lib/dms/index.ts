// Direct messages, as a page builds on them: the DM views (the main canvas
// and the right panel's DMs tab) and the inbox that lists them. REALTIME.md
// says what is behind each.
export { directMessageName } from "./names"
export { DM_MAX_BODY_LENGTH } from "./protocol"
export type { Conversation, DmMessage } from "./types"
export { useDirectMessages } from "./use-direct-messages"
export type {
  DirectMessages,
  DirectMessagesStatus,
} from "./use-direct-messages"
export { useInbox } from "./use-inbox"
export type { Inbox, InboxStatus } from "./use-inbox"
// A request between the two people, as a DM's log says it (REALTIME.md,
// "Request notes"); the shell wraps what shows a DM in OpenRequestProvider.
export { OpenRequestProvider, useOpenRequest } from "./request-navigation"
export type { OpenRequest } from "./request-navigation"
export type {
  DmRequestNote,
  RequestLineState,
  RequestNoteState,
} from "./request-notes"

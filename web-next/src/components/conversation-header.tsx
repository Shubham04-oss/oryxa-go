import type { Dispatch } from "react"
import { useQuery } from "@tanstack/react-query"
import { useHydrated } from "@tanstack/react-router"
import { Files, SquarePen } from "lucide-react"
import { firstMessage, shownThread } from "@/lib/threads/chat-choice"
import type {
  ChatScope,
  PrimaryChat,
  PrimaryChatAction,
} from "@/lib/threads/chat-choice"
import { threadListQueryOptions } from "@/lib/threads/client"

/** The conversation's files, as its header offers them. */
export type ConversationFilesToggle = {
  /** How many files the conversation holds, when known. */
  count: number
  open: boolean
  onToggle: () => void
  /** The id of what the button shows and hides. */
  controls: string
}

/**
 * The open conversation's title, in the header the center shares with every
 * screen (DESIGN.md §2), with the conversation's Files and New chat. The
 * title is Go's (the first line of the first message), "New conversation"
 * until it has one.
 */
export function ConversationHeader({
  organizationId,
  scope,
  chat,
  dispatch,
  files,
  onNewChat,
}: {
  organizationId: string
  /** Whose conversations: the list read is this member's own entry. */
  scope?: ChatScope
  chat: PrimaryChat
  dispatch: Dispatch<PrimaryChatAction>
  /** Offered once there is a conversation to hold files. */
  files?: ConversationFilesToggle
  /** Told after New chat moved the chat to a new conversation. */
  onNewChat?: () => void
}) {
  const hydrated = useHydrated()
  const threads = useQuery({
    ...threadListQueryOptions(organizationId, scope?.userId),
    enabled: hydrated,
  })
  const threadId = shownThread(chat)
  const title =
    (threadId &&
      threads.data?.find((thread) => thread.id === threadId)?.title.trim()) ||
    "New conversation"
  // A first message on its way goes first: the chat can't be moved until its
  // conversation has it.
  const busy = firstMessage(chat) !== null

  return (
    <>
      <h1 className="workspace-conversation-title" title={title}>
        {title}
      </h1>
      <div className="workspace-header-end">
        {files && typeof threadId === "string" && (
          <button
            className="workspace-header-action"
            type="button"
            aria-expanded={files.open}
            aria-controls={files.controls}
            aria-label={
              files.count > 0
                ? `Files, ${files.count} in this conversation`
                : "Files"
            }
            title="Files in this conversation"
            onClick={files.onToggle}
          >
            <Files size={15} strokeWidth={1.7} aria-hidden="true" />
            <span className="workspace-header-action-label">Files</span>
            {files.count > 0 && (
              <span className="workspace-header-count" aria-hidden="true">
                {files.count}
              </span>
            )}
          </button>
        )}
        <button
          className="workspace-header-action"
          type="button"
          aria-label="New conversation"
          title="New conversation"
          disabled={busy}
          onClick={() => {
            dispatch({ type: "move", thread: null })
            onNewChat?.()
          }}
        >
          <SquarePen size={15} strokeWidth={1.7} aria-hidden="true" />
          <span className="workspace-header-action-label">New chat</span>
        </button>
      </div>
    </>
  )
}

import { useCallback } from "react"
import type { ReactNode } from "react"
import { releaseAgentChatSlot } from "@/hooks/use-agent-chat-host"
import "./agent-session.css"

/**
 * The primary agent's working session: the conversation on the left, the
 * work on the right (the overview, a file, or something live). The
 * conversation column is an empty slot here; the agent panel moves its
 * mounted chat into it (useAgentChatHost), so nothing in the chat restarts
 * when the session opens or closes.
 */
export function AgentSession({
  chatSlotRef,
  chatLabel,
  workLabel,
  list,
  work = "tabs",
  children,
}: {
  /** Receives the conversation column, for the panel to move its chat into. */
  chatSlotRef: (element: HTMLElement | null) => void
  /** Names the conversation column, with the agent's name where it is known. */
  chatLabel: string
  /** What the work column holds, for assistive technology. */
  workLabel: string
  /** A column before the chat: the Chats page's list. */
  list?: ReactNode
  /** Whether the work column shows its tabs or a viewer (a file, something live). */
  work?: "tabs" | "viewer"
  /** The work: the overview, a file viewer or a live pane. */
  children: ReactNode
}) {
  // The column hands the chat back while it is still in the page: removed
  // first, it would take the chat's focus with it.
  const slotRef = useCallback(
    (element: HTMLElement | null) => {
      if (!element) return
      chatSlotRef(element)
      return () => {
        releaseAgentChatSlot(element)
        chatSlotRef(null)
      }
    },
    [chatSlotRef]
  )
  return (
    <main
      className="workspace-focus-layout workspace-primary-focus-layout agent-session"
      data-list={list ? "true" : undefined}
      data-work={work}
    >
      {list}
      <section
        ref={slotRef}
        className="workspace-conversation agent-session-chat"
        aria-label={chatLabel}
      />
      <div className="workspace-work-column">
        <section className="workspace-work-panel" aria-label={workLabel}>
          {children}
        </section>
      </div>
    </main>
  )
}

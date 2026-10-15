import type { ReactNode } from "react"
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
  workLabel,
  children,
}: {
  /** Receives the conversation column, for the panel to move its chat into. */
  chatSlotRef: (element: HTMLElement | null) => void
  /** What the work column holds, for assistive technology. */
  workLabel: string
  /** The work: the overview, a file viewer or a live pane. */
  children: ReactNode
}) {
  return (
    <main className="workspace-focus-layout workspace-primary-focus-layout agent-session">
      <section
        ref={chatSlotRef}
        className="workspace-conversation agent-session-chat"
        aria-label="Agent conversation"
      />
      <div className="workspace-work-column">
        <section className="workspace-work-panel" aria-label={workLabel}>
          {children}
        </section>
      </div>
    </main>
  )
}

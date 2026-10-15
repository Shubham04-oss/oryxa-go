import { useEffect, useLayoutEffect, useRef } from "react"
import type { ReactNode, UIEvent } from "react"

/** How near the end still counts as reading the latest, in pixels. */
const followDistance = 48

/**
 * The agent panel's conversation log. It keeps the end in view as the
 * conversation grows, unless the member has scrolled up to read; `follow`
 * changing (such as their own new message) brings them back to the end.
 */
export function AgentConversation({
  agentName,
  state = "ready",
  error,
  onRetry,
  follow,
  variant,
  children,
}: {
  agentName?: string
  /** `loading` and `empty` show their line in place of the children. */
  state?: "loading" | "empty" | "ready"
  /** A quiet error line under the conversation. */
  error?: string
  onRetry?: () => void
  follow?: unknown
  /** `session` for an agent's working session, whose tasks are lines and whose work is run cards (`agent-run.tsx`). */
  variant?: "session"
  children?: ReactNode
}) {
  const logRef = useRef<HTMLDivElement>(null)
  const following = useRef(true)

  function toEnd() {
    const log = logRef.current
    if (log) log.scrollTop = log.scrollHeight
  }

  // After every render (a streamed chunk, a new block), unless the member
  // scrolled away from the end.
  useLayoutEffect(() => {
    if (following.current) toEnd()
  })

  useEffect(() => {
    following.current = true
    toEnd()
  }, [follow])

  function onScroll(event: UIEvent<HTMLDivElement>) {
    const log = event.currentTarget
    following.current =
      log.scrollHeight - log.scrollTop - log.clientHeight < followDistance
  }

  return (
    <div
      className="workspace-agent-conversation"
      data-variant={variant}
      role="log"
      aria-label={
        agentName
          ? `Conversation with ${agentName}`
          : "Conversation with your agent"
      }
      ref={logRef}
      onScroll={onScroll}
      // An image that finishes loading grows the log without a render.
      onLoadCapture={() => {
        if (following.current) toEnd()
      }}
    >
      {state === "ready" ? (
        children
      ) : (
        <p className="workspace-agent-empty">
          {state === "loading"
            ? "Loading the conversation…"
            : "Ask anything to start a conversation."}
        </p>
      )}
      {error && (
        <p className="workspace-agent-error" role="alert">
          {error}
          {onRetry && (
            <>
              {" "}
              <button
                className="workspace-agent-text-button"
                type="button"
                onClick={onRetry}
              >
                Try again
              </button>
            </>
          )}
        </p>
      )}
    </div>
  )
}

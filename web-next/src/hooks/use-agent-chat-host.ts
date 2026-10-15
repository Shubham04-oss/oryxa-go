import { useEffect, useLayoutEffect, useRef, useState } from "react"
import type { ReactNode } from "react"
import { createPortal } from "react-dom"

/**
 * The primary agent's conversation, kept in one host element that sits in
 * the agent panel (at `pageRef`) or, while the agent session is open, in the
 * session's chat column (`slot`). Moving the host moves the conversation
 * without remounting it: its draft, scroll and socket carry over, and so does
 * keyboard focus. The same pattern as a room's conversation moving into a
 * parley's Room pane (useRoomChatHost).
 */
export function useAgentChatHost(slot: HTMLElement | null | undefined) {
  const [host, setHost] = useState<HTMLDivElement | null>(null)
  const pageRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const element = document.createElement("div")
    element.className = "agent-chat-host"
    setHost(element)
    return () => {
      element.remove()
    }
  }, [])
  useLayoutEffect(() => {
    const target = slot ?? pageRef.current
    if (!host || !target || host.parentElement === target) return
    // A browser drops focus from an element that moves; give it back, so
    // the member keeps typing where they were.
    const focused = document.activeElement
    const keep = focused instanceof HTMLElement && host.contains(focused)
    target.appendChild(host)
    if (keep && document.activeElement !== focused)
      focused.focus({ preventScroll: true })
  })
  return {
    pageRef,
    render: (conversation: ReactNode) =>
      host ? createPortal(conversation, host) : null,
  }
}

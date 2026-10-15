import { useEffect, useLayoutEffect, useRef, useState } from "react"
import type { ReactNode } from "react"
import { createPortal } from "react-dom"

/**
 * What takes the conversation out of a slot that is about to go, by slot.
 * The session calls it (releaseAgentChatSlot) while the slot is still in the
 * page: once React removes the slot, the host and anything focused in it are
 * detached, and the focus can no longer be told apart from none.
 */
const releases = new WeakMap<HTMLElement, () => void>()

/**
 * Moves the host to `target`, keeping keyboard focus where it was in it.
 * Returns what had focus in the host, if anything did.
 */
function moveHost(host: HTMLElement, target: HTMLElement) {
  if (host.parentElement === target) return null
  // A browser drops focus from an element that moves; give it back, so the
  // member keeps typing where they were.
  const focused = document.activeElement
  const keep = focused instanceof HTMLElement && host.contains(focused)
  target.appendChild(host)
  if (!keep) return null
  if (document.activeElement !== focused) focused.focus({ preventScroll: true })
  return focused
}

/**
 * Sends the conversation back to the panel before `slot` leaves the page.
 * The agent session calls it when its chat column unmounts.
 */
export function releaseAgentChatSlot(slot: HTMLElement) {
  releases.get(slot)?.()
}

/**
 * The primary agent's conversation, kept in one host element that sits in
 * the agent panel (at `pageRef`) or, while the agent session is open, in the
 * session's chat column (`slot`). Moving the host moves the conversation
 * without remounting it: its draft, scroll and socket carry over, and so does
 * keyboard focus, in both directions.
 */
export function useAgentChatHost(slot: HTMLElement | null | undefined) {
  const [host, setHost] = useState<HTMLDivElement | null>(null)
  const pageRef = useRef<HTMLDivElement>(null)
  // What had focus when the session let the conversation go, given focus
  // again once the commit is done: the panel may still have been hidden
  // when the host arrived in it.
  const released = useRef<HTMLElement | null>(null)
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
    if (host && target) moveHost(host, target)
    const focused = released.current
    released.current = null
    if (
      focused &&
      focused.isConnected &&
      document.activeElement !== focused &&
      host?.contains(focused)
    )
      focused.focus({ preventScroll: true })
  })
  useLayoutEffect(() => {
    if (!host || !slot) return
    // Kept after the slot is passed on: the panel may hear that the session
    // closed before the session's column goes. A slot the host has already
    // left releases nothing.
    releases.set(slot, () => {
      if (pageRef.current && slot.contains(host))
        released.current = moveHost(host, pageRef.current)
    })
  }, [host, slot])
  return {
    pageRef,
    render: (conversation: ReactNode) =>
      host ? createPortal(conversation, host) : null,
  }
}

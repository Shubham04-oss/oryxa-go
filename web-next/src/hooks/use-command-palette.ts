import { useCallback, useEffect, useState } from "react"

export type CommandPaletteScope = {
  organizationId?: string
  viewerId?: string
}

/** Mount once beside the palette; pass IDs from the authenticated session. */
export function useCommandPalette({
  organizationId,
  viewerId,
}: CommandPaletteScope) {
  const scope = JSON.stringify([organizationId, viewerId])
  const enabled = Boolean(organizationId && viewerId)
  const [state, setState] = useState({ scope, open: false })
  // Forget the old choice, so returning to that scope does not reopen it.
  if (state.scope !== scope) setState({ scope, open: false })
  // A scope change closes immediately, before effects or a new read can run.
  const open = enabled && state.scope === scope && state.open
  const onOpenChange = useCallback(
    (next: boolean) => setState({ scope, open: enabled && next }),
    [scope, enabled]
  )

  useEffect(() => {
    if (!enabled) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (
        event.defaultPrevented ||
        event.repeat ||
        event.isComposing ||
        event.altKey ||
        event.shiftKey ||
        (!event.metaKey && !event.ctrlKey) ||
        event.key.toLowerCase() !== "k"
      )
        return
      event.preventDefault()
      event.stopPropagation()
      onOpenChange(!open)
    }
    window.addEventListener("keydown", onKeyDown, true)
    return () => window.removeEventListener("keydown", onKeyDown, true)
  }, [enabled, open, onOpenChange])

  return { open, onOpenChange }
}

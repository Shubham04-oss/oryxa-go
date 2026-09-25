import { useEffect, useState } from "react"

// While a small-screen chat is composing, the virtual keyboard shrinks the
// visual viewport; this is the height it leaves, so the chat can fit its
// conversation and composer into it (DESIGN.md §5). Null otherwise.
export function useVisibleHeight(active: boolean) {
  const [height, setHeight] = useState<number | null>(null)

  useEffect(() => {
    if (!active) return

    const media = window.matchMedia("(max-width: 800px)")
    const viewport = window.visualViewport
    function updateHeight() {
      setHeight(
        media.matches
          ? Math.round(
              Math.min(
                viewport?.height ?? window.innerHeight,
                window.innerHeight
              )
            )
          : null
      )
    }

    updateHeight()
    viewport?.addEventListener("resize", updateHeight)
    window.addEventListener("resize", updateHeight)
    media.addEventListener("change", updateHeight)
    return () => {
      viewport?.removeEventListener("resize", updateHeight)
      window.removeEventListener("resize", updateHeight)
      media.removeEventListener("change", updateHeight)
      setHeight(null)
    }
  }, [active])

  return active ? height : null
}

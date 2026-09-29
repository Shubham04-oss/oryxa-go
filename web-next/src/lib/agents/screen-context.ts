/** A bounded description of the Oryxa page, sent with a member's agent run. */
export type ScreenItem = { kind: string; id: string; name: string }
export type ScreenContext = {
  view: string
  selection?: ScreenItem
  visible_items: ScreenItem[]
}

// Go's bounds for a snapshot (agents/primary/internal/agents/screen.go), so
// that what the page sends is what Go keeps. Go leaves out any hint that
// breaks one, and never refuses a member's message over it: a hint costs the
// run that hint, nothing more. Lengths are in characters, counted as Go
// counts them, not in UTF-16 units.
const viewPattern = /^(home|team|agents)\/[a-z-]{1,60}$/
const maxVisibleItems = 12
const maxKindLength = 40
const maxIdLength = 120
const maxNameLength = 120
const lineBreak = /[\r\n]/

/** Whether `value` is longer than `limit` characters (code points, as Go counts them). */
const exceeds = (value: string, limit: number) =>
  value.length > limit * 2 ||
  (value.length > limit && Array.from(value).length > limit)
const clip = (value: string, limit: number) =>
  value.length <= limit ? value : Array.from(value).slice(0, limit).join("")

/**
 * The hint as Go would keep it, or nothing where Go would leave it out: no
 * kind or id to name it by, a line break in any field, or a field past its
 * limit.
 */
export function usableScreenItem(value: unknown): ScreenItem | undefined {
  if (!value || typeof value !== "object") return undefined
  const { kind, id, name } = value as Record<string, unknown>
  if (
    typeof kind !== "string" ||
    typeof id !== "string" ||
    typeof name !== "string"
  )
    return undefined
  if (kind === "" || id === "") return undefined
  if (lineBreak.test(kind) || lineBreak.test(id) || lineBreak.test(name))
    return undefined
  if (
    exceeds(kind, maxKindLength) ||
    exceeds(id, maxIdLength) ||
    exceeds(name, maxNameLength)
  )
    return undefined
  return { kind, id, name }
}

/**
 * Untrusted socket bodies are reduced to the shape the page emits, with what
 * Go would keep: a hint that would not survive is left out and the rest go
 * on, as they do in Go. A view that is no Oryxa page leaves nothing to point
 * at, so there is no snapshot at all.
 */
export function screenContextInput(value: unknown): ScreenContext | undefined {
  if (!value || typeof value !== "object") return undefined
  const input = value as Record<string, unknown>
  if (typeof input.view !== "string" || !viewPattern.test(input.view))
    return undefined
  const selection = usableScreenItem(input.selection)
  const items: unknown[] = Array.isArray(input.visible_items)
    ? input.visible_items
    : []
  const visible_items: ScreenItem[] = []
  for (const candidate of items) {
    const item = usableScreenItem(candidate)
    if (item && visible_items.length < maxVisibleItems) visible_items.push(item)
  }
  return {
    view: input.view,
    ...(selection ? { selection } : {}),
    visible_items,
  }
}

/** Reads only explicit semantic markers in the current work canvas, not page text or pixels. */
export function captureScreenContext(
  view: string,
  selection?: ScreenItem
): ScreenContext {
  const canvas = document.querySelector(".workspace-middle")
  const visible_items: ScreenItem[] = []
  let markedSelection: ScreenItem | undefined
  if (canvas) {
    const bounds = canvas.getBoundingClientRect()
    for (const element of canvas.querySelectorAll<HTMLElement>(
      "[data-screen-context-kind][data-screen-context-id][data-screen-context-name]"
    )) {
      const rect = element.getBoundingClientRect()
      if (
        rect.width <= 0 ||
        rect.height <= 0 ||
        rect.bottom <= bounds.top ||
        rect.top >= bounds.bottom ||
        rect.right <= bounds.left ||
        rect.left >= bounds.right
      )
        continue
      const {
        screenContextKind: kind,
        screenContextId: id,
        screenContextName: name,
      } = element.dataset
      if (kind && id && name) {
        // A long handle or name is cut to its limit; one Go would drop
        // anyway (a line break in a file's name) never leaves the page.
        const item = usableScreenItem({
          kind: clip(kind, maxKindLength),
          id: clip(id, maxIdLength),
          name: clip(name, maxNameLength),
        })
        if (!item) continue
        if (visible_items.length < maxVisibleItems) visible_items.push(item)
        if (element.dataset.screenContextSelected === "true")
          markedSelection = item
      }
    }
  }
  // The caller's own selection is held to the same bounds.
  const chosen = selection
    ? usableScreenItem({
        kind: clip(selection.kind, maxKindLength),
        id: clip(selection.id, maxIdLength),
        name: clip(selection.name, maxNameLength),
      })
    : undefined
  const marked = chosen ?? markedSelection
  return {
    view: view.slice(0, 80),
    ...(marked ? { selection: marked } : {}),
    visible_items,
  }
}

/** A bounded description of the Oryxa page, sent with a member's agent run. */
export type ScreenItem = { kind: string; id: string; name: string }
export type ScreenContext = {
  view: string
  selection?: ScreenItem
  visible_items: ScreenItem[]
}

/** Untrusted socket bodies are reduced to the same bounded shape as the page emits. */
export function screenContextInput(value: unknown): ScreenContext | undefined {
  if (!value || typeof value !== "object") return undefined
  const input = value as Record<string, unknown>
  if (
    typeof input.view !== "string" ||
    !/^(home|team|agents)\/[a-z-]{1,60}$/.test(input.view)
  )
    return undefined
  const item = (itemValue: unknown): ScreenItem | undefined => {
    if (!itemValue || typeof itemValue !== "object") return undefined
    const entry = itemValue as Record<string, unknown>
    if (
      typeof entry.kind !== "string" ||
      typeof entry.id !== "string" ||
      typeof entry.name !== "string"
    )
      return undefined
    if (
      entry.kind.length > 40 ||
      entry.id.length > 120 ||
      entry.name.length > 120
    )
      return undefined
    return { kind: entry.kind, id: entry.id, name: entry.name }
  }
  const selection =
    input.selection === undefined ? undefined : item(input.selection)
  if (input.selection !== undefined && !selection) return undefined
  if (!Array.isArray(input.visible_items) || input.visible_items.length > 12)
    return undefined
  const visible_items = input.visible_items.map(item)
  if (visible_items.some((entry) => !entry)) return undefined
  return {
    view: input.view,
    ...(selection ? { selection } : {}),
    visible_items: visible_items as ScreenItem[],
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
        const item = {
          kind: kind.slice(0, 40),
          id: id.slice(0, 120),
          name: name.slice(0, 120),
        }
        if (visible_items.length < 12) visible_items.push(item)
        if (element.dataset.screenContextSelected === "true")
          markedSelection = item
      }
    }
  }
  return {
    view: view.slice(0, 80),
    ...(selection || markedSelection
      ? { selection: selection || markedSelection }
      : {}),
    visible_items,
  }
}

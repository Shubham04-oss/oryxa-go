// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest"
import { captureScreenContext, screenContextInput } from "./screen-context"

afterEach(() => {
  document.body.replaceChildren()
})

describe("screen context", () => {
  it("keeps only marked items actually in the work canvas and retains the selected item", () => {
    const canvas = document.createElement("main")
    canvas.className = "workspace-middle"
    canvas.getBoundingClientRect = () =>
      ({
        top: 0,
        left: 0,
        right: 300,
        bottom: 300,
        width: 300,
        height: 300,
      }) as DOMRect
    document.body.appendChild(canvas)
    for (let index = 0; index < 14; index++) {
      const row = document.createElement("button")
      row.dataset.screenContextKind = "teamspace"
      row.dataset.screenContextId = `space_${index}`
      row.dataset.screenContextName = `Space ${index}`
      row.dataset.screenContextSelected = String(index === 13)
      row.getBoundingClientRect = () =>
        ({
          top: 5,
          left: 5,
          right: 200,
          bottom: 30,
          width: 195,
          height: 25,
        }) as DOMRect
      canvas.appendChild(row)
    }
    const hidden = document.createElement("button")
    hidden.dataset.screenContextKind = "teamspace"
    hidden.dataset.screenContextId = "hidden"
    hidden.dataset.screenContextName = "Hidden"
    hidden.getBoundingClientRect = () =>
      ({
        top: 500,
        left: 5,
        right: 200,
        bottom: 530,
        width: 195,
        height: 30,
      }) as DOMRect
    canvas.appendChild(hidden)

    const result = captureScreenContext("team/teamspaces")
    expect(result.visible_items).toHaveLength(12)
    expect(result.visible_items.some((item) => item.id === "hidden")).toBe(
      false
    )
    expect(result.selection?.id).toBe("space_13")
  })

  it("rejects malformed socket context rather than forwarding it to a run", () => {
    expect(
      screenContextInput({ view: "other/window", visible_items: [] })
    ).toBeUndefined()
    expect(
      screenContextInput({
        view: "team/teamspaces",
        visible_items: [{ kind: "person", id: "a" }],
      })
    ).toBeUndefined()
    expect(
      screenContextInput({ view: "team/teamspaces", visible_items: [] })
    ).toEqual({ view: "team/teamspaces", visible_items: [] })
  })
})

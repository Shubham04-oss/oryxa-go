// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest"
import {
  captureScreenContext,
  screenContextInput,
  usableScreenItem,
} from "./screen-context"

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

  it("drops the hints Go would drop and keeps the rest, rather than losing the run its snapshot", () => {
    const good = { kind: "artifact", id: "notes.md", name: "Notes" }
    // The same cases as agents/primary's TestSanitizeDropsTheHintsThat...
    const view = "home/overview"
    expect(
      screenContextInput({
        view,
        selection: good,
        visible_items: [good, { kind: "person", id: "user_1", name: "" }],
      })
    ).toEqual({
      view,
      selection: good,
      visible_items: [good, { kind: "person", id: "user_1", name: "" }],
    })
    for (const bad of [
      { kind: "", id: "x", name: "No kind" },
      { kind: "artifact", id: "", name: "No id" },
      { kind: "artifact", id: "x" },
      { kind: "a\nb", id: "x", name: "" },
      { kind: "artifact", id: "a\rb", name: "" },
      { kind: "artifact", id: "x", name: "one\ntwo" },
      { kind: "k".repeat(41), id: "x", name: "" },
      { kind: "artifact", id: "i".repeat(121), name: "" },
      { kind: "artifact", id: "x", name: "n".repeat(121) },
      "a string",
      null,
    ]) {
      expect(screenContextInput({ view, visible_items: [bad, good] })).toEqual({
        view,
        visible_items: [good],
      })
      // A selection that breaks a rule goes alone; the items stay.
      expect(
        screenContextInput({ view, selection: bad, visible_items: [good] })
      ).toEqual({ view, visible_items: [good] })
    }
    // Twelve items at most, the first twelve.
    const many = Array.from({ length: 14 }, (_, index) => ({
      kind: "artifact",
      id: `f${index}`,
      name: "",
    }))
    expect(
      screenContextInput({ view, visible_items: many })?.visible_items
    ).toEqual(many.slice(0, 12))
    // No list is an empty list; a view that is no Oryxa page is no snapshot.
    expect(screenContextInput({ view })).toEqual({ view, visible_items: [] })
    expect(
      screenContextInput({ view: "other/window", visible_items: [good] })
    ).toBeUndefined()
    expect(screenContextInput({ view: 7 })).toBeUndefined()
    expect(screenContextInput(null)).toBeUndefined()
    expect(
      screenContextInput({ view: "team/teamspaces", visible_items: [] })
    ).toEqual({ view: "team/teamspaces", visible_items: [] })
  })

  it("counts characters as Go does, so fields at their limits are kept", () => {
    const at = (kind: string, id: string, name: string) =>
      usableScreenItem({ kind, id, name })
    expect(at("k".repeat(40), "i".repeat(120), "n".repeat(120))).toBeDefined()
    expect(at("k".repeat(41), "i", "")).toBeUndefined()
    // Astral characters take two UTF-16 units and count once, as in Go.
    expect(at("artifact", "x", "🙂".repeat(120))).toBeDefined()
    expect(at("artifact", "x", "🙂".repeat(121))).toBeUndefined()
    expect(at("artifact", "x", "é".repeat(120))).toBeDefined()
  })

  it("leaves a file name with a line break, and anything past a limit, off the run instead of sending what Go would refuse", () => {
    const canvas = document.createElement("main")
    canvas.className = "workspace-middle"
    const inView = () =>
      ({
        top: 5,
        left: 5,
        right: 200,
        bottom: 30,
        width: 195,
        height: 25,
      }) as DOMRect
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
    for (const [id, name] of [
      ["notes.md", "Notes"],
      ["odd.md", "Odd\nname"],
      ["long.md", "n".repeat(300)],
    ]) {
      const row = document.createElement("button")
      row.dataset.screenContextKind = "artifact"
      row.dataset.screenContextId = id
      row.dataset.screenContextName = name
      row.getBoundingClientRect = inView
      canvas.appendChild(row)
    }
    const result = captureScreenContext("agents/overview", {
      kind: "teamspace",
      id: "space\n1",
      name: "Launch",
    })
    expect(result.visible_items.map((item) => item.id)).toEqual([
      "notes.md",
      "long.md",
    ])
    // The long name is cut to Go's limit; the one with a line break is gone.
    expect(result.visible_items[1].name).toHaveLength(120)
    // The caller's own selection is held to the same rules.
    expect(result.selection).toBeUndefined()
    expect(
      captureScreenContext("agents/overview", {
        kind: "teamspace",
        id: "space_1",
        name: "Launch",
      }).selection
    ).toEqual({ kind: "teamspace", id: "space_1", name: "Launch" })
  })
})

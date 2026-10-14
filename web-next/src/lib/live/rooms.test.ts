import { describe, expect, it } from "vitest"
import { directRoom, sideRoom } from "./rooms"

describe("two people's rooms", () => {
  it("names a direct message the same from either side", () => {
    expect(directRoom("ubob", "uada")).toEqual({ kind: "dm", id: "uada_ubob" })
    expect(directRoom("uada", "ubob")).toEqual({ kind: "dm", id: "uada_ubob" })
  })

  it("names a side conversation by its teamspace, then the two members", () => {
    expect(sideRoom("tspEU", "ubob", "uada")).toEqual({
      kind: "side",
      id: "tspEU_uada_ubob",
    })
  })

  it("offers no room for ids the edge can't take", () => {
    expect(directRoom("usr_ada", "ubob")).toBeUndefined()
    expect(directRoom("uada", "uada")).toBeUndefined()
    expect(directRoom("u ada", "ubob")).toBeUndefined()
    expect(sideRoom("tsp/1", "uada", "ubob")).toBeUndefined()
    expect(directRoom("a".repeat(70), "b".repeat(70))).toBeUndefined()
  })
})

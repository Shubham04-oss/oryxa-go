// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { DocumentPreview } from "./document-preview"
import { splitPassage } from "./passage-view"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const bytes = (text: string) => new TextEncoder().encode(text).buffer

describe("splitting a cited passage", () => {
  it("cuts by bytes, so text before it in any script lands right", () => {
    // "Prix € 10. " is 13 bytes: the euro sign takes three.
    expect(
      splitPassage(bytes("Prix € 10. Ship today."), { start: 13, end: 24 })
    ).toEqual({ before: "Prix € 10. ", cited: "Ship today.", after: "" })
  })

  it("holds offsets past the end to it, and has nothing for an empty range", () => {
    expect(splitPassage(bytes("Short"), { start: 2, end: 99 })).toEqual({
      before: "Sh",
      cited: "ort",
      after: "",
    })
    expect(splitPassage(bytes("Short"), { start: 9, end: 12 })).toBeUndefined()
  })
})

describe("a file opened on a cited passage", () => {
  it("marks the passage, and switches to the file as it usually reads", () => {
    const text = "# Launch\n\nWe ship on the 14th.\n\nThen we rest."
    const start = new TextEncoder().encode("# Launch\n\n").length
    const end = start + new TextEncoder().encode("We ship on the 14th.").length
    render(
      <DocumentPreview
        text={text}
        name="launch.md"
        contentType="text/markdown"
        passage={{ start, end }}
      />
    )
    const mark = screen.getByLabelText("Cited passage")
    expect(mark.tagName).toBe("MARK")
    expect(mark.textContent).toBe("We ship on the 14th.")
    expect(
      screen
        .getByRole("button", { name: "Cited passage" })
        .getAttribute("aria-pressed")
    ).toBe("true")

    fireEvent.click(screen.getByRole("button", { name: "Formatted" }))
    expect(screen.getByRole("heading", { name: "Launch" })).toBeTruthy()
    expect(screen.queryByLabelText("Cited passage")).toBeNull()
  })

  it("says when the passage is no longer in the file", () => {
    render(
      <DocumentPreview
        text="Rewritten since."
        name="notes.txt"
        contentType="text/plain"
        passage={{ start: 400, end: 420 }}
      />
    )
    expect(
      screen.getByText("The cited passage is no longer in this file.")
    ).toBeTruthy()
    expect(screen.getByText("Rewritten since.")).toBeTruthy()
  })
})

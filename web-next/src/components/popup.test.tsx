// @vitest-environment jsdom

import { useState } from "react"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  Popup,
  PopupButton,
  PopupConfirm,
  PopupFoot,
  PopupHead,
  PopupLine,
  PopupList,
} from "./popup"

afterEach(cleanup)

function Notes({ onClose }: { onClose: () => void }) {
  const [open, setOpen] = useState(true)
  return (
    <Popup
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) onClose()
      }}
      scene="sea"
    >
      <PopupHead kicker="Parley ended · 35 min" title="Notes from the parley">
        What was decided.
      </PopupHead>
      <PopupList label="Notes">
        <PopupLine tag="Decision">Launch stays on 4 November</PopupLine>
        <PopupLine>Rohan closes the findings</PopupLine>
      </PopupList>
      <PopupFoot note="2 notes">
        <PopupButton tone="primary" onClick={() => setOpen(false)}>
          Done
        </PopupButton>
      </PopupFoot>
    </Popup>
  )
}

describe("the pop-up", () => {
  it("is a named dialog with a scene, its lines and a close X", async () => {
    const onClose = vi.fn()
    render(<Notes onClose={onClose} />)
    const dialog = await screen.findByRole("dialog", {
      name: "Notes from the parley",
    })
    expect(dialog.querySelector('[data-scene="sea"]')).toBeTruthy()
    expect(within(dialog).getByText("Parley ended · 35 min")).toBeTruthy()
    const notes = within(dialog).getByRole("list", { name: "Notes" })
    expect(within(notes).getAllByRole("listitem")).toHaveLength(2)
    expect(within(notes).getByText("Decision")).toBeTruthy()
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it("closes on Escape", async () => {
    const onClose = vi.fn()
    render(<Notes onClose={onClose} />)
    const dialog = await screen.findByRole("dialog")
    fireEvent.keyDown(dialog, { key: "Escape" })
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it("asks plainly before something destructive, with no scene", async () => {
    const onConfirm = vi.fn()
    render(
      <PopupConfirm
        open
        onOpenChange={() => undefined}
        title="Delete EU launch?"
        confirmLabel="Delete"
        onConfirm={onConfirm}
      >
        Its room and files go with it.
      </PopupConfirm>
    )
    const dialog = await screen.findByRole("alertdialog", {
      name: "Delete EU launch?",
    })
    expect(dialog.querySelector("[data-scene]")).toBeNull()
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeTruthy()
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }))
    expect(onConfirm).toHaveBeenCalled()
  })
})

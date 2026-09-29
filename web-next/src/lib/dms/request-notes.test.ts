import { describe, expect, it } from "vitest"
import {
  DM_REQUEST_LIMIT,
  REQUEST_NOTE_STATES,
  isNoteInstant,
  isOpenRequestState,
  isRequestNoteState,
  isRequestOrigin,
  isRevision,
  readRequestNote,
  requestLine,
  requestPreview,
  requestStateWord,
} from "./request-notes"

const note = {
  requestId: "irq_1",
  revision: 2,
  askerId: "user-a",
  ownerId: "user-b",
  origin: "slack",
  state: "approved",
  changedAt: "2026-09-24T10:00:01.123Z",
}

describe("reading a note", () => {
  it("takes a well-formed note, and keeps only its own fields", () => {
    expect(readRequestNote({ ...note, body: "what was asked" })).toEqual(note)
  })

  it("refuses whatever is not one", () => {
    for (const bad of [
      undefined,
      null,
      "note",
      [],
      {},
      { ...note, requestId: "irq~1" },
      { ...note, requestId: "" },
      { ...note, requestId: 7 },
      { ...note, revision: 0 },
      { ...note, revision: 1.5 },
      { ...note, revision: "2" },
      { ...note, askerId: "user a" },
      { ...note, ownerId: undefined },
      { ...note, ownerId: note.askerId },
      { ...note, origin: "Slack" },
      { ...note, origin: "" },
      { ...note, state: "expired" },
      { ...note, state: "pending" },
      { ...note, changedAt: "2026-09-24T10:00:01Z" },
      { ...note, changedAt: "yesterday" },
      { ...note, changedAt: "2026-02-30T10:00:00.000Z" },
    ]) {
      expect(readRequestNote(bad)).toBeUndefined()
    }
  })

  it("knows the states, the origins, the revisions and the times a note may carry", () => {
    expect(REQUEST_NOTE_STATES).toEqual([
      "waiting",
      "approved",
      "declined",
      "answered",
      "held",
      "failed",
      "withheld",
    ])
    for (const state of REQUEST_NOTE_STATES) {
      expect(isRequestNoteState(state)).toBe(true)
    }
    expect(isRequestNoteState("expired")).toBe(false)
    expect(isRequestNoteState(undefined)).toBe(false)
    for (const origin of ["agent", "slack", "teams", "a", "web_app-2"]) {
      expect(isRequestOrigin(origin)).toBe(true)
    }
    for (const origin of ["", "Agent", "2fa", "a b", "a".repeat(33), 3]) {
      expect(isRequestOrigin(origin)).toBe(false)
    }
    expect(isRevision(1)).toBe(true)
    expect(isRevision(0)).toBe(false)
    expect(isRevision(2 ** 60)).toBe(false)
    expect(isNoteInstant("2026-09-24T10:00:00.000Z")).toBe(true)
    expect(isNoteInstant("2026-09-24T10:00:00Z")).toBe(false)
    expect(isNoteInstant("2026-13-24T10:00:00.000Z")).toBe(false)
    expect(DM_REQUEST_LIMIT).toBeGreaterThan(0)
  })
})

describe("what a line says", () => {
  it("says who asked whose agent from the viewer's side, and where when it was Slack", () => {
    expect(requestLine(true, "Priya", "agent")).toBe("You asked Priya's agent")
    expect(requestLine(false, "Priya", "agent")).toBe("Priya asked your agent")
    expect(requestLine(true, "Priya", "slack")).toBe(
      "You asked Priya's agent in Slack"
    )
    expect(requestLine(false, "Priya", "slack")).toBe(
      "Priya asked your agent in Slack"
    )
    // A word a later build adds says no more than that it was asked.
    expect(requestLine(false, "Priya", "teams")).toBe("Priya asked your agent")
  })

  it("says each state in one plain word, and only the open ones can still move", () => {
    expect(
      [...REQUEST_NOTE_STATES, "expired" as const].map(requestStateWord)
    ).toEqual([
      "waiting",
      "approved",
      "declined",
      "answered",
      "held",
      "failed",
      "not shared",
      "expired",
    ])
    expect(
      [...REQUEST_NOTE_STATES, "expired" as const].filter(isOpenRequestState)
    ).toEqual(["waiting", "approved", "held"])
  })

  it("previews the latest request in fixed words, with nobody's name", () => {
    expect(requestPreview(false, "agent", "waiting")).toBe(
      "Asked your agent · waiting"
    )
    expect(requestPreview(true, "agent", "answered")).toBe(
      "You asked their agent · answered"
    )
    expect(requestPreview(false, "slack", "held")).toBe(
      "Asked your agent in Slack · held"
    )
    expect(requestPreview(true, "slack", "withheld")).toBe(
      "You asked their agent in Slack · not shared"
    )
  })
})

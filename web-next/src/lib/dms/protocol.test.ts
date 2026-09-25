import { describe, expect, it } from "vitest"
import {
  DM_MAX_PAGE_SIZE,
  PREVIEW_LENGTH,
  codePoints,
  parseDmClientMessage,
  parseDmServerMessage,
  parseInboxServerMessage,
  preview,
} from "./protocol"

describe("what a page says to a DM", () => {
  it("takes each message only in its own shape", () => {
    expect(
      parseDmClientMessage(
        JSON.stringify({
          type: "dm-send",
          clientId: "dmc-12345678",
          body: "hi",
        })
      )
    ).toEqual({ type: "dm-send", clientId: "dmc-12345678", body: "hi" })
    expect(
      parseDmClientMessage(
        JSON.stringify({
          type: "dm-history",
          requestId: "older-1",
          beforeSeq: 40,
          limit: 1_000,
        })
      )
    ).toEqual({
      type: "dm-history",
      requestId: "older-1",
      beforeSeq: 40,
      limit: DM_MAX_PAGE_SIZE,
    })
    expect(
      parseDmClientMessage(
        JSON.stringify({
          type: "dm-history",
          requestId: "x",
          beforeSeq: 0,
          limit: 0,
        })
      )
    ).toEqual({ type: "dm-history", requestId: "x", beforeSeq: 0, limit: 1 })
    for (const message of [
      { type: "dm-send", clientId: "short", body: "hi" },
      { type: "dm-send", clientId: "dmc-12345678" },
      { type: "dm-read", seq: "3" },
      { type: "dm-read", seq: 2 ** 60 },
      { type: "dm-visibility" },
      { type: "dm-typing", typing: 1 },
      { type: "dm-history", requestId: "older 1", beforeSeq: 0, limit: 5 },
      { type: "cf_agent_state", state: {} },
      { type: "rpc", id: "1", method: "purge", args: [] },
    ]) {
      expect(parseDmClientMessage(JSON.stringify(message))).toBeUndefined()
    }
    expect(parseDmClientMessage("{")).toBeUndefined()
    expect(parseDmClientMessage(new ArrayBuffer(4))).toBeUndefined()
  })
})

describe("what the objects say", () => {
  it("keeps a message's own fields and drops the rest", () => {
    expect(
      parseDmServerMessage(
        JSON.stringify({
          type: "dm-message",
          message: {
            id: "dmm_1",
            seq: 1,
            authorId: "user-admin",
            body: "hi",
            createdAt: "2026-09-24T09:00:00.000Z",
            extra: "dropped",
          },
        })
      )
    ).toEqual({
      type: "dm-message",
      message: {
        id: "dmm_1",
        seq: 1,
        authorId: "user-admin",
        body: "hi",
        createdAt: "2026-09-24T09:00:00.000Z",
      },
    })
    expect(
      parseDmServerMessage(
        JSON.stringify({ type: "dm-message", message: { seq: 0 } })
      )
    ).toBeUndefined()
    expect(
      parseDmServerMessage(
        JSON.stringify({ type: "cf_agent_identity", name: "x" })
      )
    ).toBeUndefined()
    expect(
      parseInboxServerMessage(
        JSON.stringify({
          type: "inbox-snapshot",
          conversations: [
            { kind: "dm", name: "a", peerId: "b", unread: 1, updatedAt: "t" },
            {
              kind: "group",
              name: "c",
              peerId: "d",
              unread: 0,
              updatedAt: "t",
            },
          ],
          unreadTotal: 1,
        })
      )
    ).toEqual({
      type: "inbox-snapshot",
      conversations: [
        { kind: "dm", name: "a", peerId: "b", unread: 1, updatedAt: "t" },
      ],
      unreadTotal: 1,
    })
  })
})

describe("counting and previews", () => {
  it("counts code points, and previews one line of at most PREVIEW_LENGTH", () => {
    expect(codePoints("🙂🙂")).toBe(2)
    expect(preview("  one\n\ntwo  ")).toBe("one two")
    const long = preview("word ".repeat(100))
    expect(Array.from(long)).toHaveLength(PREVIEW_LENGTH)
    expect(long.endsWith("…")).toBe(true)
    expect(Array.from(preview("🙂".repeat(500)))).toHaveLength(PREVIEW_LENGTH)
  })
})

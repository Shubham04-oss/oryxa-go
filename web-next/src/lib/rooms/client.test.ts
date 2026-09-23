import { afterEach, describe, expect, it, vi } from "vitest"
import { catchUpOnRoom, mergeRoomMessages, readRoom, sayInRoom } from "./client"
import type { RoomBrief, RoomMessage, RoomSeat } from "./client"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function stubFetch(respond: (url: string, init: RequestInit) => Response) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) =>
    respond(url, init)
  )
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function sent(fetchMock: ReturnType<typeof stubFetch>) {
  return fetchMock.mock.calls.map(([url, init]) => [
    init.method,
    url,
    init.body,
  ])
}

function message(seq: number, body = `line ${seq}`): RoomMessage {
  return {
    id: `msg_${seq}`,
    room_id: "tsp_1",
    seq,
    author_handle: "ada",
    author_kind: "person",
    author_user_id: "user-1",
    body,
    created_at: "2026-09-23T00:00:00Z",
  }
}

const seat: RoomSeat = {
  handle: "ada",
  kind: "person",
  user_id: "user-1",
  display_name: "Ada",
  present: true,
}

const brief: RoomBrief = {
  room_id: "tsp_1",
  revision: 3,
  through_seq: 2,
  summary: "Launch is on track.",
  decided: ["Ship Friday"],
  open: [],
  owed: [],
  updated_at: "2026-09-23T00:00:00Z",
}

const room = "/v1/organizations/org%20%2F%20one/teamspaces/tsp%20%2F%20one"

afterEach(() => vi.unstubAllGlobals())

describe("room API", () => {
  it("reads a room from the start, or after a sequence with a limit", async () => {
    const fetchMock = stubFetch(() =>
      json({ messages: [message(1)], seats: [seat] })
    )

    expect(await readRoom("org / one", "tsp / one")).toEqual({
      messages: [message(1)],
      seats: [seat],
    })
    await readRoom("org / one", "tsp / one", { after: 12, limit: 200 })

    expect(sent(fetchMock)).toEqual([
      ["GET", `${room}/messages`, undefined],
      ["GET", `${room}/messages?after=12&limit=200`, undefined],
    ])
  })

  it("opens a room whose answer leaves out what it has none of", async () => {
    stubFetch(() => json({}))
    expect(await readRoom("org-1", "tsp_1")).toEqual({
      messages: [],
      seats: [],
    })
  })

  it("says something and returns it with every reply", async () => {
    const said = [message(3, "morning all"), message(4, "Morning!")]
    const fetchMock = stubFetch(() => json({ messages: said }, 201))

    expect(await sayInRoom("org / one", "tsp / one", "morning all")).toEqual(
      said
    )
    expect(sent(fetchMock)).toEqual([
      ["POST", `${room}/messages`, '{"body":"morning all"}'],
    ])
  })

  it("catches up from the start, or after the last message read", async () => {
    const fetchMock = stubFetch(() => json({ brief, messages: [message(3)] }))

    expect(await catchUpOnRoom("org / one", "tsp / one")).toEqual({
      brief,
      messages: [message(3)],
    })
    await catchUpOnRoom("org / one", "tsp / one", { after: 3 })

    expect(sent(fetchMock)).toEqual([
      ["GET", `${room}/catchup`, undefined],
      ["GET", `${room}/catchup?after=3`, undefined],
    ])
  })

  it("keeps the service's answer for a paused room", async () => {
    stubFetch(() =>
      json(
        {
          error: { code: "invalid_request", message: "this room is paused" },
        },
        400
      )
    )

    await expect(readRoom("org-1", "tsp_1")).rejects.toMatchObject({
      status: 400,
      code: "invalid_request",
      message: "this room is paused",
    })
  })
})

describe("mergeRoomMessages", () => {
  it("keeps one message per sequence, the newer copy, in order", () => {
    const merged = mergeRoomMessages(
      [message(1), message(2, "working")],
      [message(3), message(2, "done")]
    )
    expect(merged.map((item) => [item.seq, item.body])).toEqual([
      [1, "line 1"],
      [2, "done"],
      [3, "line 3"],
    ])
  })
})

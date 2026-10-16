import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError } from "@/lib/api/client"
import {
  catchUpOnRoom,
  checkTeamspaceRoom,
  isRoomPausedRefusal,
  mergeRoomMessages,
  roomApi,
  teamspaceRoomPath,
} from "./client"
import type { RoomBrief, RoomMessage, RoomRequest, RoomSeat } from "./client"

function json(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
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

/** A request function answering as `answer` does, recording each call. */
function recorded(
  answer: (path: string, options: Parameters<RoomRequest>[1]) => unknown
) {
  const calls: Array<{ path: string; method: string; body: unknown }> = []
  const request = vi.fn(async (path: string, options = {}) => {
    calls.push({
      path,
      method: options.method ?? "GET",
      body: options.body,
    })
    const answered = answer(path, options)
    if (answered instanceof Error) throw answered
    return answered
  }) as unknown as RoomRequest
  return { request, calls }
}

// A room that answers as agents/primary/internal/rooms does: reads return
// up to `limit` (at most 200) messages after `after`, oldest first; a
// catch-up returns the brief and up to 200 messages after the later of
// `after` and the brief's through_seq.
function fakeRoom(count: number, throughSeq = 0) {
  const messages = Array.from({ length: count }, (_, index) =>
    message(index + 1)
  )
  const { request, calls } = recorded((path) => {
    const url = new URL(path, "https://go.invalid")
    const after = Number(url.searchParams.get("after") ?? 0)
    if (url.pathname.endsWith("/catchup")) {
      const from = Math.max(after, throughSeq)
      return {
        brief: { ...brief, through_seq: throughSeq },
        messages: messages.filter((item) => item.seq > from).slice(0, 200),
      }
    }
    const limit = Math.min(Number(url.searchParams.get("limit") ?? 200), 200)
    return {
      messages: messages.filter((item) => item.seq > after).slice(0, limit),
      seats: [seat],
    }
  })
  return {
    api: roomApi(request),
    add: (...added: RoomMessage[]) => messages.push(...added),
    paths: () =>
      calls.map((call) =>
        call.path.replace("/v1/organizations/org-1/teamspaces/tsp_1", "")
      ),
  }
}

afterEach(() => vi.unstubAllGlobals())

describe("roomApi", () => {
  it("reads a room from the start, or after a sequence with a limit", async () => {
    const { request, calls } = recorded(() => ({
      messages: [message(1)],
      seats: [seat],
    }))
    const api = roomApi(request)

    expect(await api.read("org / one", "tsp / one")).toEqual({
      messages: [message(1)],
      seats: [seat],
    })
    await api.read("org / one", "tsp / one", { after: 12, limit: 200 })

    expect(calls).toEqual([
      { path: `${room}/messages`, method: "GET", body: undefined },
      {
        path: `${room}/messages?after=12&limit=200`,
        method: "GET",
        body: undefined,
      },
    ])
  })

  it("opens a room whose answer leaves out what it has none of", async () => {
    const api = roomApi(recorded(() => ({})).request)
    expect(await api.read("org-1", "tsp_1")).toEqual({
      messages: [],
      seats: [],
    })
  })

  it("says something and returns it with every reply", async () => {
    const said = [message(3, "morning all"), message(4, "Morning!")]
    const { request, calls } = recorded(() => ({ messages: said }))

    expect(
      await roomApi(request).say("org / one", "tsp / one", "morning all")
    ).toEqual(said)
    expect(calls).toEqual([
      {
        path: `${room}/messages`,
        method: "POST",
        body: { body: "morning all" },
      },
    ])
  })

  it("catches up from the start, or after the last message read", async () => {
    const { request, calls } = recorded(() => ({
      brief,
      messages: [message(3)],
    }))
    const api = roomApi(request)

    expect(await api.catchUp("org / one", "tsp / one")).toEqual({
      brief,
      messages: [message(3)],
    })
    await api.catchUp("org / one", "tsp / one", { after: 3 })

    expect(calls.map((call) => call.path)).toEqual([
      `${room}/catchup`,
      `${room}/catchup?after=3`,
    ])
  })

  it("passes on Go's refusal as it came", async () => {
    const refusal = new ApiError("this room is paused", 400, "invalid_request")
    const api = roomApi(recorded(() => refusal).request)
    await expect(api.read("org-1", "tsp_1")).rejects.toBe(refusal)
  })

  it("carries Go's paused flag from a read, a catch-up and readAfter, and leaves it out when Go sent none", async () => {
    const paused = roomApi(
      recorded((path) =>
        path.includes("/catchup")
          ? { brief, messages: [message(3)], paused: true }
          : { messages: [message(3)], seats: [seat], paused: true }
      ).request
    )
    expect(await paused.read("org-1", "tsp_1")).toEqual({
      messages: [message(3)],
      seats: [seat],
      paused: true,
    })
    expect(await paused.catchUp("org-1", "tsp_1")).toEqual({
      brief,
      messages: [message(3)],
      paused: true,
    })
    expect(await paused.readAfter("org-1", "tsp_1", 2)).toMatchObject({
      paused: true,
    })

    const open = roomApi(
      recorded(() => ({ messages: [], seats: [seat], paused: false })).request
    )
    expect(await open.read("org-1", "tsp_1")).toMatchObject({ paused: false })

    // A Go before read-only rooms: nothing is made up.
    const before = fakeRoom(3)
    expect(await before.api.read("org-1", "tsp_1")).not.toHaveProperty("paused")
    expect(
      await before.api.readAfter("org-1", "tsp_1", undefined)
    ).not.toHaveProperty("paused")
  })
})

describe("isRoomPausedRefusal", () => {
  it("reads 409 teamspace_disabled, and a read's 400 from a Go before read-only rooms, as paused", () => {
    const disabled = new ApiError(
      "teamspace is disabled",
      409,
      "teamspace_disabled"
    )
    const legacy = new ApiError("this room is paused", 400, "invalid_request")
    expect(isRoomPausedRefusal(disabled, "post")).toBe(true)
    expect(isRoomPausedRefusal(disabled, "read")).toBe(true)
    expect(isRoomPausedRefusal(legacy, "read")).toBe(true)
    // A post's 400 is a message Go will not take.
    expect(isRoomPausedRefusal(legacy, "post")).toBe(false)
    expect(
      isRoomPausedRefusal(
        new ApiError("name taken", 409, "teamspace_conflict"),
        "post"
      )
    ).toBe(false)
  })
})

describe("readAfter", () => {
  it("opens a long room at its latest messages, not its first ones", async () => {
    const fake = fakeRoom(450, 440)

    const read = await fake.api.readAfter("org-1", "tsp_1", undefined)

    // The old app read ?limit=200 with no `after`, which is seqs 1–200 for
    // ever once a room is longer than that.
    expect(fake.paths()).toEqual(["/catchup", "/messages?after=251&limit=200"])
    expect(read.messages[0].seq).toBe(252)
    expect(read.messages.at(-1)?.seq).toBe(450)
    expect(read.readThrough).toBe(450)
    expect(read.seats).toEqual([seat])
  })

  it("opens an empty room, and a short one, from the beginning", async () => {
    const empty = fakeRoom(0)
    expect(await empty.api.readAfter("org-1", "tsp_1", undefined)).toEqual({
      messages: [],
      seats: [seat],
      readThrough: 0,
    })
    expect(empty.paths()).toEqual(["/catchup", "/messages?after=0&limit=200"])

    const short = await fakeRoom(3).api.readAfter("org-1", "tsp_1", undefined)
    expect(short.messages.map((item) => item.seq)).toEqual([1, 2, 3])
  })

  it("then reads only what was said since, and the seats with it", async () => {
    const fake = fakeRoom(10)
    fake.add(message(11), message(12))

    const read = await fake.api.readAfter("org-1", "tsp_1", 10)

    expect(fake.paths()).toEqual(["/messages?after=10&limit=200"])
    expect(read.messages.map((item) => item.seq)).toEqual([11, 12])
    expect(read.readThrough).toBe(12)
  })

  it("stays where it was when nothing was said", async () => {
    const read = await fakeRoom(10).api.readAfter("org-1", "tsp_1", 10)
    expect(read).toEqual({ messages: [], seats: [seat], readThrough: 10 })
  })

  it("reads page after page when more than a page arrived", async () => {
    const fake = fakeRoom(5)
    fake.add(...Array.from({ length: 250 }, (_, index) => message(6 + index)))

    const read = await fake.api.readAfter("org-1", "tsp_1", 5)

    expect(fake.paths()).toEqual([
      "/messages?after=5&limit=200",
      "/messages?after=205&limit=200",
    ])
    expect(read.messages).toHaveLength(250)
    expect(read.readThrough).toBe(255)
  })

  it("stops after ten pages and carries on from there next time", async () => {
    const fake = fakeRoom(2_500)
    const read = await fake.api.readAfter("org-1", "tsp_1", 0)
    expect(fake.paths()).toHaveLength(10)
    expect(read.readThrough).toBe(2_000)
  })
})

describe("the page's own calls", () => {
  it("catches up through the page's session", async () => {
    const fetchMock = vi.fn(async () => json({ brief, messages: [message(3)] }))
    vi.stubGlobal("fetch", fetchMock)

    expect(await catchUpOnRoom("org / one", "tsp / one", { after: 3 })).toEqual(
      { brief, messages: [message(3)] }
    )
    expect(fetchMock).toHaveBeenCalledWith(
      `${room}/catchup?after=3`,
      expect.objectContaining({ credentials: "include" })
    )
  })

  it("asks the Worker whether the room's socket would be let in", async () => {
    expect(teamspaceRoomPath("org_sim", "tsp_launch")).toBe(
      "/agents/teamspace-room-agent/org_sim~tsp_launch"
    )
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetchMock)
    await checkTeamspaceRoom("org_sim", "tsp_launch")
    expect(fetchMock).toHaveBeenCalledWith(
      "/agents/teamspace-room-agent/org_sim~tsp_launch",
      expect.objectContaining({ credentials: "include" })
    )

    fetchMock.mockImplementation(async () =>
      json(
        { error: { code: "not_found", message: "teamspace not found" } },
        404
      )
    )
    await expect(
      checkTeamspaceRoom("org_sim", "tsp_launch")
    ).rejects.toMatchObject({ status: 404, code: "not_found" })
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

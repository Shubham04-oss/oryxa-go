import { QueryClient } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"
import { teamspaceKeys } from "@/lib/teamspaces/queries"
import {
  ROOM_HISTORY_LIMIT,
  ROOM_POLL_MS,
  afterSayInRoom,
  mergeIntoRoom,
  roomCatchUpQueryOptions,
  roomKeys,
  roomQueryOptions,
} from "./queries"
import type { RoomView } from "./queries"
import type { RoomMessage, RoomSeat } from "./client"

const org = "org-1"
const teamspace = "tsp_1"
const path = `/v1/organizations/${org}/teamspaces/${teamspace}`

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
  })
}

function message(seq: number, author = "ada"): RoomMessage {
  return {
    id: `msg_${seq}`,
    room_id: teamspace,
    seq,
    author_handle: author,
    author_kind: "person",
    author_user_id: `user-${author}`,
    body: `line ${seq}`,
    created_at: "2026-09-23T00:00:00Z",
  }
}

const seat: RoomSeat = {
  handle: "ada",
  kind: "person",
  user_id: "user-ada",
  display_name: "Ada",
  present: true,
}

// A room that answers as agents/primary/internal/rooms does: reads return
// up to `limit` (at most 200) messages after `after`, oldest first; a
// catch-up returns the brief and up to 200 messages after the later of
// `after` and the brief's through_seq.
function fakeRoom(count: number, throughSeq = 0) {
  const messages = Array.from({ length: count }, (_, index) =>
    message(index + 1)
  )
  const fetchMock = vi.fn(async (url: string, _init: RequestInit) => {
    const parsed = new URL(url, "https://app.invalid")
    const after = Number(parsed.searchParams.get("after") ?? 0)
    if (parsed.pathname.endsWith("/catchup")) {
      const from = Math.max(after, throughSeq)
      return json({
        brief: { room_id: teamspace, revision: 1, through_seq: throughSeq },
        messages: messages.filter((item) => item.seq > from).slice(0, 200),
      })
    }
    const limit = Math.min(Number(parsed.searchParams.get("limit") ?? 200), 200)
    return json({
      messages: messages.filter((item) => item.seq > after).slice(0, limit),
      seats: [seat],
    })
  })
  vi.stubGlobal("fetch", fetchMock)
  return {
    fetchMock,
    messages,
    urls: () => fetchMock.mock.calls.map(([url]) => url.replace(path, "")),
    add: (...added: RoomMessage[]) => messages.push(...added),
  }
}

function seqs(view: RoomView | undefined) {
  return view?.messages.map((item) => item.seq) ?? []
}

afterEach(() => vi.unstubAllGlobals())

describe("roomQueryOptions", () => {
  it("keeps the room under its teamspace and re-reads it every 5 s, never in the background", () => {
    const options = roomQueryOptions(org, teamspace)
    expect(options.queryKey).toEqual(roomKeys.room(org, teamspace))
    expect(options.queryKey).toEqual([
      ...teamspaceKeys.teamspace(org, teamspace),
      "room",
    ])
    expect(options.refetchInterval).toBe(ROOM_POLL_MS)
    expect(ROOM_POLL_MS).toBe(5_000)
    expect(options.refetchIntervalInBackground).toBe(false)
  })

  it("opens a long room at its latest messages, not its first ones", async () => {
    const room = fakeRoom(450, 440)
    const queryClient = new QueryClient()

    const view = await queryClient.fetchQuery(roomQueryOptions(org, teamspace))

    // The old app read ?limit=200 with no `after`, which is seqs 1–200 for
    // ever once a room is longer than that.
    expect(room.urls()).toEqual(["/catchup", "/messages?after=251&limit=200"])
    expect(seqs(view)[0]).toBe(252)
    expect(seqs(view).at(-1)).toBe(450)
    expect(view.readThrough).toBe(450)
    expect(view.seats).toEqual([seat])
  })

  it("opens an empty room, and a short one, from the beginning", async () => {
    const empty = fakeRoom(0)
    const queryClient = new QueryClient()
    const view = await queryClient.fetchQuery(roomQueryOptions(org, teamspace))
    expect(empty.urls()).toEqual(["/catchup", "/messages?after=0&limit=200"])
    expect(view).toEqual({ messages: [], seats: [seat], readThrough: 0 })

    vi.unstubAllGlobals()
    fakeRoom(3)
    const short = await new QueryClient().fetchQuery(
      roomQueryOptions(org, teamspace)
    )
    expect(seqs(short)).toEqual([1, 2, 3])
  })

  it("then reads only what was said since, and the seats with it", async () => {
    const room = fakeRoom(10)
    const queryClient = new QueryClient()
    const options = roomQueryOptions(org, teamspace)
    await queryClient.fetchQuery(options)

    room.add(message(11), message(12))
    const view = await queryClient.fetchQuery({ ...options, staleTime: 0 })

    expect(room.urls().slice(2)).toEqual(["/messages?after=10&limit=200"])
    expect(seqs(view)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12])
    expect(view.readThrough).toBe(12)
  })

  it("reads page after page when more than a page arrived", async () => {
    const room = fakeRoom(5)
    const queryClient = new QueryClient()
    const options = roomQueryOptions(org, teamspace)
    await queryClient.fetchQuery(options)

    room.add(...Array.from({ length: 250 }, (_, index) => message(6 + index)))
    const view = await queryClient.fetchQuery({ ...options, staleTime: 0 })

    expect(room.urls().slice(2)).toEqual([
      "/messages?after=5&limit=200",
      "/messages?after=205&limit=200",
    ])
    expect(seqs(view)).toHaveLength(255)
    expect(view.readThrough).toBe(255)
  })

  it("keeps at most the newest 1,000 messages", async () => {
    const room = fakeRoom(1)
    const queryClient = new QueryClient()
    const options = roomQueryOptions(org, teamspace)
    await queryClient.fetchQuery(options)

    room.add(
      ...Array.from({ length: ROOM_HISTORY_LIMIT + 50 }, (_, index) =>
        message(2 + index)
      )
    )
    const view = await queryClient.fetchQuery({ ...options, staleTime: 0 })

    expect(seqs(view)).toHaveLength(ROOM_HISTORY_LIMIT)
    expect(seqs(view).at(-1)).toBe(ROOM_HISTORY_LIMIT + 51)
  })

  it("keeps a post's answer that was merged in while a read was out", async () => {
    const room = fakeRoom(3)
    const queryClient = new QueryClient()
    const options = roomQueryOptions(org, teamspace)
    await queryClient.fetchQuery(options)

    room.fetchMock.mockImplementationOnce(async () => {
      // The post lands while this read is on its way.
      mergeIntoRoom(queryClient, org, teamspace, [message(5)])
      return json({ messages: [message(4)], seats: [seat] })
    })
    const view = await queryClient.fetchQuery({ ...options, staleTime: 0 })

    expect(seqs(view)).toEqual([1, 2, 3, 4, 5])
    expect(view.readThrough).toBe(4)
  })
})

describe("afterSayInRoom", () => {
  it("shows the post at once, and the next read still finds what somebody said in between", async () => {
    const room = fakeRoom(3)
    const queryClient = new QueryClient()
    const options = roomQueryOptions(org, teamspace)
    await queryClient.fetchQuery(options)

    // Bo said 4 while Ada's post (5) and its agent reply (6) were running;
    // the post answers with 5 and 6 only.
    room.add(message(4, "bo"), message(5), message(6, "nia"))
    await afterSayInRoom(queryClient, org, teamspace, [
      message(5),
      message(6, "nia"),
    ])
    const shown = queryClient.getQueryData<RoomView>(options.queryKey)
    expect(seqs(shown)).toEqual([1, 2, 3, 5, 6])
    expect(shown?.readThrough).toBe(3)

    const view = await queryClient.fetchQuery(options)
    expect(room.urls().at(-1)).toBe("/messages?after=3&limit=200")
    expect(seqs(view)).toEqual([1, 2, 3, 4, 5, 6])
  })

  it("re-reads the room and the ledger, not the rest of the teamspace", async () => {
    const queryClient = new QueryClient()
    const keys = {
      room: roomKeys.room(org, teamspace),
      catchUp: roomKeys.catchUp(org, teamspace, 3),
      entries: teamspaceKeys.entries(org, teamspace, 100),
      teamspace: teamspaceKeys.teamspace(org, teamspace),
      list: teamspaceKeys.list(org),
    }
    for (const key of Object.values(keys))
      queryClient.setQueryData(key, {
        messages: [],
        seats: [],
        readThrough: 0,
      })

    await afterSayInRoom(queryClient, org, teamspace, [message(1)])

    const invalidated = Object.entries(keys)
      .filter(([, key]) => queryClient.getQueryState(key)?.isInvalidated)
      .map(([name]) => name)
    expect(invalidated).toEqual(["room", "entries"])
  })
})

describe("mergeIntoRoom", () => {
  it("leaves a room nobody has read alone", () => {
    const queryClient = new QueryClient()
    mergeIntoRoom(queryClient, org, teamspace, [message(1)])
    expect(
      queryClient.getQueryData(roomKeys.room(org, teamspace))
    ).toBeUndefined()
  })
})

describe("roomCatchUpQueryOptions", () => {
  it("keys the catch-up by where the member left off and asks from there", async () => {
    const room = fakeRoom(5, 2)
    const options = roomCatchUpQueryOptions(org, teamspace, 3)

    expect(options.queryKey).toEqual([
      ...roomKeys.room(org, teamspace),
      "catch-up",
      3,
    ])
    expect(options.refetchInterval).toBeUndefined()
    const caught = await new QueryClient().fetchQuery(options)
    expect(room.urls()).toEqual(["/catchup?after=3"])
    expect(caught.messages.map((item) => item.seq)).toEqual([4, 5])
  })
})

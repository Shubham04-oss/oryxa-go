import { describe, expect, it, vi } from "vitest"
import {
  conversationPreview,
  countLabel,
  dayLabel,
  dmTimeline,
  firstName,
  listTime,
  seenMessageId,
} from "./direct-message-format"
import { filterPeople, personContext } from "./direct-message-people"
import type { DmPerson } from "./direct-message-people"
import { createPresenceStore } from "./direct-message-presence"
import type { Conversation, DmMessage } from "@/lib/dms"

const viewer = "user-member"
const peer = "user-priya"

/** A local time, as the objects write it. */
function at(
  year: number,
  month: number,
  day: number,
  hours = 12,
  minutes = 0,
  seconds = 0
): string {
  return new Date(year, month - 1, day, hours, minutes, seconds).toISOString()
}

const now = Date.parse(at(2026, 9, 25, 15, 30))

function message(
  seq: number,
  authorId: string,
  createdAt: string,
  extra: Partial<DmMessage> = {}
): DmMessage {
  return {
    id: `dmm_${seq}`,
    seq,
    authorId,
    body: `line ${seq}`,
    createdAt,
    ...extra,
  }
}

describe("dayLabel", () => {
  it("says Today, Yesterday, the day this year, and the date before it", () => {
    expect(dayLabel(at(2026, 9, 25, 0, 1), now)).toBe("Today")
    expect(dayLabel(at(2026, 9, 24, 23, 59), now)).toBe("Yesterday")
    expect(dayLabel(at(2026, 9, 3), now)).toBe(
      new Intl.DateTimeFormat(undefined, {
        weekday: "short",
        day: "numeric",
        month: "short",
      }).format(new Date(at(2026, 9, 3)))
    )
    expect(dayLabel(at(2025, 12, 31), now)).toBe(
      new Intl.DateTimeFormat(undefined, {
        day: "numeric",
        month: "short",
        year: "numeric",
      }).format(new Date(at(2025, 12, 31)))
    )
    expect(dayLabel("not a time", now)).toBe("")
  })
})

describe("listTime", () => {
  it("is short: now, minutes, hours, Yesterday, a weekday, then the date", () => {
    expect(listTime(new Date(now - 20_000).toISOString(), now)).toBe("now")
    expect(listTime(new Date(now - 5 * 60_000).toISOString(), now)).toBe("5m")
    expect(listTime(new Date(now - 3 * 3_600_000).toISOString(), now)).toBe(
      "3h"
    )
    expect(listTime(at(2026, 9, 24, 9, 0), now)).toBe("Yesterday")
    expect(listTime(at(2026, 9, 21, 9, 0), now)).toBe(
      new Intl.DateTimeFormat(undefined, { weekday: "short" }).format(
        new Date(at(2026, 9, 21, 9, 0))
      )
    )
    expect(listTime(at(2026, 8, 1), now)).toBe(
      new Intl.DateTimeFormat(undefined, {
        day: "numeric",
        month: "short",
      }).format(new Date(at(2026, 8, 1)))
    )
    expect(listTime("", now)).toBe("")
  })
})

describe("dmTimeline", () => {
  it("starts each day with a separator, and groups one person's messages by the minute", () => {
    const timeline = dmTimeline(
      [
        message(1, peer, at(2026, 9, 24, 18, 0, 5)),
        message(2, peer, at(2026, 9, 25, 9, 0, 5)),
        message(3, peer, at(2026, 9, 25, 9, 0, 55)),
        message(4, viewer, at(2026, 9, 25, 9, 0, 58)),
        message(5, viewer, at(2026, 9, 25, 9, 1, 2)),
        // Not confirmed yet: grouped by when the page sent it.
        message(0, viewer, at(2026, 9, 25, 9, 1, 20), {
          id: "dmc-1",
          clientId: "dmc-1",
          status: "sending",
        }),
      ],
      viewer,
      now
    )
    expect(
      timeline.map((item) =>
        item.kind === "day"
          ? item.label
          : `${item.mine ? "me" : "them"}:${item.messages.map((each) => each.seq).join(",")}`
      )
    ).toEqual(["Yesterday", "them:1", "Today", "them:2,3", "me:4", "me:5,0"])
  })

  it("keys a group by its first message, the same before and after the object confirms it", () => {
    const sending = message(0, viewer, at(2026, 9, 25, 9, 1, 20), {
      id: "dmc-1",
      clientId: "dmc-1",
      status: "sending",
    })
    const confirmed = message(6, viewer, at(2026, 9, 25, 9, 1, 21), {
      clientId: "dmc-1",
    })
    expect(dmTimeline([sending], viewer, now).at(-1)?.key).toBe("group:dmc-1")
    expect(dmTimeline([confirmed], viewer, now).at(-1)?.key).toBe("group:dmc-1")
  })

  it("keeps a message whose time does not parse with what came before it", () => {
    const timeline = dmTimeline(
      [message(1, peer, at(2026, 9, 25, 9, 0, 5)), message(2, peer, "garbled")],
      viewer,
      now
    )
    expect(timeline.filter((item) => item.kind === "day")).toHaveLength(1)
    expect(timeline.filter((item) => item.kind === "group")).toHaveLength(1)
  })
})

describe("seenMessageId", () => {
  const mine = (seq: number) => message(seq, viewer, at(2026, 9, 25, 9, seq))
  const theirs = (seq: number) => message(seq, peer, at(2026, 9, 25, 9, seq))

  it("is the member's last message the other person has read", () => {
    expect(seenMessageId([theirs(1), mine(2), mine(3)], viewer, 0)).toBe(
      undefined
    )
    expect(seenMessageId([theirs(1), mine(2), mine(3)], viewer, 2)).toBe(
      "dmm_2"
    )
    expect(seenMessageId([theirs(1), mine(2), mine(3)], viewer, 3)).toBe(
      "dmm_3"
    )
  })

  it("is nothing once they have replied, and skips what is still sending", () => {
    expect(seenMessageId([mine(1), theirs(2)], viewer, 2)).toBe(undefined)
    expect(
      seenMessageId(
        [
          mine(1),
          message(0, viewer, at(2026, 9, 25, 9, 5), {
            id: "dmc-1",
            clientId: "dmc-1",
            status: "sending",
          }),
        ],
        viewer,
        1
      )
    ).toBe("dmm_1")
  })
})

describe("inbox rows", () => {
  const base: Conversation = {
    kind: "dm",
    name: "org_sim~user-member~user-priya",
    peerId: peer,
    unread: 0,
    updatedAt: at(2026, 9, 25),
  }

  it("previews the last message, with You: when it was the member's", () => {
    expect(conversationPreview(base, viewer)).toBe("No messages yet")
    expect(
      conversationPreview(
        {
          ...base,
          lastMessage: {
            authorId: peer,
            body: "Hi",
            createdAt: base.updatedAt,
          },
        },
        viewer
      )
    ).toBe("Hi")
    expect(
      conversationPreview(
        {
          ...base,
          lastMessage: {
            authorId: viewer,
            body: "Hello",
            createdAt: base.updatedAt,
          },
        },
        viewer
      )
    ).toBe("You: Hello")
  })

  it("caps counts, and greets people by their first name", () => {
    expect(countLabel(3, 9)).toBe("3")
    expect(countLabel(9, 9)).toBe("9")
    expect(countLabel(10, 9)).toBe("9+")
    expect(countLabel(120, 99)).toBe("99+")
    expect(firstName("Priya Shah")).toBe("Priya")
    expect(firstName("  Ada  ")).toBe("Ada")
    expect(firstName("sam@sim.oryxa")).toBe("sam@sim.oryxa")
  })
})

describe("people", () => {
  const people: DmPerson[] = [
    {
      id: "user-ada",
      name: "Ada Lovelace",
      email: "ada@sim.oryxa",
      role: "member",
      spaces: ["Launch room", "Design"],
      agents: [],
    },
    {
      id: "user-owen",
      name: "Owen Park",
      email: "owen@sim.oryxa",
      role: "owner",
      spaces: [],
      agents: [],
    },
  ]

  it("finds people by name or email, and says their role and shared teamspaces", () => {
    expect(filterPeople(people, "").map((each) => each.id)).toEqual([
      "user-ada",
      "user-owen",
    ])
    expect(filterPeople(people, " LOVE ").map((each) => each.id)).toEqual([
      "user-ada",
    ])
    expect(filterPeople(people, "owen@").map((each) => each.id)).toEqual([
      "user-owen",
    ])
    expect(personContext(people[0])).toBe("Member · Launch room, Design")
    expect(personContext(people[1])).toBe("Owner")
  })
})

describe("the presence store", () => {
  it("knows someone is online while any open view says so, and forgets a view that closes", () => {
    const store = createPresenceStore()
    const listener = vi.fn()
    const stop = store.subscribe(listener)
    expect(store.online(peer)).toBe(false)

    store.report("canvas", peer, true)
    store.report("panel", peer, false)
    expect(store.online(peer)).toBe(true)
    expect(listener).toHaveBeenCalledTimes(2)
    // The same word again changes nothing.
    store.report("canvas", peer, true)
    expect(listener).toHaveBeenCalledTimes(2)

    store.report("canvas", undefined, false)
    expect(store.online(peer)).toBe(false)
    store.report("panel", undefined, false)
    store.report("panel", undefined, false)
    expect(listener).toHaveBeenCalledTimes(4)
    stop()
    store.report("canvas", peer, true)
    expect(listener).toHaveBeenCalledTimes(4)
  })
})

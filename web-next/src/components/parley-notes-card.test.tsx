// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"
import { actionOwner, notesLength } from "./parley-notes-card"
import { TeamspaceConversation } from "./teamspace-room"
import type { TeamspaceRoom } from "@/hooks/use-teamspace-room"
import type { RoomMessage } from "@/lib/rooms/client"
import { roomMessage, seatsFor, teamspace, viewerId } from "@/test/teamspaces"

afterEach(cleanup)

const space = teamspace()
const seats = seatsFor(space)
const member = seats.find((seat) => seat.user_id === viewerId)!
const ada = seats.find(
  (seat) => seat.kind === "person" && seat.user_id === "user-ada"
)!

/** Notes as the organization agent posts them (#392): it has no seat. */
function notesMessage(seq: number, data: unknown): RoomMessage {
  return {
    id: `rmg_${seq}`,
    room_id: space.id,
    seq,
    kind: "parley_notes",
    data,
    author_handle: "oryxa-agent",
    author_kind: "agent",
    author_user_id: "",
    author_agent_id: space.organization_agent.id,
    body: "Parley notes (13 min)\nDecided:\n- Ship on the 14th\nActions:\n- Send the price list\n- Book the venue",
    created_at: "2026-10-14T10:00:00Z",
  }
}

const notes = {
  call_id: "call_1",
  duration_ms: 754_000,
  decisions: ["Ship on the 14th"],
  actions: [
    { text: "Send the price list", owner_user_id: "user-ada" },
    { text: "Book the venue" },
    { text: "Chase the lawyers", owner_user_id: "user-nobody" },
    { text: "Write the post", owner_user_id: viewerId },
  ],
}

function renderRoom(messages: RoomMessage[]) {
  const room: TeamspaceRoom = {
    messages,
    send: async () => true,
    presence: { seats, here: [member], me: member },
    status: { connection: "live", outgoing: [], canPost: true },
  }
  return render(
    <TeamspaceConversation
      space={space}
      room={room}
      viewerId={viewerId}
      onComposingChange={() => {}}
    />
  )
}

describe("parley notes in the room", () => {
  it("shows the notes as a card: how long, who posted them, decisions and who owes what", () => {
    renderRoom([notesMessage(1, notes)])
    const card = screen.getByRole("article", {
      name: "Parley notes, posted by Simulated Org Agent",
    })
    expect(
      within(card).getByText(
        "Parley notes · 13 min · posted by Simulated Org Agent"
      )
    ).toBeTruthy()
    const lines = within(card).getAllByRole("listitem")
    expect(
      lines.map((line) =>
        [...line.querySelectorAll(":scope > :not([aria-hidden])")]
          .map((part) => part.textContent)
          .join(" | ")
      )
    ).toEqual([
      "Ship on the 14th | Decision",
      "Send the price list | Ada",
      "Book the venue | Unassigned",
      // An owner the room can't seat is nobody's, not a guess.
      "Chase the lawyers | Unassigned",
      "Write the post | You",
    ])
    // The owner's face leads their action; an unassigned one has none.
    expect(lines[1].querySelector(".workspace-avatar")).not.toBeNull()
    expect(lines[2].querySelector(".workspace-avatar")).toBeNull()
    // The body's words are not shown twice.
    expect(within(card).queryByText(/Decided:/)).toBeNull()
  })

  it("leaves plain messages as they were", () => {
    renderRoom([
      roomMessage(1, ada, "Morning"),
      { ...roomMessage(2, member, "Hello"), kind: "message" },
    ])
    expect(screen.getByRole("article", { name: "Ada" })).toBeTruthy()
    expect(screen.getByText("Hello")).toBeTruthy()
    expect(screen.queryByText(/Parley notes/)).toBeNull()
  })

  it("reads notes it can't draw, and kinds it doesn't know, as their body", () => {
    renderRoom([
      notesMessage(1, null),
      { ...notesMessage(2, notes), kind: "something_new" },
    ])
    expect(screen.queryByRole("list", { name: "Decisions and actions" })).toBe(
      null
    )
    expect(screen.queryByText(/posted by/)).toBeNull()
    // Each shows its body: the notes in words.
    expect(screen.getAllByText(/Decided:/)).toHaveLength(2)
    expect(screen.getAllByText(/Ship on the 14th/)).toHaveLength(2)
  })

  it("reads notes with an item it can't read as their body, not as fewer notes", () => {
    renderRoom([
      notesMessage(1, {
        ...notes,
        decisions: [{ text: "Ship on the 14th" }],
      }),
      notesMessage(2, { ...notes, actions: [{ text: 5 }] }),
    ])
    expect(screen.queryByText("Nothing was decided or assigned.")).toBeNull()
    expect(screen.queryByText(/posted by/)).toBeNull()
    expect(screen.getAllByText(/Decided:/)).toHaveLength(2)
    expect(screen.getAllByText(/Send the price list/)).toHaveLength(2)
  })

  it("says so when a parley decided nothing and left nobody anything", () => {
    renderRoom([
      notesMessage(1, { call_id: "call_2", decisions: [], actions: [] }),
    ])
    const card = screen.getByRole("article", {
      name: "Parley notes, posted by Simulated Org Agent",
    })
    // No length it wasn't given.
    expect(
      within(card).getByText("Parley notes · posted by Simulated Org Agent")
    ).toBeTruthy()
    expect(
      within(card).getByText("Nothing was decided or assigned.")
    ).toBeTruthy()
  })
})

describe("notesLength", () => {
  it("says the length as the Parley ended pop-up does", () => {
    expect(notesLength(754_000)).toBe("13 min")
    expect(notesLength(20_000)).toBe("under a minute")
    expect(notesLength(undefined)).toBeUndefined()
  })
})

describe("actionOwner", () => {
  it("seats only people on the team", () => {
    expect(actionOwner("user-ada", seats, viewerId)).toEqual({
      userId: "user-ada",
      name: "Ada",
    })
    expect(actionOwner(viewerId, seats, viewerId)?.name).toBe("You")
    expect(actionOwner("user-ghost", seats, viewerId)).toBeUndefined()
    expect(actionOwner(undefined, seats, viewerId)).toBeUndefined()
  })
})

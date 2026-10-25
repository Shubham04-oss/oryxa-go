// @vitest-environment jsdom

import { describe, expect, it } from "vitest"
import { speakerSeat } from "./teamspace-room"
import { roomMessage, seatsFor, teamspace } from "@/test/teamspaces"

const space = teamspace()
const seats = seatsFor(space)
const agent = seats.find((seat) => seat.kind === "agent")!
const person = seats.find((seat) => seat.kind === "person")!

describe("who said a room message (T8)", () => {
  it("finds an agent by its id, not a handle a rename or a namesake moved", () => {
    const stale = {
      ...roomMessage(1, agent, "Done."),
      author_handle: "arlo-ujvx",
    }
    expect(speakerSeat(stale, seats)).toBe(agent)
    // Another seat now holding the old handle is not the speaker.
    const namesake = { ...person, handle: "arlo-ujvx" }
    expect(speakerSeat(stale, [namesake, ...seats])).toBe(agent)
  })

  it("finds a person by their user id, and falls back to the handle", () => {
    expect(
      speakerSeat(
        { ...roomMessage(2, person, "Hi"), author_handle: "old" },
        seats
      )
    ).toBe(person)
    expect(
      speakerSeat(
        { ...roomMessage(3, agent, "x"), author_agent_id: undefined },
        seats
      )
    ).toBe(agent)
  })
})

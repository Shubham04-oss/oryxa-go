// @vitest-environment jsdom

import { cleanup, render } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import {
  agents,
  avatarDataUri,
  IdentityAvatar,
  people,
} from "./workspace-avatar"

afterEach(cleanup)

describe("workspace avatars", () => {
  it("generates stable, distinct local SVGs for people and agents", () => {
    const userAvatar = avatarDataUri(people.avery)
    const agentAvatar = avatarDataUri(agents.research)

    expect(userAvatar).toMatch(/^data:image\/svg\+xml/)
    expect(agentAvatar).toMatch(/^data:image\/svg\+xml/)
    expect(avatarDataUri(people.avery)).toBe(userAvatar)
    expect(userAvatar).not.toBe(avatarDataUri(people.jordan))
    expect(userAvatar).not.toBe(agentAvatar)

    for (const [identity, color] of [
      [people.avery, "#d8e9dc"],
      [people.jordan, "#dcdaf1"],
      [people.sam, "#f3dfd5"],
      [people.noah, "#d9e8ed"],
    ] as const) {
      expect(decodeURIComponent(avatarDataUri(identity))).toContain(color)
    }
  })

  it("uses the shadcn avatar shell, named for the person or agent", () => {
    const { container } = render(
      <>
        <IdentityAvatar identity={people.avery} />
        <IdentityAvatar identity={agents.research} />
      </>
    )

    const avatars = container.querySelectorAll('[data-slot="avatar"]')
    expect(avatars).toHaveLength(2)
    expect(avatars[0].getAttribute("aria-label")).toBe("Avery Morgan")
    expect(avatars[1].getAttribute("aria-label")).toBe("Research agent")
    // The initials wait behind the art while it loads.
    expect(
      avatars[0].querySelector('[data-slot="avatar-fallback"]')?.textContent
    ).toBe("AM")
  })
})

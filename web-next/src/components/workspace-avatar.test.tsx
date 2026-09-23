// @vitest-environment jsdom

import { cleanup, render } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import {
  agents,
  avatarDataUri,
  IdentityAvatar,
  people,
  TeamspaceAvatar,
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

  it("uses the shadcn avatar shell and a teamspace fallback", () => {
    const { container } = render(
      <>
        <IdentityAvatar identity={people.avery} />
        <TeamspaceAvatar name="Product sync" />
        <TeamspaceAvatar name="Launch planning" />
      </>
    )

    const avatars = container.querySelectorAll('[data-slot="avatar"]')
    expect(avatars).toHaveLength(3)
    expect(avatars[0].getAttribute("aria-label")).toBe("Avery Morgan")
    expect(avatars[1].getAttribute("aria-label")).toBe("Product sync teamspace")
    expect(
      avatars[1].querySelector('[data-slot="avatar-fallback"]')
    ).toBeTruthy()
    expect(avatars[1].querySelector('[data-slot="avatar-image"]')).toBeNull()
    expect(avatars[1].getAttribute("data-avatar-kind")).toBe("planet")
    expect(avatars[1].querySelector(".workspace-planet-art")).toBeTruthy()
    expect(avatars[1].querySelectorAll("ellipse")).toHaveLength(2)
    expect(avatars[2].querySelectorAll("ellipse")).toHaveLength(0)
    expect(
      avatars[1].querySelector("stop")?.getAttribute("stop-color")
    ).not.toBe(avatars[2].querySelector("stop")?.getAttribute("stop-color"))
  })
})

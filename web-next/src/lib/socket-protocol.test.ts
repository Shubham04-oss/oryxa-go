import { describe, expect, it } from "vitest"
import { socketProtocol } from "./socket-protocol"

describe("socketProtocol", () => {
  it("follows the page's own scheme", () => {
    expect(socketProtocol(new URL("http://admin.localhost:3004/"))).toBe("ws")
    expect(socketProtocol(new URL("https://app.oryxa.in/"))).toBe("wss")
  })

  it("leaves the choice to PartySocket where there is no page", () => {
    expect(socketProtocol(undefined)).toBeUndefined()
  })
})

import { describe, expect, it } from "vitest"
import {
  buildTriggerConfig,
  connectRedirect,
  connectReturnTo,
  connectionCountLabel,
  sentence,
  sourceTarget,
  sourceTargetProblem,
  triggerCapability,
  withoutConnectReturn,
} from "./workspace-tool-helpers"

describe("trigger configuration", () => {
  it("requires fields and keeps numbers and enum values typed", () => {
    const capability = triggerCapability({
      properties: {
        repo: { type: "string", title: "Repository" },
        count: { type: "integer", title: "Count" },
        enabled: { type: "boolean", default: true },
        priority: { enum: [1, 2, 3] },
      },
      required: ["repo", "count"],
    })
    expect(capability.kind).toBe("form")
    if (capability.kind !== "form") return

    expect(buildTriggerConfig(capability.fields, {}).problem).toBe(
      "Repository is required."
    )
    expect(
      buildTriggerConfig(capability.fields, { repo: "oryxa/app", count: "1.5" })
        .problem
    ).toBe("Count must be a whole number.")
    expect(
      buildTriggerConfig(capability.fields, {
        repo: "oryxa/app",
        count: "2",
        priority: 1,
      }).config
    ).toEqual({
      repo: "oryxa/app",
      count: 2,
      enabled: true,
      priority: 1,
    })
  })

  it("does not offer a form for required object-shaped setup", () => {
    expect(
      triggerCapability({
        properties: { payload: { type: "object" } },
        required: ["payload"],
      })
    ).toEqual({ kind: "unsupported" })
  })
})

describe("source registration", () => {
  it("uses provider-specific targets and validates manual IDs", () => {
    expect(sourceTarget("github", "oryxa/app ")).toEqual({
      repository: "oryxa/app",
    })
    expect(sourceTarget("googledrive", " ")).toEqual({})
    expect(sourceTargetProblem("github", "app")).toBe(
      "Enter a repository as owner/name."
    )
    expect(sourceTargetProblem("github", "oryxa/app")).toBeUndefined()
  })
})

describe("connecting and coming back", () => {
  it("returns to Tools in the organization it connected in, without a previous return", () => {
    expect(
      connectReturnTo(
        "https://app.oryxa.in/?org=org_a&view=tools&app_connection=apc_1&status=failed#top",
        "org_b"
      )
    ).toBe("https://app.oryxa.in/?org=org_b&view=tools")
    expect(connectReturnTo("http://localhost:3005/", "org_1")).toBe(
      "http://localhost:3005/?org=org_1&view=tools"
    )
  })

  it("takes only Composio's two parameters off the address", () => {
    expect(
      withoutConnectReturn(
        "https://app.oryxa.in/?view=tools&app_connection=apc_1&org=o&status=active#x"
      )
    ).toBe("/?view=tools&org=o#x")
  })

  it("follows an https link or one on this app's own origin, and nothing else", () => {
    expect(
      connectRedirect(
        "https://connect.composio.dev/link/lk_1",
        "http://localhost:3005"
      )
    ).toBe("https://connect.composio.dev/link/lk_1")
    expect(
      connectRedirect("http://localhost:3005/stub", "http://localhost:3005")
    ).toBe("http://localhost:3005/stub")
    expect(() =>
      connectRedirect("http://evil.example/link", "http://localhost:3005")
    ).toThrow("The connection link was not secure.")
    expect(() =>
      connectRedirect("javascript:alert(1)", "http://localhost:3005")
    ).toThrow()
  })

  it("counts only an active connection as connected", () => {
    expect(connectionCountLabel([])).toBe("0 connected")
    expect(
      connectionCountLabel([
        { id: "a", status: "active" },
        { id: "b", status: "pending" },
        { id: "c", status: "failed" },
        { id: "d", status: "expired" },
      ])
    ).toBe("1 connected · 1 connecting · 2 need attention")
    expect(connectionCountLabel([{ id: "d", status: "expired" }])).toBe(
      "0 connected · 1 needs attention"
    )
  })

  it("counts an expired connection a newer one replaced as replaced, not as needing attention", () => {
    expect(
      connectionCountLabel(
        [
          { id: "new", status: "active" },
          { id: "old", status: "expired" },
        ],
        new Set(["old"])
      )
    ).toBe("1 connected · 1 replaced")
  })

  it("ends a sentence once", () => {
    expect(sentence("the link expired")).toBe("the link expired.")
    expect(sentence("Refresh token revoked.")).toBe("Refresh token revoked.")
    expect(sentence("Connecting… ")).toBe("Connecting…")
  })
})

import { describe, expect, it } from "vitest"
import {
  buildTriggerConfig,
  sourceTarget,
  sourceTargetProblem,
  triggerCapability,
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

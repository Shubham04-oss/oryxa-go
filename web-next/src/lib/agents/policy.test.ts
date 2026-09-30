import { describe, expect, it } from "vitest"
import { ApiError } from "@/lib/api/client"
import {
  canDecideAgentRequest,
  canRespondToAgentRequest,
  isUnavailableHere,
  primaryAgent,
} from "./policy"
import type { AgentRequest } from "./agent-requests"
import type { ManagedAgent } from "./managed-agents"

function agent(overrides: Partial<ManagedAgent>): ManagedAgent {
  return {
    id: "agt_1",
    organization_id: "org-1",
    owner_user_id: "user-1",
    kind: "specialist",
    name: "Agent",
    description: "",
    instructions: "Help.",
    status: "active",
    revision: 1,
    created_at: "2026-09-23T00:00:00Z",
    updated_at: "2026-09-23T00:00:00Z",
    ...overrides,
  }
}

function request(overrides: Partial<AgentRequest>): AgentRequest {
  return {
    id: "irq_1",
    organization_id: "org-1",
    requester: { user_id: "user-2", agent_id: "agt_b", agent_name: "Bo" },
    target: { user_id: "user-1", agent_id: "agt_a", agent_name: "Ada" },
    purpose: "Architecture",
    task: "Explain.",
    context: [],
    requested_tools: [],
    requested_permissions: [],
    status: "pending",
    revision: 1,
    expires_at: "2026-09-24T00:00:00Z",
    created_at: "2026-09-23T00:00:00Z",
    updated_at: "2026-09-23T00:00:00Z",
    ...overrides,
  }
}

describe("managed agent helpers", () => {
  it("finds the primary agent among whatever the list returns", () => {
    const primary = agent({ id: "agt_p", kind: "primary", name: "Nia" })
    const research = agent({ id: "agt_r", name: "Research" })
    const paused = agent({ id: "agt_x", name: "Paused", status: "disabled" })

    expect(primaryAgent([primary, research, paused])).toBe(primary)
    expect(primaryAgent([research])).toBeUndefined()
  })
})

describe("agent request permissions", () => {
  const now = Date.parse("2026-09-23T12:00:00Z")

  it("lets only the target decide, only while pending and unexpired", () => {
    expect(canDecideAgentRequest(request({}), "user-1", now)).toBe(true)
    expect(canDecideAgentRequest(request({}), "user-2", now)).toBe(false)
    expect(
      canDecideAgentRequest(request({ status: "approved" }), "user-1", now)
    ).toBe(false)
    expect(
      canDecideAgentRequest(
        request({ expires_at: "2026-09-23T11:59:59Z" }),
        "user-1",
        now
      )
    ).toBe(false)
  })

  it("lets only the target answer, only once approved and unexpired", () => {
    const approved = request({
      status: "approved",
      decision: {
        decision: "approved",
        message: "Go ahead",
        allowed_tools: [],
        granted_permissions: [],
        decided_at: "2026-09-23T10:00:00Z",
      },
    })
    expect(canRespondToAgentRequest(approved, "user-1", now)).toBe(true)
    expect(canRespondToAgentRequest(approved, "user-2", now)).toBe(false)
    expect(canRespondToAgentRequest(request({}), "user-1", now)).toBe(false)
    expect(
      canRespondToAgentRequest(
        { ...approved, expires_at: "2026-09-23T12:00:00Z" },
        "user-1",
        now
      )
    ).toBe(false)
  })
})

describe("isUnavailableHere", () => {
  it("recognises a feature this server does not run, not a failure", () => {
    expect(
      isUnavailableHere(
        new ApiError(
          "teamspaces are not configured",
          503,
          "teamspaces_unavailable"
        )
      )
    ).toBe(true)
    expect(
      isUnavailableHere(
        new ApiError(
          "Krude is not set up on this server.",
          503,
          "agents_unavailable"
        )
      )
    ).toBe(true)
    // Auth being down is worth retrying, not a missing feature.
    expect(isUnavailableHere(new ApiError("down", 503, "unavailable"))).toBe(
      false
    )
    expect(
      isUnavailableHere(new ApiError("boom", 500, "agents_unavailable"))
    ).toBe(false)
    expect(isUnavailableHere(new Error("offline"))).toBe(false)
  })
})

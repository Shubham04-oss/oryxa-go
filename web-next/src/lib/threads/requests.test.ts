import { describe, expect, it } from "vitest"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import { ApiError } from "@/lib/api/client"
import {
  requestCardAnswer,
  requestCardPollInterval,
  requestCardState,
  requestCardStatus,
} from "./requests"

describe("request cards", () => {
  it("polls open requests and stops after terminal results or a refusal", () => {
    for (const status of ["pending", "approved", "review"] as const) {
      expect(requestCardPollInterval({ status } as AgentRequest, null)).toBe(
        10_000
      )
    }
    for (const status of [
      "completed",
      "failed",
      "rejected",
      "withheld",
      "expired",
    ] as const) {
      expect(requestCardPollInterval({ status } as AgentRequest, null)).toBe(
        false
      )
    }
    expect(requestCardPollInterval(undefined, new Error("network"))).toBe(
      10_000
    )
    for (const status of [401, 403, 404]) {
      expect(
        requestCardPollInterval(
          undefined,
          new ApiError("refused", status, "refused")
        )
      ).toBe(false)
    }
  })

  it("never reads an answer held for review or withheld", () => {
    const result = { summary: "Held summary", content: "Private content" }
    for (const status of [
      "pending",
      "approved",
      "review",
      "withheld",
      "expired",
      "rejected",
    ] as const) {
      expect(
        requestCardAnswer({ status, result, held: { result } } as AgentRequest)
      ).toBeUndefined()
    }
    expect(
      requestCardAnswer({ status: "completed", result } as AgentRequest)
    ).toBe("Held summary\n\nPrivate content")
    expect(
      requestCardAnswer({ status: "failed", result } as AgentRequest)
    ).toBe("Held summary\n\nPrivate content")
  })

  it("says who an ask that needs its owner's own reply waits on, and who replied", () => {
    const pending = { status: "pending", answer_by: "owner" } as AgentRequest
    const replied = {
      status: "completed",
      result: { summary: "Yes.", content: "", written_by_owner: true },
    } as AgentRequest

    expect(requestCardState(pending, "asked", "Ada")).toBe(
      "Waiting for Ada to reply"
    )
    expect(requestCardState(pending, "asked")).toBe(
      "Waiting for the owner to reply"
    )
    expect(requestCardState(replied, "answered", "Ada")).toBe("Ada replied")
    expect(requestCardState(replied, "answered")).toBe("The owner replied")
    // The decision alone says the same, when the answer carries no mark.
    expect(
      requestCardState(
        {
          status: "completed",
          result: { summary: "Yes.", content: "" },
          decision: { consent: "reply" },
        } as AgentRequest,
        "answered",
        "Ada"
      )
    ).toBe("Ada replied")
  })

  it("keeps the agent's words for everything else, and the conversation's own note before the request is read", () => {
    expect(
      requestCardState({ status: "pending" } as AgentRequest, "asked", "Ada")
    ).toBe("Waiting for approval")
    expect(
      requestCardState(
        { status: "pending", answer_by: "agent" } as AgentRequest,
        "asked",
        "Ada"
      )
    ).toBe("Waiting for approval")
    expect(
      requestCardState(
        { status: "completed" } as AgentRequest,
        "answered",
        "Ada"
      )
    ).toBe("Answered")
    // Once it is being worked on it is the agent's, whoever was to answer.
    expect(
      requestCardState(
        { status: "approved", answer_by: "owner" } as AgentRequest,
        "asked",
        "Ada"
      )
    ).toBe("Approved · working")
    expect(requestCardState(undefined, "asked", "Ada")).toBe(
      "Waiting for approval"
    )
    expect(requestCardState(undefined, undefined)).toBe("Request filed")
  })

  it("avoids repeating a summary that is the full answer", () => {
    expect(
      requestCardAnswer({
        status: "completed",
        result: { summary: "14 October", content: "14 October" },
      } as AgentRequest)
    ).toBe("14 October")
    expect(requestCardStatus("review")).toBe(
      "Waiting for the answer to be reviewed"
    )
    expect(requestCardStatus("withheld")).toBe("Not shared")
    expect(requestCardStatus("closed")).toBe("Closed")
  })
})

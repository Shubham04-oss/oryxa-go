import { describe, expect, it } from "vitest"
import type { AgentRequest } from "@/lib/agents/agent-requests"
import { ApiError } from "@/lib/api/client"
import {
  requestCardAnswer,
  requestCardPollInterval,
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

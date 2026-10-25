import { describe, expect, it } from "vitest"
import {
  CONVERSATION_GAP_MS,
  conversationOf,
  conversationsOf,
} from "./requests-view-group"
import {
  ASKER,
  OWNER,
  agentRequest,
  slackRequest,
} from "./request-card-fixtures"
import type { AgentRequest } from "@/lib/agents/agent-requests"

const at = (minutes: number) =>
  new Date(Date.parse("2026-09-29T12:00:00Z") + minutes * 60_000).toISOString()
const ask = (
  id: string,
  minutes: number,
  overrides: Partial<AgentRequest> = {}
) => slackRequest({ id, created_at: at(minutes), task: id, ...overrides })
/** As Go lists them: the latest first. */
const listed = (...requests: AgentRequest[]) =>
  [...requests].sort((a, b) => b.created_at.localeCompare(a.created_at))
const ids = (requests: readonly AgentRequest[]) => requests.map((r) => r.id)

describe("conversationsOf", () => {
  it("makes one conversation of one teammate's asks in a row, newest first", () => {
    const seven = Array.from({ length: 7 }, (_, i) => ask(`q${i}`, i * 5))
    const conversations = conversationsOf(listed(...seven), "inbox")
    expect(conversations).toHaveLength(1)
    expect(conversations[0].id).toBe("q0")
    expect(conversations[0].latest.id).toBe("q6")
    expect(ids(conversations[0].requests)).toEqual([
      "q6",
      "q5",
      "q4",
      "q3",
      "q2",
      "q1",
      "q0",
    ])
  })

  it("starts a new conversation after a quiet longer than the gap", () => {
    const gap = CONVERSATION_GAP_MS / 60_000
    const conversations = conversationsOf(
      listed(ask("a", 0), ask("b", gap), ask("c", 2 * gap + 1)),
      "inbox"
    )
    expect(conversations.map((c) => ids(c.requests))).toEqual([
      ["c"],
      ["b", "a"],
    ])
  })

  it("keeps people, channels and teamspaces apart", () => {
    const conversations = conversationsOf(
      listed(
        ask("raya-slack", 0),
        ask("raya-oryxa", 1, { origin: "agent" }),
        ask("raya-space", 2, { teamspace_id: "tsp_1" }),
        ask("kenji", 3, {
          requester: {
            user_id: "user-kenji",
            agent_id: "agt_k",
            agent_name: "K",
          },
        })
      ),
      "inbox"
    )
    expect(conversations).toHaveLength(4)
  })

  it("groups Sent by who was asked", () => {
    const toRaya = (id: string, minutes: number) =>
      agentRequest({
        id,
        created_at: at(minutes),
        requester: {
          user_id: OWNER,
          agent_id: "agt_krude",
          agent_name: "Krude",
        },
        target: { user_id: ASKER, agent_id: "agt_nia", agent_name: "Nia" },
      })
    const conversations = conversationsOf(
      listed(toRaya("s1", 0), toRaya("s2", 3)),
      "sent"
    )
    expect(conversations).toHaveLength(1)
    expect(conversationOf(conversations, "s1")?.id).toBe("s1")
    expect(conversationOf(conversations, "missing")).toBeUndefined()
  })

  it("keeps the list's own order between conversations", () => {
    const kenji = {
      requester: { user_id: "user-kenji", agent_id: "agt_k", agent_name: "K" },
    }
    // Go put Kenji's ask first (its activity is the latest), though Raya's is newer.
    const conversations = conversationsOf(
      [ask("kenji", 0, kenji), ask("raya", 10)],
      "inbox"
    )
    expect(conversations.map((c) => c.id)).toEqual(["kenji", "raya"])
  })
})

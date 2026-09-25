import { afterEach, describe, expect, it, vi } from "vitest"
import {
  decideAgentRequest,
  getAgentRequest,
  listAgentRequests,
  respondToAgentRequest,
} from "./agent-requests"
import type { AgentRequest, DecideAgentRequestInput } from "./agent-requests"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function stubFetch(respond: (url: string, init: RequestInit) => Response) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) =>
    respond(url, init)
  )
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function sent(fetchMock: ReturnType<typeof stubFetch>) {
  return fetchMock.mock.calls.map(([url, init]) => [
    init.method,
    url,
    init.body,
  ])
}

const request: AgentRequest = {
  id: "irq_1",
  organization_id: "org-1",
  requester: { user_id: "user-2", agent_id: "agt_b", agent_name: "Bo" },
  target: { user_id: "user-1", agent_id: "agt_a", agent_name: "Ada" },
  purpose: "Architecture",
  task: "Explain the boundary.",
  context: [],
  requested_tools: ["github.search"],
  requested_permissions: [],
  status: "pending",
  revision: 1,
  expires_at: "2026-09-24T00:00:00Z",
  created_at: "2026-09-23T00:00:00Z",
  updated_at: "2026-09-23T00:00:00Z",
}

afterEach(() => vi.unstubAllGlobals())

describe("agent requests API", () => {
  it("lists either box, with the limit only when one is asked for", async () => {
    const fetchMock = stubFetch(() => json({ requests: [request] }))

    expect(await listAgentRequests("org-1", "inbox", { limit: 50 })).toEqual([
      request,
    ])
    await listAgentRequests("org-1", "sent")

    expect(sent(fetchMock)).toEqual([
      [
        "GET",
        "/v1/organizations/org-1/agent-requests/inbox?limit=50",
        undefined,
      ],
      ["GET", "/v1/organizations/org-1/agent-requests/sent", undefined],
    ])
  })

  it("reads one request", async () => {
    const fetchMock = stubFetch(() => json(request))

    expect(await getAgentRequest("org-1", "irq_1")).toEqual(request)
    expect(sent(fetchMock)).toEqual([
      ["GET", "/v1/organizations/org-1/agent-requests/irq_1", undefined],
    ])
  })

  it("decides and answers with only the fields each handler reads", async () => {
    const fetchMock = stubFetch(() => json(request))

    await decideAgentRequest("org-1", "irq_1", {
      decision: "approved",
      message: "Read-only is fine.",
      allowed_tools: ["github.search"],
      granted_permissions: [],
    })
    await decideAgentRequest("org-1", "irq_1", {
      decision: "rejected",
      message: "Not this week.",
      requester: request.requester,
    } as DecideAgentRequestInput)
    await respondToAgentRequest("org-1", "irq_1", {
      status: "completed",
      summary: "Explained.",
      content: "The boundary is the team.",
      tools_used: ["github.search"],
    })

    expect(sent(fetchMock)).toEqual([
      [
        "POST",
        "/v1/organizations/org-1/agent-requests/irq_1/decision",
        '{"decision":"approved","message":"Read-only is fine.","allowed_tools":["github.search"],"granted_permissions":[]}',
      ],
      [
        "POST",
        "/v1/organizations/org-1/agent-requests/irq_1/decision",
        '{"decision":"rejected","message":"Not this week."}',
      ],
      [
        "POST",
        "/v1/organizations/org-1/agent-requests/irq_1/response",
        '{"status":"completed","summary":"Explained.","content":"The boundary is the team.","tools_used":["github.search"]}',
      ],
    ])
  })

  it("keeps the service's codes for an expired request and the wrong member", async () => {
    const answers = [
      json(
        {
          error: {
            code: "request_expired",
            message: "agent request has expired",
          },
        },
        409
      ),
      json(
        {
          error: {
            code: "invalid_state",
            message: "agent request cannot make that transition",
          },
        },
        409
      ),
      json(
        {
          error: {
            code: "forbidden",
            message: "agent request is not available to this agent",
          },
        },
        403
      ),
    ]
    stubFetch(() => answers.shift() ?? json({}))
    const decision = { decision: "approved", message: "Yes" } as const

    await expect(
      decideAgentRequest("org-1", "irq_1", decision)
    ).rejects.toMatchObject({ status: 409, code: "request_expired" })
    await expect(
      decideAgentRequest("org-1", "irq_1", decision)
    ).rejects.toMatchObject({ status: 409, code: "invalid_state" })
    await expect(
      respondToAgentRequest("org-1", "irq_1", {
        status: "failed",
        summary: "No",
      })
    ).rejects.toMatchObject({ status: 403, code: "forbidden" })
  })
})

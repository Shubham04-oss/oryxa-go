import { afterEach, describe, expect, it, vi } from "vitest"
import {
  AgentRequestAlreadyDecided,
  asksForConsent,
  canCarrySecret,
  consentAgentRequest,
  decideAgentRequest,
  getAgentRequest,
  isLeaseLive,
  isOwnerWritten,
  isSecretGone,
  listAgentActivity,
  listAgentRequests,
  needsOwnerReply,
  openAgentRequestSecret,
  originOf,
  replyToAgentRequest,
  respondToAgentRequest,
  revokeAgentLease,
  revokeAgentRequestSecret,
  reviewAgentRequest,
  shareAgentRequestSecret,
  winnerOf,
} from "./agent-requests"
import type {
  AgentLease,
  AgentRequest,
  DecideAgentRequestInput,
} from "./agent-requests"

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

const lease: AgentLease = {
  id: "ils_1",
  organization_id: "org-1",
  asker_user_id: "user-2",
  owner_user_id: "user-1",
  channel_id: "C123",
  thread_ts: "1727600000.000100",
  request_id: "irq_1",
  created_at: "2026-09-29T14:02:00Z",
  expires_at: "2026-09-29T15:02:00Z",
}

describe("listing by origin and ask", () => {
  it("sends the filters that are set, and no others", async () => {
    const fetchMock = stubFetch(() => json({ requests: [] }))

    await listAgentRequests("org-1", "inbox", { limit: 20, origin: "slack" })
    await listAgentRequests("org-1", "sent", { askId: "iak_9" })
    await listAgentRequests("org-1", "inbox", { origin: "agent" })

    expect(sent(fetchMock).map(([, url]) => url)).toEqual([
      "/v1/organizations/org-1/agent-requests/inbox?limit=20&origin=slack",
      "/v1/organizations/org-1/agent-requests/sent?ask_id=iak_9",
      "/v1/organizations/org-1/agent-requests/inbox?origin=agent",
    ])
  })

  const slack: AgentRequest = { ...request, id: "irq_slack", origin: "slack" }
  const agent: AgentRequest = { ...request, id: "irq_agent", origin: "agent" }
  const legacy: AgentRequest = { ...request, id: "irq_legacy" }
  const anotherSlack: AgentRequest = {
    ...slack,
    id: "irq_another_slack",
  }
  const mixed = [slack, legacy, agent, anotherSlack]

  it.each(["inbox", "sent"] as const)(
    "filters a mixed %s when an older server ignores origin, treating legacy requests as agents'",
    async (box) => {
      const fetchMock = stubFetch(() => json({ requests: mixed }))

      expect(
        await listAgentRequests("org-1", box, { origin: "slack" })
      ).toEqual([slack, anotherSlack])
      expect(
        await listAgentRequests("org-1", box, { origin: "agent" })
      ).toEqual([legacy, agent])
      expect(sent(fetchMock)).toEqual([
        [
          "GET",
          `/v1/organizations/org-1/agent-requests/${box}?origin=slack`,
          undefined,
        ],
        [
          "GET",
          `/v1/organizations/org-1/agent-requests/${box}?origin=agent`,
          undefined,
        ],
      ])
    }
  )

  it.each(["inbox", "sent"] as const)(
    "leaves an unfiltered %s in the server's order",
    async (box) => {
      stubFetch(() => json({ requests: mixed }))

      expect(await listAgentRequests("org-1", box)).toEqual(mixed)
    }
  )
})

describe("reviewing a held answer", () => {
  it("posts only the action", async () => {
    const fetchMock = stubFetch(() => json(request))

    await reviewAgentRequest("org-1", "irq_1", "send_masked")

    expect(sent(fetchMock)).toEqual([
      [
        "POST",
        "/v1/organizations/org-1/agent-requests/irq_1/review",
        '{"action":"send_masked"}',
      ],
    ])
  })
})

describe("the owner's consent choice", () => {
  it("posts the choice and answers with the request and the lease access gave", async () => {
    const decided = { ...request, status: "approved", revision: 2 }
    const fetchMock = stubFetch(() => json({ request: decided, lease }))

    const result = await consentAgentRequest("org-1", "irq_1", "access")

    expect(result).toEqual({ request: decided, lease })
    expect(sent(fetchMock)).toEqual([
      [
        "POST",
        "/v1/organizations/org-1/agent-requests/irq_1/consent",
        '{"choice":"access"}',
      ],
    ])
  })

  it("throws the winner of a choice that lost", async () => {
    stubFetch(() =>
      json(
        {
          error: {
            code: "already_decided",
            message: "agent request was already decided",
          },
          winner: {
            request_id: "irq_1",
            status: "approved",
            revision: 2,
            decision: "approved",
            consent: "summary",
            decided_by: { kind: "member", user_id: "user-1", surface: "slack" },
            decided_at: "2026-09-29T14:02:00Z",
            leaked: "not a field of the winner",
          },
        },
        409
      )
    )

    const lost = await consentAgentRequest("org-1", "irq_1", "deny").catch(
      (error: unknown) => error
    )

    expect(lost).toBeInstanceOf(AgentRequestAlreadyDecided)
    expect(lost).toMatchObject({
      status: 409,
      code: "already_decided",
      message: "agent request was already decided",
      winner: {
        request_id: "irq_1",
        status: "approved",
        revision: 2,
        decision: "approved",
        consent: "summary",
        decided_by: { kind: "member", user_id: "user-1", surface: "slack" },
        decided_at: "2026-09-29T14:02:00Z",
      },
    })
    expect((lost as AgentRequestAlreadyDecided).winner).not.toHaveProperty(
      "leaked"
    )
  })

  it("still says it lost when Go names no winner", async () => {
    stubFetch(() =>
      json({ error: { code: "already_decided", message: "decided" } }, 409)
    )

    await expect(
      consentAgentRequest("org-1", "irq_1", "summary")
    ).rejects.toMatchObject({
      status: 409,
      code: "already_decided",
      winner: undefined,
    })
  })

  it("keeps the codes of every other refusal", async () => {
    const answers = [
      json({ error: { code: "request_expired", message: "expired" } }, 409),
      json({ error: { code: "invalid_request", message: "no consent" } }, 400),
      json({ error: { code: "consent_unavailable", message: "off" } }, 503),
    ]
    stubFetch(() => answers.shift() ?? json({}))

    for (const expected of [
      { status: 409, code: "request_expired" },
      { status: 400, code: "invalid_request" },
      { status: 503, code: "consent_unavailable" },
    ]) {
      const refused = await consentAgentRequest(
        "org-1",
        "irq_1",
        "summary"
      ).catch((error: unknown) => error)
      expect(refused).not.toBeInstanceOf(AgentRequestAlreadyDecided)
      expect(refused).toMatchObject(expected)
    }
  })

  it("reads a winner as data: what is not a winner is none", () => {
    expect(winnerOf(undefined)).toBeUndefined()
    expect(winnerOf("approved")).toBeUndefined()
    expect(
      winnerOf({ request_id: "irq_1", status: "sideways", revision: 1 })
    ).toBeUndefined()
    expect(
      winnerOf({ request_id: "irq_1", status: "expired", revision: 3 })
    ).toEqual({ request_id: "irq_1", status: "expired", revision: 3 })
  })
})

describe("the owner's own reply", () => {
  it("posts only the text, and answers the request as the owner now reads it", async () => {
    const answered = {
      ...request,
      status: "completed",
      revision: 3,
      result: {
        status: "completed",
        summary: "Yes, go ahead.",
        content: "",
        tools_used: [],
        artifacts: [],
        completed_at: "2026-09-29T14:02:00Z",
        written_by_owner: true,
      },
    }
    const fetchMock = stubFetch(() => json({ request: answered }))

    const result = await replyToAgentRequest("org-1", "irq_1", "Yes, go ahead.")

    expect(result).toEqual(answered)
    expect(sent(fetchMock)).toEqual([
      [
        "POST",
        "/v1/organizations/org-1/agent-requests/irq_1/reply",
        '{"text":"Yes, go ahead."}',
      ],
    ])
  })

  it("throws the winner of a reply that lost, as a choice does", async () => {
    stubFetch(() =>
      json(
        {
          error: {
            code: "already_decided",
            message: "agent request was already decided",
          },
          winner: {
            request_id: "irq_1",
            status: "completed",
            revision: 3,
            decision: "approved",
            consent: "reply",
            decided_by: { kind: "member", user_id: "user-1", surface: "slack" },
            decided_at: "2026-09-29T14:02:00Z",
          },
        },
        409
      )
    )

    const lost = await replyToAgentRequest("org-1", "irq_1", "Hello").catch(
      (error: unknown) => error
    )

    expect(lost).toBeInstanceOf(AgentRequestAlreadyDecided)
    expect(lost).toMatchObject({
      status: 409,
      code: "already_decided",
      winner: {
        status: "completed",
        decision: "approved",
        consent: "reply",
        decided_by: { kind: "member", user_id: "user-1", surface: "slack" },
      },
    })
  })

  it("keeps the codes of every other refusal", async () => {
    const answers = [
      json({ error: { code: "request_expired", message: "expired" } }, 409),
      json({ error: { code: "invalid_request", message: "too long" } }, 400),
      json({ error: { code: "forbidden", message: "not yours" } }, 403),
      json({ error: { code: "not_found", message: "gone" } }, 404),
    ]
    stubFetch(() => answers.shift() ?? json({}))

    for (const expected of [
      { status: 409, code: "request_expired" },
      { status: 400, code: "invalid_request" },
      { status: 403, code: "forbidden" },
      { status: 404, code: "not_found" },
    ]) {
      const refused = await replyToAgentRequest(
        "org-1",
        "irq_1",
        "Hello"
      ).catch((error: unknown) => error)
      expect(refused).not.toBeInstanceOf(AgentRequestAlreadyDecided)
      expect(refused).toMatchObject(expected)
    }
  })

  it("reads a winner that replied", () => {
    expect(
      winnerOf({
        request_id: "irq_1",
        status: "completed",
        revision: 3,
        decision: "approved",
        consent: "reply",
      })
    ).toEqual({
      request_id: "irq_1",
      status: "completed",
      revision: 3,
      decision: "approved",
      consent: "reply",
    })
    expect(
      winnerOf({
        request_id: "irq_1",
        status: "completed",
        revision: 3,
        consent: "sideways",
      })
    ).not.toHaveProperty("consent")
  })

  it("knows a request that needs its owner's own reply, and an answer they wrote", () => {
    expect(needsOwnerReply({ answer_by: "owner" })).toBe(true)
    expect(needsOwnerReply({ answer_by: "agent" })).toBe(false)
    // Older requests say nothing, which means the agent answers.
    expect(needsOwnerReply({})).toBe(false)

    const result = {
      status: "completed" as const,
      summary: "Done.",
      content: "",
      tools_used: [],
      artifacts: [],
      completed_at: "2026-09-29T14:02:00Z",
    }
    const decision = {
      decision: "approved" as const,
      message: "",
      allowed_tools: [],
      granted_permissions: [],
      decided_at: "2026-09-29T14:02:00Z",
    }
    const review = {
      decided_by: { kind: "member" },
      decided_at: "2026-09-29T14:02:00Z",
    }
    expect(isOwnerWritten({ result })).toBe(false)
    expect(
      isOwnerWritten({ result: { ...result, written_by_owner: true } })
    ).toBe(true)
    expect(
      isOwnerWritten({ result: { ...result, written_by_owner: false } })
    ).toBe(false)
    // The two records of how it came to be say the same.
    expect(
      isOwnerWritten({ result, decision: { ...decision, consent: "reply" } })
    ).toBe(true)
    expect(
      isOwnerWritten({ result, review: { ...review, action: "reply" } })
    ).toBe(true)
    expect(
      isOwnerWritten({
        result,
        decision: { ...decision, consent: "summary" },
        review: { ...review, action: "send_masked" },
      })
    ).toBe(false)
  })
})

describe("leases and Activity", () => {
  it("lists the latest activity", async () => {
    const entries = [
      {
        kind: "asked",
        at: "2026-09-29T14:00:00Z",
        role: "owner",
        with_user_id: "user-2",
        request_id: "irq_1",
        status: "pending",
      },
    ]
    const fetchMock = stubFetch(() => json({ activity: entries }))

    expect(await listAgentActivity("org-1", { limit: 100 })).toEqual(entries)
    await listAgentActivity("org-1")

    expect(sent(fetchMock).map(([method, url]) => [method, url])).toEqual([
      ["GET", "/v1/organizations/org-1/agent-requests/activity?limit=100"],
      ["GET", "/v1/organizations/org-1/agent-requests/activity"],
    ])
  })

  it("revokes a lease and answers it as it now stands", async () => {
    const ended = { ...lease, revoked_at: "2026-09-29T14:30:00Z" }
    const fetchMock = stubFetch(() => json({ lease: ended }))

    expect(await revokeAgentLease("org-1", "ils_1")).toEqual(ended)
    expect(sent(fetchMock)).toEqual([
      ["DELETE", "/v1/organizations/org-1/agent-leases/ils_1", undefined],
    ])
  })

  it("knows a live lease from one that ended or ran out", () => {
    const now = Date.parse("2026-09-29T14:30:00Z")
    expect(isLeaseLive(lease, now)).toBe(true)
    expect(isLeaseLive(lease, Date.parse("2026-09-29T15:02:00Z"))).toBe(false)
    expect(
      isLeaseLive({ ...lease, revoked_at: "2026-09-29T14:10:00Z" }, now)
    ).toBe(false)
  })
})

describe("one-time secrets", () => {
  it("shares a value with a lifetime, or with none", async () => {
    const fetchMock = stubFetch(() =>
      json({ request_id: "irq_1", expires_at: "2026-09-29T15:00:00Z" })
    )

    expect(
      await shareAgentRequestSecret("org-1", "irq_1", {
        value: "hunter2",
        ttlSeconds: 600,
      })
    ).toEqual({ request_id: "irq_1", expires_at: "2026-09-29T15:00:00Z" })
    await shareAgentRequestSecret("org-1", "irq_1", { value: "hunter3" })

    expect(sent(fetchMock)).toEqual([
      [
        "PUT",
        "/v1/organizations/org-1/agent-requests/irq_1/secret",
        '{"value":"hunter2","ttl_seconds":600}',
      ],
      [
        "PUT",
        "/v1/organizations/org-1/agent-requests/irq_1/secret",
        '{"value":"hunter3"}',
      ],
    ])
  })

  it("opens a secret once: the second call is gone", async () => {
    const answers = [
      json({ value: "hunter2", expires_at: "2026-09-29T15:00:00Z" }),
      json({ error: { code: "secret_gone", message: "already opened" } }, 410),
    ]
    const fetchMock = stubFetch(() => answers.shift() ?? json({}))

    expect(await openAgentRequestSecret("org-1", "irq_1")).toEqual({
      value: "hunter2",
      expires_at: "2026-09-29T15:00:00Z",
    })
    const gone = await openAgentRequestSecret("org-1", "irq_1").catch(
      (error: unknown) => error
    )

    expect(isSecretGone(gone)).toBe(true)
    expect(gone).toMatchObject({ status: 410, code: "secret_gone" })
    expect(sent(fetchMock).map(([method]) => method)).toEqual(["GET", "GET"])
  })

  it("takes a secret back, whether or not one was still there", async () => {
    const fetchMock = stubFetch(() => new Response(null, { status: 204 }))

    await expect(
      revokeAgentRequestSecret("org-1", "irq_1")
    ).resolves.toBeUndefined()
    expect(sent(fetchMock)).toEqual([
      [
        "DELETE",
        "/v1/organizations/org-1/agent-requests/irq_1/secret",
        undefined,
      ],
    ])
  })

  it("does not take other errors for a secret that is gone", () => {
    expect(isSecretGone(new Error("offline"))).toBe(false)
    expect(isSecretGone(undefined)).toBe(false)
  })
})

describe("what a request is", () => {
  const slack: AgentRequest = {
    ...request,
    origin: "slack",
    requested_permissions: ["consent:summary", "consent:access"],
  }

  it("asks for consent only when it asks for both consent permissions", () => {
    expect(asksForConsent(slack)).toBe(true)
    expect(asksForConsent({ requested_permissions: ["consent:summary"] })).toBe(
      false
    )
    expect(asksForConsent(request)).toBe(false)
  })

  it("was an agent's unless it says Slack", () => {
    expect(originOf(slack)).toBe("slack")
    expect(originOf(request)).toBe("agent")
    expect(originOf({ origin: "agent" })).toBe("agent")
  })

  it("carries a secret only once approved with access, and not after it closed", () => {
    const decision = {
      decision: "approved" as const,
      message: "",
      allowed_tools: [],
      granted_permissions: [],
      decided_at: "2026-09-29T14:02:00Z",
    }
    const access = { ...decision, consent: "access" as const }
    const summary = { ...decision, consent: "summary" as const }

    expect(canCarrySecret({ status: "approved", decision: access })).toBe(true)
    expect(canCarrySecret({ status: "review", decision: access })).toBe(true)
    expect(canCarrySecret({ status: "completed", decision: access })).toBe(true)
    expect(canCarrySecret({ status: "approved", decision: summary })).toBe(
      false
    )
    expect(canCarrySecret({ status: "pending", decision: undefined })).toBe(
      false
    )
    for (const status of [
      "expired",
      "withheld",
      "failed",
      "rejected",
    ] as const) {
      expect(canCarrySecret({ status, decision: access })).toBe(false)
    }
  })
})

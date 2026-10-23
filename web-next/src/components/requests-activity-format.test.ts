import { describe, expect, it } from "vitest"
import { clockTime } from "./direct-message-format"
import {
  accessReason,
  activityIcon,
  activitySentence,
  openableRows,
  repliedRequestIds,
} from "./requests-activity-format"
import {
  ASKER,
  OWNER,
  activityEntry,
  lease,
  reader,
} from "./request-card-fixtures"
import type {
  AgentAccessDecision,
  AgentActivityEntry,
} from "@/lib/agents/agent-requests"
import type { ThingNames } from "./requests-activity-format"

const owner = reader(OWNER)
const asker = reader(ASKER)

/** The same entry as each of its two parties read it: the owner's, and the asker's. */
function both(entry: Omit<AgentActivityEntry, "role" | "with_user_id">) {
  return {
    owner: activitySentence(
      { ...entry, role: "owner", with_user_id: ASKER },
      owner
    ),
    asker: activitySentence(
      { ...entry, role: "asker", with_user_id: OWNER },
      asker
    ),
  }
}

describe("what Activity says", () => {
  it("says an ask, from each side", () => {
    expect(both({ kind: "asked", at: "2026-09-29T12:00:00Z" })).toEqual({
      owner: "Raya asked you in Slack.",
      asker: "You asked Ada in Slack.",
    })
  })

  it("says where a choice was made and how far it went", () => {
    const decided = {
      kind: "decided" as const,
      at: "2026-09-29T12:02:00Z",
      decided_by: { kind: "member", surface: "slack" },
    }
    expect(
      both({ ...decided, decision: "approved", consent: "summary" })
    ).toEqual({
      owner: "You approved Raya's request in Slack. Answer shared.",
      asker: "Ada approved your request in Slack. Answer shared.",
    })
    expect(
      both({ ...decided, decision: "approved", consent: "access" }).owner
    ).toBe("You approved Raya's request in Slack. Access for 1 hour.")
    expect(both({ ...decided, decision: "rejected" })).toEqual({
      owner: "You denied Raya's request in Slack.",
      asker: "Ada denied your request in Slack.",
    })
    expect(
      both({
        kind: "decided",
        at: "2026-09-29T12:02:00Z",
        decision: "rejected",
        decided_by: { kind: "member", surface: "web" },
      }).owner
    ).toBe("You denied Raya's request in Oryxa.")
  })

  it("says a follow-up was approved under access, and never the lease's id", () => {
    const sentence = both({
      kind: "decided",
      at: "2026-09-29T12:20:00Z",
      decision: "approved",
      consent: "access",
      decided_by: { kind: "lease", surface: "lease", reason: "ils_1" },
    })
    expect(sentence).toEqual({
      owner: "Raya's follow-up was approved under the access you gave.",
      asker: "Your follow-up was approved under the access Ada gave you.",
    })
    expect(JSON.stringify(sentence)).not.toContain("ils_1")
  })

  it("says why a bypass needed no click", () => {
    const bypass = (reason?: string) =>
      both({
        kind: "decided",
        at: "2026-09-29T12:02:00Z",
        decision: "approved",
        decided_by: { kind: "bypass", surface: "bypass", reason },
      })

    expect(bypass("priority_channel")).toEqual({
      owner:
        "Raya's request was approved without asking you: it was asked in a high-priority channel.",
      asker:
        "Your request was approved without asking Ada: it was asked in a high-priority channel.",
    })
    expect(bypass("org_chart")).toEqual({
      owner:
        "Raya's request was approved without asking you: Raya is above you in your organization's chart.",
      asker:
        "Your request was approved without asking Ada: you are above Ada in your organization's chart.",
    })
    expect(bypass("something_new").owner).toBe(
      "Raya's request was approved without asking you."
    )
  })

  it("says what Oryxa and an agent did in words", () => {
    expect(
      both({
        kind: "decided",
        at: "2026-09-29T12:02:00Z",
        decision: "rejected",
        decided_by: { kind: "system", reason: "answered_elsewhere" },
      }).owner
    ).toBe("Closed because another owner answered first.")
    expect(
      both({
        kind: "decided",
        at: "2026-09-29T12:02:00Z",
        decision: "rejected",
        decided_by: { kind: "system" },
      }).owner
    ).toBe("Oryxa closed the request.")
    expect(
      both({
        kind: "decided",
        at: "2026-09-29T12:02:00Z",
        decision: "approved",
        decided_by: { kind: "agent" },
      })
    ).toEqual({
      owner: "Your agent decided the request.",
      asker: "Ada's agent decided your request.",
    })
  })

  it("says an answer, and one that failed", () => {
    expect(
      both({
        kind: "answered",
        at: "2026-09-29T12:10:00Z",
        status: "completed",
      })
    ).toEqual({
      owner: "Your agent answered Raya.",
      asker: "Ada's agent answered you.",
    })
    expect(
      both({ kind: "answered", at: "2026-09-29T12:10:00Z", status: "failed" })
    ).toEqual({
      owner: "Your agent couldn't answer Raya.",
      asker: "Ada's agent couldn't answer you.",
    })
  })

  it("says an owner replied, as a reply and not as an approval", () => {
    const replied = {
      kind: "decided" as const,
      at: "2026-09-29T12:31:00Z",
      decision: "approved" as const,
      consent: "reply" as const,
    }

    expect(
      both({ ...replied, decided_by: { kind: "member", surface: "web" } })
    ).toEqual({
      owner: "You replied to Raya's request in Oryxa.",
      asker: "Ada replied to your request in Oryxa.",
    })
    expect(
      both({ ...replied, decided_by: { kind: "member", surface: "slack" } })
    ).toEqual({
      owner: "You replied to Raya's request in Slack.",
      asker: "Ada replied to your request in Slack.",
    })
    expect(both({ ...replied }).owner).toBe("You replied to Raya's request.")
    const sentence = both({
      ...replied,
      decided_by: { kind: "member", surface: "web" },
    })
    expect(JSON.stringify(sentence)).not.toMatch(/approved|Answer shared/i)
  })

  it("says it was the owner's reply, and not their agent's answer, that reached the asker", () => {
    const answered = {
      kind: "answered" as const,
      at: "2026-09-29T12:31:00Z",
      status: "completed" as const,
      request_id: "irq_slack",
    }
    const replied = new Set(["irq_slack"])
    const read = (role: "owner" | "asker", requests: ReadonlySet<string>) =>
      activitySentence(
        role === "owner"
          ? { ...answered, role, with_user_id: ASKER }
          : { ...answered, role, with_user_id: OWNER },
        role === "owner" ? owner : asker,
        requests
      )

    expect(read("owner", replied)).toBe("Your reply reached Raya.")
    expect(read("asker", replied)).toBe("Ada's reply reached you.")
    // An answer of another request, or of one not known to be a reply, is the agent's.
    expect(read("owner", new Set(["irq_other"]))).toBe(
      "Your agent answered Raya."
    )
    expect(read("asker", new Set())).toBe("Ada's agent answered you.")
    // A reply cannot have failed: a failed answer is still the agent's.
    expect(
      activitySentence(
        { ...answered, status: "failed", role: "owner", with_user_id: ASKER },
        owner,
        replied
      )
    ).toBe("Your agent couldn't answer Raya.")
  })

  it("knows the requests that were replied to from their decisions, and from nothing else", () => {
    const entries = [
      activityEntry({ kind: "answered", request_id: "irq_a" }),
      activityEntry({ kind: "decided", consent: "reply", request_id: "irq_a" }),
      activityEntry({
        kind: "decided",
        consent: "summary",
        request_id: "irq_b",
      }),
      activityEntry({ kind: "answered", request_id: "irq_b" }),
      activityEntry({
        kind: "decided",
        consent: "reply",
        request_id: undefined,
      }),
      activityEntry({ kind: "asked", request_id: "irq_c" }),
    ]

    expect([...repliedRequestIds(entries)]).toEqual(["irq_a"])
    expect(repliedRequestIds([]).size).toBe(0)
  })

  it("says a secret was opened, and access given and ended", () => {
    expect(both({ kind: "secret_viewed", at: "2026-09-29T12:20:00Z" })).toEqual(
      {
        owner: "Raya opened the secret you shared.",
        asker: "You opened the secret Ada shared.",
      }
    )
    expect(
      both({ kind: "lease_given", at: "2026-09-29T12:02:00Z", lease })
    ).toEqual({
      owner: `You gave Raya access until ${clockTime(lease.expires_at)}.`,
      asker: `Ada gave you access until ${clockTime(lease.expires_at)}.`,
    })
    expect(
      both({ kind: "lease_given", at: "2026-09-29T12:02:00Z" }).owner
    ).toBe("You gave Raya access.")
    expect(
      both({ kind: "lease_revoked", at: "2026-09-29T12:40:00Z", lease })
    ).toEqual({
      owner: "You ended Raya's access.",
      asker: "Ada ended your access.",
    })
  })

  it("names someone the list does not, and a kind it does not know, without a code", () => {
    const nobody = reader(OWNER, { nameOf: () => undefined })
    expect(
      activitySentence(
        activityEntry({ kind: "asked", with_user_id: "user-x" }),
        nobody
      )
    ).toBe("someone asked you in Slack.")
    const strange = activitySentence(
      {
        ...activityEntry({ kind: "asked" }),
        kind: "invented",
      } as unknown as AgentActivityEntry,
      owner
    )
    expect(strange).toBe("Something happened to a Slack request.")
  })
})

describe("how an entry reads at a glance", () => {
  it("picks an icon by what happened", () => {
    const icon = (
      entry: Partial<AgentActivityEntry> & Pick<AgentActivityEntry, "kind">
    ) => activityIcon(activityEntry(entry))

    expect(icon({ kind: "asked" })).toBe("asked")
    expect(icon({ kind: "decided", decision: "approved" })).toBe("approved")
    expect(icon({ kind: "decided", decision: "rejected" })).toBe("denied")
    expect(
      icon({
        kind: "decided",
        decision: "rejected",
        decided_by: { kind: "system" },
      })
    ).toBe("denied")
    expect(icon({ kind: "answered" })).toBe("answered")
    // A reply is an answer, not a click on Approve.
    expect(
      icon({ kind: "decided", decision: "approved", consent: "reply" })
    ).toBe("answered")
    expect(icon({ kind: "secret_viewed" })).toBe("secret")
    expect(icon({ kind: "lease_given" })).toBe("access")
    expect(icon({ kind: "lease_revoked" })).toBe("access-ended")
  })

  it("offers to open a request once: at its oldest listed row", () => {
    // Newest first, as Go lists them.
    const entries = [
      activityEntry({ kind: "answered", request_id: "irq_a" }),
      activityEntry({ kind: "asked", request_id: "irq_b" }),
      activityEntry({ kind: "decided", request_id: "irq_a" }),
      activityEntry({ kind: "asked", request_id: "irq_a" }),
      activityEntry({ kind: "lease_given", request_id: undefined }),
    ]

    expect([...openableRows(entries)].sort()).toEqual([1, 3])
  })
})

/** An access entry from the decision log, as Go lists it for its reader. */
function accessed(
  access: Partial<AgentAccessDecision>,
  role: "owner" | "asker",
  withUserId?: string
): AgentActivityEntry {
  return {
    kind: "accessed",
    at: "2026-09-29T12:00:00Z",
    role,
    with_user_id: withUserId,
    access: {
      at: "2026-09-29T12:00:00Z",
      service: "files",
      action: "read",
      resource_kind: "file",
      resource_id: "fil_1",
      actor_user_id: ASKER,
      reason: "team_member",
      ...access,
    },
  }
}

const named: ThingNames = {
  agentName: (id) => (id === "agt_researcher" ? "Researcher" : undefined),
  resourceName: (kind, id) =>
    kind === "corpus" && id === "cor_sales" ? "Sales playbooks" : undefined,
}

describe("what Activity says of access", () => {
  it("says who reached the member's things, and how", () => {
    // Raya's agent read Ada's file.
    expect(
      activitySentence(
        accessed(
          {
            owner_id: OWNER,
            actor_agent_id: "agt_raya",
            actor_agent_role: "primary",
          },
          "owner",
          ASKER
        ),
        owner
      )
    ).toBe("Raya's agent read your file.")
    // Raya, an admin, opened Ada's source herself.
    expect(
      activitySentence(
        accessed(
          { owner_id: OWNER, resource_kind: "source", reason: "org_admin" },
          "owner",
          ASKER
        ),
        owner
      )
    ).toBe("An admin, Raya, opened your source.")
    expect(
      activitySentence(
        accessed({ owner_id: OWNER, action: "write" }, "owner", ASKER),
        owner
      )
    ).toBe("Raya changed your file.")
    expect(
      activitySentence(
        accessed(
          {
            owner_id: OWNER,
            action: "delete",
            resource_kind: "document",
            actor_agent_id: "agt_raya",
            actor_agent_role: "specialist",
          },
          "owner",
          ASKER
        ),
        owner
      )
    ).toBe("Raya's specialist deleted your document.")
  })

  it("says what the member's own agents reached", () => {
    expect(
      activitySentence(
        accessed(
          {
            owner_id: undefined,
            actor_user_id: OWNER,
            actor_agent_id: "agt_researcher",
            actor_agent_role: "specialist",
            action: "search",
            resource_kind: "corpus",
            resource_id: "cor_sales",
            reason: "grant",
          },
          "asker"
        ),
        owner,
        undefined,
        named
      )
    ).toBe("Your specialist Researcher searched Sales playbooks.")
    expect(
      activitySentence(
        accessed(
          {
            owner_id: ASKER,
            actor_user_id: OWNER,
            actor_agent_id: "agt_ada",
            actor_agent_role: "primary",
          },
          "asker",
          ASKER
        ),
        owner
      )
    ).toBe("Your agent read Raya's file.")
    // Their own agent, on their own thing: Go lists it as the owner's.
    expect(
      activitySentence(
        accessed(
          {
            owner_id: OWNER,
            actor_user_id: OWNER,
            actor_agent_id: "agt_ada",
            actor_agent_role: "primary",
            reason: "owner",
          },
          "owner"
        ),
        owner
      )
    ).toBe("Your agent read your file.")
    // The member themself, as an admin, in someone else's things.
    expect(
      activitySentence(
        accessed(
          { owner_id: ASKER, actor_user_id: OWNER, reason: "org_admin" },
          "asker",
          ASKER
        ),
        owner
      )
    ).toBe("You opened Raya's file.")
  })

  it("says each rule as a short phrase", () => {
    const reasons = {
      owner: "your own",
      org_admin: "org admin",
      team_member: "on the same team",
      team_admin: "team admin",
      organization: "shared with everyone",
      grant: "allowed by its grant",
    } as const
    for (const [reason, phrase] of Object.entries(reasons)) {
      expect(
        accessReason(
          accessed(
            {
              actor_user_id: OWNER,
              actor_agent_id: "agt_ada",
              reason: reason as AgentAccessDecision["reason"],
            },
            "asker"
          ),
          owner
        )
      ).toBe(phrase)
    }
    expect(
      accessReason(accessed({ reason: "owner" }, "owner", ASKER), owner)
    ).toBe("their own")
    // Only access entries carry a reason.
    expect(
      accessReason(activityEntry({ kind: "asked" }), owner)
    ).toBeUndefined()
  })

  it("falls back to someone, an agent, or the kind, never an id", () => {
    const strangers = reader(OWNER, { nameOf: () => undefined })
    const sentences = [
      accessed({ owner_id: OWNER, actor_user_id: "user-x" }, "owner", "user-x"),
      accessed(
        { owner_id: OWNER, actor_user_id: "user-x", actor_agent_id: "agt_x" },
        "owner",
        "user-x"
      ),
      accessed(
        { owner_id: OWNER, actor_user_id: "user-x", reason: "org_admin" },
        "owner",
        "user-x"
      ),
      accessed(
        {
          owner_id: "user-x",
          actor_user_id: OWNER,
          actor_agent_id: "agt_ada",
          resource_kind: "corpus",
          resource_id: "cor_unknown",
        },
        "asker",
        "user-x"
      ),
    ].map((entry) => activitySentence(entry, strangers, undefined, named))

    expect(sentences).toEqual([
      "Someone opened your file.",
      "An agent read your file.",
      "An admin opened your file.",
      "Your agent read someone's collection.",
    ])
    for (const sentence of sentences) {
      expect(sentence).not.toMatch(/user-x|agt_|cor_|fil_/)
    }
  })

  it("doesn't trip on what it doesn't know", () => {
    const odd = {
      kind: "accessed",
      at: "2026-09-29T12:00:00Z",
      role: "owner",
      access: {
        action: "teleport",
        resource_kind: "hologram",
        reason: "vibes",
        extra: { nested: true },
      },
    } as unknown as AgentActivityEntry

    expect(activitySentence(odd, owner)).toBe("Someone reached an item.")
    expect(accessReason(odd, owner)).toBeUndefined()
    expect(activityIcon(odd)).toBe("reached")
    const bare = { ...odd, access: undefined }
    expect(activitySentence(bare, owner)).toBe("Someone reached an item.")
  })

  it("reads with an icon for what was done", () => {
    expect(activityIcon(accessed({ action: "read" }, "owner"))).toBe("reached")
    expect(activityIcon(accessed({ action: "search" }, "owner"))).toBe(
      "reached"
    )
    expect(activityIcon(accessed({ action: "write" }, "owner"))).toBe("changed")
    expect(activityIcon(accessed({ action: "delete" }, "owner"))).toBe(
      "deleted"
    )
  })
})

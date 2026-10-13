import { describe, expect, it } from "vitest"
import { clockTime } from "./direct-message-format"
import {
  activityIcon,
  activitySentence,
  openableRows,
} from "./requests-activity-format"
import {
  ASKER,
  OWNER,
  activityEntry,
  lease,
  reader,
} from "./request-card-fixtures"
import type { AgentActivityEntry } from "@/lib/agents/agent-requests"

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
      owner: "You approved Raya's request in Slack. Summary only.",
      asker: "Ada approved your request in Slack. Summary only.",
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

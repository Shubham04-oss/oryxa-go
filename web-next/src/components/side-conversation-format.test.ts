import { describe, expect, it } from "vitest"
import {
  agentRole,
  askedAgentName,
  cardState,
  isSideNews,
  sideNews,
  sideTimeline,
  targetOptions,
  teammatesOf,
  viaLine,
} from "./side-conversation-format"
import type { SideAgents, SideReader } from "./side-conversation-format"
import type {
  AgentRef,
  SideAgentMessage,
  SideMessage,
  SideRequestCard,
  SideStatusLine,
} from "@/lib/side"
import {
  adaWhisperer,
  contribution,
  memberWhisperer,
  teamspace,
  viewerId,
  whisperer,
} from "@/test/teamspaces"

const priya = "user-priya"

const reader: SideReader = {
  viewerId,
  peer: { id: priya, name: "Priya Shah", short: "Priya" },
}

const leo: AgentRef = {
  id: "agt_leo",
  name: "Leo",
  ownerId: viewerId,
  kind: "primary",
  approval: "owner",
}
const nia: AgentRef = {
  id: "agt_nia",
  name: "Nia",
  ownerId: priya,
  kind: "primary",
  approval: "owner",
}
const pricing: AgentRef = {
  id: "agt_pricing",
  name: "Pricing",
  ownerId: priya,
  kind: "specialist",
  approval: "agent",
  role: "Prices the launch",
}
const agents: SideAgents = { mine: leo, theirs: [nia, pricing] }

/** 2026-09-24 at hh:mm:ss, local time. */
function local(hours: number, minutes: number, seconds = 0): string {
  return new Date(2026, 8, 24, hours, minutes, seconds).toISOString()
}

function message(
  seq: number,
  authorId: string,
  createdAt: string,
  extra: Partial<SideMessage> = {}
): SideMessage {
  return {
    kind: "message",
    id: `sdi_${seq}`,
    seq,
    authorId,
    body: `line ${seq}`,
    createdAt,
    ...extra,
  }
}

function agentMessage(
  seq: number,
  createdAt: string,
  extra: Partial<SideAgentMessage> = {}
): SideAgentMessage {
  return {
    kind: "agent-message",
    id: `sdi_${seq}`,
    seq,
    agentId: "agt_leo",
    agentName: "Leo",
    ownerId: viewerId,
    body: "",
    createdAt,
    updatedAt: createdAt,
    status: "done",
    ...extra,
  }
}

function card(extra: Partial<SideRequestCard> = {}): SideRequestCard {
  return {
    kind: "request-card",
    id: "sdi_9",
    seq: 9,
    requestId: "irq_9",
    requesterId: viewerId,
    requesterAgentId: "agt_leo",
    requesterAgentName: "Leo",
    targetId: priya,
    targetAgentId: "agt_nia",
    targetAgentName: "Nia",
    approval: "owner",
    status: "pending",
    task: "Check the notes",
    createdAt: local(9, 0),
    updatedAt: local(9, 0),
    expiresAt: local(23, 0),
    ...extra,
  }
}

const decided = {
  decision: "approved" as const,
  message: "Approved in the side conversation.",
  decidedAt: local(9, 5),
}

describe("sideTimeline", () => {
  it("groups people's messages as a DM's, and sets each other item on its own", () => {
    const line: SideStatusLine = {
      kind: "status",
      id: "sdi_6",
      seq: 6,
      requestId: "irq_5",
      outcome: "expired",
      authorId: priya,
      text: "The request to Nia expired before Priya approved it.",
      createdAt: local(9, 3),
    }
    const pending = card({
      id: "sdc-page",
      seq: 0,
      requestId: "",
      status: "sending",
      clientId: "sdc-page",
      createdAt: local(9, 4),
    })
    const timeline = sideTimeline(
      [
        message(1, priya, local(9, 0, 5)),
        message(2, priya, local(9, 0, 40)),
        agentMessage(3, local(9, 1)),
        message(4, priya, local(9, 1, 10)),
        message(5, viewerId, local(9, 2)),
        line,
        pending,
      ],
      viewerId,
      new Date(2026, 8, 24, 12).getTime()
    )
    expect(
      timeline.map((entry) =>
        entry.kind === "day"
          ? `day ${entry.label}`
          : entry.kind === "group"
            ? `${entry.mine ? "mine" : "theirs"} ${entry.messages.map((each) => each.seq).join(",")}`
            : `${entry.item.kind} ${entry.key}`
      )
    ).toEqual([
      "day Today",
      "theirs 1,2",
      "agent-message entry:sdi_3",
      // The agent's message ended the group: the same minute starts another.
      "theirs 4",
      "mine 5",
      "status entry:sdi_6",
      // Keyed by the page's own id, so it keeps its place once confirmed.
      "request-card entry:sdc-page",
    ])
  })
})

describe("who an agent is to the reader", () => {
  it("names the reader's own agents as theirs, and the other person's by name", () => {
    expect(agentRole("agt_leo", viewerId, agents, reader)).toBe("your agent")
    expect(agentRole("agt_nia", priya, agents, reader)).toBe("Priya's agent")
    expect(agentRole("agt_pricing", priya, agents, reader)).toBe(
      "Priya's specialist"
    )
    // One the roster does not know is an agent.
    expect(agentRole("agt_other", viewerId, agents, reader)).toBe("your agent")
  })

  it("says which agent a question asked", () => {
    expect(
      askedAgentName(
        message(1, viewerId, local(9, 0), { asksAgentId: "agt_leo" }),
        agents
      )
    ).toBe("Leo")
    expect(
      askedAgentName(
        message(1, priya, local(9, 0), { asksAgentId: "agt_gone" }),
        agents
      )
    ).toBe("their agent")
    expect(askedAgentName(message(1, priya, local(9, 0)), agents)).toBe(
      undefined
    )
  })

  it("says whom a result was for and who approved it", () => {
    expect(
      viaLine(
        { requestId: "irq_1", onBehalfOf: priya, approvedBy: priya },
        reader
      )
    ).toBe("on behalf of Priya · approved by Priya")
    expect(viaLine({ requestId: "irq_1", onBehalfOf: priya }, reader)).toBe(
      "on behalf of Priya · ran without asking"
    )
    expect(
      viaLine(
        { requestId: "irq_1", onBehalfOf: viewerId, approvedBy: viewerId },
        reader
      )
    ).toBe("on your behalf · approved by you")
  })
})

describe("cardState", () => {
  it("says where a request stands to the one who asked", () => {
    const words = (extra: Partial<SideRequestCard>) =>
      cardState(card(extra), reader)
    expect(words({ status: "sending" })).toEqual({
      tone: "waiting",
      text: "Sending…",
    })
    expect(
      words({ status: "not_filed", error: "Priya's agent has no seat here." })
    ).toEqual({
      tone: "failed",
      text: "Not filed. Priya's agent has no seat here.",
    })
    expect(words({ status: "not_filed" }).text).toBe("Not filed.")
    expect(words({}).text).toBe("Waiting for Priya to approve.")
    expect(words({ status: "approved", decision: decided })).toEqual({
      tone: "working",
      text: "Approved by Priya. Nia is working on it…",
    })
    expect(words({ status: "completed" })).toEqual({
      tone: "done",
      text: "Done.",
    })
    expect(words({ status: "rejected" })).toEqual({
      tone: "ended",
      text: "Declined by Priya.",
    })
    expect(words({ status: "failed" })).toEqual({
      tone: "failed",
      text: "Failed.",
    })
    expect(words({ status: "expired" })).toEqual({
      tone: "ended",
      text: "Expired.",
    })
  })

  it("says an automatic one runs without its owner, and speaks to the owner as you", () => {
    const automatic = card({
      targetAgentId: "agt_pricing",
      targetAgentName: "Pricing",
      approval: "agent",
    })
    expect(cardState(automatic, reader).text).toBe(
      "Waiting for Pricing to start."
    )
    expect(cardState({ ...automatic, status: "approved" }, reader).text).toBe(
      "Pricing is working on it…"
    )
    expect(cardState({ ...automatic, status: "rejected" }, reader).text).toBe(
      "Declined by Pricing."
    )

    const forMe = card({
      requesterId: priya,
      targetId: viewerId,
      targetAgentId: "agt_leo",
      targetAgentName: "Leo",
    })
    expect(cardState(forMe, reader).text).toBe("Waiting for you to approve.")
    expect(
      cardState({ ...forMe, status: "approved", decision: decided }, reader)
        .text
    ).toBe("Approved by you. Leo is working on it…")
    expect(cardState({ ...forMe, status: "rejected" }, reader).text).toBe(
      "Declined by you."
    )
  })
})

describe("targetOptions", () => {
  it("offers the other person first, then the reader's own agent, then the other person's agents", () => {
    const options = targetOptions(agents, reader)
    expect(
      options.map(({ key, label, detail, name, placeholder, hint }) => ({
        key,
        label,
        detail,
        name,
        placeholder,
        hint,
      }))
    ).toEqual([
      {
        key: "person",
        label: "Priya",
        detail: "A message to Priya",
        name: "Priya",
        placeholder: "Message Priya",
        hint: undefined,
      },
      {
        key: "mine",
        label: "Ask my agent",
        detail: "Leo · Priya sees the answer too",
        name: "Leo",
        placeholder: "Ask Leo, your agent",
        hint: "Priya sees Leo's answer too.",
      },
      {
        key: "agent:agt_nia",
        label: "Ask Priya's agent",
        detail: "Nia · asks Priya first",
        name: "Nia",
        placeholder: "Ask Nia, Priya's agent",
        hint: "Priya approves this before Nia starts.",
      },
      {
        key: "agent:agt_pricing",
        label: "Ask Pricing",
        detail: "Priya's specialist · runs without asking",
        name: "Pricing",
        placeholder: "Ask Pricing, Priya's specialist",
        hint: "Pricing runs this without asking Priya.",
      },
    ])
    expect(options.map((option) => option.identity.kind)).toEqual([
      "user",
      "agent",
      "agent",
      "agent",
    ])
  })

  it("offers no agent of the reader's when they have none here", () => {
    expect(
      targetOptions({ mine: undefined, theirs: [nia] }, reader).map(
        (option) => option.key
      )
    ).toEqual(["person", "agent:agt_nia"])
  })
})

describe("teammatesOf", () => {
  it("lists everyone else with a seat, by name, with their agent and active contributions", () => {
    const space = teamspace({
      whisperers: [
        memberWhisperer,
        whisperer(priya, "Priya", "agt_nia", "Nia"),
        adaWhisperer,
        whisperer("user-sam", "", "agt_sol", "Sol"),
      ],
      contributions: [
        contribution({
          agent_id: "agt_pricing",
          owner_user_id: priya,
          name: "Pricing",
          role: "Prices the launch",
          delegation_mode: "automatic",
        }),
        contribution({
          agent_id: "agt_drafts",
          owner_user_id: "user-ada",
          name: "Drafts",
          status: "disabled",
        }),
        contribution({
          agent_id: "agt_notes",
          owner_user_id: viewerId,
          name: "Notes",
        }),
      ],
    })
    const names: Record<string, string> = { [priya]: "Priya Shah" }
    expect(teammatesOf(space, viewerId, (userId) => names[userId])).toEqual([
      {
        userId: "user-ada",
        name: "Ada",
        agents: [
          { id: "agt_juno", name: "Juno", kind: "primary", automatic: false },
        ],
      },
      {
        userId: priya,
        name: "Priya Shah",
        agents: [
          { id: "agt_nia", name: "Nia", kind: "primary", automatic: false },
          {
            id: "agt_pricing",
            name: "Pricing",
            kind: "specialist",
            automatic: true,
            role: "Prices the launch",
          },
        ],
      },
      {
        userId: "user-sam",
        name: "Teammate",
        agents: [
          { id: "agt_sol", name: "Sol", kind: "primary", automatic: false },
        ],
      },
    ])
  })
})

describe("news", () => {
  it("is what someone or some agent said, never the reader's own", () => {
    expect(isSideNews(message(1, priya, local(9, 0)), viewerId)).toBe(true)
    expect(isSideNews(message(1, viewerId, local(9, 0)), viewerId)).toBe(false)
    expect(isSideNews(card(), viewerId)).toBe(false)
    expect(isSideNews(card({ requesterId: priya }), viewerId)).toBe(true)
    // The reader's own agent answering them is news to them.
    expect(isSideNews(agentMessage(2, local(9, 0)), viewerId)).toBe(true)
  })

  it("is said in words that name who said it", () => {
    expect(sideNews(message(1, priya, local(9, 0)), reader)).toBe(
      "Priya Shah: line 1"
    )
    expect(
      sideNews(agentMessage(2, local(9, 0), { status: "streaming" }), reader)
    ).toBe("Leo is answering.")
    expect(
      sideNews(agentMessage(2, local(9, 0), { body: "Two tasks." }), reader)
    ).toBe("Leo: Two tasks.")
    expect(
      sideNews(agentMessage(2, local(9, 0), { status: "waiting" }), reader)
    ).toBe("Leo is waiting for a confirmation.")
    expect(
      sideNews(agentMessage(2, local(9, 0), { status: "failed" }), reader)
    ).toBe("Leo couldn't answer.")
    expect(sideNews(card({ requesterId: priya }), reader)).toBe(
      "Priya Shah asked Nia: Check the notes"
    )
  })
})

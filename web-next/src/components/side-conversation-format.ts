// How a side conversation reads on screen (DESIGN.md §3, "Side
// conversations"): its log, which is a DM's with agents' messages, request
// cards and quiet lines between people's groups; who each agent is to the
// reader; where a request stands, in words; who a message can go to; and a
// teamspace's teammates with their agents. Pure, so the views and their
// tests share one set of rules.
import { conversationTimeline } from "./direct-message-format"
import type { DmDay, DmEntry, DmGroup } from "./direct-message-format"
import { agentIdentity, personIdentity } from "./teamspace-format"
import type { AvatarIdentity } from "./workspace-avatar"
import type {
  AgentRef,
  SideAgentMessage,
  SideItem,
  SideMessage,
  SideRequestCard,
  SideStatusLine,
} from "@/lib/side"
import { AGENT_REQUEST_TOOLBOXES } from "@/lib/agents/agent-requests"
import type { AgentRequestToolbox } from "@/lib/agents/agent-requests"
import type { Teamspace } from "@/lib/teamspaces/client"

/** Who reads the conversation, and the other person, in full and in a sentence. */
export type SideReader = {
  viewerId: string
  peer: { id: string; name: string; short: string }
}

/** Both people's agents in the conversation, as useSideConversation hands them over. */
export type SideAgents = { mine: AgentRef | undefined; theirs: AgentRef[] }

/** What stands on its own in the log. */
export type SideEntryItem = SideAgentMessage | SideRequestCard | SideStatusLine

export type SideTimelineItem =
  DmDay | DmGroup<SideMessage> | DmEntry<SideEntryItem>

export function isSideMessage(item: SideItem): item is SideMessage {
  return item.kind === "message"
}

/**
 * The conversation as it reads: a separator as each day starts, each
 * person's messages in a minute as one group (as a DM's), and each agent's
 * message, card and quiet line on its own.
 */
export function sideTimeline(
  items: readonly SideItem[],
  viewerId: string,
  now = Date.now()
): SideTimelineItem[] {
  return conversationTimeline<SideMessage, SideEntryItem>(
    items,
    viewerId,
    isSideMessage,
    now
  )
}

function knownAgent(agents: SideAgents, agentId: string): AgentRef | undefined {
  if (agents.mine?.id === agentId) return agents.mine
  return agents.theirs.find((agent) => agent.id === agentId)
}

/** What an agent is to the reader: "your agent", "Priya's agent", or someone's specialist. */
export function agentRole(
  agentId: string,
  ownerId: string,
  agents: SideAgents,
  reader: SideReader
): string {
  const noun =
    knownAgent(agents, agentId)?.kind === "specialist" ? "specialist" : "agent"
  return ownerId === reader.viewerId
    ? `your ${noun}`
    : `${reader.peer.short}'s ${noun}`
}

/** The agent a person's message asked, by name, when it asked one. */
export function askedAgentName(
  message: SideMessage,
  agents: SideAgents
): string | undefined {
  if (!message.asksAgentId) return undefined
  return knownAgent(agents, message.asksAgentId)?.name ?? "their agent"
}

/**
 * A request's result, in one quiet line: whom the agent worked for and who
 * approved it, or that it ran without asking (automatic delegation), and
 * who reviewed it when the answer guard held it for them first.
 */
export function viaLine(
  via: NonNullable<SideAgentMessage["via"]>,
  reader: SideReader
): string {
  const who = (userId: string) =>
    userId === reader.viewerId ? "you" : reader.peer.short
  const behalf =
    via.onBehalfOf === reader.viewerId
      ? "on your behalf"
      : `on behalf of ${reader.peer.short}`
  const approval =
    via.approvedBy === undefined
      ? "ran without asking"
      : `approved by ${who(via.approvedBy)}`
  const review =
    via.reviewedBy === undefined ? "" : ` · reviewed by ${who(via.reviewedBy)}`
  return `${behalf} · ${approval}${review}`
}

/** Each toolbox a request may ask for, as its owner reads what it opens. */
const toolboxWords: Record<AgentRequestToolbox, string> = {
  apps: "your connected apps",
  knowledge: "team knowledge",
}

/**
 * What approving a request lets its agent read, as its owner is asked: the
 * toolboxes among what it asked for ("Nia may read your connected apps and
 * team knowledge."), or nothing when it asked for none. Other names grant
 * nothing, so they are not said.
 */
export function toolsLine(
  agentName: string,
  requestedTools: readonly string[] | undefined
): string | undefined {
  const asked = new Set(requestedTools)
  const read = AGENT_REQUEST_TOOLBOXES.filter((toolbox) =>
    asked.has(toolbox)
  ).map((toolbox) => toolboxWords[toolbox])
  return read.length > 0
    ? `${capitalized(agentName)} may read ${listWords(read)}.`
    : undefined
}

/** How a card's status reads: its tone (for its icon), and its words. */
export type CardState = {
  tone: "waiting" | "working" | "done" | "ended" | "failed"
  text: string
}

/** Where a request stands, as its card says it to the reader. */
export function cardState(
  card: SideRequestCard,
  reader: SideReader
): CardState {
  const forMe = card.targetId === reader.viewerId
  const owner = forMe ? "you" : reader.peer.short
  const agent =
    card.targetAgentName || (forMe ? "your agent" : `${owner}'s agent`)
  const decidedBy = card.approval === "owner" ? owner : agent
  switch (card.status) {
    case "review":
      // Its owner is told why it waits for them; the one who asked only
      // that it does.
      return forMe
        ? { tone: "waiting", text: reviewHeadline(agent, card.guard?.kinds) }
        : {
            tone: "waiting",
            text: `Waiting for ${owner} to review the answer.`,
          }
    case "withheld":
      // Its quiet line says who chose, or that time ran out first.
      return card.review
        ? { tone: "ended", text: `Not shared by ${owner}.` }
        : { tone: "ended", text: "Expired." }
    case "sending":
      return { tone: "waiting", text: "Sending…" }
    case "not_filed":
      return {
        tone: "failed",
        text: card.error ? `Not filed. ${card.error}` : "Not filed.",
      }
    case "pending":
      if (card.approval === "agent") {
        return { tone: "waiting", text: `Waiting for ${agent} to start.` }
      }
      return { tone: "waiting", text: `Waiting for ${owner} to approve.` }
    case "approved":
      return card.approval === "owner" && card.decision?.decision === "approved"
        ? {
            tone: "working",
            text: `Approved by ${owner}. ${agent} is working on it…`,
          }
        : { tone: "working", text: `${agent} is working on it…` }
    case "completed":
      return { tone: "done", text: "Done." }
    case "rejected":
      return { tone: "ended", text: `Declined by ${decidedBy}.` }
    case "failed":
      return { tone: "failed", text: "Failed." }
    case "expired":
      return { tone: "ended", text: "Expired." }
  }
}

/** The kind the answer guard reports when it could not check an answer at all. */
const uncheckedKind = "UNCHECKED"

/**
 * The kinds the answer guard holds an answer for (Bedrock's entity types:
 * a held answer's kinds are these alone), in plain words, as its owner
 * reads why it waits for them. Any other kind reads as "other personal
 * details": its code is never shown.
 */
const kindWords = new Map<string, string>([
  ["EMAIL", "an email address"],
  ["PHONE", "a phone number"],
  ["ADDRESS", "a street address"],
])

/**
 * Why an answer waits for its owner's review, in plain words: "Nia's answer
 * includes an email address and a phone number. Review it before it's
 * shared." An answer the guard could not check says so instead.
 */
export function reviewHeadline(
  agentName: string,
  kinds: readonly string[] = []
): string {
  const unchecked = kinds.includes(uncheckedKind)
  const found = [
    ...new Set(
      kinds
        .filter((kind) => kind !== uncheckedKind)
        .map((kind) => kindWords.get(kind) ?? "other personal details")
    ),
  ]
  const why =
    found.length > 0
      ? `includes ${listWords(found)}${unchecked ? ", and couldn't be checked automatically" : ""}`
      : unchecked
        ? "couldn't be checked automatically"
        : undefined
  return why
    ? `${capitalized(agentName)}'s answer ${why}. Review it before it's shared.`
    : `Review ${agentName}'s answer before it's shared.`
}

/** Words as a sentence lists them: "a", "a and b", "a, b and c". */
function listWords(words: readonly string[]): string {
  if (words.length < 2) return words.join("")
  return `${words.slice(0, -1).join(", ")} and ${words.at(-1)}`
}

/** A name that starts a sentence: "your agent" is "Your agent" there. */
export function capitalized(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

/** Who approves a request to an agent, as a card and a menu say it. */
export function delegationWords(
  approval: AgentRef["approval"],
  ownerId: string,
  reader: SideReader
): string {
  if (approval === "agent") return "runs without asking"
  return `asks ${ownerId === reader.viewerId ? "you" : reader.peer.short} first`
}

/**
 * An agent's Markdown as it is said aloud: its words without the marks
 * (emphasis, headings, quotes, code), a link as its text, and every run of
 * whitespace one space.
 */
export function spokenMarkdown(markdown: string): string {
  return markdown
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/^\s{0,3}(?:#{1,6}|>|[-*+]|\d+[.)])\s+/gm, "")
    .replace(/[*_~`]+/g, "")
    .replace(/\s+/g, " ")
    .trim()
}

/** What something arriving says aloud, as news. */
export function sideNews(item: SideItem, reader: SideReader): string {
  const who = (userId: string) =>
    userId === reader.viewerId ? "You" : reader.peer.name
  switch (item.kind) {
    case "message":
      return `${who(item.authorId)}: ${item.body}`
    case "agent-message":
      if (item.status === "failed") return `${item.agentName} couldn't answer.`
      if (item.status === "waiting") {
        return `${item.agentName} is waiting for a confirmation.`
      }
      return item.body
        ? `${item.agentName}: ${spokenMarkdown(item.body)}`
        : `${item.agentName} is answering.`
    case "request-card":
      return `${who(item.requesterId)} asked ${item.targetAgentName}: ${item.task}`
    case "status":
      return item.text
  }
}

/** Whether an item is news to the reader: anything they did not say or ask themselves. */
export function isSideNews(item: SideItem, viewerId: string): boolean {
  if (item.kind === "message") return item.authorId !== viewerId
  if (item.kind === "request-card") return item.requesterId !== viewerId
  return true
}

/** Who a message goes to: the other person, the reader's own agent, or one of the other person's agents. */
export type SideTargetKey = "person" | "mine" | `agent:${string}`

export type SideTargetOption = {
  key: SideTargetKey
  /** In the menu: who it goes to. */
  label: string
  /** Under it: whose it is, and who approves. */
  detail: string
  /** On the pill beside the field. */
  name: string
  identity: AvatarIdentity
  /** The field's words while it is chosen, and its accessible name. */
  placeholder: string
  /** What the composer says under the field while it is chosen. */
  hint?: string
}

/**
 * Whom the reader can write to here: the other person (the default, a
 * plain message), their own agent, and each of the other person's agents
 * (their primary agent first, then the specialists they contributed).
 */
export function targetOptions(
  agents: SideAgents,
  reader: SideReader
): SideTargetOption[] {
  const { peer } = reader
  const options: SideTargetOption[] = [
    {
      key: "person",
      label: peer.short,
      detail: `A message to ${peer.short}`,
      name: peer.short,
      identity: personIdentity(peer.id, peer.name),
      placeholder: `Message ${peer.short}`,
    },
  ]
  const mine = agents.mine
  if (mine) {
    options.push({
      key: "mine",
      label: "Ask my agent",
      detail: `${mine.name} · ${peer.short} sees the answer too`,
      name: mine.name,
      identity: agentIdentity(mine.id, mine.name),
      placeholder: `Ask ${mine.name}, your agent`,
      hint: `${peer.short} sees ${mine.name}'s answer too.`,
    })
  }
  for (const agent of agents.theirs) {
    const specialist = agent.kind === "specialist"
    const automatic = agent.approval === "agent"
    options.push({
      key: `agent:${agent.id}`,
      label: specialist ? `Ask ${agent.name}` : `Ask ${peer.short}'s agent`,
      detail: `${specialist ? `${peer.short}'s specialist` : agent.name} · ${delegationWords(agent.approval, agent.ownerId, reader)}`,
      name: agent.name,
      identity: agentIdentity(agent.id, agent.name),
      placeholder: `Ask ${agent.name}, ${peer.short}'s ${specialist ? "specialist" : "agent"}`,
      hint: automatic
        ? `${agent.name} runs this without asking ${peer.short}.`
        : `${peer.short} approves this before ${agent.name} starts.`,
    })
  }
  return options
}

/** A teammate on a teamspace's team, with the agents a side conversation with them can ask. */
export type Teammate = {
  userId: string
  name: string
  agents: Array<{
    id: string
    name: string
    kind: "primary" | "specialist"
    /** Contributed with automatic delegation: it runs without asking its owner. */
    automatic: boolean
    role?: string
  }>
}

/**
 * The teammates a member can open a side conversation with in `space`:
 * everyone else with a seat (an active primary agent), by name, each with
 * their primary agent and the active specialists they contributed.
 */
export function teammatesOf(
  space: Teamspace,
  viewerId: string,
  nameOf: (userId: string) => string | undefined
): Teammate[] {
  return space.whisperers
    .filter((seat) => seat.user_id !== viewerId)
    .map((seat) => ({
      userId: seat.user_id,
      name:
        nameOf(seat.user_id) ?? (seat.user_display_name?.trim() || "Teammate"),
      agents: [
        {
          id: seat.agent_id,
          name: seat.agent_name,
          kind: "primary" as const,
          automatic: false,
        },
        ...space.contributions
          .filter(
            (contribution) =>
              contribution.owner_user_id === seat.user_id &&
              contribution.status === "active"
          )
          .map((contribution) => ({
            id: contribution.agent_id,
            name: contribution.name,
            kind: "specialist" as const,
            automatic: contribution.delegation_mode === "automatic",
            ...(contribution.role ? { role: contribution.role } : {}),
          })),
      ],
    }))
    .sort((left, right) => left.name.localeCompare(right.name))
}

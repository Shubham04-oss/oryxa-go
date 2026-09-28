// A side conversation (REALTIME.md, "Side conversations"): two teammates
// and both their agents, about work in one teamspace, away from its room.
// It opens in the agent panel, from a teamspace page's Agents tab or from
// the DMs tab's list, and reads as a DM does (DESIGN.md §3), with agents'
// messages, request cards and quiet lines between people's messages, and a
// composer that can also ask either person's agents. Each view opens its
// own socket on the conversation's one object (useSideConversation).
import { useEffect, useId, useMemo, useRef, useState } from "react"
import type { KeyboardEvent } from "react"
import {
  ArrowLeft,
  Ban,
  Check,
  ChevronDown,
  CircleAlert,
  CircleCheck,
  Clock3,
  LoaderCircle,
} from "lucide-react"
import { AgentComposer } from "./agent-composer"
import {
  AgentActivity,
  AgentMarkdown,
  ConfirmationCard,
} from "./agent-timeline"
import {
  ConversationNotice,
  LogAnnouncer,
  LogHistory,
  NewMessagesPill,
  conversationNotice,
  useConversationLog,
} from "./conversation-log"
import type { LogFailure, Notice } from "./conversation-log"
import {
  DaySeparator,
  MessageGroup,
  PersonAvatar,
  composerLength,
  latestConfirmed,
} from "./direct-message"
import { clockTime, seenMessageId } from "./direct-message-format"
import type { DmPeer } from "./direct-message-people"
import {
  agentRole,
  askedAgentName,
  capitalized,
  cardState,
  delegationWords,
  isSideMessage,
  isSideNews,
  sideNews,
  sideTimeline,
  targetOptions,
  toolsLine,
  viaLine,
} from "./side-conversation-format"
import type {
  CardState,
  SideAgents,
  SideReader,
  SideTargetKey,
  SideTargetOption,
} from "./side-conversation-format"
import { agentIdentity, fullTime } from "./teamspace-format"
import { IdentityAvatar } from "./workspace-avatar"
import {
  SIDE_MAX_BODY_LENGTH,
  SideActionError,
  useSideConversation,
} from "@/lib/side"
import type {
  AgentRef,
  SideAgentMessage,
  SideConversation,
  SideItem,
  SideMessage,
  SideRequestCard,
  SideReviewAction,
  SideStatusLine,
} from "@/lib/side"
import { codePoints } from "@/lib/dms/protocol"
import { SIDE_MAX_DECISION_MESSAGE } from "@/lib/side/protocol"

/**
 * What the member has written in a side conversation, and whom it goes to.
 * The panel keeps it, so it outlasts the view (another tab, the panel
 * collapsing).
 */
export type SideDraft = { text: string; target: SideTargetKey }

export const emptySideDraft: SideDraft = { text: "", target: "person" }

/** How many agents the header shows before "+N". */
const headerAgents = 3

/** From how many characters a card folds its task away behind "Show more". */
const longTask = 280

type ViewProps = {
  organizationId: string
  viewerId: string
  teamspaceId: string
  peer: DmPeer
  /** What the teamspace is called until the conversation says: the page's or the inbox's name for it. */
  teamspaceName?: string
  draft: SideDraft
  onDraftChange: (draft: SideDraft) => void
  onComposingChange?: (composing: boolean) => void
  /** Moves focus into the conversation as it opens, when the member chose to open it. */
  autoFocus?: boolean
  /** On screen now: only then does reaching the end mark it read. */
  active?: boolean
  /** The way back, in words: to the teammates or to the conversations. */
  backLabel: string
  onBack: () => void
}

/** A side conversation with `peer` in a teamspace, over its own socket for as long as it is mounted. */
export function SideConversationView({
  organizationId,
  teamspaceId,
  ...props
}: ViewProps) {
  const side = useSideConversation(
    organizationId,
    props.viewerId,
    teamspaceId,
    props.peer.id
  )
  return <SideConversationPanel side={side} {...props} />
}

/** The key a view keeps for an item across its confirmation: the page's id while it has one. */
function itemKey(item: SideItem): string {
  return item.kind === "message" || item.kind === "request-card"
    ? (item.clientId ?? item.id)
    : item.id
}

/**
 * The conversation itself, from what `side` (useSideConversation) holds:
 * the compact bar that names the teammate, the teamspace and the agents
 * here; the log; and the composer with whom a message goes to.
 */
export function SideConversationPanel({
  side,
  viewerId,
  peer,
  teamspaceName: knownName,
  draft,
  onDraftChange,
  onComposingChange,
  autoFocus = false,
  active = true,
  backLabel,
  onBack,
}: Omit<ViewProps, "organizationId" | "teamspaceId"> & {
  side: SideConversation
}) {
  const { items, status, agents } = side
  const reader: SideReader = useMemo(
    () => ({
      viewerId,
      peer: { id: peer.id, name: peer.name, short: peer.short },
    }),
    [viewerId, peer.id, peer.name, peer.short]
  )
  const teamspaceName = side.teamspace.name || knownName || "this teamspace"

  // Nothing to show or send: the conversation refused the viewer (one of
  // them is off the team, or the teamspace is gone), or there is none to
  // open. A paused teamspace's conversation stays readable.
  const refused = status === "not-allowed" || status === "closed"
  const sessionEnded = status === "session_expired"
  const paused = side.teamspace.paused
  const readOnly = refused || sessionEnded || paused
  const shown = useMemo(() => (refused ? [] : items), [items, refused])
  const timeline = useMemo(
    () => sideTimeline(shown, viewerId),
    [shown, viewerId]
  )
  const seenId = useMemo(
    () =>
      seenMessageId(
        shown.filter(isSideMessage),
        viewerId,
        side.peer.lastReadSeq
      ),
    [shown, viewerId, side.peer.lastReadSeq]
  )

  const log = useConversationLog<SideItem>({
    keys: shown.map(itemKey),
    items,
    latestSeq: latestConfirmed(items),
    seqOf: (item) => item.seq,
    isNews: (item) => isSideNews(item, viewerId),
    sayArrived: (arrived) =>
      arrived.length === 1
        ? spoken(sideNews(arrived[0], reader))
        : `${arrived.length} new messages in your conversation with ${peer.name}`,
    sayBelow: (total) =>
      `${total} new ${total === 1 ? "message" : "messages"} below`,
    failed: failures(items, agents, peer),
    live: status === "live",
    hasOlder: side.hasOlder,
    loadOlder: side.loadOlder,
    active,
    markRead: side.markRead,
    autoFocus,
    onComposingChange,
  })
  const { announce } = log

  // Items also change in place: an agent's answer as its run ends or
  // waits, a card as its request is approved. Those are said aloud here; a
  // new item is said as news (above), and a result, a decline, a failure or
  // an expiry each arrive as an item of their own.
  const statusesBefore = useRef<ReadonlyMap<string, string>>(undefined)
  useEffect(() => {
    if (status !== "live") return
    const statuses = new Map<string, string>()
    for (const item of items) {
      if (item.kind === "agent-message" || item.kind === "request-card") {
        statuses.set(item.id, item.status)
      }
    }
    const before = statusesBefore.current
    statusesBefore.current = statuses
    if (!before) return
    for (const item of items) {
      if (item.kind !== "agent-message" && item.kind !== "request-card") {
        continue
      }
      const was = before.get(item.id)
      if (was === undefined || was === item.status) continue
      if (item.kind === "agent-message" && item.status !== "streaming") {
        announce(spoken(sideNews(item, reader)))
      } else if (
        item.kind === "request-card" &&
        (item.status === "approved" || item.status === "review")
      ) {
        announce(cardState(item, reader).text)
      }
    }
  }, [announce, items, reader, status])

  const options = useMemo(() => targetOptions(agents, reader), [agents, reader])
  const chosen = options.find((option) => option.key === draft.target)
  // Opened on one of their agents before the conversation said who is
  // here: the choice holds until it does.
  const waitingForRoster =
    !chosen && draft.target !== "person" && status !== "live"
  const target = waitingForRoster ? undefined : (chosen ?? options[0])
  const length = composerLength(draft.text, SIDE_MAX_BODY_LENGTH)
  // The member's own agent answers one question here at a time: while it
  // still answers, or waits on their confirmation, asking it again waits.
  const ownRun =
    target?.key === "mine"
      ? items.find(
          (item): item is SideAgentMessage =>
            item.kind === "agent-message" &&
            item.ownerId === viewerId &&
            (item.status === "streaming" || item.status === "waiting")
        )
      : undefined
  const busy = ownRun
    ? ownRun.status === "waiting"
      ? `Answer ${ownRun.agentName}'s confirmation above first.`
      : `${ownRun.agentName} is still answering. Ask again once it has.`
    : undefined

  function send(text: string) {
    if (!target || busy || codePoints(text) > SIDE_MAX_BODY_LENGTH) return
    log.follow()
    if (target.key === "person") side.send(text)
    else if (target.key === "mine") side.askMyAgent(text)
    else side.askTeammateAgent(target.key.slice("agent:".length), text)
    // Asking an agent is for one message: the next goes to the person again.
    onDraftChange(emptySideDraft)
  }

  function focusComposer() {
    log.composerRef.current?.querySelector("textarea")?.focus()
  }

  const notice = conversationNotice(status, side.reconnect)
  const empty = shown.length === 0
  const composerStatus = readOnly
    ? paused && !refused && !sessionEnded
      ? `${teamspaceName} is paused, so this conversation is read-only until a team admin resumes it.`
      : undefined
    : (length.status ??
      busy ??
      (waitingForRoster ? "Connecting…" : target?.hint))
  // The field is named for whom it writes to; its placeholder says when it
  // cannot.
  const label = target?.placeholder ?? `Message ${peer.short}`
  const placeholder = refused
    ? `You can't message ${peer.short} here`
    : sessionEnded
      ? "Sign in again to send messages"
      : paused
        ? `${teamspaceName} is paused`
        : (target?.placeholder ?? `Message ${peer.short}`)

  return (
    <section
      className="workspace-dm workspace-side"
      data-layout="panel"
      aria-label={`Side conversation with ${peer.name} in ${teamspaceName}`}
    >
      <div className="workspace-dm-bar workspace-side-bar">
        <button
          className="workspace-agent-new"
          type="button"
          aria-label={backLabel}
          onClick={onBack}
        >
          <ArrowLeft size={15} strokeWidth={1.7} />
        </button>
        <PersonAvatar id={peer.id} name={peer.name} online={side.peer.online} />
        <div className="workspace-dm-bar-name">
          <h2>{peer.name}</h2>
          <small>
            {[
              side.teamspace.name || knownName,
              side.peer.online ? "Online" : undefined,
            ]
              .filter(Boolean)
              .join(" · ")}
          </small>
        </div>
        <AgentsHere agents={agents} reader={reader} />
      </div>
      <div className="workspace-dm-log-wrap">
        <div
          className="workspace-dm-log workspace-side-log"
          role="log"
          // News is said through the status below, so the page before,
          // arriving above, is not read out as news.
          aria-live="off"
          aria-label={`Side conversation with ${peer.name}`}
          tabIndex={0}
          ref={log.logRef}
          onScroll={log.onScroll}
        >
          {!empty && status === "live" && (
            <LogHistory
              hasOlder={side.hasOlder}
              loadingOlder={log.loadingOlder}
              onLoadOlder={log.loadOlder}
              start={`This is the start of your side conversation with ${peer.name} in ${teamspaceName}.`}
            />
          )}
          {timeline.map((entry) => {
            if (entry.kind === "day") {
              return <DaySeparator key={entry.key} label={entry.label} />
            }
            if (entry.kind === "group") {
              return (
                <MessageGroup
                  key={entry.key}
                  group={entry}
                  peer={peer}
                  seenId={seenId}
                  canRetry={!readOnly}
                  onRetry={side.retry}
                  aside={(message: SideMessage) => {
                    const asked = askedAgentName(message, agents)
                    return asked ? (
                      <p className="workspace-side-asked">Asked {asked}</p>
                    ) : null
                  }}
                  failure={(message: SideMessage) =>
                    message.error ? `Not sent. ${message.error}` : "Not sent."
                  }
                />
              )
            }
            const item = entry.item
            switch (item.kind) {
              case "agent-message":
                return (
                  <AgentMessage
                    key={entry.key}
                    item={item}
                    reader={reader}
                    agents={agents}
                    onConfirm={side.confirm}
                  />
                )
              case "request-card":
                return (
                  <RequestCard
                    key={entry.key}
                    card={item}
                    reader={reader}
                    agents={agents}
                    canAct={!readOnly}
                    onDecide={side.decide}
                    onReview={side.review}
                    onRetry={side.retry}
                  />
                )
              case "status":
                return <StatusLine key={entry.key} line={item} />
            }
          })}
          {empty && (
            <EmptySide
              status={status}
              peer={peer}
              teamspaceName={teamspaceName}
              paused={paused}
              notice={notice}
            />
          )}
          {!refused && side.peer.typing && (
            <p className="workspace-dm-typing">
              {capitalized(peer.short)} is typing…
            </p>
          )}
          {!empty && notice && <ConversationNotice notice={notice} />}
        </div>
        {log.unseen > 0 && !log.atEnd && (
          <NewMessagesPill count={log.unseen} onClick={log.toEnd} />
        )}
      </div>
      <LogAnnouncer announcement={log.announcement} />
      <div ref={log.composerRef}>
        <AgentComposer
          label={label}
          placeholder={placeholder}
          value={draft.text}
          onChange={(text) => {
            onDraftChange({ ...draft, text })
            side.setTyping(text.trim() !== "")
          }}
          onSubmit={send}
          disabled={readOnly}
          sendDisabled={length.tooLong || !target || busy !== undefined}
          status={composerStatus}
          leading={
            <TargetPicker
              options={options}
              selected={target}
              disabled={readOnly}
              onSelect={(key) => onDraftChange({ ...draft, target: key })}
              onChosen={focusComposer}
            />
          }
          onFocus={() => onComposingChange?.(true)}
          onBlur={() => {
            onComposingChange?.(false)
            side.setTyping(false)
          }}
        />
      </div>
    </section>
  )
}

/** The longest an answer is said aloud: the rest is in the log. */
const spokenLength = 280

function spoken(text: string): string {
  const characters = Array.from(text.replace(/\s+/g, " ").trim())
  return characters.length <= spokenLength
    ? characters.join("")
    : `${characters.slice(0, spokenLength - 1).join("")}…`
}

/** What of the member's failed to send, and how each is said aloud as it fails. */
function failures(
  items: readonly SideItem[],
  agents: SideAgents,
  peer: DmPeer
): LogFailure[] {
  return items.flatMap((item): LogFailure[] => {
    if (item.kind === "message" && item.status === "failed") {
      const asked = askedAgentName(item, agents)
      return [
        {
          key: itemKey(item),
          text: asked
            ? `Your question to ${asked} wasn't sent.`
            : `Your message to ${peer.name} wasn't sent.`,
        },
      ]
    }
    if (item.kind === "request-card" && item.status === "not_filed") {
      return [
        {
          key: itemKey(item),
          text: `Your request to ${item.targetAgentName || "their agent"} wasn't filed.`,
        },
      ]
    }
    return []
  })
}

/**
 * The agents in the conversation, in the bar: both people's own agents
 * first (the other person's, then the member's), then the specialists the
 * other person contributed.
 */
function AgentsHere({
  agents,
  reader,
}: {
  agents: SideAgents
  reader: SideReader
}) {
  const all: AgentRef[] = [
    ...agents.theirs.filter((agent) => agent.kind === "primary"),
    ...(agents.mine ? [agents.mine] : []),
    ...agents.theirs.filter((agent) => agent.kind !== "primary"),
  ]
  if (all.length === 0) return null
  const more = all.length - headerAgents
  return (
    <ul className="workspace-side-agents" aria-label="Agents here">
      {all.slice(0, headerAgents).map((agent) => {
        const role = agentRole(agent.id, agent.ownerId, agents, reader)
        return (
          <li key={agent.id} title={`${agent.name} · ${role}`}>
            <span className="workspace-side-agents-avatar" aria-hidden="true">
              <IdentityAvatar identity={agentIdentity(agent.id, agent.name)} />
            </span>
            <span className="sr-only">
              {agent.name}, {role}
            </span>
          </li>
        )
      })}
      {more > 0 && (
        <li className="workspace-side-agents-more">
          +{more}
          <span className="sr-only"> more</span>
        </li>
      )}
    </ul>
  )
}

/**
 * An agent's message: its avatar, name and whose it is, its Markdown (a
 * working line while it has no words yet), what it waits on, and, for a
 * request's result, the quiet line that says for whom and approved by whom.
 */
function AgentMessage({
  item,
  reader,
  agents,
  onConfirm,
}: {
  item: SideAgentMessage
  reader: SideReader
  agents: SideAgents
  onConfirm: SideConversation["confirm"]
}) {
  const role = agentRole(item.agentId, item.ownerId, agents, reader)
  const mine = item.ownerId === reader.viewerId
  return (
    <article
      className="workspace-side-agent"
      data-status={item.status}
      aria-label={`${item.agentName}, ${role}`}
      aria-busy={item.status === "streaming" ? "true" : undefined}
    >
      <IdentityAvatar identity={agentIdentity(item.agentId, item.agentName)} />
      <div className="workspace-side-agent-body">
        <p className="workspace-side-agent-meta">
          <strong>{item.agentName}</strong>
          <span className="workspace-side-agent-role"> · {role}</span>
          <time dateTime={item.createdAt} title={fullTime(item.createdAt)}>
            {clockTime(item.createdAt)}
          </time>
        </p>
        {item.body ? (
          <AgentMarkdown text={item.body} />
        ) : item.status === "streaming" ? (
          <AgentActivity name={item.agentName} />
        ) : item.status === "done" ? (
          <p className="workspace-agent-note">It finished without an answer.</p>
        ) : null}
        {item.status === "waiting" &&
          (mine && item.confirmations?.length ? (
            <SideConfirmations item={item} onConfirm={onConfirm} />
          ) : (
            <p className="workspace-agent-note">
              {item.agentName} is waiting for {mine ? "you" : reader.peer.short}{" "}
              to confirm a step.
            </p>
          ))}
        {item.status === "failed" && (
          <p className="workspace-agent-error">
            {item.error || `${item.agentName} couldn't answer.`}
          </p>
        )}
        {item.via && (
          <p className="workspace-side-via">{viaLine(item.via, reader)}</p>
        )}
      </div>
    </article>
  )
}

/**
 * What the member's own agent waits on, shown to them alone (the object
 * sends it to its owner's sockets only): the chat's confirmation cards,
 * answered together as Go takes them.
 */
function SideConfirmations({
  item,
  onConfirm,
}: {
  item: SideAgentMessage
  onConfirm: SideConversation["confirm"]
}) {
  const confirmations = item.confirmations ?? []
  const [choices, setChoices] = useState<Record<string, boolean>>({})
  const [answering, setAnswering] = useState(false)
  const [error, setError] = useState("")
  const multiple = confirmations.length > 1

  function answer(answers: Array<{ id: string; confirmed: boolean }>) {
    setAnswering(true)
    setError("")
    onConfirm(item.seq, answers).then(
      () => setAnswering(false),
      (failure: unknown) => {
        setAnswering(false)
        setError(
          failure instanceof SideActionError
            ? failure.message
            : "Your answer wasn't sent. Try again."
        )
      }
    )
  }

  return (
    <div className="workspace-side-confirmations">
      {confirmations.map((confirmation) => (
        <ConfirmationCard
          key={confirmation.callId}
          entry={{
            kind: "confirmation",
            key: confirmation.callId,
            callId: confirmation.callId,
            tool: confirmation.tool,
            ...(confirmation.arguments
              ? { arguments: confirmation.arguments }
              : {}),
            ...(confirmation.hint ? { hint: confirmation.hint } : {}),
            status: "pending",
          }}
          actionable
          multiple={multiple}
          choice={choices[confirmation.callId]}
          disabled={answering}
          onChoose={(confirmed) =>
            setChoices((current) => ({
              ...current,
              [confirmation.callId]: confirmed,
            }))
          }
          onAnswer={(confirmed) =>
            answer([{ id: confirmation.callId, confirmed }])
          }
        />
      ))}
      {multiple && (
        <div className="workspace-agent-answers">
          <button
            className="workspace-agent-pill"
            type="button"
            data-primary="true"
            disabled={
              answering ||
              confirmations.some(
                (confirmation) => !(confirmation.callId in choices)
              )
            }
            onClick={() =>
              answer(
                confirmations.map((confirmation) => ({
                  id: confirmation.callId,
                  confirmed: choices[confirmation.callId] ?? false,
                }))
              )
            }
          >
            Send answers
          </button>
        </div>
      )}
      {error && (
        <p className="workspace-agent-error" role="alert">
          {error}
        </p>
      )}
    </div>
  )
}

function CardIcon({ tone }: { tone: CardState["tone"] }) {
  const props = { size: 13, strokeWidth: 1.9, "aria-hidden": true }
  switch (tone) {
    case "waiting":
      return <Clock3 {...props} />
    case "working":
      return <LoaderCircle {...props} className="workspace-agent-spin" />
    case "done":
      return <CircleCheck {...props} />
    case "ended":
      return <Ban {...props} />
    case "failed":
      return <CircleAlert {...props} />
  }
}

/** Which of a card's buttons its owner pressed, while what it sends is on its way. */
type CardPress = "approve" | "approve-bare" | "decline" | SideReviewAction

/**
 * A request to one of the two people's agents: who asked which agent what,
 * where it stands, and, for the agent's owner while it waits on them,
 * what approving lets it read, and Approve (with those tools), Approve
 * without tools and Decline, with an optional note. An answer the answer
 * guard holds for its owner shows them why, the answer, and Send, Send
 * masked and Don't send.
 */
function RequestCard({
  card,
  reader,
  agents,
  canAct,
  onDecide,
  onReview,
  onRetry,
}: {
  card: SideRequestCard
  reader: SideReader
  agents: SideAgents
  /** The conversation takes decisions, reviews and retries now (not read-only). */
  canAct: boolean
  onDecide: SideConversation["decide"]
  onReview: SideConversation["review"]
  onRetry: (clientId: string) => void
}) {
  const [note, setNote] = useState("")
  const [noting, setNoting] = useState(false)
  const [pressed, setPressed] = useState<CardPress>()
  const [expanded, setExpanded] = useState(false)
  const cardRef = useRef<HTMLElement>(null)
  const noteRef = useRef<HTMLInputElement>(null)
  const noteId = useId()
  const mineAsked = card.requesterId === reader.viewerId
  const agent =
    card.targetAgentName ||
    (card.targetId === reader.viewerId
      ? "your agent"
      : `${reader.peer.short}'s agent`)
  const role = agentRole(card.targetAgentId, card.targetId, agents, reader)
  const state = cardState(card, reader)
  const forMe = card.targetId === reader.viewerId
  // Only its owner decides a request held for them, from their own view,
  // and is told what approving it lets their agent read.
  const heldForMe =
    forMe && card.approval === "owner" && card.status === "pending"
  const decidable = canAct && heldForMe && card.requestId !== ""
  const tools = heldForMe ? toolsLine(agent, card.requestedTools) : undefined
  // Only its owner reviews an answer held for them.
  const inReview = forMe && card.status === "review"
  const long =
    codePoints(card.task) > longTask || card.task.split("\n").length > 6
  const requesterName = mineAsked ? "you" : reader.peer.name

  useEffect(() => {
    if (noting) noteRef.current?.focus()
  }, [noting])

  function settled(sent: Promise<void>) {
    sent.then(
      () => {
        setPressed(undefined)
        // The buttons go with what was sent; focus stays with the card.
        cardRef.current?.focus()
      },
      () => {
        // The card says why (`decisionError`).
        setPressed(undefined)
      }
    )
  }

  function decide(press: "approve" | "approve-bare" | "decline") {
    setPressed(press)
    settled(
      press === "decline"
        ? onDecide(card.requestId, "rejected", note)
        : // Approve allows what the request asked for; without tools, none.
          onDecide(
            card.requestId,
            "approved",
            note,
            press === "approve" ? (card.requestedTools ?? []) : []
          )
    )
  }

  function review(action: SideReviewAction) {
    setPressed(action)
    settled(onReview(card.requestId, action))
  }

  return (
    <article
      className="workspace-side-card"
      data-tone={state.tone}
      aria-label={`${mineAsked ? "You" : reader.peer.name} asked ${agent}, ${role}`}
      aria-busy={card.deciding ? "true" : undefined}
      tabIndex={-1}
      ref={cardRef}
    >
      <div className="workspace-side-card-head">
        <IdentityAvatar identity={agentIdentity(card.targetAgentId, agent)} />
        <p className="workspace-side-card-title">
          <strong>
            {mineAsked ? "You" : reader.peer.short} asked {agent}
          </strong>
          <span>
            {role} · {delegationWords(card.approval, card.targetId, reader)}
          </span>
        </p>
        <time dateTime={card.createdAt} title={fullTime(card.createdAt)}>
          {clockTime(card.createdAt)}
        </time>
      </div>
      <p
        className="workspace-side-card-task"
        data-folded={long && !expanded ? "true" : undefined}
      >
        {card.task}
      </p>
      {long && (
        <button
          className="workspace-agent-text-button"
          type="button"
          aria-expanded={expanded}
          onClick={() => setExpanded((open) => !open)}
        >
          {expanded ? "Show less" : "Show more"}
        </button>
      )}
      <p className="workspace-side-card-status">
        <CardIcon tone={state.tone} />
        <span>{state.text}</span>
        {card.status === "not_filed" && card.clientId && canAct && (
          <>
            {" "}
            <button
              className="workspace-dm-retry"
              type="button"
              aria-label={`Ask ${agent} again`}
              onClick={() => onRetry(card.clientId ?? "")}
            >
              Retry
            </button>
          </>
        )}
      </p>
      {tools && <p className="workspace-side-card-tools">{tools}</p>}
      {inReview && (
        <HeldAnswer
          card={card}
          agent={agent}
          requesterName={requesterName}
          canAct={canAct}
          pressed={pressed}
          onReview={review}
        />
      )}
      {decidable && (
        <div className="workspace-side-card-decide">
          {noting ? (
            <label className="workspace-side-card-note" htmlFor={noteId}>
              <span className="sr-only">
                A note for {reader.peer.name} (optional)
              </span>
              <input
                id={noteId}
                ref={noteRef}
                type="text"
                value={note}
                maxLength={SIDE_MAX_DECISION_MESSAGE}
                placeholder={`A note for ${reader.peer.short} (optional)`}
                disabled={card.deciding}
                onChange={(event) => setNote(event.target.value)}
              />
            </label>
          ) : (
            <button
              className="workspace-agent-text-button"
              type="button"
              onClick={() => setNoting(true)}
            >
              Add a note
            </button>
          )}
          <div className="workspace-agent-confirmation-actions">
            <button
              className="workspace-agent-pill"
              type="button"
              aria-label={`Decline ${agent}'s request from ${requesterName}`}
              disabled={card.deciding}
              onClick={() => decide("decline")}
            >
              {card.deciding && pressed === "decline"
                ? "Declining…"
                : "Decline"}
            </button>
            {tools && (
              <button
                className="workspace-agent-pill"
                type="button"
                aria-label={`Approve ${agent}'s request from ${requesterName} without tools`}
                disabled={card.deciding}
                onClick={() => decide("approve-bare")}
              >
                {card.deciding && pressed === "approve-bare"
                  ? "Approving…"
                  : "Approve without tools"}
              </button>
            )}
            <button
              className="workspace-agent-pill"
              type="button"
              data-primary="true"
              aria-label={`Approve ${agent}'s request from ${requesterName}`}
              disabled={card.deciding}
              onClick={() => decide("approve")}
            >
              {card.deciding && pressed === "approve"
                ? "Approving…"
                : "Approve"}
            </button>
          </div>
        </div>
      )}
      {card.decisionError && (
        <p className="workspace-agent-error" role="alert">
          {card.decisionError}
        </p>
      )}
    </article>
  )
}

/**
 * An answer the answer guard holds for its owner, on their card: the
 * answer as Send shares it (said to be its agent's failure when it was
 * one), a preview of what Send masked shares, and the choices. Send and
 * Send masked wait for the answer: nobody shares what they have not seen.
 * Don't send never does. An answer the guard could not check has no masked
 * version, so no Send masked.
 */
function HeldAnswer({
  card,
  agent,
  requesterName,
  canAct,
  pressed,
  onReview,
}: {
  card: SideRequestCard
  agent: string
  requesterName: string
  canAct: boolean
  /** The choice on its way, if any. */
  pressed: CardPress | undefined
  onReview: (action: SideReviewAction) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const held = card.heldAnswer
  const long =
    held !== undefined &&
    (codePoints(held.stored) > longTask || held.stored.split("\n").length > 6)
  const folded = long && !expanded
  const busy = card.deciding === true
  const to = `${agent}'s answer to ${requesterName}`
  return (
    <div className="workspace-side-card-held">
      {held ? (
        <>
          {held.failed && (
            <p className="workspace-side-card-masked-label">
              {capitalized(agent)} couldn't finish the request, and says:
            </p>
          )}
          <div
            className="workspace-side-card-answer"
            data-folded={folded ? "true" : undefined}
          >
            <AgentMarkdown text={held.stored} />
          </div>
          {held.masked !== undefined && (
            <>
              <p className="workspace-side-card-masked-label">
                Send masked shares:
              </p>
              <div
                className="workspace-side-card-answer"
                data-masked="true"
                data-folded={folded ? "true" : undefined}
              >
                <AgentMarkdown text={held.masked} />
              </div>
            </>
          )}
          {long && (
            <button
              className="workspace-agent-text-button"
              type="button"
              aria-expanded={expanded}
              onClick={() => setExpanded((open) => !open)}
            >
              {expanded ? "Show less" : "Show more"}
            </button>
          )}
        </>
      ) : (
        <p className="workspace-agent-note" role="status">
          Loading the answer…
        </p>
      )}
      {canAct && card.requestId !== "" && (
        <div className="workspace-agent-confirmation-actions">
          <button
            className="workspace-agent-pill"
            type="button"
            aria-label={`Don't send ${to}`}
            disabled={busy}
            onClick={() => onReview("withhold")}
          >
            {busy && pressed === "withhold" ? "Withholding…" : "Don't send"}
          </button>
          {held && (
            <>
              <button
                className="workspace-agent-pill"
                type="button"
                aria-label={`Send ${to}`}
                disabled={busy}
                onClick={() => onReview("send")}
              >
                {busy && pressed === "send" ? "Sending…" : "Send"}
              </button>
              {held.masked !== undefined && (
                <button
                  className="workspace-agent-pill"
                  type="button"
                  data-primary="true"
                  aria-label={`Send ${to}, masked`}
                  disabled={busy}
                  onClick={() => onReview("send_masked")}
                >
                  {busy && pressed === "send_masked"
                    ? "Sending…"
                    : "Send masked"}
                </button>
              )}
            </>
          )}
        </div>
      )}
    </div>
  )
}

/** A request that ended without a result, in the object's words: quiet, centered. */
function StatusLine({ line }: { line: SideStatusLine }) {
  return (
    <p className="workspace-side-status" data-outcome={line.outcome}>
      {line.text}{" "}
      <time dateTime={line.createdAt} title={fullTime(line.createdAt)}>
        {clockTime(line.createdAt)}
      </time>
    </p>
  )
}

/** The log with nothing in it: why, or an invitation to start. */
function EmptySide({
  status,
  peer,
  teamspaceName,
  paused,
  notice,
}: {
  status: SideConversation["status"]
  peer: DmPeer
  teamspaceName: string
  paused: boolean
  notice?: Notice
}) {
  if (status === "not-allowed") {
    return (
      <div className="workspace-dm-empty" role="status">
        <strong>You can't open this conversation</strong>
        <p>
          {capitalized(peer.short)} may no longer be on {teamspaceName}'s team,
          or the teamspace is gone.
        </p>
      </div>
    )
  }
  if (status === "closed") {
    return (
      <div className="workspace-dm-empty" role="status">
        <p>This conversation isn't available.</p>
      </div>
    )
  }
  if (notice) {
    return (
      <div className="workspace-dm-empty">
        <ConversationNotice notice={notice} />
      </div>
    )
  }
  if (status === "connecting") {
    return (
      <div className="workspace-dm-empty">
        <p role="status">Loading the conversation…</p>
      </div>
    )
  }
  return (
    <div className="workspace-dm-empty">
      <PersonAvatar id={peer.id} name={peer.name} />
      <strong>
        Talk with {peer.short} about {teamspaceName}
      </strong>
      <p>
        {paused
          ? `${teamspaceName} is paused, so nothing can be said here until it's resumed.`
          : "Your agents can help. It stays between the two of you, away from the room."}
      </p>
    </div>
  )
}

/**
 * Who a message goes to: a pill inside the composer with that one's avatar
 * and name, opening a small menu of the choices: the other person (a plain
 * message), the member's own agent, and each of the other person's agents.
 */
function TargetPicker({
  options,
  selected,
  disabled,
  onSelect,
  onChosen,
}: {
  options: SideTargetOption[]
  /** Nothing while the choice waits for the conversation to say who is here. */
  selected: SideTargetOption | undefined
  disabled: boolean
  onSelect: (key: SideTargetKey) => void
  /** A choice was made: the composer takes focus back. */
  onChosen: () => void
}) {
  const [opened, setOpen] = useState(false)
  const buttonRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const menuId = useId()
  const openable = !disabled && selected !== undefined && options.length > 1
  // A conversation that turns read-only (paused, refused) closes the menu.
  const open = opened && openable

  // The menu takes focus on its checked choice, and closes on a press
  // anywhere else.
  useEffect(() => {
    if (!open) return
    const choices = menuItems(menuRef.current)
    ;(
      choices.find((item) => item.getAttribute("aria-checked") === "true") ??
      choices.at(0)
    )?.focus()
    function onPointerDown(event: PointerEvent) {
      const target = event.target as Node | null
      if (
        target &&
        !menuRef.current?.contains(target) &&
        !buttonRef.current?.contains(target)
      ) {
        setOpen(false)
      }
    }
    document.addEventListener("pointerdown", onPointerDown)
    return () => document.removeEventListener("pointerdown", onPointerDown)
  }, [open])

  function close(returnFocus: boolean) {
    setOpen(false)
    if (returnFocus) buttonRef.current?.focus()
  }

  function onMenuKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const choices = menuItems(menuRef.current)
    const at = choices.indexOf(document.activeElement as HTMLButtonElement)
    const last = choices.length - 1
    const moves: Partial<Record<string, number>> = {
      ArrowDown: at >= last ? 0 : at + 1,
      ArrowUp: at <= 0 ? last : at - 1,
      Home: 0,
      End: last,
    }
    if (event.key === "Escape") {
      event.preventDefault()
      close(true)
      return
    }
    if (event.key === "Tab") {
      close(false)
      return
    }
    const next = moves[event.key]
    if (next === undefined) return
    event.preventDefault()
    choices.at(next)?.focus()
  }

  return (
    <div className="workspace-side-target">
      <button
        ref={buttonRef}
        className="workspace-side-target-button"
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={`Who this goes to: ${selected?.name ?? "loading"}`}
        title={selected ? `${selected.label}: ${selected.detail}` : undefined}
        disabled={!openable}
        onClick={() => setOpen((current) => !current)}
        onKeyDown={(event) => {
          if (event.key === "ArrowUp" || event.key === "ArrowDown") {
            event.preventDefault()
            if (openable) setOpen(true)
          }
        }}
      >
        {selected ? (
          <IdentityAvatar identity={selected.identity} />
        ) : (
          <span className="workspace-side-target-waiting" aria-hidden="true" />
        )}
        <span className="workspace-side-target-name">
          {selected?.name ?? "…"}
        </span>
        {options.length > 1 && (
          <ChevronDown size={13} strokeWidth={1.9} aria-hidden="true" />
        )}
      </button>
      {open && (
        <div
          ref={menuRef}
          id={menuId}
          className="workspace-side-target-menu"
          role="menu"
          aria-label="Who this goes to"
          onKeyDown={onMenuKeyDown}
        >
          {options.map((option) => {
            const checked = option.key === selected.key
            return (
              <button
                key={option.key}
                className="workspace-side-target-option"
                type="button"
                role="menuitemradio"
                aria-checked={checked}
                tabIndex={-1}
                onClick={() => {
                  onSelect(option.key)
                  close(false)
                  onChosen()
                }}
              >
                <IdentityAvatar identity={option.identity} />
                <span className="workspace-side-target-copy">
                  <strong>{option.label}</strong>
                  <small>{option.detail}</small>
                </span>
                {checked && (
                  <Check size={14} strokeWidth={1.9} aria-hidden="true" />
                )}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

function menuItems(menu: HTMLElement | null): HTMLButtonElement[] {
  return Array.from(
    menu?.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]') ?? []
  )
}

import { Suspense, lazy, useEffect, useRef, useState } from "react"
import { AgentComposer } from "./agent-composer"
import { IdentityAvatar } from "./workspace-avatar"
import { useReportRoomActivity } from "./call-provider"
import {
  agentIdentity,
  fullTime,
  messageTime,
  pausedLine,
  personIdentity,
} from "./teamspace-format"
import type {
  RoomMessage,
  RoomSeat,
  RoomStatus,
  TeamspaceRoom,
} from "@/hooks/use-teamspace-room"
import { useMarkTeamspaceRead } from "@/hooks/use-teamspace-read"
import type { Teamspace } from "@/lib/teamspaces/client"

/** How many seats the presence row shows before "+N". */
const visibleSeats = 8

/**
 * The teamspace's room: the team's people and their agents in one
 * conversation. Everything it shows and sends goes through `room` (the
 * useTeamspaceRoom seam), never through a transport directly.
 */
export function TeamspaceConversation({
  space,
  room,
  viewerId,
  onComposingChange,
}: {
  space: Teamspace
  room: TeamspaceRoom
  viewerId?: string
  onComposingChange: (composing: boolean) => void
}) {
  const [draft, setDraft] = useState("")
  const listRef = useRef<HTMLDivElement>(null)
  const composerRef = useRef<HTMLDivElement>(null)
  // Follows the conversation while the reader is at its end, and stops the
  // moment they scroll back to read something.
  const following = useRef(true)
  const { messages, presence, status } = room
  // The parley's Room button counts what others said while it was closed.
  useReportRoomActivity(
    space.organization_id,
    { kind: "teamspace", id: space.id },
    messages
      .filter(
        (message) =>
          message.author_user_id !== viewerId || message.author_agent_id
      )
      .map((message) => message.seq)
  )
  const lastId = messages.at(-1)?.id
  const lastSeq = messages.at(-1)?.seq
  const outgoingCount = status.outgoing.length
  // The newest message the member has had on screen in this room: the room
  // is marked read through it (useMarkTeamspaceRead), not through what
  // arrived while they had scrolled back or were on another tab.
  const [seen, setSeen] = useState<{ room: string; seq: number }>()
  const seenSeq = seen?.room === space.id ? seen.seq : undefined
  useMarkTeamspaceRead(space, seenSeq)

  useEffect(() => {
    const list = listRef.current
    if (!list || lastSeq === undefined) return
    const spaceId = space.id
    const saw = () =>
      setSeen((held) =>
        held?.room === spaceId && held.seq >= lastSeq
          ? held
          : { room: spaceId, seq: lastSeq }
      )
    const last = list.querySelector(`[data-seq="${lastSeq}"]`)
    if (!last) return
    if (typeof IntersectionObserver === "undefined") {
      saw()
      return
    }
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) saw()
    })
    observer.observe(last)
    return () => observer.disconnect()
  }, [lastSeq, space.id])

  useEffect(() => {
    const list = listRef.current
    if (list && following.current) list.scrollTop = list.scrollHeight
  }, [lastId, outgoingCount])

  async function submit(text: string) {
    following.current = true
    setDraft("")
    const kept = await room.send(text)
    if (!kept) setDraft((current) => current || text)
  }

  function mention(seat: RoomSeat) {
    setDraft((current) => withMention(current, seat.handle))
    composerRef.current?.querySelector("textarea")?.focus()
  }

  const people = presence.seats
    .filter((seat) => seat.kind === "person")
    .sort((left, right) => Number(right.present) - Number(left.present))
  const agents = presence.seats.filter((seat) => seat.kind === "agent")
  const seats = [...people, ...agents]
  const pending = status.outgoing.filter((outgoing) => !outgoing.landed)
  const blocked = blockedCopy(space, status, viewerId, presence.seats)

  return (
    <section
      className="workspace-conversation"
      aria-label={`${space.name} conversation`}
    >
      <div className="workspace-conversation-presence">
        <span
          className="workspace-presence-pill"
          data-connection={status.connection}
          role="status"
        >
          {presenceLabel(status, presence.here, presence.me)}
        </span>
        {seats.length > 0 && (
          <div
            className="workspace-participants"
            role="group"
            aria-label="Participants"
          >
            {seats.slice(0, visibleSeats).map((seat) => (
              <button
                type="button"
                className="workspace-room-seat"
                data-kind={seat.kind}
                data-present={seat.kind === "person" && seat.present}
                key={seat.handle}
                title={seatTitle(seat, presence.seats, viewerId)}
                aria-label={`Mention ${seatName(seat, viewerId)} (@${seat.handle})`}
                disabled={!status.canPost}
                onClick={() => mention(seat)}
              >
                <IdentityAvatar identity={seatIdentity(seat)} />
              </button>
            ))}
            {seats.length > visibleSeats && (
              <span className="workspace-room-more">
                +{seats.length - visibleSeats}
              </span>
            )}
          </div>
        )}
      </div>

      <div
        className="workspace-messages"
        aria-live="polite"
        ref={listRef}
        onScroll={(event) => {
          const list = event.currentTarget
          following.current =
            list.scrollHeight - list.scrollTop - list.clientHeight < 48
        }}
      >
        {messages.length === 0 && pending.length === 0 && (
          <p className="workspace-messages-empty">
            {blocked ??
              (status.connection === "live"
                ? "No messages yet. Say hello, or @mention someone's agent to bring it in."
                : "Opening the room…")}
          </p>
        )}
        {messages.map((message) => (
          <RoomLine
            key={message.id}
            message={message}
            seats={presence.seats}
            viewerId={viewerId}
          />
        ))}
        {pending.map((outgoing) => (
          <div
            className="workspace-message workspace-room-pending"
            key={outgoing.id}
            aria-busy="true"
          >
            <IdentityAvatar
              identity={personIdentity(
                viewerId ?? "you",
                presence.me?.display_name ?? "You"
              )}
            />
            <div className="workspace-message-body">
              <div className="workspace-message-meta">
                <span className="workspace-room-author">
                  <strong>You</strong>
                </span>
                <span className="workspace-room-sending">Sending…</span>
              </div>
              <p>{outgoing.body}</p>
            </div>
          </div>
        ))}
        {messages.length > 0 && blocked && (
          <p className="workspace-room-notice" role="status">
            {blocked}
          </p>
        )}
      </div>

      <div ref={composerRef}>
        <AgentComposer
          label={`Message ${space.name}`}
          placeholder={composerPlaceholder(status, presence.me)}
          value={draft}
          onChange={setDraft}
          onSubmit={(text) => void submit(text)}
          disabled={!status.canPost}
          status={composerStatus(status, presence.me)}
          onFocus={() => onComposingChange(true)}
          onBlur={() => onComposingChange(false)}
        />
      </div>
    </section>
  )
}

function RoomLine({
  message,
  seats,
  viewerId,
}: {
  message: RoomMessage
  seats: readonly RoomSeat[]
  viewerId?: string
}) {
  const seat = seats.find((item) => item.handle === message.author_handle)
  const isAgent = message.author_kind === "agent"
  const mine = !isAgent && message.author_user_id === viewerId
  const name = mine
    ? "You"
    : (seat?.display_name ?? `@${message.author_handle}`)
  const identity = isAgent
    ? agentIdentity(
        message.author_agent_id ?? seat?.agent_id ?? message.author_handle,
        seat?.agent_name ?? seat?.display_name ?? message.author_handle
      )
    : personIdentity(
        message.author_user_id,
        seat?.display_name ?? message.author_handle
      )
  const tag = isAgent ? agentTag(seat, seats, viewerId) : undefined

  return (
    <article
      className="workspace-message"
      data-author={message.author_kind}
      data-mine={mine ? "true" : undefined}
      data-seq={message.seq}
      aria-label={`${name}${tag ? `, ${tag}` : ""}`}
    >
      <IdentityAvatar identity={identity} />
      <div className="workspace-message-body">
        <div className="workspace-message-meta">
          <span className="workspace-room-author">
            <strong>{name}</strong>
            {tag && <span className="workspace-room-tag">{tag}</span>}
          </span>
          <time
            dateTime={message.created_at}
            title={fullTime(message.created_at)}
          >
            {messageTime(message.created_at)}
          </time>
        </div>
        {isAgent ? (
          // Until the Markdown renderer arrives, the answer shows as the
          // plain text it is.
          <Suspense
            fallback={<p className="workspace-room-text">{message.body}</p>}
          >
            <RoomMarkdown text={message.body} />
          </Suspense>
        ) : (
          <p className="workspace-room-text">{message.body}</p>
        )}
      </div>
    </article>
  )
}

const RoomMarkdown = lazy(() => import("./room-markdown"))

/**
 * Whose agent spoke. A member's own agent speaks for them, beside them or,
 * while they are away, instead of them; a contributed specialist speaks for
 * nobody. A reader must be able to tell which.
 */
function agentTag(
  seat: RoomSeat | undefined,
  seats: readonly RoomSeat[],
  viewerId: string | undefined
): string {
  if (!seat) return "Agent"
  if (!seat.speaks_for) return "Specialist"
  if (seat.speaks_for === viewerId) return "Your agent"
  const person = seats.find(
    (item) => item.kind === "person" && item.user_id === seat.speaks_for
  )
  return `${person?.display_name ?? "A teammate"}'s agent`
}

function seatIdentity(seat: RoomSeat) {
  return seat.kind === "agent"
    ? agentIdentity(
        seat.agent_id ?? seat.handle,
        seat.agent_name ?? seat.display_name
      )
    : personIdentity(seat.user_id, seat.display_name)
}

function seatName(seat: RoomSeat, viewerId: string | undefined) {
  if (seat.kind === "person" && seat.user_id === viewerId) {
    return `${seat.display_name} (you)`
  }
  return seat.display_name
}

function seatTitle(
  seat: RoomSeat,
  seats: readonly RoomSeat[],
  viewerId: string | undefined
): string {
  if (seat.kind === "agent") {
    const whose = agentTag(seat, seats, viewerId)
    return `${seat.display_name}: ${whose}${seat.role && !seat.speaks_for ? `, ${seat.role}` : ""}`
  }
  return `${seatName(seat, viewerId)}: ${seat.present ? "here now" : "away"}`
}

/** Names a seat in the draft without doubling a mention already there. */
export function withMention(draft: string, handle: string): string {
  if (draft.toLowerCase().includes(`@${handle.toLowerCase()}`)) return draft
  return draft.trim() === "" ? `@${handle} ` : `${draft.trimEnd()} @${handle} `
}

function presenceLabel(
  status: RoomStatus,
  here: readonly RoomSeat[],
  me: RoomSeat | undefined
): string {
  switch (status.connection) {
    case "live":
      if (here.length === 0) return "Live"
      if (here.length === 1 && me?.present) return "Just you here"
      return `${here.length} here`
    case "paused":
      return "Paused"
    case "not-member":
      return "Members only"
    case "gone":
    case "unavailable":
      return "Unavailable"
    case "error":
      return "Reconnecting…"
    default:
      return "Connecting…"
  }
}

/** Why the room cannot be used, when it cannot. */
function blockedCopy(
  space: Teamspace,
  status: RoomStatus,
  viewerId: string | undefined,
  seats: readonly RoomSeat[]
): string | undefined {
  switch (status.connection) {
    case "paused": {
      const why = space.can_manage
        ? "This teamspace is paused. Resume it from Teamspaces to open its room again."
        : "This teamspace is paused. Its room opens again when a team admin resumes it."
      // Who paused it and when, once Go says so.
      const by = pausedLine(space, {
        viewerId,
        nameOf: (userId) =>
          seats.find(
            (seat) => seat.kind === "person" && seat.user_id === userId
          )?.display_name,
      })
      return by ? `${why} ${by}.` : why
    }
    case "not-member":
      return `Only members of the ${space.team_name} team can join this room.`
    case "gone":
      return "This room is no longer available to you."
    case "unavailable":
      return "Rooms aren't set up on this server."
    case "error":
      return `${status.error ?? "Couldn't reach the room."} Trying again…`
    default:
      return undefined
  }
}

function composerPlaceholder(
  status: RoomStatus,
  me: RoomSeat | undefined
): string {
  if (status.canPost) return "Message the room, or @mention someone"
  switch (status.connection) {
    case "paused":
      return "This room is paused"
    case "not-member":
      return "Only team members can post here"
    case "gone":
    case "unavailable":
      return "This room is unavailable"
    case "live":
      return me ? "Message the room" : "You can read this room, not post"
    default:
      return "Opening the room…"
  }
}

function composerStatus(
  status: RoomStatus,
  me: RoomSeat | undefined
): string | undefined {
  if (status.outgoing.length > 0) {
    return status.outgoing.every((outgoing) => outgoing.landed)
      ? "Sent. The room's agents are answering…"
      : "Sending… agents may take a little while to answer."
  }
  if (status.sendError) return status.sendError
  if (status.connection === "live" && !me) {
    return "Only teammates with an active agent can post in this room."
  }
  return undefined
}

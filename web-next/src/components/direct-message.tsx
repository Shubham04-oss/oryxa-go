// One direct message conversation (REALTIME.md, "Direct messages"), in two
// layouts: the main canvas (Team → People, or Team's recent conversations)
// and the agent panel's DMs tab. Each place mounts its own view, with its own
// socket on the DM's one object (useDirectMessages), so both stay current.
// DESIGN.md §3, "Direct messages", says how it reads. A request between the
// two people (an agent asked the other's, from Oryxa or Slack) has a quiet
// line of its own in the log (REALTIME.md, "Request notes").
import { useEffect, useMemo, useRef } from "react"
import type { ReactNode } from "react"
import { ArrowLeft } from "lucide-react"
import { AgentComposer } from "./agent-composer"
import {
  ConversationNotice,
  LogAnnouncer,
  LogHistory,
  NewMessagesPill,
  conversationNotice,
  useConversationLog,
} from "./conversation-log"
import type { Notice } from "./conversation-log"
import {
  clockTime,
  conversationTimeline,
  countLabel,
  messageKey,
  seenMessageId,
} from "./direct-message-format"
import type { DmGroup } from "./direct-message-format"
import type { DmPeer } from "./direct-message-people"
import { useReportPresence } from "./direct-message-presence"
import { fullTime, personIdentity } from "./teamspace-format"
import { IdentityAvatar } from "./workspace-avatar"
import { DM_MAX_BODY_LENGTH, useDirectMessages } from "@/lib/dms"
import type { DirectMessages, DmMessage } from "@/lib/dms"
import { codePoints } from "@/lib/dms/protocol"
import { useOpenRequest } from "@/lib/dms/request-navigation"
import type { OpenRequest } from "@/lib/dms/request-navigation"
import { requestLine, requestStateWord } from "@/lib/dms/request-notes"
import type { DmRequestNote } from "@/lib/dms/request-notes"
import { useRequestStanding } from "@/lib/dms/use-request-standing"
import { cn } from "@/lib/utils"

/** `canvas`: the main canvas, under the header that names the person. `panel`: the DMs tab, under a compact row with the way back to its list. */
export type DirectMessageLayout = "canvas" | "panel"

/** A person's avatar, with a dot while they are known to be online. */
export function PersonAvatar({
  id,
  name,
  online = false,
  className,
}: {
  id: string
  name: string
  online?: boolean
  className?: string
}) {
  return (
    <span
      className={cn("workspace-presence-avatar", className)}
      data-online={online ? "true" : undefined}
    >
      <IdentityAvatar identity={personIdentity(id, name)} />
    </span>
  )
}

/** A conversation's unread count, as a quiet pill. */
export function UnreadCount({ count }: { count: number }) {
  return (
    <span className="workspace-unread-count">
      {countLabel(count, 99)}
      <span className="sr-only"> unread</span>
    </span>
  )
}

type ViewProps = {
  organizationId: string
  viewerId: string
  peer: DmPeer
  layout: DirectMessageLayout
  /** On screen now: only then does reaching the end mark it read. */
  active?: boolean
  draft: string
  onDraftChange: (value: string) => void
  onComposingChange?: (composing: boolean) => void
  /** Moves focus into the conversation as it opens, when a person chose to open it. */
  autoFocus?: boolean
  /** The panel's way back to its list. */
  onBack?: () => void
  /**
   * What a request's line calls to open the request in the Requests view,
   * by its id. Without it the line takes the shell's (`OpenRequestProvider`,
   * src/lib/dms/request-navigation.ts), and without either the line has no
   * Open, since it would open nothing.
   */
  onOpenRequest?: OpenRequest
}

/** A DM with `peer`, over its own socket for as long as it is mounted. */
export function DirectMessageView({ organizationId, ...props }: ViewProps) {
  const dm = useDirectMessages(organizationId, props.viewerId, props.peer.id)
  // The rest of the page shows the dot for whoever an open view knows is online.
  useReportPresence(props.peer.id, dm.peer.online)
  return (
    <DirectMessageConversation
      dm={dm}
      organizationId={organizationId}
      {...props}
    />
  )
}

/** A request's line in the log: where it stands in it, and the request it says. */
type RequestEntry = { id: string; createdAt: string; note: DmRequestNote }

function isMessage(item: DmMessage | RequestEntry): item is DmMessage {
  return "authorId" in item
}

/** The messages with each request's line among them, by when its line stands. */
export function withRequests(
  messages: readonly DmMessage[],
  requests: readonly DmRequestNote[]
): Array<DmMessage | RequestEntry> {
  const entries = requests.map((note) => ({
    id: note.requestId,
    createdAt: note.at,
    note,
  }))
  if (entries.length === 0) return [...messages]
  const merged: Array<DmMessage | RequestEntry> = []
  let next = 0
  for (const message of messages) {
    // A line goes before the first message that came after it.
    while (
      next < entries.length &&
      entries[next].createdAt <= message.createdAt
    ) {
      merged.push(entries[next])
      next += 1
    }
    merged.push(message)
  }
  merged.push(...entries.slice(next))
  return merged
}

/**
 * The conversation itself, from what `dm` (useDirectMessages) holds: its
 * days and groups, what is still sending or failed, "Seen", the other
 * person typing, the page before on the way up, and the composer.
 */
export function DirectMessageConversation({
  dm,
  organizationId,
  viewerId,
  peer,
  layout,
  active = true,
  draft,
  onDraftChange,
  onComposingChange,
  autoFocus = false,
  onBack,
  onOpenRequest,
}: Omit<ViewProps, "organizationId"> & {
  dm: DirectMessages
  /** The DM's organization: where its requests are read from Go, as the member. */
  organizationId?: string
}) {
  const { messages, status } = dm
  const name = peer.name
  const short = peer.short
  const shellOpensRequests = useOpenRequest()
  const openRequest = onOpenRequest ?? shellOpensRequests

  // Nothing to show or send: the DM refused the viewer (the other person
  // left, or a purge ended it for good), or there is no DM to open.
  const refused = status === "not-allowed" || status === "closed"
  const sessionEnded = status === "session_expired"
  const shown = refused ? [] : messages
  const requests = refused ? none : dm.requests
  const entries = useMemo(
    () => withRequests(shown, requests),
    [shown, requests]
  )
  const timeline = useMemo(
    () =>
      conversationTimeline<DmMessage, RequestEntry>(
        entries,
        viewerId,
        isMessage
      ),
    [entries, viewerId]
  )
  const seenId = useMemo(
    () => seenMessageId(shown, viewerId, dm.peer.lastReadSeq),
    [shown, viewerId, dm.peer.lastReadSeq]
  )
  const log = useConversationLog({
    keys: entries.map((entry) =>
      isMessage(entry) ? messageKey(entry) : `request:${entry.id}`
    ),
    items: messages,
    latestSeq: latestConfirmed(messages),
    seqOf: (message) => message.seq,
    isNews: (message) => message.authorId !== viewerId,
    sayArrived: (arrived) =>
      arrived.length === 1
        ? `${name}: ${arrived[0].body}`
        : `${arrived.length} new messages from ${name}`,
    sayBelow: (total) =>
      `${total} new ${total === 1 ? "message" : "messages"} from ${name} below`,
    failed: messages
      .filter((message) => message.status === "failed")
      .map((message) => ({
        key: messageKey(message),
        text: `Your message to ${name} wasn't sent.`,
      })),
    live: status === "live",
    hasOlder: dm.hasOlder,
    loadOlder: dm.loadOlder,
    active,
    markRead: dm.markRead,
    autoFocus,
    onComposingChange,
  })

  // A request that is new, or moved on, while the conversation is open is
  // said aloud, as an approval landing in a side conversation is; what was
  // there when it opened is not news.
  const requestRevisions = useRef<ReadonlyMap<string, number>>(undefined)
  const { announce } = log
  useEffect(() => {
    if (status !== "live") return
    const now = new Map(requests.map((note) => [note.requestId, note.revision]))
    const before = requestRevisions.current
    requestRevisions.current = now
    if (!before) return
    for (const note of requests) {
      if ((before.get(note.requestId) ?? 0) < note.revision) {
        announce(
          `${requestLine(note.askerId === viewerId, short, note.origin)}: ${requestStateWord(note.state)}`
        )
      }
    }
  }, [requests, status, viewerId, short, announce])

  const length = composerLength(draft, DM_MAX_BODY_LENGTH)

  function send(text: string) {
    if (codePoints(text) > DM_MAX_BODY_LENGTH) return
    log.follow()
    dm.send(text)
    onDraftChange("")
  }

  const notice = conversationNotice(status, dm.reconnect)
  const empty = shown.length === 0

  return (
    <section
      className="workspace-dm"
      data-layout={layout}
      aria-label={`Direct message with ${name}`}
    >
      {layout === "panel" && (
        <div className="workspace-dm-bar">
          <button
            className="workspace-agent-new"
            type="button"
            aria-label="Back to conversations"
            onClick={onBack}
          >
            <ArrowLeft size={15} strokeWidth={1.7} />
          </button>
          <PersonAvatar id={peer.id} name={name} online={dm.peer.online} />
          <div className="workspace-dm-bar-name">
            <h2>{name}</h2>
            {dm.peer.online && <small>Online</small>}
          </div>
        </div>
      )}
      <div className="workspace-dm-log-wrap">
        <div
          className="workspace-dm-log"
          role="log"
          // New messages are said through the status below, so the page
          // before, arriving above, is not read out as news.
          aria-live="off"
          aria-label={`Conversation with ${name}`}
          tabIndex={0}
          ref={log.logRef}
          onScroll={log.onScroll}
        >
          {!empty && status === "live" && (
            <LogHistory
              hasOlder={dm.hasOlder}
              loadingOlder={log.loadingOlder}
              onLoadOlder={log.loadOlder}
              start={`This is the start of your conversation with ${name}.`}
            />
          )}
          {timeline.map((item) => {
            if (item.kind === "day") {
              return <DaySeparator key={item.key} label={item.label} />
            }
            if (item.kind === "entry") {
              return (
                <RequestLine
                  key={item.key}
                  note={item.item.note}
                  organizationId={organizationId}
                  viewerId={viewerId}
                  peer={peer}
                  onOpen={openRequest}
                />
              )
            }
            return (
              <MessageGroup
                key={item.key}
                group={item}
                peer={peer}
                seenId={seenId}
                canRetry={!refused && !sessionEnded}
                onRetry={dm.retry}
              />
            )
          })}
          {empty && (
            <EmptyConversation
              status={status}
              peer={peer}
              short={short}
              notice={notice}
            />
          )}
          {!refused && dm.peer.typing && (
            <p className="workspace-dm-typing">{short} is typing…</p>
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
          label={`Message ${name}`}
          placeholder={
            refused
              ? `You can't message ${short}`
              : sessionEnded
                ? "Sign in again to send messages"
                : `Message ${short}`
          }
          value={draft}
          onChange={(value) => {
            onDraftChange(value)
            dm.setTyping(value.trim() !== "")
          }}
          onSubmit={send}
          disabled={refused || sessionEnded}
          sendDisabled={length.tooLong}
          status={refused || sessionEnded ? undefined : length.status}
          onFocus={() => onComposingChange?.(true)}
          onBlur={() => {
            onComposingChange?.(false)
            dm.setTyping(false)
          }}
        />
      </div>
    </section>
  )
}

/** No requests, always the same array. */
const none: DmRequestNote[] = []

/**
 * A request between the two people, as a quiet line in the log: who asked
 * whose agent, where it was made (Slack, when it was), where the request
 * stands, and when its line was first noted, with Open for the Requests view
 * when the shell wired one. Nothing in it is anybody's words: the sentence is
 * fixed, the other person's name is the directory's, and where the request
 * stands is Go's own word to the member, read as their session (the note's
 * until Go has answered). Go refusing the member the request hides the line.
 */
export function RequestLine({
  note,
  organizationId,
  viewerId,
  peer,
  onOpen,
}: {
  note: DmRequestNote
  organizationId?: string
  viewerId: string
  peer: DmPeer
  onOpen?: OpenRequest
}) {
  const standing = useRequestStanding(organizationId, note)
  if (standing.hidden) return null
  const said = requestLine(note.askerId === viewerId, peer.short, note.origin)
  const state = requestStateWord(standing.state)
  return (
    <p
      className="workspace-side-status"
      data-request-state={standing.state}
      data-request-id={note.requestId}
    >
      {said} · {state}{" "}
      <time dateTime={note.at} title={fullTime(note.at)}>
        {clockTime(note.at)}
      </time>
      {onOpen && (
        <>
          {" "}
          <button
            className="workspace-dm-retry"
            type="button"
            aria-label={`Open the request: ${said}, ${state}`}
            onClick={() => onOpen(note.requestId)}
          >
            Open
          </button>
        </>
      )}
    </p>
  )
}

const numberFormat = new Intl.NumberFormat()

/**
 * What a composer says about its draft's length, against a conversation's
 * limit: nothing until it nears it, then the count, then that it is too long
 * to send.
 */
export function composerLength(
  draft: string,
  max: number
): { tooLong: boolean; status?: string } {
  const length = codePoints(draft.trim())
  if (length > max) {
    return {
      tooLong: true,
      status: `Too long to send: ${numberFormat.format(length)} of ${numberFormat.format(max)} characters.`,
    }
  }
  // It counts from 200 characters before the limit.
  if (length > max - 200) {
    return {
      tooLong: false,
      status: `${numberFormat.format(length)} of ${numberFormat.format(max)} characters`,
    }
  }
  return { tooLong: false }
}

/** The seq of the latest item the object confirmed, or 0. */
export function latestConfirmed(items: ReadonlyArray<{ seq: number }>): number {
  for (let index = items.length - 1; index >= 0; index -= 1) {
    if (items[index].seq > 0) return items[index].seq
  }
  return 0
}

/** Where a day starts in a conversation's log. */
export function DaySeparator({ label }: { label: string }) {
  return (
    <div className="workspace-dm-day">
      <span>{label}</span>
    </div>
  )
}

/**
 * One person's messages within a minute: the other person's beside their
 * avatar, the member's in bubbles on the right, one time for them all, and
 * each one's state (sending, not sent with Retry, "Seen"). `aside` adds a
 * quiet line above a message (a side conversation's "Asked Leo"), and
 * `failure` the words for one that failed.
 */
export function MessageGroup<TMessage extends DmMessage>({
  group,
  peer,
  seenId,
  canRetry,
  onRetry,
  aside,
  failure,
}: {
  group: DmGroup<TMessage>
  peer: DmPeer
  seenId?: string
  canRetry: boolean
  onRetry: (clientId: string) => void
  aside?: (message: TMessage) => ReactNode
  failure?: (message: TMessage) => string
}) {
  const author = group.mine ? "You" : peer.name
  return (
    <div
      className="workspace-dm-group"
      data-mine={group.mine ? "true" : undefined}
    >
      {!group.mine && <PersonAvatar id={peer.id} name={peer.name} />}
      <div className="workspace-dm-group-body">
        <p className="workspace-dm-meta">
          <span className="sr-only">{author}, </span>
          <time dateTime={group.at} title={fullTime(group.at)}>
            {clockTime(group.at)}
          </time>
        </p>
        {group.messages.map((message) => {
          const tooLong = codePoints(message.body) > DM_MAX_BODY_LENGTH
          return (
            <div
              className="workspace-dm-message"
              data-status={message.status}
              aria-busy={message.status === "sending" ? "true" : undefined}
              key={messageKey(message)}
            >
              {aside?.(message)}
              <p className="workspace-dm-text">{message.body}</p>
              {message.status === "sending" && (
                <span className="sr-only">Sending</span>
              )}
              {message.status === "failed" && (
                <p className="workspace-dm-note workspace-dm-failed">
                  {tooLong
                    ? "Too long to send."
                    : (failure?.(message) ?? "Not sent.")}
                  {canRetry && message.clientId !== undefined && !tooLong && (
                    <>
                      {" "}
                      <button
                        className="workspace-dm-retry"
                        type="button"
                        onClick={() => onRetry(message.clientId ?? "")}
                      >
                        Retry
                      </button>
                    </>
                  )}
                </p>
              )}
              {message.id === seenId && (
                <p className="workspace-dm-note">Seen</p>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}

/** The log with nothing in it: why, or an invitation to start. */
function EmptyConversation({
  status,
  peer,
  short,
  notice,
}: {
  status: DirectMessages["status"]
  peer: DmPeer
  short: string
  notice?: Notice
}) {
  if (status === "not-allowed") {
    return (
      <div className="workspace-dm-empty" role="status">
        <strong>You can't message this person</strong>
        <p>They're no longer in this organization.</p>
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
      <strong>Say hello to {short}</strong>
      <p>Only the two of you can see these messages.</p>
    </div>
  )
}

// One direct message conversation (REALTIME.md, "Direct messages"), in two
// layouts: the main canvas (Team → People, or Team's recent conversations)
// and the agent panel's DMs tab. Each place mounts its own view, with its own
// socket on the DM's one object (useDirectMessages), so both stay current.
// DESIGN.md §3, "Direct messages", says how it reads.
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import type { UIEvent } from "react"
import { ArrowDown, ArrowLeft } from "lucide-react"
import { AgentComposer } from "./agent-composer"
import {
  clockTime,
  countLabel,
  dmTimeline,
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
import { cn } from "@/lib/utils"

/** `canvas`: the main canvas, under the header that names the person. `panel`: the DMs tab, under a compact row with the way back to its list. */
export type DirectMessageLayout = "canvas" | "panel"

/** How near the end still counts as reading the latest, in pixels. */
const followDistance = 48

/** How near the top starts loading the page before, in pixels. */
const olderDistance = 120

/** From how many characters the composer counts them. */
const countFrom = DM_MAX_BODY_LENGTH - 200

const numberFormat = new Intl.NumberFormat()

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
}

/** A DM with `peer`, over its own socket for as long as it is mounted. */
export function DirectMessageView({ organizationId, ...props }: ViewProps) {
  const dm = useDirectMessages(organizationId, props.viewerId, props.peer.id)
  // The rest of the page shows the dot for whoever an open view knows is online.
  useReportPresence(props.peer.id, dm.peer.online)
  return <DirectMessageConversation dm={dm} {...props} />
}

type Announcement = { id: number; text: string }

/**
 * The conversation itself, from what `dm` (useDirectMessages) holds: its
 * days and groups, what is still sending or failed, "Seen", the other
 * person typing, the page before on the way up, and the composer.
 */
export function DirectMessageConversation({
  dm,
  viewerId,
  peer,
  layout,
  active = true,
  draft,
  onDraftChange,
  onComposingChange,
  autoFocus = false,
  onBack,
}: Omit<ViewProps, "organizationId"> & { dm: DirectMessages }) {
  const { messages, status, markRead, loadOlder: loadPage } = dm
  const name = peer.name
  const short = peer.short
  const logRef = useRef<HTMLDivElement>(null)
  const composerRef = useRef<HTMLDivElement>(null)
  // Follows the conversation while the reader is at its end, and stops the
  // moment they scroll back to read something.
  const following = useRef(true)
  const [atEnd, setAtEnd] = useState(true)
  // What arrived from the other person while the reader was further up.
  const [unseen, setUnseen] = useState(0)
  const unseenCount = useRef(0)
  const [loadingOlder, setLoadingOlder] = useState(false)
  const loading = useRef(false)
  const [announcement, setAnnouncement] = useState<Announcement>()
  // The distance from the log's end, as of the last scroll or render: what
  // keeps the reader's place when the page before arrives above it.
  const fromEnd = useRef(0)
  const firstShown = useRef<string>(undefined)

  // Nothing to show or send: the DM refused the viewer (the other person
  // left, or a purge ended it for good), or there is no DM to open.
  const refused = status === "not-allowed" || status === "closed"
  const sessionEnded = status === "session_expired"
  const shown = refused ? [] : messages
  const timeline = useMemo(() => dmTimeline(shown, viewerId), [shown, viewerId])
  const seenId = useMemo(
    () => seenMessageId(shown, viewerId, dm.peer.lastReadSeq),
    [shown, viewerId, dm.peer.lastReadSeq]
  )
  const latestSeq = latestConfirmed(messages)
  const firstKey = shown[0] ? messageKey(shown[0]) : undefined

  function announce(text: string) {
    setAnnouncement((current) => ({ id: (current?.id ?? 0) + 1, text }))
  }

  // After every render: keep the reader's place when the page before
  // arrived above it, and otherwise the end in view while they follow.
  useLayoutEffect(() => {
    const log = logRef.current
    if (!log) return
    const before = firstShown.current
    firstShown.current = firstKey
    const prepended =
      before !== undefined &&
      firstKey !== before &&
      shown.findIndex((message) => messageKey(message) === before) > 0
    if (prepended && !following.current) {
      log.scrollTop = log.scrollHeight - fromEnd.current
    } else if (following.current) {
      log.scrollTop = log.scrollHeight
    }
    fromEnd.current = log.scrollHeight - log.scrollTop
  })

  const loadOlder = useCallback(() => {
    if (loading.current || !dm.hasOlder || status !== "live") return
    loading.current = true
    setLoadingOlder(true)
    void loadPage().finally(() => {
      loading.current = false
      setLoadingOlder(false)
    })
  }, [dm.hasOlder, loadPage, status])

  function onScroll(event: UIEvent<HTMLDivElement>) {
    const log = event.currentTarget
    const end =
      log.scrollHeight - log.scrollTop - log.clientHeight < followDistance
    following.current = end
    fromEnd.current = log.scrollHeight - log.scrollTop
    setAtEnd(end)
    if (end) clearUnseen()
    if (log.scrollTop < olderDistance) loadOlder()
  }

  function clearUnseen() {
    unseenCount.current = 0
    setUnseen(0)
  }

  /** Back to the latest; following from here on keeps it in view. */
  function toEnd() {
    following.current = true
    setAtEnd(true)
    clearUnseen()
    const log = logRef.current
    if (log) log.scrollTop = log.scrollHeight
  }

  // What arrived from the other person since the reader last saw the
  // conversation: said aloud, and counted on the pill while they read
  // further up. The first snapshot, and the page before, are not news.
  const told = useRef<number>(undefined)
  useEffect(() => {
    if (status !== "live") return
    const seen = told.current
    told.current = latestSeq
    // A new run of the conversation (after a purge) numbers afresh.
    if (seen === undefined || latestSeq <= seen) return
    const arrived = messages.filter(
      (message) => message.seq > seen && message.authorId !== viewerId
    )
    if (arrived.length === 0) return
    if (following.current) {
      announce(
        arrived.length === 1
          ? `${name}: ${arrived[0].body}`
          : `${arrived.length} new messages from ${name}`
      )
      return
    }
    const total = unseenCount.current + arrived.length
    unseenCount.current = total
    setUnseen(total)
    announce(
      `${total} new ${total === 1 ? "message" : "messages"} from ${name} below`
    )
  }, [latestSeq, messages, name, status, viewerId])

  // A message the object refused, or never answered, is said as it fails
  // (again, if a retry fails too).
  const failedBefore = useRef(new Set<string>())
  useEffect(() => {
    const failed = new Set(
      messages.filter((message) => message.status === "failed").map(messageKey)
    )
    const fresh = [...failed].some((key) => !failedBefore.current.has(key))
    failedBefore.current = failed
    if (fresh) announce(`Your message to ${name} wasn't sent.`)
  }, [messages, name])

  // Reading: while this view is on screen, at the end of the conversation,
  // and its tab visible (markRead checks that), the latest counts as read.
  useEffect(() => {
    if (active && atEnd && status === "live") markRead()
  }, [active, atEnd, latestSeq, markRead, status])
  useEffect(() => {
    if (!active) return
    const readIfFollowing = () => {
      if (following.current) markRead()
    }
    document.addEventListener("visibilitychange", readIfFollowing)
    window.addEventListener("focus", readIfFollowing)
    return () => {
      document.removeEventListener("visibilitychange", readIfFollowing)
      window.removeEventListener("focus", readIfFollowing)
    }
  }, [active, markRead])

  // Opened by the member: straight to the composer, except on a small
  // screen, where that would raise the keyboard over what they came to read.
  useEffect(() => {
    if (!autoFocus) return
    const small = mediaMatches("(max-width: 800px)")
    if (small) logRef.current?.focus({ preventScroll: true })
    else composerRef.current?.querySelector("textarea")?.focus()
  }, [autoFocus])

  // A view that leaves while its composer has focus is never blurred, so
  // it says composing is over itself (the page gives back its height).
  const composing = useRef(onComposingChange)
  useEffect(() => {
    composing.current = onComposingChange
  })
  useEffect(() => () => composing.current?.(false), [])

  const length = codePoints(draft.trim())
  const tooLong = length > DM_MAX_BODY_LENGTH
  const composerStatus = tooLong
    ? `Too long to send: ${numberFormat.format(length)} of ${numberFormat.format(DM_MAX_BODY_LENGTH)} characters.`
    : length > countFrom
      ? `${numberFormat.format(length)} of ${numberFormat.format(DM_MAX_BODY_LENGTH)} characters`
      : undefined

  function send(text: string) {
    if (codePoints(text) > DM_MAX_BODY_LENGTH) return
    following.current = true
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
          ref={logRef}
          onScroll={onScroll}
        >
          {!empty && status === "live" && (
            <div className="workspace-dm-history">
              {dm.hasOlder ? (
                loadingOlder ? (
                  <p role="status">Loading earlier messages…</p>
                ) : (
                  <button type="button" onClick={loadOlder}>
                    Load earlier messages
                  </button>
                )
              ) : (
                <p>This is the start of your conversation with {name}.</p>
              )}
            </div>
          )}
          {timeline.map((item) =>
            item.kind === "day" ? (
              <div className="workspace-dm-day" key={item.key}>
                <span>{item.label}</span>
              </div>
            ) : (
              <MessageGroup
                key={item.key}
                group={item}
                peer={peer}
                seenId={seenId}
                canRetry={!refused && !sessionEnded}
                onRetry={dm.retry}
              />
            )
          )}
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
        {unseen > 0 && !atEnd && (
          <button className="workspace-dm-jump" type="button" onClick={toEnd}>
            <ArrowDown size={14} strokeWidth={1.8} aria-hidden="true" />
            {unseen === 1 ? "1 new message" : `${unseen} new messages`}
          </button>
        )}
      </div>
      <p
        className="workspace-dm-announcer sr-only"
        role="status"
        aria-live="polite"
      >
        {announcement && <span key={announcement.id}>{announcement.text}</span>}
      </p>
      <div ref={composerRef}>
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
          sendDisabled={tooLong}
          status={refused || sessionEnded ? undefined : composerStatus}
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

/** Whether a media query holds, where the page can ask. */
function mediaMatches(query: string): boolean {
  return (
    typeof window.matchMedia === "function" && window.matchMedia(query).matches
  )
}

/** The seq of the latest message the object confirmed, or 0. */
function latestConfirmed(messages: readonly DmMessage[]): number {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index].seq > 0) return messages[index].seq
  }
  return 0
}

function MessageGroup({
  group,
  peer,
  seenId,
  canRetry,
  onRetry,
}: {
  group: DmGroup
  peer: DmPeer
  seenId?: string
  canRetry: boolean
  onRetry: (clientId: string) => void
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
        {group.messages.map((message) => (
          <div
            className="workspace-dm-message"
            data-status={message.status}
            aria-busy={message.status === "sending" ? "true" : undefined}
            key={messageKey(message)}
          >
            <p className="workspace-dm-text">{message.body}</p>
            {message.status === "sending" && (
              <span className="sr-only">Sending</span>
            )}
            {message.status === "failed" && (
              <p className="workspace-dm-note workspace-dm-failed">
                {codePoints(message.body) > DM_MAX_BODY_LENGTH
                  ? "Too long to send."
                  : "Not sent."}
                {canRetry &&
                  message.clientId !== undefined &&
                  codePoints(message.body) <= DM_MAX_BODY_LENGTH && (
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
            {message.id === seenId && <p className="workspace-dm-note">Seen</p>}
          </div>
        ))}
      </div>
    </div>
  )
}

type Notice = {
  text: string
  action?: { label: string; run: () => void }
  tone: "status" | "alert"
}

/** What the conversation says about its connection, when that is worth saying. */
function conversationNotice(
  status: DirectMessages["status"],
  reconnect: () => void
): Notice | undefined {
  switch (status) {
    case "session_expired":
      return {
        text: "Your session has ended. Sign in again to keep messaging.",
        action: { label: "Sign in", run: signInAgain },
        tone: "alert",
      }
    case "error":
      return {
        text: "Can't reach this conversation. Trying again…",
        action: { label: "Try again", run: reconnect },
        tone: "status",
      }
    default:
      return undefined
  }
}

/** Back to sign-in, returning here after. */
export function signInAgain() {
  const here = `${window.location.pathname}${window.location.search}`
  window.location.assign(`/auth/sign-in?redirect=${encodeURIComponent(here)}`)
}

function ConversationNotice({ notice }: { notice: Notice }) {
  return (
    <p className="workspace-dm-notice" role={notice.tone}>
      {notice.text}
      {notice.action && (
        <>
          {" "}
          <button type="button" onClick={notice.action.run}>
            {notice.action.label}
          </button>
        </>
      )}
    </p>
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

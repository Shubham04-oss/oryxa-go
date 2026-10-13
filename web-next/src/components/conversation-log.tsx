// What every conversation's log shares, a DM's and a side conversation's
// (DESIGN.md §3, "Direct messages"): it follows the end while the reader is
// there, keeps their place when the page before arrives above, counts what
// arrived below on the "N new messages" pill and says it aloud, reads the
// conversation while it is on screen, and says when it cannot be reached.
import {
  useCallback,
  useEffect,
  useEffectEvent,
  useLayoutEffect,
  useRef,
  useState,
} from "react"
import type { RefObject, UIEvent } from "react"
import { ArrowDown } from "lucide-react"

/** How near the end still counts as reading the latest, in pixels. */
const followDistance = 48

/** How near the top starts loading the page before, in pixels. */
const olderDistance = 120

type Announcement = { id: number; text: string }

/** Something of the reader's that failed to send, and how that is said aloud. */
export type LogFailure = { key: string; text: string }

export type ConversationLogOptions<TItem> = {
  /** The keys of what the log shows, oldest first: the page before, arriving above, is told apart from news. */
  keys: readonly string[]
  /** What the conversation holds, oldest first. */
  items: readonly TItem[]
  /** The seq of the latest item the object confirmed, or 0. */
  latestSeq: number
  seqOf: (item: TItem) => number
  /** Whether an item is news to the reader: someone else's. */
  isNews: (item: TItem) => boolean
  /** Said as news arrives while the reader is at the end. */
  sayArrived: (arrived: readonly TItem[]) => string
  /** Said while they read further up: how much waits below. */
  sayBelow: (total: number) => string
  /** What of the reader's failed to send: each is said aloud as it fails (again, if a retry fails too). */
  failed: readonly LogFailure[]
  /** The socket has the conversation. */
  live: boolean
  hasOlder: boolean
  loadOlder: () => Promise<void>
  /** On screen now: only then does reaching the end mark it read. */
  active: boolean
  markRead: () => void
  /** Moves focus into the conversation as it opens: the composer, or the log on a small screen. */
  autoFocus: boolean
  onComposingChange?: (composing: boolean) => void
}

export type ConversationLog = {
  logRef: RefObject<HTMLDivElement | null>
  /** Wraps the composer, whose field the conversation focuses as it opens. */
  composerRef: RefObject<HTMLDivElement | null>
  onScroll: (event: UIEvent<HTMLDivElement>) => void
  /** The reader is at the end. */
  atEnd: boolean
  /** How much arrived below while the reader was further up. */
  unseen: number
  /** Back to the latest; following from here on keeps it in view. */
  toEnd: () => void
  loadOlder: () => void
  loadingOlder: boolean
  announcement?: Announcement
  announce: (text: string) => void
  /** The reader sent something: the log follows the end again. */
  follow: () => void
}

/** Whether a media query holds, where the page can ask. */
export function mediaMatches(query: string): boolean {
  return (
    typeof window.matchMedia === "function" && window.matchMedia(query).matches
  )
}

/** A conversation log's scrolling, reading and announcements. */
export function useConversationLog<TItem>({
  keys,
  items,
  latestSeq,
  seqOf,
  isNews,
  sayArrived,
  sayBelow,
  failed,
  live,
  hasOlder,
  loadOlder: loadPage,
  active,
  markRead,
  autoFocus,
  onComposingChange,
}: ConversationLogOptions<TItem>): ConversationLog {
  const logRef = useRef<HTMLDivElement>(null)
  const composerRef = useRef<HTMLDivElement>(null)
  // Follows the conversation while the reader is at its end, and stops the
  // moment they scroll back to read something.
  const following = useRef(true)
  const [atEnd, setAtEnd] = useState(true)
  // What arrived from others while the reader was further up.
  const [unseen, setUnseen] = useState(0)
  const unseenCount = useRef(0)
  const [loadingOlder, setLoadingOlder] = useState(false)
  const loading = useRef(false)
  const [announcement, setAnnouncement] = useState<Announcement>()
  // The distance from the log's end, as of the last scroll or render: what
  // keeps the reader's place when the page before arrives above it.
  const fromEnd = useRef(0)
  const firstShown = useRef<string>(undefined)
  const firstKey = keys.at(0)

  const announce = useCallback((text: string) => {
    setAnnouncement((current) => ({ id: (current?.id ?? 0) + 1, text }))
  }, [])

  // After every render: keep the reader's place when the page before
  // arrived above it, and otherwise the end in view while they follow.
  useLayoutEffect(() => {
    const log = logRef.current
    if (!log) return
    const before = firstShown.current
    firstShown.current = firstKey
    const prepended =
      before !== undefined && firstKey !== before && keys.indexOf(before) > 0
    if (prepended && !following.current) {
      log.scrollTop = log.scrollHeight - fromEnd.current
    } else if (following.current) {
      log.scrollTop = log.scrollHeight
    }
    fromEnd.current = log.scrollHeight - log.scrollTop
  })

  const loadOlder = useCallback(() => {
    if (loading.current || !hasOlder || !live) return
    loading.current = true
    setLoadingOlder(true)
    void loadPage().finally(() => {
      loading.current = false
      setLoadingOlder(false)
    })
  }, [hasOlder, live, loadPage])

  function clearUnseen() {
    unseenCount.current = 0
    setUnseen(0)
  }

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

  function toEnd() {
    following.current = true
    setAtEnd(true)
    clearUnseen()
    const log = logRef.current
    if (log) log.scrollTop = log.scrollHeight
  }

  // What arrived from others since the reader last saw the conversation:
  // said aloud, and counted on the pill while they read further up. The
  // first snapshot, and the page before, are not news.
  const told = useRef<number>(undefined)
  const tell = useEffectEvent((seen: number) => {
    const arrived = items.filter((item) => seqOf(item) > seen && isNews(item))
    if (arrived.length === 0) return
    if (following.current) {
      announce(sayArrived(arrived))
      return
    }
    const total = unseenCount.current + arrived.length
    unseenCount.current = total
    setUnseen(total)
    announce(sayBelow(total))
  })
  useEffect(() => {
    if (!live) return
    const seen = told.current
    told.current = latestSeq
    // A new run of the conversation (after a purge) numbers afresh.
    if (seen === undefined || latestSeq <= seen) return
    tell(seen)
  }, [latestSeq, live])

  // What the object refused, or never answered, is said as it fails.
  const failedBefore = useRef(new Set<string>())
  const failedKeys = failed.map((failure) => failure.key).join("\n")
  const sayFailure = useEffectEvent(() => {
    const fresh = failed.filter(
      (failure) => !failedBefore.current.has(failure.key)
    )
    failedBefore.current = new Set(failed.map((failure) => failure.key))
    const last = fresh.at(-1)
    if (last) announce(last.text)
  })
  useEffect(() => {
    sayFailure()
  }, [failedKeys])

  // Reading: while this view is on screen, at the end of the conversation,
  // and its tab visible (markRead checks that), the latest counts as read.
  useEffect(() => {
    if (active && atEnd && live) markRead()
  }, [active, atEnd, latestSeq, markRead, live])
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

  return {
    logRef,
    composerRef,
    onScroll,
    atEnd,
    unseen,
    toEnd,
    loadOlder,
    loadingOlder,
    announcement,
    announce,
    follow: () => {
      following.current = true
    },
  }
}

/**
 * The top of a log with something in it: the way to the page before, or
 * that this is where the conversation starts.
 */
export function LogHistory({
  hasOlder,
  loadingOlder,
  onLoadOlder,
  start,
}: {
  hasOlder: boolean
  loadingOlder: boolean
  onLoadOlder: () => void
  /** Where the conversation starts, in words. */
  start: string
}) {
  return (
    <div className="workspace-dm-history">
      {hasOlder ? (
        loadingOlder ? (
          <p role="status">Loading earlier messages…</p>
        ) : (
          <button type="button" onClick={onLoadOlder}>
            Load earlier messages
          </button>
        )
      ) : (
        <p>{start}</p>
      )}
    </div>
  )
}

/** The pill to what arrived below while the reader was further up. */
export function NewMessagesPill({
  count,
  onClick,
}: {
  count: number
  onClick: () => void
}) {
  return (
    <button className="workspace-dm-jump" type="button" onClick={onClick}>
      <ArrowDown size={14} strokeWidth={1.8} aria-hidden="true" />
      {count === 1 ? "1 new message" : `${count} new messages`}
    </button>
  )
}

/**
 * What the log says aloud. The log itself is not live, so the page before,
 * arriving above, is not read out as news.
 */
export function LogAnnouncer({
  announcement,
}: {
  announcement?: Announcement
}) {
  return (
    <p
      className="workspace-dm-announcer sr-only"
      role="status"
      aria-live="polite"
    >
      {announcement && <span key={announcement.id}>{announcement.text}</span>}
    </p>
  )
}

export type Notice = {
  text: string
  action?: { label: string; run: () => void }
  tone: "status" | "alert"
}

/** What a conversation says about its connection, when that is worth saying. */
export function conversationNotice(
  status: string,
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

export function ConversationNotice({ notice }: { notice: Notice }) {
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

// A room's log, kept live (F5): the page reads what is there, then follows
// the room's socket. A dropped socket reconnects with a growing pause and
// reads what it missed, so no event is lost or shown twice.
//
// One subscription per room per page (T5): every hook on the same room
// shares one read of the log and one socket, counted by reference. The
// last one to let go keeps it open a little longer, and the events stay
// cached, so a room opened again shows what it had at once (T6). A socket
// that stops answering pings is counted dead and reopened, and coming back
// online or to the tab reads what was missed (T12).
import { useCallback, useSyncExternalStore } from "react"
import { ApiError } from "@/lib/api/client"
import { liveSocketUrl, parseLiveFrame, readLog } from "./client"
import type { LiveEvent, LiveRoom } from "./client"

export type RoomLogStatus = "loading" | "live" | "reconnecting" | "refused"

export const PING_MS = 25_000
/** A socket that has said nothing for this long is dead, not quiet. */
export const DEAD_AFTER_MS = 2 * PING_MS + 5_000
const RETRY_MS = [1_000, 2_000, 5_000, 10_000]
/** How many pages one catch-up reads at most (the edge's page is 500 events). */
const CATCH_UP_PAGES = 20
/** How long a room nobody follows stays open, for the next hook to reuse. */
export const CLOSE_AFTER_MS = 30_000
/** How many rooms' events stay cached once nobody follows them. */
const MAX_CACHED_ROOMS = 20

export type RoomLog = {
  events: LiveEvent[]
  status: RoomLogStatus
  /** Why the room refused the member: 403 or 404 from the edge. */
  refusal?: ApiError
  /**
   * The last seq of the first read: events at or below it were history
   * when the page opened the room, not news.
   */
  history: number
  /** The first read stopped at its page cap: older events are missing. */
  truncated: boolean
}

const IDLE: RoomLog = {
  events: [],
  status: "loading",
  history: 0,
  truncated: false,
}

/** Merges `incoming` into `events`, in seq order and once each. */
export function mergeEvents(
  events: LiveEvent[],
  incoming: LiveEvent[]
): LiveEvent[] {
  if (incoming.length === 0) return events
  const last = events.at(-1)?.seq ?? 0
  const fresh = incoming.filter((event) => event.seq > last)
  if (fresh.length === incoming.length && fresh.length > 0) {
    return [...events, ...fresh].sort((a, b) => a.seq - b.seq)
  }
  const bySeq = new Map(events.map((event) => [event.seq, event]))
  for (const event of incoming) bySeq.set(event.seq, event)
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq)
}

type Subscription = {
  state: RoomLog
  listeners: Set<() => void>
  refs: number
  lastSeq: number
  /** Whether the first read has finished, which fixes `history`. */
  read: boolean
  stop?: () => void
  closeTimer?: ReturnType<typeof setTimeout>
}

const subscriptions = new Map<string, Subscription>()

function roomLogKey(organizationId: string, room: LiveRoom) {
  return `${organizationId}/${room.kind}/${room.id}`
}

function update(subscription: Subscription, next: (state: RoomLog) => RoomLog) {
  subscription.state = next(subscription.state)
  for (const listener of subscription.listeners) listener()
}

// connect reads the log after what the subscription has, then follows the
// socket until stopped.
function connect(
  subscription: Subscription,
  organizationId: string,
  room: LiveRoom
): () => void {
  let stopped = false
  let socket: WebSocket | undefined
  let ping: ReturnType<typeof setInterval> | undefined
  let retry: ReturnType<typeof setTimeout> | undefined
  let attempt = 0
  let lastHeard = Date.now()
  const controller = new AbortController()

  const take = (incoming: LiveEvent[]) => {
    if (incoming.length === 0) return
    update(subscription, (current) => {
      const events = mergeEvents(current.events, incoming)
      subscription.lastSeq = events.at(-1)?.seq ?? subscription.lastSeq
      return { ...current, events }
    })
  }

  const catchUp = async () => {
    for (let page = 0; page < CATCH_UP_PAGES; page += 1) {
      const answer = await readLog(
        organizationId,
        room,
        { after: subscription.lastSeq },
        controller.signal
      )
      if (stopped) return
      take(answer.events)
      subscription.lastSeq = Math.max(subscription.lastSeq, answer.next)
      if (!answer.more) return
    }
    if (!subscription.read) {
      update(subscription, (current) => ({ ...current, truncated: true }))
    }
  }

  const scheduleRetry = () => {
    if (stopped) return
    const delay = RETRY_MS[Math.min(attempt, RETRY_MS.length - 1)] ?? 10_000
    attempt += 1
    update(subscription, (current) => ({ ...current, status: "reconnecting" }))
    retry = setTimeout(() => void open(), delay)
  }

  const dropSocket = () => {
    clearInterval(ping)
    const dead = socket
    socket = undefined
    try {
      dead?.close()
    } catch {
      // already closed
    }
  }

  const open = async () => {
    retry = undefined
    try {
      await catchUp()
    } catch (error) {
      if (stopped) return
      if (
        error instanceof ApiError &&
        (error.status === 403 || error.status === 404)
      ) {
        update(subscription, (current) => ({
          ...current,
          status: "refused",
          refusal: error,
        }))
        return
      }
      scheduleRetry()
      return
    }
    if (stopped) return
    if (!subscription.read) {
      subscription.read = true
      update(subscription, (current) => ({
        ...current,
        history: subscription.lastSeq,
      }))
    }
    const opened = new WebSocket(liveSocketUrl(organizationId, room, "/live"))
    socket = opened
    opened.addEventListener("open", () => {
      if (socket !== opened) return
      attempt = 0
      lastHeard = Date.now()
      update(subscription, (current) => ({
        ...current,
        status: "live",
        refusal: undefined,
      }))
      ping = setInterval(() => {
        // No pong for two pings: a half-open connection never closes by
        // itself, so close it and open another.
        if (Date.now() - lastHeard > DEAD_AFTER_MS) {
          dropSocket()
          scheduleRetry()
          return
        }
        try {
          opened.send("ping")
        } catch {
          dropSocket()
          scheduleRetry()
        }
      }, PING_MS)
      // Events appended between the read and the socket opening reach no
      // socket: read them now. Merging by seq drops any seen twice.
      void catchUp().catch(() => undefined)
    })
    opened.addEventListener("message", (message) => {
      if (socket !== opened) return
      lastHeard = Date.now()
      const event = parseLiveFrame(message.data)
      if (event) take([event])
    })
    opened.addEventListener("close", () => {
      if (socket !== opened) return
      clearInterval(ping)
      socket = undefined
      scheduleRetry()
    })
  }

  // Back online or back to the tab: read what was missed now, and reopen
  // at once rather than after the pause.
  const resume = () => {
    if (stopped || subscription.state.status === "refused") return
    if (
      typeof document !== "undefined" &&
      document.visibilityState === "hidden"
    )
      return
    if (socket) {
      void catchUp().catch(() => undefined)
      return
    }
    if (retry !== undefined) {
      clearTimeout(retry)
      void open()
    }
  }
  window.addEventListener("online", resume)
  document.addEventListener("visibilitychange", resume)

  void open()
  return () => {
    stopped = true
    controller.abort()
    clearTimeout(retry)
    window.removeEventListener("online", resume)
    document.removeEventListener("visibilitychange", resume)
    dropSocket()
  }
}

function acquire(organizationId: string, room: LiveRoom): Subscription {
  const key = roomLogKey(organizationId, room)
  let subscription = subscriptions.get(key)
  if (!subscription) {
    subscription = {
      state: IDLE,
      listeners: new Set(),
      refs: 0,
      lastSeq: 0,
      read: false,
    }
    subscriptions.set(key, subscription)
  }
  // Most recently used last, so the oldest cached room is evicted first.
  subscriptions.delete(key)
  subscriptions.set(key, subscription)
  subscription.refs += 1
  clearTimeout(subscription.closeTimer)
  subscription.closeTimer = undefined
  if (!subscription.stop) {
    if (subscription.state.status === "refused") {
      // Refused before: their access may have changed since.
      subscription.state = {
        ...subscription.state,
        status: "loading",
        refusal: undefined,
      }
    }
    subscription.stop = connect(subscription, organizationId, room)
  }
  return subscription
}

function release(organizationId: string, room: LiveRoom) {
  const key = roomLogKey(organizationId, room)
  const subscription = subscriptions.get(key)
  if (!subscription) return
  subscription.refs -= 1
  if (subscription.refs > 0) return
  subscription.closeTimer = setTimeout(() => {
    subscription.closeTimer = undefined
    if (subscription.refs > 0) return
    subscription.stop?.()
    subscription.stop = undefined
    evict()
  }, CLOSE_AFTER_MS)
}

// evict forgets the oldest rooms nobody follows beyond the cache's size.
function evict() {
  let idle = [...subscriptions.entries()].filter(
    ([, s]) => s.refs === 0 && !s.stop
  )
  while (idle.length > MAX_CACHED_ROOMS) {
    const [key] = idle[0]
    subscriptions.delete(key)
    idle = idle.slice(1)
  }
}

/** Closes every room's subscription and forgets their events; for tests. */
export function forgetRoomLogs() {
  for (const subscription of subscriptions.values()) {
    clearTimeout(subscription.closeTimer)
    subscription.stop?.()
  }
  subscriptions.clear()
}

/** How many rooms this page follows now; for tests. */
export function openRoomLogs(): number {
  return [...subscriptions.values()].filter((s) => s.stop).length
}

export function useRoomLog(
  organizationId: string | undefined,
  room: LiveRoom | undefined,
  { enabled = true }: { enabled?: boolean } = {}
): RoomLog {
  const kind = room?.kind
  const id = room?.id
  const active = enabled && Boolean(organizationId && kind && id)
  const key = active
    ? roomLogKey(organizationId!, { kind: kind!, id: id! })
    : ""

  const subscribe = useCallback(
    (listener: () => void) => {
      if (!active) return () => undefined
      const target = { kind: kind!, id: id! }
      const subscription = acquire(organizationId!, target)
      subscription.listeners.add(listener)
      return () => {
        subscription.listeners.delete(listener)
        release(organizationId!, target)
      }
    },
    // key names the room; the object itself changes on every render.
    [key]
  )
  const snapshot = useCallback(
    () => (key ? (subscriptions.get(key)?.state ?? IDLE) : IDLE),
    [key]
  )
  return useSyncExternalStore(subscribe, snapshot, () => IDLE)
}

// A room's log, kept live (F5): the page reads what is there, then follows
// the room's socket. A dropped socket reconnects with a growing pause and
// reads what it missed, so no event is lost or shown twice.
import { useEffect, useRef, useState } from "react"
import { ApiError } from "@/lib/api/client"
import { liveSocketUrl, parseLiveFrame, readLog } from "./client"
import type { LiveEvent, LiveRoom } from "./client"

export type RoomLogStatus = "loading" | "live" | "reconnecting" | "refused"

export const PING_MS = 25_000
const RETRY_MS = [1_000, 2_000, 5_000, 10_000]
/** How many pages one catch-up reads at most (the edge's page is 500 events). */
const CATCH_UP_PAGES = 20

export type RoomLog = {
  events: LiveEvent[]
  status: RoomLogStatus
  /** Why the room refused the member: 403 or 404 from the edge. */
  refusal?: ApiError
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

export function useRoomLog(
  organizationId: string | undefined,
  room: LiveRoom | undefined,
  { enabled = true }: { enabled?: boolean } = {}
): RoomLog {
  const [state, setState] = useState<RoomLog>({ events: [], status: "loading" })
  const lastSeq = useRef(0)
  const roomKey = room ? `${room.kind}:${room.id}` : ""

  useEffect(() => {
    if (!enabled || !organizationId || !room) return
    let stopped = false
    let socket: WebSocket | undefined
    let ping: ReturnType<typeof setInterval> | undefined
    let retry: ReturnType<typeof setTimeout> | undefined
    let attempt = 0
    const controller = new AbortController()
    lastSeq.current = 0
    setState({ events: [], status: "loading" })

    const take = (incoming: LiveEvent[]) => {
      if (incoming.length === 0) return
      setState((current) => {
        const events = mergeEvents(current.events, incoming)
        lastSeq.current = events.at(-1)?.seq ?? lastSeq.current
        return { ...current, events }
      })
    }

    const catchUp = async () => {
      for (let page = 0; page < CATCH_UP_PAGES; page += 1) {
        const answer = await readLog(
          organizationId,
          room,
          { after: lastSeq.current },
          controller.signal
        )
        if (stopped) return
        take(answer.events)
        lastSeq.current = Math.max(lastSeq.current, answer.next)
        if (!answer.more) return
      }
    }

    const scheduleRetry = () => {
      if (stopped) return
      const delay = RETRY_MS[Math.min(attempt, RETRY_MS.length - 1)] ?? 10_000
      attempt += 1
      setState((current) => ({ ...current, status: "reconnecting" }))
      retry = setTimeout(() => void open(), delay)
    }

    const open = async () => {
      try {
        await catchUp()
      } catch (error) {
        if (stopped) return
        if (
          error instanceof ApiError &&
          (error.status === 403 || error.status === 404)
        ) {
          setState((current) => ({
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
      socket = new WebSocket(liveSocketUrl(organizationId, room, "/live"))
      socket.addEventListener("open", () => {
        attempt = 0
        setState((current) => ({
          ...current,
          status: "live",
          refusal: undefined,
        }))
        ping = setInterval(() => socket?.send("ping"), PING_MS)
        // Events appended between the read and the socket opening reach no
        // socket: read them now. Merging by seq drops any seen twice.
        void catchUp().catch(() => undefined)
      })
      socket.addEventListener("message", (message) => {
        const event = parseLiveFrame(message.data)
        if (event) take([event])
      })
      socket.addEventListener("close", () => {
        clearInterval(ping)
        socket = undefined
        scheduleRetry()
      })
    }

    void open()
    return () => {
      stopped = true
      controller.abort()
      clearInterval(ping)
      clearTimeout(retry)
      socket?.close()
    }
    // roomKey names the room; the object itself changes on every render.
  }, [enabled, organizationId, roomKey])

  return state
}

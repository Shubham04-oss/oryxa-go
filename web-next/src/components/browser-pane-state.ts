// Whether a room's browser is running, as every place that shows it decides
// it: the Live tab's count, its tiles, the pane's header and its body (B1,
// B16). The room's row only says the browser was running when last used;
// this also believes the clock (the edge closes a browser idle past its
// limit), the room's log (a browser.stopped or reset after the last use),
// and the edge itself (a live view refused because the browser stopped).
import { useEffect, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import type { LiveChild } from "@/lib/live/children"
import type { LiveRoom } from "@/lib/live/client"

/** The edge closes a browser no agent call has used for this long. */
export const BROWSER_IDLE_MS = 10 * 60_000

/** The events after which a browser is no longer running. */
const ENDED = new Set(["browser.stopped", "browser.reset"])

/** How far the page's clock and the edge's may disagree. */
const CLOCK_SLACK_MS = 30_000

type LogEvent = { type: string; at: string; data: Record<string, unknown> }

export function browserEndedKey(
  organizationId: string,
  room: LiveRoom,
  browser: string
) {
  return [
    "live",
    organizationId,
    room.kind,
    room.id,
    "browser",
    browser,
    "ended",
  ] as const
}

/**
 * Whether `child` is running. A sandbox follows its row; a browser must also
 * have been used within the idle limit, have no later stop or reset in the
 * log, and not have been reported stopped by the edge since its last use
 * (`endedAt`, when the edge refused its live view).
 */
export function isChildRunning(
  child: LiveChild,
  {
    now = Date.now(),
    events = [],
    endedAt,
  }: { now?: number; events?: readonly LogEvent[]; endedAt?: number } = {}
): boolean {
  if (child.status !== "running") return false
  if (child.kind !== "browser") return true
  const lastUsed = Date.parse(child.lastUsedAt)
  if (Number.isFinite(lastUsed) && now - lastUsed >= BROWSER_IDLE_MS) {
    return false
  }
  // The latest browser event decides; an end older than the row's last use
  // was followed by a call that started it again.
  const latest = [...events]
    .reverse()
    .find(
      (event) =>
        event.data.child === child.name && event.type.startsWith("browser.")
    )
  const latestAt = latest ? Date.parse(latest.at) : undefined
  if (
    latest &&
    ENDED.has(latest.type) &&
    latestAt !== undefined &&
    latestAt >= lastUsed - CLOCK_SLACK_MS
  ) {
    return false
  }
  // The edge's refusal stands until the log shows the browser used again.
  if (
    endedAt !== undefined &&
    endedAt + CLOCK_SLACK_MS >= lastUsed &&
    !(latest && !ENDED.has(latest.type) && latestAt! > endedAt)
  ) {
    return false
  }
  return true
}

/** The current time, moving on every `everyMs`, for idle limits to pass. */
export function useNow(everyMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), everyMs)
    return () => clearInterval(timer)
  }, [everyMs])
  return now
}

/** When the edge last said this browser had stopped, if it has. */
export function useBrowserEnded(
  organizationId: string,
  room: LiveRoom,
  browser: string
): number | undefined {
  const ended = useQuery<number | null>({
    queryKey: browserEndedKey(organizationId, room, browser),
    // Never fetched: the pane writes it when the edge refuses a live view.
    queryFn: () => null,
    enabled: false,
    staleTime: Infinity,
  })
  return ended.data ?? undefined
}

/** Records that the edge reported this browser stopped. */
export function useMarkBrowserEnded(
  organizationId: string,
  room: LiveRoom,
  browser: string
) {
  const queryClient = useQueryClient()
  return () =>
    queryClient.setQueryData(
      browserEndedKey(organizationId, room, browser),
      Date.now()
    )
}

/** Every browser the edge reported stopped, for counting many at once. */
export function useEndedBrowsers(
  organizationId: string,
  room: LiveRoom | undefined
): (browser: string) => number | undefined {
  const queryClient = useQueryClient()
  return (browser) =>
    room
      ? (queryClient.getQueryData<number | null>(
          browserEndedKey(organizationId, room, browser)
        ) ?? undefined)
      : undefined
}

/** Whether one child is running, following the clock, the log and the edge. */
export function useChildRunning(
  organizationId: string,
  room: LiveRoom,
  child: LiveChild,
  events: readonly LogEvent[]
): boolean {
  const now = useNow()
  const endedAt = useBrowserEnded(organizationId, room, child.name)
  return isChildRunning(child, {
    now,
    events,
    endedAt: child.kind === "browser" ? endedAt : undefined,
  })
}

/** isChildRunning for many children at once (a count, a list of tiles). */
export function useRunningPredicate(
  organizationId: string,
  room: LiveRoom | undefined,
  events: readonly LogEvent[]
): (child: LiveChild) => boolean {
  const now = useNow()
  const endedAt = useEndedBrowsers(organizationId, room)
  return (child) =>
    isChildRunning(child, {
      now,
      events,
      endedAt: child.kind === "browser" ? endedAt(child.name) : undefined,
    })
}

/** The events that start or end a browser's ask for a hand. */
const HANDOFF_EVENTS = new Set([
  "browser.handoff",
  "browser.handoff.timeout",
  "browser.resume",
  "browser.stopped",
  "browser.reset",
])

/**
 * The latest event that started or ended an ask for a hand with `browser`,
 * including a handoff that timed out with nobody answering (B8).
 */
export function lastHandoffEvent<TEvent extends LogEvent>(
  events: readonly TEvent[],
  browser: string
): TEvent | undefined {
  for (const event of [...events].reverse()) {
    if (HANDOFF_EVENTS.has(event.type) && event.data.child === browser) {
      return event
    }
  }
  return undefined
}

/** Whether the agent is still asking for a hand with `browser`. */
export function isAskingForHand(
  events: readonly LogEvent[],
  browser: string
): boolean {
  return lastHandoffEvent(events, browser)?.type === "browser.handoff"
}

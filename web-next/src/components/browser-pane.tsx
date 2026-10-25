// A room's browser, as a member sees it (A8): the agent's browser live, and
// when the agent asks for a hand (a login, a captcha), the member takes it
// over and hands it back. Only the member whose browser it is reaches a
// private one; the edge decides that.
import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowUpRight, Hand } from "lucide-react"
import { handBack, openLiveView, readHandoff } from "@/lib/live/children"
import type { LiveChild } from "@/lib/live/children"
import type { LiveEvent, LiveRoom } from "@/lib/live/client"
import { ApiError } from "@/lib/api/client"
import {
  lastHandoffEvent as findLastHandoffEvent,
  useChildRunning,
  useMarkBrowserEnded,
} from "./browser-pane-state"

/** A live-view link is used until this long before it expires. */
export const LIVE_LINK_MARGIN_MS = 60_000

/** While the agent asks for a hand, how often to check the ask still stands. */
const HANDOFF_RECHECK_MS = 60_000

function isStatus(error: unknown, status: number): boolean {
  return error instanceof ApiError && error.status === status
}

/** What the pane says when the edge wouldn't give it a live view (B16). */
function liveErrorText(error: unknown): string {
  if (isStatus(error, 403)) return "This browser is private to someone else."
  if (isStatus(error, 404)) return "This browser isn't in this room any more."
  return "Couldn't reach the browser. Check your connection and try again."
}

function handoffKey(organizationId: string, room: LiveRoom, browser: string) {
  return [
    "live",
    organizationId,
    room.kind,
    room.id,
    "browser",
    browser,
    "handoff",
  ] as const
}

function elapsed(ms: number | undefined): string | undefined {
  if (ms === undefined) return undefined
  const minutes = Math.floor(ms / 60_000)
  return minutes < 1 ? "just now" : `${minutes} min ago`
}

/**
 * Every request to a browser keeps it awake (and billed), so the pane asks
 * as little as it can: the live view only of a running browser, and the
 * handoff state once when it opens, again when the room's log says the
 * handoff changed, and once a minute while the agent asks for a hand.
 * Opening the pane never starts a browser: the edge answers 409 for one that
 * has stopped, and the pane says so (B3).
 */
export function BrowserPane({
  organizationId,
  room,
  child,
  events,
  agentName = "The agent",
  compact = false,
}: {
  organizationId: string
  room: LiveRoom
  child: LiveChild
  /** The room's log, which says when the agent asks for a hand. */
  events: readonly LiveEvent[]
  agentName?: string
  /** A tile among others (in a parley): the live page fills it. */
  compact?: boolean
}) {
  const browser = child.name
  const running = useChildRunning(organizationId, room, child, events)
  const markEnded = useMarkBrowserEnded(organizationId, room, browser)
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<"tab" | "full">("tab")
  const [note, setNote] = useState("")
  const live = useQuery({
    queryKey: [
      "live",
      organizationId,
      room.kind,
      room.id,
      "browser",
      browser,
      "view",
      mode,
    ],
    queryFn: async () => {
      try {
        return await openLiveView(organizationId, room, browser, mode)
      } catch (error) {
        // The browser has stopped: every place that shows it says so now.
        if (isStatus(error, 409)) markEnded()
        throw error
      }
    },
    enabled: running,
    // A link is good until shortly before it expires.
    staleTime: (query) =>
      query.state.data
        ? Math.max(
            0,
            Date.parse(query.state.data.expiresAt) -
              Date.now() -
              LIVE_LINK_MARGIN_MS
          )
        : 0,
    // Fetch a new link before this one expires, while the pane is open and
    // the page visible (B7); a control link is short-lived (B13).
    refetchInterval: (query) =>
      query.state.data
        ? Math.max(
            5_000,
            Date.parse(query.state.data.expiresAt) -
              Date.now() -
              LIVE_LINK_MARGIN_MS
          )
        : false,
    refetchOnWindowFocus: false,
    retry: false,
  })
  const handoff = useQuery({
    queryKey: handoffKey(organizationId, room, browser),
    queryFn: ({ signal }) => readHandoff(organizationId, room, browser, signal),
    enabled: running,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    retry: false,
  })
  // The latest event that changed this browser's handoff, if the log has one.
  const lastHandoffEvent = findLastHandoffEvent(events, browser)
  useEffect(() => {
    if (lastHandoffEvent === undefined || !running) return
    void queryClient.invalidateQueries({
      queryKey: handoffKey(organizationId, room, browser),
    })
  }, [
    lastHandoffEvent?.seq,
    running,
    organizationId,
    room.kind,
    room.id,
    browser,
    queryClient,
  ])
  // The handoff this page already answered, until the log says what's next.
  const [answered, setAnswered] = useState<number | null>(null)
  const asked =
    running &&
    handoff.data?.active !== false &&
    (lastHandoffEvent
      ? lastHandoffEvent.type === "browser.handoff" &&
        lastHandoffEvent.seq !== answered
      : handoff.data?.active === true && answered === null)
  // While the banner shows, check now and then that the ask still stands: a
  // handoff nobody answered times out without the page hearing (B8).
  useEffect(() => {
    if (!asked) return
    const timer = setInterval(() => {
      void queryClient.invalidateQueries({
        queryKey: handoffKey(organizationId, room, browser),
      })
    }, HANDOFF_RECHECK_MS)
    return () => clearInterval(timer)
  }, [asked, organizationId, room.kind, room.id, browser, queryClient])
  const back = useMutation({
    mutationFn: async (success: boolean) => {
      try {
        return await handBack(
          organizationId,
          room,
          browser,
          success,
          note.trim() || undefined
        )
      } catch (error) {
        // No handoff is open any more, or the browser stopped: there is
        // nothing left to hand back, which is what the member wanted.
        if (isStatus(error, 409)) return { resumed: false }
        throw error
      }
    },
    onSuccess: () => {
      setAnswered(lastHandoffEvent?.seq ?? -1)
      setMode("tab")
      setNote("")
      return queryClient.invalidateQueries({
        queryKey: handoffKey(organizationId, room, browser),
      })
    },
  })

  // Once the agent stops asking, the member is only watching again.
  useEffect(() => {
    if (!asked) setMode("tab")
  }, [asked])

  return (
    <div className="live-pane" data-compact={compact ? "true" : undefined}>
      {asked && (
        <div className="browser-handoff" role="alert">
          <Hand size={16} aria-hidden="true" />
          <div className="browser-handoff-text">
            <strong>{agentName} needs a hand</strong>
            <span>
              {handoff.data?.reason ?? lastHandoffEvent?.summary}
              {elapsed(handoff.data?.elapsedMs)
                ? ` · asked ${elapsed(handoff.data?.elapsedMs)}`
                : ""}
            </span>
          </div>
          {mode === "tab" ? (
            <button
              type="button"
              className="live-button"
              onClick={() => setMode("full")}
            >
              Take over
            </button>
          ) : (
            <div className="browser-handoff-back">
              <input
                value={note}
                maxLength={1024}
                placeholder="A note for the agent (optional)"
                aria-label="A note for the agent"
                onChange={(event) => setNote(event.target.value)}
              />
              <button
                type="button"
                className="live-button"
                disabled={back.isPending}
                onClick={() => back.mutate(true)}
              >
                {back.isPending ? "Handing back…" : "Hand back"}
              </button>
              <button
                type="button"
                className="live-link"
                disabled={back.isPending}
                onClick={() => back.mutate(false)}
              >
                I couldn't do it
              </button>
            </div>
          )}
        </div>
      )}
      {back.isError && (
        <p className="live-quiet" role="alert">
          Couldn't hand the browser back. Try again.
        </p>
      )}
      <div className="live-pane-body">
        {!running ? (
          <p className="live-quiet">
            This browser has stopped. The agent starts it again when it needs
            it.
          </p>
        ) : live.isPending ? (
          <p className="live-quiet">Opening the browser…</p>
        ) : live.isError ? (
          <div className="live-quiet" role="alert">
            <p>{liveErrorText(live.error)}</p>
            {!isStatus(live.error, 403) && !isStatus(live.error, 404) && (
              <button
                type="button"
                className="live-link"
                onClick={() => void live.refetch()}
              >
                Try again
              </button>
            )}
          </div>
        ) : (
          <>
            <iframe
              key={live.data.url}
              className="browser-live-frame"
              src={live.data.url}
              title={
                mode === "full"
                  ? "The browser, under your control"
                  : "The agent's browser, live"
              }
              allow="clipboard-read; clipboard-write"
            />
            <a
              className="live-link browser-live-open"
              href={live.data.url}
              target="_blank"
              rel="noopener noreferrer"
            >
              Open in a new tab
              <ArrowUpRight size={13} aria-hidden="true" />
            </a>
          </>
        )}
      </div>
    </div>
  )
}

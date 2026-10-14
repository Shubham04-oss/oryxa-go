// A room's browser, as a member sees it (A8): the agent's browser live, and
// when the agent asks for a hand (a login, a captcha), the member takes it
// over and hands it back. Only the member whose browser it is reaches a
// private one; the edge decides that.
import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowUpRight, Hand } from "lucide-react"
import {
  askedForHand,
  handBack,
  openLiveView,
  readHandoff,
} from "@/lib/live/children"
import type { LiveChild } from "@/lib/live/children"
import type { LiveEvent, LiveRoom } from "@/lib/live/client"

/** A live-view link is used until this long before it expires. */
export const LIVE_LINK_MARGIN_MS = 60_000

/** The events that change whether a browser is asking for a hand. */
const HANDOFF_TYPES = /^browser\.(handoff|resume|stopped|reset)$/

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
 * handoff state once when it opens, and again only when the room's log says
 * the handoff changed.
 */
export function BrowserPane({
  organizationId,
  room,
  child,
  events,
  agentName = "The agent",
}: {
  organizationId: string
  room: LiveRoom
  child: LiveChild
  /** The room's log, which says when the agent asks for a hand. */
  events: readonly LiveEvent[]
  agentName?: string
}) {
  const browser = child.name
  const running = child.status === "running"
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
    queryFn: () => openLiveView(organizationId, room, browser, mode),
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
  const lastHandoffEvent = [...events]
    .reverse()
    .find(
      (event) => HANDOFF_TYPES.test(event.type) && event.data.child === browser
    )
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
    (lastHandoffEvent
      ? askedForHand(events, browser) && lastHandoffEvent.seq !== answered
      : handoff.data?.active === true && answered === null)
  const back = useMutation({
    mutationFn: (success: boolean) =>
      handBack(
        organizationId,
        room,
        browser,
        success,
        note.trim() || undefined
      ),
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
    <div className="live-pane">
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
          <p className="live-quiet" role="alert">
            This browser isn't running, or it's private to someone else.
          </p>
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

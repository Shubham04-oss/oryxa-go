// A room's browser, as a member sees it (A8): the agent's browser live, and
// when the agent asks for a hand (a login, a captcha), the member takes it
// over and hands it back. Only the member whose browser it is reaches a
// private one; the edge decides that.
import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowUpRight, Hand } from "lucide-react"
import { handBack, openLiveView, readHandoff } from "@/lib/live/children"
import type { LiveRoom } from "@/lib/live/client"

/** How often a watched browser is asked whether the agent wants a hand. */
export const HANDOFF_POLL_MS = 5_000

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

export function BrowserPane({
  organizationId,
  room,
  browser,
  agentName = "The agent",
}: {
  organizationId: string
  room: LiveRoom
  browser: string
  agentName?: string
}) {
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
    staleTime: Infinity,
    retry: false,
  })
  const handoff = useQuery({
    queryKey: handoffKey(organizationId, room, browser),
    queryFn: ({ signal }) => readHandoff(organizationId, room, browser, signal),
    refetchInterval: HANDOFF_POLL_MS,
  })
  const asked = handoff.data?.active === true
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
              {handoff.data?.reason}
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
        {live.isPending ? (
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

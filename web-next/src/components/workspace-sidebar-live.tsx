// What is live, in the sidebar (board Sidebar, "Live now"): the member's own
// parley, with its timer, mute, camera and leave, and a dot on each listed
// teamspace whose room has a parley running. There is no org-wide list of
// live rooms yet (#322, the org live list), so each teamspace's call is read
// on its own, for the first few only, and agents' live sessions (the board's
// "Krude is browsing Notion") are not shown at all.
import { useEffect, useState } from "react"
import { useQueries } from "@tanstack/react-query"
import { Mic, MicOff, PhoneOff, Video, VideoOff } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useCall } from "./call-provider"
import { ApiError } from "@/lib/api/client"
import { callDuration, callKey, readCall } from "@/lib/call/client"
import { CALL_REFRESH_MS, useRoomCall } from "@/lib/call/use-room-call"
import type { Teamspace } from "@/lib/teamspaces/client"

/**
 * How many teamspaces get a live dot: each is one read every 20 s until the
 * org-wide live list exists. The rest show none.
 */
export const LIVE_DOT_CAP = 8

/** The ids of the listed teamspaces with a parley running (the first LIVE_DOT_CAP only). */
export function useLiveTeamspaces(
  organizationId: string | undefined,
  teamspaces: readonly Teamspace[]
): ReadonlySet<string> {
  const checked = organizationId ? teamspaces.slice(0, LIVE_DOT_CAP) : []
  const reads = useQueries({
    queries: checked.map((space) => {
      const room = { kind: "teamspace" as const, id: space.id }
      return {
        queryKey: callKey(organizationId ?? "", room),
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          readCall(organizationId ?? "", room, signal),
        refetchInterval: CALL_REFRESH_MS,
        // A room the edge refuses or doesn't know has no parley to show.
        retry: (count: number, error: unknown) =>
          !(
            error instanceof ApiError && [401, 403, 404].includes(error.status)
          ) && count < 2,
      }
    }),
  })
  return new Set(
    checked
      .filter((_, index) => Boolean(reads[index]?.data))
      .map((space) => space.id)
  )
}

/** The green dot of a running parley. */
export function LiveDot({ className }: { className?: string }) {
  return (
    <span
      className={["workspace-sidebar-live-dot", className]
        .filter(Boolean)
        .join(" ")}
      aria-hidden="true"
    />
  )
}

/** The time since `startedAt`, ticking each second. */
function useElapsed(startedAt: string | undefined): string | undefined {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!startedAt) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [startedAt])
  return startedAt ? callDuration(startedAt, now) : undefined
}

/**
 * The member's own parley while they are in one, or joining: its name and
 * how long it has run (opening it), and mute, camera and leave.
 */
export function LiveNow() {
  const { active, setMic, setCamera, leave, show } = useCall()
  const live = active && active.phase !== "failed" ? active : null
  const { call } = useRoomCall(live?.organizationId, live?.room, {
    enabled: Boolean(live),
  })
  const elapsed = useElapsed(call?.startedAt)
  if (!live) return null
  const joining = live.phase === "joining"
  return (
    <section className="workspace-sidebar-live" aria-label="Live now">
      <Button
        className="workspace-sidebar-live-row"
        variant="ghost"
        aria-label={`Open the parley in ${live.roomName}`}
        onClick={() => show("stage")}
      >
        <LiveDot />
        <span className="workspace-sidebar-label">
          Parley · {live.roomName}
        </span>
        <span className="workspace-sidebar-live-time">
          {joining ? "Joining…" : elapsed}
        </span>
      </Button>
      <div className="workspace-sidebar-live-actions">
        <Button
          variant="outline"
          size="icon-sm"
          aria-label={live.mic ? "Mute" : "Unmute"}
          aria-pressed={!live.mic}
          disabled={joining}
          onClick={() => void setMic(!live.mic)}
        >
          {live.mic ? <Mic /> : <MicOff />}
        </Button>
        <Button
          variant="outline"
          size="icon-sm"
          aria-label={live.camera ? "Turn camera off" : "Turn camera on"}
          aria-pressed={live.camera}
          disabled={joining}
          onClick={() => void setCamera(!live.camera)}
        >
          {live.camera ? <Video /> : <VideoOff />}
        </Button>
        <Button
          variant="destructive"
          size="icon-sm"
          aria-label="Leave parley"
          onClick={() => void leave()}
        >
          <PhoneOff />
        </Button>
      </div>
    </section>
  )
}

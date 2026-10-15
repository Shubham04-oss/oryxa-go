// My workspace's live parley pill (WorkspaceV3, founder's redesign of 15 Oct
// 2026): shown only while a parley is live. The member's own parley comes
// first; otherwise one of their teamspaces with a call running, read the way
// the teamspace cards read it (one `readCall` per open teamspace they are on,
// no log socket). With nothing live, there is no pill.
import { useEffect, useState } from "react"
import { useQueries, useQuery } from "@tanstack/react-query"
import { Phone } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ApiError } from "@/lib/api/client"
import { callKey, readCall } from "@/lib/call/client"
import type { CallView } from "@/lib/call/client"
import { teamspacesQueryOptions } from "@/lib/teamspaces/queries"
import { useCall, useHasCalls } from "./call-provider"
import { personIdentity } from "./teamspace-format"
import { IdentityAvatar } from "./workspace-avatar"

/** At most this many teamspaces are asked; the org-wide live list (#322) replaces it. */
export const LIVE_CHECK_CAP = 8
const LIVE_REFRESH_MS = 60_000

/** "18 min", "1 h 05": how long a parley has run. */
export function parleyDuration(startedAt: string, now: number): string {
  const start = Date.parse(startedAt)
  if (!Number.isFinite(start)) return ""
  const minutes = Math.max(0, Math.floor((now - start) / 60_000))
  if (minutes < 1) return "just started"
  if (minutes < 60) return `${minutes} min`
  return `${Math.floor(minutes / 60)} h ${String(minutes % 60).padStart(2, "0")}`
}

type Props = {
  organizationId: string
  enabled: boolean
  onOpenTeamspace?: (teamspaceId: string) => void
}

export function HomeLivePill(props: Props) {
  // Outside the workspace's CallProvider (a preview or a test) there is no
  // own parley to show, only the teamspaces'.
  return useHasCalls() ? (
    <OwnParley {...props} />
  ) : (
    <TeamspaceParley {...props} />
  )
}

function OwnParley(props: Props) {
  const call = useCall()
  const active = call.active
  if (!active || active.phase === "failed")
    return <TeamspaceParley {...props} />
  return (
    <div className="home-live" role="status" aria-label="Live parley">
      <span className="home-live-dot" aria-hidden="true" />
      <span>
        <b>You’re in a parley</b> in {active.roomName}
      </span>
      <Button
        className="home-live-action"
        size="sm"
        variant="secondary"
        onClick={() => call.expand(true)}
      >
        Open
      </Button>
    </div>
  )
}

function useClock(enabled: boolean) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!enabled) return
    const timer = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(timer)
  }, [enabled])
  return now
}

function TeamspaceParley({ organizationId, enabled, onOpenTeamspace }: Props) {
  const list = useQuery({
    ...teamspacesQueryOptions(organizationId),
    enabled: enabled && Boolean(organizationId),
  })
  const spaces = (list.data ?? [])
    .filter((space) => space.status === "active" && space.is_member)
    .slice(0, LIVE_CHECK_CAP)
  const calls = useQueries({
    queries: spaces.map((space) => {
      const room = { kind: "teamspace" as const, id: space.id }
      return {
        queryKey: callKey(organizationId, room),
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          readCall(organizationId, room, signal),
        enabled,
        refetchInterval: LIVE_REFRESH_MS,
        retry: (count: number, error: Error) =>
          !(
            error instanceof ApiError && [401, 403, 404].includes(error.status)
          ) && count < 1,
      }
    }),
  })
  const liveIndex = calls.findIndex((query) => query.data?.participants.length)
  const now = useClock(enabled && liveIndex !== -1)
  if (liveIndex === -1) return null
  const space = spaces[liveIndex]
  const call = calls[liveIndex].data as CallView
  const others = calls.filter(
    (query, index) => index !== liveIndex && query.data?.participants.length
  ).length
  const duration = parleyDuration(call.startedAt, now)
  return (
    <div className="home-live" role="status" aria-label="Live parley">
      <span className="home-live-dot" aria-hidden="true" />
      <span>
        <b>Parley in {space.name}</b>
        {duration && ` · ${duration}`}
        {others > 0 && ` · ${others} more live`}
      </span>
      <span className="home-live-faces" aria-hidden="true">
        {call.participants.slice(0, 3).map((person) => (
          <IdentityAvatar
            key={person.member}
            identity={personIdentity(person.member, person.name)}
          />
        ))}
      </span>
      <Button
        className="home-live-action"
        size="sm"
        variant="secondary"
        disabled={!onOpenTeamspace}
        aria-label={`Join the parley in ${space.name}`}
        onClick={() => onOpenTeamspace?.(space.id)}
      >
        <Phone aria-hidden="true" />
        Join
      </Button>
    </div>
  )
}

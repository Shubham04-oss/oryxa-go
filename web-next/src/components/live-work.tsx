// A room's live work (A8): the sandboxes and browsers its agents run, as
// tiles, each opening as a pane; and the run log, as it happens. The edge
// shows a member only what they may see: a private sandbox or browser only
// to its member.
import { useEffect } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, Globe, Hand, SquareTerminal } from "lucide-react"
import { BrowserPane, HANDOFF_POLL_MS } from "./browser-pane"
import { SandboxPane } from "./sandbox-pane"
import { timeAgo } from "./agents-format"
import {
  childKey,
  childLabel,
  listChildren,
  readHandoff,
} from "@/lib/live/children"
import type { LiveChild } from "@/lib/live/children"
import type { LiveEvent, LiveRoom } from "@/lib/live/client"
import { useRoomLog } from "@/lib/live/use-room-log"
import "./live.css"

/** The room's children, refreshed whenever the log says one changed. */
export function useRoomChildren(
  organizationId: string,
  room: LiveRoom | undefined,
  { enabled = true }: { enabled?: boolean } = {}
) {
  const queryClient = useQueryClient()
  const active = enabled && room !== undefined
  const children = useQuery({
    queryKey: childKey(organizationId, room ?? { kind: "thread", id: "" }),
    queryFn: ({ signal }) =>
      listChildren(organizationId, room ?? { kind: "thread", id: "" }, signal),
    enabled: active,
    retry: false,
  })
  const log = useRoomLog(organizationId, room, { enabled: active && children.isSuccess })
  const lastChildEvent = [...log.events]
    .reverse()
    .find((event) => /^(sandbox|browser)\./.test(event.type))?.seq
  useEffect(() => {
    if (lastChildEvent === undefined || !room) return
    void queryClient.invalidateQueries({ queryKey: childKey(organizationId, room) })
  }, [lastChildEvent, organizationId, room?.kind, room?.id, queryClient])
  return { children, log }
}

function ChildIcon({ child }: { child: LiveChild }) {
  return child.kind === "browser" ? (
    <Globe size={16} strokeWidth={1.7} aria-hidden="true" />
  ) : (
    <SquareTerminal size={16} strokeWidth={1.7} aria-hidden="true" />
  )
}

/** A running browser is asked whether its agent wants a hand. */
function useAskedForHand(organizationId: string, room: LiveRoom, child: LiveChild) {
  const watch = child.kind === "browser" && child.status === "running"
  const handoff = useQuery({
    queryKey: ["live", organizationId, room.kind, room.id, "browser", child.name, "handoff"],
    queryFn: ({ signal }) => readHandoff(organizationId, room, child.name, signal),
    enabled: watch,
    refetchInterval: watch ? HANDOFF_POLL_MS : false,
    retry: false,
  })
  return handoff.data?.active === true
}

function ChildTile({
  organizationId,
  room,
  child,
  viewerId,
  onOpen,
}: {
  organizationId: string
  room: LiveRoom
  child: LiveChild
  viewerId?: string
  onOpen: (child: LiveChild) => void
}) {
  const asked = useAskedForHand(organizationId, room, child)
  const running = child.status === "running"
  const label = childLabel(child, viewerId)
  return (
    <button
      type="button"
      className="live-tile"
      data-running={running ? "true" : undefined}
      data-asked={asked ? "true" : undefined}
      aria-label={`Open ${label}${asked ? ", needs a hand" : ""}`}
      onClick={() => onOpen(child)}
    >
      <span className="live-tile-icon">
        <ChildIcon child={child} />
      </span>
      <span className="live-tile-copy">
        <strong>{label}</strong>
        <small>
          {asked ? (
            <span className="live-tile-asked">
              <Hand size={11} aria-hidden="true" /> Needs a hand
            </span>
          ) : running ? (
            <>
              <span className="live-dot" aria-hidden="true" />
              Running · used {timeAgo(child.lastUsedAt)}
            </>
          ) : (
            `Stopped · used ${timeAgo(child.lastUsedAt)}`
          )}
        </small>
      </span>
    </button>
  )
}

/** The room's sandboxes and browsers as tiles; nothing while there are none. */
export function LiveChildren({
  organizationId,
  room,
  viewerId,
  onOpen,
  title = "Sandbox and browser",
}: {
  organizationId: string
  room: LiveRoom | undefined
  viewerId?: string
  onOpen: (child: LiveChild) => void
  title?: string
}) {
  const { children } = useRoomChildren(organizationId, room)
  if (!room || !children.data || children.data.length === 0) return null
  const sorted = [...children.data].sort(
    (a, b) =>
      Number(b.status === "running") - Number(a.status === "running") ||
      b.lastUsedAt.localeCompare(a.lastUsedAt)
  )
  return (
    <section className="live-children" aria-label={title}>
      <p className="workspace-eyebrow">{title}</p>
      <div className="live-tiles">
        {sorted.map((child) => (
          <ChildTile
            key={`${child.kind}:${child.name}`}
            organizationId={organizationId}
            room={room}
            child={child}
            viewerId={viewerId}
            onOpen={onOpen}
          />
        ))}
      </div>
    </section>
  )
}

/** One sandbox or browser, as the work panel shows it. */
export function LivePane({
  organizationId,
  room,
  child,
  viewerId,
  agentName,
  onBack,
}: {
  organizationId: string
  room: LiveRoom
  child: LiveChild
  viewerId?: string
  agentName?: string
  onBack: () => void
}) {
  const label = childLabel(child, viewerId)
  return (
    <div className="live-viewer" aria-label={label} role="region">
      <header className="live-viewer-bar">
        <button
          type="button"
          className="workspace-icon-button"
          aria-label="Back"
          onClick={onBack}
        >
          <ArrowLeft size={17} strokeWidth={1.7} />
        </button>
        <span className="live-tile-icon">
          <ChildIcon child={child} />
        </span>
        <strong>{label}</strong>
        <small>{child.status === "running" ? "Running" : "Stopped"}</small>
      </header>
      {child.kind === "sandbox" ? (
        <SandboxPane organizationId={organizationId} room={room} sandbox={child.name} />
      ) : (
        <BrowserPane
          organizationId={organizationId}
          room={room}
          browser={child.name}
          agentName={agentName}
        />
      )}
    </div>
  )
}

function actorLabel(event: LiveEvent, viewerId?: string): string {
  if (event.actor.type === "system") return "Oryxa"
  if (event.actor.type === "member") {
    return event.actor.id === viewerId ? "You" : "A teammate"
  }
  return event.actor.onBehalfOf === viewerId ? "Your agent" : "An agent"
}

function duration(ms: number): string | undefined {
  if (ms <= 0) return undefined
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
}

/** The room's run log, newest first, as it happens. */
export function LiveActivity({
  events,
  viewerId,
  limit = 60,
}: {
  events: LiveEvent[]
  viewerId?: string
  limit?: number
}) {
  const shown = events
    .filter((event) => event.summary)
    .slice(-limit)
    .reverse()
  if (shown.length === 0) {
    return <p className="live-quiet">What agents do here shows up as they do it.</p>
  }
  return (
    <ol className="live-activity" aria-label="Run log">
      {shown.map((event) => (
        <li key={event.seq} data-private={event.private ? "true" : undefined}>
          <span className="live-activity-who">{actorLabel(event, viewerId)}</span>
          <span className="live-activity-what">{event.summary}</span>
          <small>
            {timeAgo(event.at)}
            {duration(event.durationMs) ? ` · ${duration(event.durationMs)}` : ""}
            {event.private ? " · only you" : ""}
          </small>
        </li>
      ))}
    </ol>
  )
}

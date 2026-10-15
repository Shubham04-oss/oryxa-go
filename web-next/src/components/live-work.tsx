// A room's live work (A8): the sandboxes and browsers its agents run, as
// tiles, each opening as a pane; and the run log, as it happens. The edge
// shows a member only what they may see: a private sandbox or browser only
// to its member.
import { useEffect, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, Globe, Hand, SquareTerminal } from "lucide-react"
import { BrowserPane } from "./browser-pane"
import { SandboxPane } from "./sandbox-pane"
import { timeAgo } from "./agents-format"
import {
  askedForHand,
  childKey,
  childLabel,
  listChildren,
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
  const log = useRoomLog(organizationId, room, {
    enabled: active && children.isSuccess,
  })
  const lastChildEvent = [...log.events]
    .reverse()
    .find((event) => /^(sandbox|browser)\./.test(event.type))?.seq
  useEffect(() => {
    if (lastChildEvent === undefined || !room) return
    void queryClient.invalidateQueries({
      queryKey: childKey(organizationId, room),
    })
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

function ChildTile({
  child,
  asked,
  viewerId,
  onOpen,
}: {
  child: LiveChild
  /** The agent is asking for a hand with this browser (from the room's log). */
  asked: boolean
  viewerId?: string
  onOpen: (child: LiveChild) => void
}) {
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
  const { children, log } = useRoomChildren(organizationId, room)
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
            child={child}
            asked={
              child.kind === "browser" &&
              child.status === "running" &&
              askedForHand(log.events, child.name)
            }
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
  events,
  onBack,
}: {
  organizationId: string
  room: LiveRoom
  child: LiveChild
  viewerId?: string
  agentName?: string
  /** The room's log when the page already follows it; otherwise the pane does. */
  events?: readonly LiveEvent[]
  onBack: () => void
}) {
  const own = useRoomLog(organizationId, room, {
    enabled: events === undefined,
  })
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
        <SandboxPane
          organizationId={organizationId}
          room={room}
          sandbox={child.name}
          viewerId={viewerId}
        />
      ) : (
        <BrowserPane
          organizationId={organizationId}
          room={room}
          child={child}
          events={events ?? own.events}
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
    return (
      <p className="live-quiet">What agents do here shows up as they do it.</p>
    )
  }
  return (
    <ol className="live-activity" aria-label="Run log">
      {shown.map((event) => (
        <li key={event.seq} data-private={event.private ? "true" : undefined}>
          <span className="live-activity-who">
            {actorLabel(event, viewerId)}
          </span>
          <span className="live-activity-what">{event.summary}</span>
          <small>
            {timeAgo(event.at)}
            {duration(event.durationMs)
              ? ` · ${duration(event.durationMs)}`
              : ""}
            {event.private ? " · only you" : ""}
          </small>
        </li>
      ))}
    </ol>
  )
}

/** What `browser` has done, oldest first, from the room's log. */
export function browserSteps(
  events: readonly LiveEvent[],
  browser: string
): LiveEvent[] {
  return events.filter(
    (event) =>
      event.type.startsWith("browser.") &&
      event.data.child === browser &&
      event.summary
  )
}

/**
 * One browser's steps, as LiveActivity shows the run log: the last few,
 * with the newest marked as happening now while the browser runs.
 */
export function BrowserSteps({
  events,
  browser,
  running,
  limit = 4,
}: {
  events: readonly LiveEvent[]
  browser: string
  running: boolean
  limit?: number
}) {
  const steps = browserSteps(events, browser).slice(-limit)
  if (steps.length === 0) return null
  return (
    <ol className="live-steps" aria-label="Steps">
      {steps.map((event, index) => {
        const now = running && index === steps.length - 1
        return (
          <li key={event.seq} data-now={now ? "true" : undefined}>
            <span className="live-steps-mark" aria-hidden="true" />
            <span className="live-steps-what">{event.summary}</span>
            <small>{now ? "now" : timeAgo(event.at)}</small>
          </li>
        )
      })}
    </ol>
  )
}

/**
 * A room's live work in one place, for a teamspace's Live tab: its
 * sandboxes and browsers, the open one, and the run log.
 */
export function RoomLive({
  organizationId,
  room,
  viewerId,
}: {
  organizationId: string
  room: LiveRoom
  viewerId?: string
}) {
  const [open, setOpen] = useState<LiveChild | null>(null)
  const { children, log } = useRoomChildren(organizationId, room)

  if (open) {
    return (
      <LivePane
        organizationId={organizationId}
        room={room}
        child={
          children.data?.find(
            (item) => item.kind === open.kind && item.name === open.name
          ) ?? open
        }
        viewerId={viewerId}
        events={log.events}
        onBack={() => setOpen(null)}
      />
    )
  }
  if (children.isPending)
    return <p className="live-quiet">Looking for live work…</p>
  if (children.isError) {
    return (
      <p className="live-quiet">
        Nothing live here yet. When an agent runs code or browses for this room,
        it shows up here.
      </p>
    )
  }
  return (
    <div className="live-room">
      {children.data.length === 0 ? (
        <p className="live-quiet">
          No sandbox or browser yet. When an agent runs code or browses for this
          room, it shows up here.
        </p>
      ) : (
        <LiveChildren
          organizationId={organizationId}
          room={room}
          viewerId={viewerId}
          onOpen={setOpen}
        />
      )}
      <section className="live-children" aria-label="Run log">
        <p className="workspace-eyebrow">Run log</p>
        <LiveActivity events={log.events} viewerId={viewerId} />
      </section>
    </div>
  )
}
